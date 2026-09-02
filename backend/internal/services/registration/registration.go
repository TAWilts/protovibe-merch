// Package registration owns the public request and system-admin approval
// workflow for onboarding a band.
package registration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

const (
	pendingTTL  = 30 * 24 * time.Hour
	decisionTTL = 14 * 24 * time.Hour
)

var (
	ErrNotFound               = errors.New("registration: no such request")
	ErrNotPending             = errors.New("registration: request has already been decided")
	ErrExpired                = errors.New("registration: request has expired")
	ErrNotApproved            = errors.New("registration: request is not approved")
	ErrCredentialsClaimed     = errors.New("registration: credentials were already retrieved")
	ErrCredentialsUnavailable = errors.New("registration: credentials are no longer available")
	ErrDecisionNoteTooLong    = errors.New("registration: decision note is too long")
)

type Service struct {
	db            *gorm.DB
	auth          *auth.Service
	platform      *platform.Service
	publicBaseURL string
}

func NewService(database *gorm.DB, authService *auth.Service, platformService *platform.Service, publicBaseURL string) *Service {
	return &Service{
		db: database, auth: authService, platform: platformService,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

func (s *Service) crossBand(ctx context.Context) *gorm.DB {
	return s.db.WithContext(tenant.WithCrossBandAccess(ctx))
}

type CreateInput struct {
	BandName      string
	BandSlug      string
	AdminUsername string
	ContactEmail  string
}

type Created struct {
	Request   *models.BandRegistrationRequest
	StatusURL string
}

// Create stores an approval request and returns its unpersisted status link.
func (s *Service) Create(ctx context.Context, input CreateInput) (*Created, error) {
	slug, name, err := platform.ValidateBandIdentity(input.BandSlug, input.BandName)
	if err != nil {
		return nil, err
	}
	username, err := auth.NormalizeUsername(input.AdminUsername)
	if err != nil {
		return nil, err
	}
	if err := s.platform.EnsureSlugAvailable(ctx, slug); err != nil {
		return nil, err
	}
	email := strings.TrimSpace(input.ContactEmail)
	if email == "" {
		return nil, errors.New("registration: contact email is required")
	}

	for attempt := 0; attempt < 3; attempt++ {
		token, err := auth.RandomToken(32)
		if err != nil {
			return nil, err
		}
		refCode, err := auth.RandomCode(10)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		request := &models.BandRegistrationRequest{
			PublicID:               "REG-" + refCode,
			TokenHash:              auth.HashToken(token),
			RequestedBandName:      name,
			RequestedBandSlug:      slug,
			RequestedAdminUsername: username,
			RequestedContactEmail:  email,
			FinalBandName:          name,
			FinalBandSlug:          slug,
			FinalAdminUsername:     username,
			FinalContactEmail:      email,
			Status:                 models.BandRegistrationPending,
			PrivacyAcceptedAt:      now,
			ExpiresAt:              now.Add(pendingTTL),
		}
		if err := s.crossBand(ctx).Create(request).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				continue
			}
			return nil, err
		}
		return &Created{
			Request:   request,
			StatusURL: s.publicBaseURL + "/#registration=" + token,
		}, nil
	}
	return nil, errors.New("registration: could not allocate a unique request token")
}

// Status resolves a secret status token and lazily expires its request.
func (s *Service) Status(ctx context.Context, token string) (*models.BandRegistrationRequest, error) {
	request, err := s.byToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if request.Status != models.BandRegistrationExpired && !time.Now().UTC().Before(request.ExpiresAt) {
		now := time.Now().UTC()
		if err := s.crossBand(ctx).Model(request).Updates(map[string]any{
			"status":               models.BandRegistrationExpired,
			"setup_code_encrypted": "",
			"updated_at":           now,
		}).Error; err != nil {
			return nil, err
		}
		request.Status = models.BandRegistrationExpired
		request.SetupCodeEncrypted = ""
		request.UpdatedAt = now
	}
	return request, nil
}

func (s *Service) byToken(ctx context.Context, token string) (*models.BandRegistrationRequest, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotFound
	}
	var request models.BandRegistrationRequest
	err := s.crossBand(ctx).Where("token_hash = ?", auth.HashToken(token)).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &request, err
}

// List returns registration requests for the system-admin inbox.
func (s *Service) List(ctx context.Context, status models.BandRegistrationStatus) ([]models.BandRegistrationRequest, error) {
	if _, err := s.Expire(ctx); err != nil {
		return nil, err
	}
	query := s.crossBand(ctx).Model(&models.BandRegistrationRequest{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var requests []models.BandRegistrationRequest
	err := query.Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, created_at DESC").Find(&requests).Error
	return requests, err
}

func (s *Service) Get(ctx context.Context, id int64) (*models.BandRegistrationRequest, error) {
	var request models.BandRegistrationRequest
	if err := s.crossBand(ctx).First(&request, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &request, nil
}

type ApproveInput struct {
	BandName      string
	BandSlug      string
	AdminUsername string
	ContactEmail  string
	DecidedByID   int64
	DecidedByName string
}

// Approve creates the tenant and its first administrator atomically.
func (s *Service) Approve(ctx context.Context, id int64, input ApproveInput) (*models.BandRegistrationRequest, error) {
	slug, name, err := platform.ValidateBandIdentity(input.BandSlug, input.BandName)
	if err != nil {
		return nil, err
	}
	username, err := auth.NormalizeUsername(input.AdminUsername)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(input.ContactEmail)
	if email == "" {
		return nil, errors.New("registration: contact email is required")
	}

	var approved models.BandRegistrationRequest
	err = s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Transaction(func(tx *gorm.DB) error {
		var request models.BandRegistrationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&request, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		now := time.Now().UTC()
		if request.Status != models.BandRegistrationPending {
			return ErrNotPending
		}
		if !now.Before(request.ExpiresAt) {
			return ErrExpired
		}

		band, err := s.platform.CreateBandInTransaction(ctx, tx, slug, name, email)
		if err != nil {
			return err
		}
		user, code, err := s.auth.CreateUserInTransaction(ctx, tx, &band.ID, username, models.RoleBandAdmin)
		if err != nil {
			return err
		}
		encrypted, err := s.auth.Cipher().Encrypt(code)
		if err != nil {
			return fmt.Errorf("encrypt setup code: %w", err)
		}
		availableUntil := now.Add(decisionTTL)
		if user.SetupCodeExpiresAt != nil && user.SetupCodeExpiresAt.Before(availableUntil) {
			availableUntil = *user.SetupCodeExpiresAt
		}
		updates := map[string]any{
			"final_band_name":             name,
			"final_band_slug":             slug,
			"final_admin_username":        username,
			"final_contact_email":         email,
			"status":                      models.BandRegistrationApproved,
			"decided_by_user_id":          input.DecidedByID,
			"decided_by_username":         input.DecidedByName,
			"decided_at":                  now,
			"band_id":                     band.ID,
			"admin_user_id":               user.ID,
			"setup_code_encrypted":        encrypted,
			"credentials_available_until": availableUntil,
			"expires_at":                  availableUntil,
			"updated_at":                  now,
		}
		if err := tx.Model(&request).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&approved, id).Error; err != nil {
			return err
		}
		return nil
	})
	return &approved, err
}

// Reject closes a pending request without creating tenant data.
func (s *Service) Reject(ctx context.Context, id, decidedByID int64, decidedByName, note string) (*models.BandRegistrationRequest, error) {
	note = strings.TrimSpace(note)
	if len(note) > 1000 {
		return nil, ErrDecisionNoteTooLong
	}
	var rejected models.BandRegistrationRequest
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Transaction(func(tx *gorm.DB) error {
		var request models.BandRegistrationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&request, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		now := time.Now().UTC()
		if request.Status != models.BandRegistrationPending {
			return ErrNotPending
		}
		if !now.Before(request.ExpiresAt) {
			return ErrExpired
		}
		if err := tx.Model(&request).Updates(map[string]any{
			"status":              models.BandRegistrationRejected,
			"decision_note":       note,
			"decided_by_user_id":  decidedByID,
			"decided_by_username": decidedByName,
			"decided_at":          now,
			"expires_at":          now.Add(decisionTTL),
			"updated_at":          now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&rejected, id).Error
	})
	return &rejected, err
}

type Credentials struct {
	BandSlug  string
	Username  string
	SetupCode string
	ExpiresAt time.Time
}

// Claim decrypts and consumes the handover exactly once.
func (s *Service) Claim(ctx context.Context, token string) (*Credentials, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotFound
	}
	var credentials Credentials
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Transaction(func(tx *gorm.DB) error {
		var request models.BandRegistrationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ?", auth.HashToken(token)).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		now := time.Now().UTC()
		if !now.Before(request.ExpiresAt) {
			return ErrExpired
		}
		if request.Status != models.BandRegistrationApproved {
			return ErrNotApproved
		}
		if request.ClaimedAt != nil {
			return ErrCredentialsClaimed
		}
		if request.SetupCodeEncrypted == "" || request.CredentialsAvailableUntil == nil || !now.Before(*request.CredentialsAvailableUntil) {
			return ErrCredentialsUnavailable
		}
		code, err := s.auth.Cipher().Decrypt(request.SetupCodeEncrypted)
		if err != nil {
			return err
		}
		if err := tx.Model(&request).Updates(map[string]any{
			"claimed_at":           now,
			"setup_code_encrypted": "",
			"updated_at":           now,
		}).Error; err != nil {
			return err
		}
		credentials = Credentials{
			BandSlug:  request.FinalBandSlug,
			Username:  request.FinalAdminUsername,
			SetupCode: code,
			ExpiresAt: *request.CredentialsAvailableUntil,
		}
		return nil
	})
	return &credentials, err
}

// Expire closes stale status links and removes any recoverable code material.
func (s *Service) Expire(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	result := s.crossBand(ctx).Model(&models.BandRegistrationRequest{}).
		Where("status <> ? AND expires_at <= ?", models.BandRegistrationExpired, now).
		Updates(map[string]any{
			"status":               models.BandRegistrationExpired,
			"setup_code_encrypted": "",
			"updated_at":           now,
		})
	return result.RowsAffected, result.Error
}
