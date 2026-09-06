package auth

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// EnsureBootstrapAdmin creates the very first system administrator when the
// instance has no platform account yet.
//
// It runs only once: as soon as any platform account exists the function is a
// no-op, so leaving BOOTSTRAP_ADMIN_PASSWORD in the environment cannot reset
// or duplicate the account. The new account still has to enrol two-factor
// authentication on its first login, because that is mandatory for its role.
func (s *Service) EnsureBootstrapAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}

	db := s.accountsDB(ctx)

	var existing int64
	if err := db.Model(&models.User{}).
		Where("role IN ?", []models.Role{models.RoleSystemAdmin, models.RoleSupportAdmin}).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	name, err := NormalizeUsername(username)
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	user := &models.User{
		Username:              name,
		PasswordHash:          hash,
		Role:                  models.RoleSystemAdmin,
		IsActive:              true,
		MFARecoveryCodeHashes: models.JSONSlice{},
	}
	if err := db.Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrUsernameTaken
		}
		return err
	}

	slog.Warn("bootstrap system administrator created; it must enrol two-factor authentication on first login",
		"username", name)
	return nil
}

// EnsurePlatformSettings makes sure the single settings row exists even when
// the instance was migrated from a dump that lacked it.
func EnsurePlatformSettings(ctx context.Context, database *gorm.DB) error {
	db := database.WithContext(tenant.WithCrossBandAccess(ctx))

	var count int64
	if err := db.Model(&models.PlatformSettings{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Create(&models.PlatformSettings{
		ID:                       1,
		DefaultStorageQuotaBytes: 5 * 1024 * 1024 * 1024,
	}).Error
}
