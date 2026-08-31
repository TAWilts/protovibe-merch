package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Errors surfaced to the HTTP layer. They are deliberately coarse: a caller
// must not be able to tell an unknown username from a wrong password.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrAccountInactive    = errors.New("auth: account is deactivated")
	ErrBandInactive       = errors.New("auth: band is deactivated")
	ErrSetupCodeExpired   = errors.New("auth: setup code has expired")
	ErrUsernameTaken      = errors.New("auth: username is already taken")
	ErrInvalidUsername    = errors.New("auth: invalid username")
)

// Service owns every account and session operation.
type Service struct {
	db     *gorm.DB
	cipher *Cipher

	sessionTTL     time.Duration
	sessionIdleTTL time.Duration
	reauthWindow   time.Duration
	setupCodeTTL   time.Duration
	mfaIssuer      string
}

// NewService builds the auth service from the application configuration.
func NewService(database *gorm.DB, cfg *config.Config) (*Service, error) {
	box, err := NewCipher(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:             database,
		cipher:         box,
		sessionTTL:     cfg.SessionTTL,
		sessionIdleTTL: cfg.SessionIdleTTL,
		reauthWindow:   cfg.ProfileReauthWindow,
		setupCodeTTL:   cfg.AccountSetupCodeTTL,
		mfaIssuer:      cfg.MFAIssuer,
	}, nil
}

// Cipher exposes the secret box so other packages (SMTP settings) can reuse
// the same key derivation instead of inventing a second one.
func (s *Service) Cipher() *Cipher { return s.cipher }

// ReauthWindow is how long a step-up confirmation stays valid.
func (s *Service) ReauthWindow() time.Duration { return s.reauthWindow }

// accountsDB returns a handle for the account tables. Those are control-plane
// tables, so the tenant callback does not apply — but the context is still
// marked explicitly, which documents the intent at every call site.
func (s *Service) accountsDB(ctx context.Context) *gorm.DB {
	return s.db.WithContext(tenant.WithCrossBandAccess(ctx))
}

// LoginResult describes what the caller must do next after a password or
// setup code was accepted.
type LoginResult struct {
	User *models.User
	// NeedsPasswordSetup means the account signed in with its one-time setup
	// code and must now choose a password.
	NeedsPasswordSetup bool
	// NeedsMFA means a second factor is still outstanding.
	NeedsMFA bool
	// NeedsMFAEnrollment means the role requires TOTP that is not set up yet.
	NeedsMFAEnrollment bool
	// PendingToken identifies the server-side pending state for the next step.
	PendingToken string
}

// Authenticate verifies a username together with either a password or a
// one-time setup code, and reports which step comes next.
//
// Usernames are unique per band, so signing in needs the band. A nil bandID
// addresses the platform accounts.
func (s *Service) Authenticate(ctx context.Context, bandID *int64, username, secret string) (*LoginResult, error) {
	user, err := s.findUserForLogin(ctx, bandID, username)
	if err != nil {
		return nil, err
	}

	// A user with a pending setup code may sign in with either the code or,
	// once chosen, their password. Checking the code first keeps the flow
	// working when a password hash placeholder is still stored.
	authenticated := false
	usedSetupCode := false
	if user.MustSetPassword && user.SetupCodeHash != "" {
		if EqualTokens(user.SetupCodeHash, HashCode(secret)) {
			if user.SetupCodeExpiresAt != nil && time.Now().UTC().After(*user.SetupCodeExpiresAt) {
				return nil, ErrSetupCodeExpired
			}
			authenticated = true
			usedSetupCode = true
		}
	}
	if !authenticated && VerifyPassword(secret, user.PasswordHash) {
		authenticated = true
	}
	if !authenticated {
		return nil, ErrInvalidCredentials
	}

	result := &LoginResult{User: user}

	switch {
	case usedSetupCode || user.MustSetPassword:
		result.NeedsPasswordSetup = true
		result.PendingToken, err = s.createPendingAuth(ctx, user.ID, models.PendingAuthPasswordSetup, 30*time.Minute)
	case user.MFAEnabled:
		result.NeedsMFA = true
		result.PendingToken, err = s.createPendingAuth(ctx, user.ID, models.PendingAuthMFALogin, 10*time.Minute)
	case user.MFARequired():
		// Platform accounts must enrol before they can do anything else.
		result.NeedsMFAEnrollment = true
		result.PendingToken, err = s.createPendingAuth(ctx, user.ID, models.PendingAuthMFAEnrollment, 30*time.Minute)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// findUserForLogin loads the account and rejects deactivated accounts and
// bands. The lookups deliberately return the same error for "unknown user" and
// "wrong password" so the endpoint does not confirm which usernames exist.
func (s *Service) findUserForLogin(ctx context.Context, bandID *int64, username string) (*models.User, error) {
	name := strings.TrimSpace(username)
	if name == "" {
		return nil, ErrInvalidCredentials
	}

	query := s.accountsDB(ctx).Where("username = ?", name)
	if bandID == nil {
		query = query.Where("band_id IS NULL")
	} else {
		query = query.Where("band_id = ?", *bandID)
	}

	var user models.User
	if err := query.First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Spend comparable time so the response does not reveal whether
			// the username exists.
			VerifyPassword("decoy", "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.IsActive {
		return nil, ErrAccountInactive
	}
	if user.BandID != nil {
		var band models.Band
		if err := s.accountsDB(ctx).First(&band, *user.BandID).Error; err != nil {
			return nil, err
		}
		if !band.IsActive || band.DeletedAt != nil {
			return nil, ErrBandInactive
		}
	}
	return &user, nil
}

// CreateUser adds an account and returns the one-time setup code, which is
// shown to the administrator exactly once.
func (s *Service) CreateUser(ctx context.Context, bandID *int64, username string, role models.Role) (*models.User, string, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return nil, "", err
	}
	if !role.Valid() {
		return nil, "", fmt.Errorf("auth: unknown role %q", role)
	}
	if role.IsPlatformRole() != (bandID == nil) {
		return nil, "", errors.New("auth: platform roles must have no band and band roles must have one")
	}

	code, err := RandomCode(12)
	if err != nil {
		return nil, "", err
	}
	expires := time.Now().UTC().Add(s.setupCodeTTL)

	user := &models.User{
		BandID:   bandID,
		Username: name,
		// No password is usable until the setup code is redeemed. The stored
		// hash is a placeholder that no input can ever match.
		PasswordHash:          "!",
		Role:                  role,
		IsActive:              true,
		MustSetPassword:       true,
		SetupCodeHash:         HashCode(code),
		SetupCodeExpiresAt:    &expires,
		MFARecoveryCodeHashes: models.JSONSlice{},
	}

	if err := s.accountsDB(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, "", ErrUsernameTaken
		}
		return nil, "", err
	}
	return user, code, nil
}

// ResetPassword issues a fresh setup code and invalidates every session the
// account currently holds.
func (s *Service) ResetPassword(ctx context.Context, user *models.User) (string, error) {
	code, err := RandomCode(12)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(s.setupCodeTTL)

	user.PasswordHash = "!"
	user.MustSetPassword = true
	user.SetupCodeHash = HashCode(code)
	user.SetupCodeExpiresAt = &expires
	user.SessionVersion++

	if err := s.accountsDB(ctx).Save(user).Error; err != nil {
		return "", err
	}
	if err := s.RevokeUserSessions(ctx, user.ID); err != nil {
		return "", err
	}
	return code, nil
}

// SetPassword stores a new password and revokes existing sessions, which is
// what makes a password change effective everywhere immediately.
func (s *Service) SetPassword(ctx context.Context, user *models.User, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	user.PasswordHash = hash
	user.MustSetPassword = false
	user.SetupCodeHash = ""
	user.SetupCodeExpiresAt = nil
	user.SessionVersion++

	if err := s.accountsDB(ctx).Save(user).Error; err != nil {
		return err
	}
	return s.RevokeUserSessions(ctx, user.ID)
}

// NormalizeUsername trims and validates a username.
func NormalizeUsername(value string) (string, error) {
	name := strings.TrimSpace(value)
	if len(name) < 3 || len(name) > 150 {
		return "", fmt.Errorf("%w: 3 to 150 characters required", ErrInvalidUsername)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidUsername)
		}
	}
	return name, nil
}
