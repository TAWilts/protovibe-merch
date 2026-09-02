// Package platform implements the control plane: managing bands, granting
// support access to their data, and the instance-wide operational levers.
//
// The tenant boundary is the point of this package. A platform account has no
// band access from its role alone; the only path to a band's data is a grant
// that a band admin approved. There is deliberately no break-glass.
package platform

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Errors returned by band management.
var (
	ErrBandNotFound  = errors.New("platform: no such band")
	ErrSlugTaken     = errors.New("platform: this slug is already in use")
	ErrInvalidSlug   = errors.New("platform: the slug must be 2 to 64 characters of a-z, 0-9 and -")
	ErrInvalidName   = errors.New("platform: the band name must be 1 to 200 characters")
	ErrBandNotEmpty  = errors.New("platform: the band still holds data; deactivate it first")
	ErrAlreadyActive = errors.New("platform: the band is already active")
)

// slugPattern keeps the slug usable in a URL and in a login form.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])$`)

// Service owns the control plane.
type Service struct {
	db *gorm.DB
}

// NewService builds the platform service.
func NewService(database *gorm.DB) *Service { return &Service{db: database} }

// crossBand returns a handle that may span all bands.
//
// Every call site here is a deliberate platform-level operation; the marker
// makes that visible rather than implicit.
func (s *Service) crossBand(ctx context.Context) *gorm.DB {
	return s.db.WithContext(tenant.WithCrossBandAccess(ctx))
}

// BandSummary is one row of the admin center's band list.
type BandSummary struct {
	models.Band
	UserCount      int64      `json:"user_count"`
	ArticleCount   int64      `json:"article_count"`
	SaleCount      int64      `json:"sale_count"`
	StorageBytes   int64      `json:"storage_bytes"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	LastBackupAt   *time.Time `json:"last_backup_at"`
	ActiveGrantID  *int64     `json:"active_grant_id"`
}

// ListBands returns every band with the numbers the admin center displays.
//
// Soft-deleted bands are included by default: the whole point of the grace
// period is that an accidental deletion stays visible and recoverable.
func (s *Service) ListBands(ctx context.Context, includeDeleted bool) ([]BandSummary, error) {
	query := s.crossBand(ctx).Model(&models.Band{})
	if !includeDeleted {
		query = query.Where("deleted_at IS NULL")
	}

	var bands []models.Band
	if err := query.Order("name").Find(&bands).Error; err != nil {
		return nil, err
	}
	if len(bands) == 0 {
		return []BandSummary{}, nil
	}

	ids := make([]int64, len(bands))
	for i, band := range bands {
		ids[i] = band.ID
	}

	counts := func(table string) (map[int64]int64, error) {
		type row struct {
			BandID int64
			Total  int64
		}
		var rows []row
		err := s.crossBand(ctx).Table(table).
			Select("band_id, COUNT(*) AS total").
			Where("band_id IN ?", ids).
			Group("band_id").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		out := make(map[int64]int64, len(rows))
		for _, entry := range rows {
			out[entry.BandID] = entry.Total
		}
		return out, nil
	}

	users, err := counts("users")
	if err != nil {
		return nil, err
	}
	articles, err := counts("articles")
	if err != nil {
		return nil, err
	}
	sales, err := counts("sales")
	if err != nil {
		return nil, err
	}

	type activityRow struct {
		BandID int64
		LastAt *time.Time
	}
	var activity []activityRow
	err = s.crossBand(ctx).Table("sales").
		Select("band_id, MAX(created_at) AS last_at").
		Where("band_id IN ?", ids).Group("band_id").Scan(&activity).Error
	if err != nil {
		return nil, err
	}
	lastActivity := make(map[int64]*time.Time, len(activity))
	for _, entry := range activity {
		lastActivity[entry.BandID] = entry.LastAt
	}

	type backupRow struct {
		BandID int64
		LastAt *time.Time
	}
	var backups []backupRow
	err = s.crossBand(ctx).Table("backup_runs").
		Select("band_id, MAX(finished_at) AS last_at").
		Where("band_id IN ? AND status = ?", ids, models.BackupStatusSucceeded).
		Group("band_id").Scan(&backups).Error
	if err != nil {
		return nil, err
	}
	lastBackup := make(map[int64]*time.Time, len(backups))
	for _, entry := range backups {
		lastBackup[entry.BandID] = entry.LastAt
	}

	live, err := s.liveGrantsByBand(ctx)
	if err != nil {
		return nil, err
	}

	summaries := make([]BandSummary, 0, len(bands))
	for _, band := range bands {
		summary := BandSummary{
			Band:           band,
			UserCount:      users[band.ID],
			ArticleCount:   articles[band.ID],
			SaleCount:      sales[band.ID],
			LastActivityAt: lastActivity[band.ID],
			LastBackupAt:   lastBackup[band.ID],
		}
		if grant, ok := live[band.ID]; ok {
			id := grant.ID
			summary.ActiveGrantID = &id
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// Band loads one band regardless of its lifecycle state.
func (s *Service) Band(ctx context.Context, id int64) (*models.Band, error) {
	var band models.Band
	if err := s.crossBand(ctx).First(&band, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBandNotFound
		}
		return nil, err
	}
	return &band, nil
}

// CreateBand adds a tenant.
func (s *Service) CreateBand(ctx context.Context, slug, name, contactEmail string) (*models.Band, error) {
	return s.createBand(s.crossBand(ctx), slug, name, contactEmail)
}

// CreateBandInTransaction adds a tenant on the caller's transaction. It is
// used by workflows that must also create the first account atomically.
func (s *Service) CreateBandInTransaction(ctx context.Context, tx *gorm.DB, slug, name, contactEmail string) (*models.Band, error) {
	return s.createBand(tx.WithContext(tenant.WithCrossBandAccess(ctx)), slug, name, contactEmail)
}

func (s *Service) createBand(database *gorm.DB, slug, name, contactEmail string) (*models.Band, error) {
	slug, name, err := ValidateBandIdentity(slug, name)
	if err != nil {
		return nil, err
	}

	band := &models.Band{
		Slug:         slug,
		Name:         name,
		ContactEmail: strings.TrimSpace(contactEmail),
		IsActive:     true,
		FeatureFlags: models.FeatureFlags{},
	}
	if err := database.Create(band).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrSlugTaken
		}
		return nil, err
	}
	return band, nil
}

// ValidateBandIdentity normalizes and validates the two public identity
// fields without creating anything.
func ValidateBandIdentity(slug, name string) (string, string, error) {
	slug, err := NormalizeBandSlug(slug)
	if err != nil {
		return "", "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", "", ErrInvalidName
	}
	return slug, name, nil
}

// NormalizeBandSlug returns the canonical login slug without checking any
// unrelated band fields.
func NormalizeBandSlug(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return "", ErrInvalidSlug
	}
	return slug, nil
}

// EnsureSlugAvailable verifies current availability. Approval repeats this
// check inside its transaction because availability can change meanwhile.
func (s *Service) EnsureSlugAvailable(ctx context.Context, slug string) error {
	normalized, err := NormalizeBandSlug(slug)
	if err != nil {
		return err
	}
	var count int64
	if err := s.crossBand(ctx).Model(&models.Band{}).Where("slug = ?", normalized).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrSlugTaken
	}
	return nil
}

// BandUpdate carries the fields the admin center can change. A nil field is
// left alone, so a partial form never clears something it did not show.
type BandUpdate struct {
	Name               *string              `json:"name"`
	ContactEmail       *string              `json:"contact_email"`
	MaintenanceMessage *string              `json:"maintenance_message"`
	StorageQuotaBytes  *int64               `json:"storage_quota_bytes"`
	UserQuota          *int                 `json:"user_quota"`
	FeatureFlags       *models.FeatureFlags `json:"feature_flags"`
}

// UpdateBand applies the admin center's changes.
func (s *Service) UpdateBand(ctx context.Context, id int64, update BandUpdate) (*models.Band, error) {
	band, err := s.Band(ctx, id)
	if err != nil {
		return nil, err
	}

	updates := map[string]any{}
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if name == "" || len(name) > 200 {
			return nil, ErrInvalidName
		}
		updates["name"] = name
	}
	if update.ContactEmail != nil {
		updates["contact_email"] = strings.TrimSpace(*update.ContactEmail)
	}
	if update.MaintenanceMessage != nil {
		updates["maintenance_message"] = strings.TrimSpace(*update.MaintenanceMessage)
	}
	if update.StorageQuotaBytes != nil {
		if *update.StorageQuotaBytes < 0 {
			return nil, fmt.Errorf("platform: the storage quota cannot be negative")
		}
		updates["storage_quota_bytes"] = *update.StorageQuotaBytes
	}
	if update.UserQuota != nil {
		if *update.UserQuota < 0 {
			return nil, fmt.Errorf("platform: the user quota cannot be negative")
		}
		updates["user_quota"] = *update.UserQuota
	}
	if update.FeatureFlags != nil {
		updates["feature_flags"] = *update.FeatureFlags
	}
	if len(updates) == 0 {
		return band, nil
	}
	updates["updated_at"] = time.Now().UTC()

	if err := s.crossBand(ctx).Model(&models.Band{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.Band(ctx, id)
}

// SetActive deactivates or reactivates a band.
//
// Deactivating stops every login immediately; it does not touch a single row
// of the band's data, which is what makes it reversible.
func (s *Service) SetActive(ctx context.Context, id int64, active bool) error {
	band, err := s.Band(ctx, id)
	if err != nil {
		return err
	}
	if band.IsActive == active {
		return nil
	}

	updates := map[string]any{"is_active": active, "updated_at": time.Now().UTC()}
	if active {
		updates["deactivated_at"] = nil
	} else {
		updates["deactivated_at"] = time.Now().UTC()
	}
	return s.crossBand(ctx).Model(&models.Band{}).Where("id = ?", id).Updates(updates).Error
}

// SoftDelete starts the grace period.
//
// Nothing is removed: the band disappears from the normal list and cannot sign
// in, but every row stays until an operator purges it deliberately. A band's
// season of bookings is not something to lose to a misclick.
func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	band, err := s.Band(ctx, id)
	if err != nil {
		return err
	}
	if band.DeletedAt != nil {
		return nil
	}

	now := time.Now().UTC()
	return s.crossBand(ctx).Model(&models.Band{}).Where("id = ?", id).Updates(map[string]any{
		"deleted_at":     now,
		"is_active":      false,
		"deactivated_at": now,
		"updated_at":     now,
	}).Error
}

// Restore reverses a soft delete.
func (s *Service) Restore(ctx context.Context, id int64) error {
	band, err := s.Band(ctx, id)
	if err != nil {
		return err
	}
	if band.DeletedAt == nil {
		return ErrAlreadyActive
	}

	return s.crossBand(ctx).Model(&models.Band{}).Where("id = ?", id).Updates(map[string]any{
		"deleted_at":     nil,
		"is_active":      true,
		"deactivated_at": nil,
		"updated_at":     time.Now().UTC(),
	}).Error
}
