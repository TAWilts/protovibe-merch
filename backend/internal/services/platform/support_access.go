package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Support-access errors.
var (
	ErrGrantNotFound    = errors.New("platform: no such support access request")
	ErrGrantNotPending  = errors.New("platform: this request has already been decided")
	ErrGrantNotApproved = errors.New("platform: this request has not been approved by the band")
	ErrGrantNotLive     = errors.New("platform: this grant is not active")
	ErrReasonRequired   = errors.New("platform: a reason is required")
	ErrOpenRequest      = errors.New("platform: there is already an open request for this band")
	ErrWrongBand        = errors.New("platform: this request belongs to a different band")
)

// Duration bounds for a grant. Long enough to actually help, short enough that
// a forgotten session closes itself the same working day.
const (
	MinGrantDuration     = 5 * time.Minute
	MaxGrantDuration     = 24 * time.Hour
	DefaultGrantDuration = time.Hour
)

// RequestAccess asks a band for permission to look at its data.
//
// This is the only entry point to band data for a platform account. The reason
// is mandatory and is shown to the band admin who decides, and later in the
// band's own audit log — "support looked at our numbers" must always come with
// a why.
func (s *Service) RequestAccess(
	ctx context.Context,
	bandID int64,
	requester *models.User,
	reason string,
	scope models.SupportAccessScope,
	duration time.Duration,
) (*models.SupportAccessGrant, error) {
	band, err := s.Band(ctx, bandID)
	if err != nil {
		return nil, err
	}
	if band.DeletedAt != nil {
		return nil, ErrBandNotFound
	}

	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ErrReasonRequired
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}

	if scope != models.SupportScopeReadOnly && scope != models.SupportScopeReadWrite {
		return nil, fmt.Errorf("platform: the scope must be read_only or read_write")
	}
	if duration <= 0 {
		duration = DefaultGrantDuration
	}
	if duration < MinGrantDuration || duration > MaxGrantDuration {
		return nil, fmt.Errorf("platform: the duration must be between 5 minutes and 24 hours")
	}

	// One open request per band at a time keeps the band admin's decision
	// unambiguous and stops a queue of identical asks.
	var open int64
	err = s.crossBand(ctx).Model(&models.SupportAccessGrant{}).
		Where("band_id = ? AND status IN ?", bandID,
			[]models.SupportAccessStatus{models.SupportStatusPending, models.SupportStatusApproved, models.SupportStatusActive}).
		Count(&open).Error
	if err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, ErrOpenRequest
	}

	now := time.Now().UTC()
	grant := &models.SupportAccessGrant{
		BandID:                   bandID,
		RequestedByUserID:        requester.ID,
		RequestedByUsername:      requester.Username,
		Reason:                   reason,
		Scope:                    scope,
		RequestedDurationSeconds: int(duration.Seconds()),
		Status:                   models.SupportStatusPending,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if err := s.crossBand(ctx).Create(grant).Error; err != nil {
		return nil, err
	}
	return grant, nil
}

// Decide records a band admin's approval or refusal.
//
// Only a band admin of that band may call it; the caller enforces the role and
// passes the band it belongs to, which is checked here as well so a mismatched
// request can never be decided by the wrong band.
func (s *Service) Decide(
	ctx context.Context,
	grantID int64,
	decider *models.User,
	approve bool,
	note string,
) (*models.SupportAccessGrant, error) {
	grant, err := s.Grant(ctx, grantID)
	if err != nil {
		return nil, err
	}
	if decider.BandID == nil || *decider.BandID != grant.BandID {
		return nil, ErrWrongBand
	}
	if grant.Status != models.SupportStatusPending {
		return nil, ErrGrantNotPending
	}

	status := models.SupportStatusDenied
	if approve {
		status = models.SupportStatusApproved
	}
	now := time.Now().UTC()

	err = s.crossBand(ctx).Model(&models.SupportAccessGrant{}).Where("id = ?", grantID).
		Updates(map[string]any{
			"status":              status,
			"decided_by_user_id":  decider.ID,
			"decided_by_username": decider.Username,
			"decided_at":          now,
			"decision_note":       strings.TrimSpace(note),
			"updated_at":          now,
		}).Error
	if err != nil {
		return nil, err
	}
	return s.Grant(ctx, grantID)
}

// Activate starts the approved window.
//
// It is a separate step from approval on purpose: the platform admin has to
// confirm with a fresh second factor at the moment they actually look, so an
// approval granted in the morning cannot be used from a stolen laptop at night.
func (s *Service) Activate(ctx context.Context, grantID int64, actor *models.User) (*models.SupportAccessGrant, error) {
	grant, err := s.Grant(ctx, grantID)
	if err != nil {
		return nil, err
	}
	if grant.RequestedByUserID != actor.ID {
		return nil, ErrWrongBand
	}
	if grant.Status != models.SupportStatusApproved {
		return nil, ErrGrantNotApproved
	}

	now := time.Now().UTC()
	expires := now.Add(time.Duration(grant.RequestedDurationSeconds) * time.Second)

	err = s.crossBand(ctx).Model(&models.SupportAccessGrant{}).Where("id = ?", grantID).
		Updates(map[string]any{
			"status":       models.SupportStatusActive,
			"activated_at": now,
			"expires_at":   expires,
			"updated_at":   now,
		}).Error
	if err != nil {
		return nil, err
	}
	return s.Grant(ctx, grantID)
}

// Revoke ends a live grant early.
//
// Both sides may call it: the platform admin when they are done, and the band
// admin the moment they change their mind. A band must never have to wait out
// a window it no longer wants.
func (s *Service) Revoke(ctx context.Context, grantID int64, actor *models.User) error {
	grant, err := s.Grant(ctx, grantID)
	if err != nil {
		return err
	}
	if grant.Status != models.SupportStatusActive && grant.Status != models.SupportStatusApproved {
		return ErrGrantNotLive
	}

	now := time.Now().UTC()
	err = s.crossBand(ctx).Model(&models.SupportAccessGrant{}).Where("id = ?", grantID).
		Updates(map[string]any{
			"status":             models.SupportStatusRevoked,
			"revoked_at":         now,
			"revoked_by_user_id": actor.ID,
			"updated_at":         now,
		}).Error
	if err != nil {
		return err
	}

	// Any session still bound to this grant loses its band scope immediately.
	return s.crossBand(ctx).Model(&models.Session{}).
		Where("acting_grant_id = ?", grantID).
		Updates(map[string]any{"band_id": nil, "acting_grant_id": nil}).Error
}

// Grant loads one request.
func (s *Service) Grant(ctx context.Context, id int64) (*models.SupportAccessGrant, error) {
	var grant models.SupportAccessGrant
	if err := s.crossBand(ctx).First(&grant, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGrantNotFound
		}
		return nil, err
	}
	return &grant, nil
}

// ListGrants returns the request history, newest first. A nil bandID lists
// every band, which is the platform view.
func (s *Service) ListGrants(ctx context.Context, bandID *int64) ([]models.SupportAccessGrant, error) {
	query := s.crossBand(ctx).Model(&models.SupportAccessGrant{}).Order("created_at DESC, id DESC")
	if bandID != nil {
		query = query.Where("band_id = ?", *bandID)
	}

	var grants []models.SupportAccessGrant
	if err := query.Find(&grants).Error; err != nil {
		return nil, err
	}
	if grants == nil {
		grants = []models.SupportAccessGrant{}
	}
	return grants, nil
}

// ExpireLapsed marks grants whose window has closed and detaches their
// sessions. It runs on a schedule so an expired grant does not depend on
// someone making a request to be noticed.
func (s *Service) ExpireLapsed(ctx context.Context) (int64, error) {
	now := time.Now().UTC()

	var lapsed []int64
	err := s.crossBand(ctx).Model(&models.SupportAccessGrant{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?", models.SupportStatusActive, now).
		Pluck("id", &lapsed).Error
	if err != nil {
		return 0, err
	}
	if len(lapsed) == 0 {
		return 0, nil
	}

	err = s.crossBand(ctx).Model(&models.SupportAccessGrant{}).
		Where("id IN ?", lapsed).
		Updates(map[string]any{"status": models.SupportStatusExpired, "updated_at": now}).Error
	if err != nil {
		return 0, err
	}
	err = s.crossBand(ctx).Model(&models.Session{}).
		Where("acting_grant_id IN ?", lapsed).
		Updates(map[string]any{"band_id": nil, "acting_grant_id": nil}).Error
	if err != nil {
		return 0, err
	}
	return int64(len(lapsed)), nil
}

// LiveGrantForBand returns the grant currently opened on a band, if any.
//
// The band's own members need it for the banner: an approved access window
// must never be invisible to the people whose data it opens, and their session
// carries no acting grant of its own to read it from.
func (s *Service) LiveGrantForBand(ctx context.Context, bandID int64) (*models.SupportAccessGrant, error) {
	var grant models.SupportAccessGrant
	err := s.crossBand(ctx).
		Where("band_id = ? AND status = ? AND expires_at > ? AND revoked_at IS NULL",
			bandID, models.SupportStatusActive, time.Now().UTC()).
		Order("expires_at DESC").First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// liveGrantsByBand indexes the currently active grants for the band list.
func (s *Service) liveGrantsByBand(ctx context.Context) (map[int64]models.SupportAccessGrant, error) {
	now := time.Now().UTC()

	var grants []models.SupportAccessGrant
	err := s.crossBand(ctx).
		Where("status = ? AND expires_at > ? AND revoked_at IS NULL", models.SupportStatusActive, now).
		Find(&grants).Error
	if err != nil {
		return nil, err
	}

	byBand := make(map[int64]models.SupportAccessGrant, len(grants))
	for _, grant := range grants {
		byBand[grant.BandID] = grant
	}
	return byBand, nil
}
