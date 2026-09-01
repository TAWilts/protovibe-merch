package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/mailer"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

const (
	passwordResetTTL         = 15 * time.Minute
	passwordResetCooldown    = time.Minute
	passwordResetMaxAttempts = 5
)

type requestPasswordResetRequest struct {
	Username string `json:"username" binding:"required"`
}

// requestSystemAdminPasswordReset intentionally returns the same answer for
// every username and every delivery outcome. A caller cannot use it to list
// platform accounts or configured private addresses.
func (s *Server) requestSystemAdminPasswordReset(c *gin.Context) {
	var req requestPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	defer c.JSON(http.StatusAccepted, gin.H{
		"message": "if an eligible system administrator exists, a reset code has been sent",
	})

	ctx := c.Request.Context()
	db := s.db.WithContext(tenant.WithCrossBandAccess(ctx))
	var user models.User
	err := db.Where("band_id IS NULL AND username = ? AND role = ? AND is_active = ?",
		strings.TrimSpace(req.Username), models.RoleSystemAdmin, true).First(&user).Error
	if err != nil || strings.TrimSpace(user.ContactEmail) == "" {
		return
	}

	var existing models.PasswordResetChallenge
	if err := db.First(&existing, "user_id = ?", user.ID).Error; err == nil &&
		time.Since(existing.RequestedAt) < passwordResetCooldown {
		return
	}

	mailSettings, err := s.outgoingMailSettings(ctx)
	if err != nil || !mailSettings.Enabled {
		return
	}
	code, err := auth.RandomCode(12)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	challenge := models.PasswordResetChallenge{
		UserID: user.ID, CodeHash: auth.HashCode(code), ExpiresAt: now.Add(passwordResetTTL),
		RequestedAt: now,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"code_hash", "expires_at", "requested_at", "failed_attempts",
		}),
	}).Create(&challenge).Error; err != nil {
		return
	}

	err = mailer.Send(ctx, mailSettings, mailer.Message{
		To: user.ContactEmail, Subject: "Merch Manager: Passwort zuruecksetzen",
		Body: fmt.Sprintf("Fuer das System-Admin-Konto %s wurde ein Passwort-Reset angefordert.\n\n"+
			"Einmalcode: %s\n\nDer Code ist 15 Minuten gueltig und kann nur einmal verwendet werden.\n"+
			"Falls du den Reset nicht angefordert hast, ignoriere diese Nachricht.", user.Username, code),
	})
	if err != nil {
		_ = db.Where("user_id = ? AND code_hash = ?", user.ID, challenge.CodeHash).
			Delete(&models.PasswordResetChallenge{}).Error
		return
	}
	s.audit.Log(ctx, audit.Actor{UserID: &user.ID, Username: user.Username, IPAddress: c.ClientIP()},
		audit.Entry{Action: "auth.password_reset_requested", EntityType: "user", EntityID: &user.ID})
}

type confirmPasswordResetRequest struct {
	Username    string `json:"username" binding:"required"`
	Code        string `json:"code" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func (s *Server) confirmSystemAdminPasswordReset(c *gin.Context) {
	var req confirmPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	passwordHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	var user models.User
	err = s.db.WithContext(ctx).Where("band_id IS NULL AND username = ? AND role = ? AND is_active = ?",
		strings.TrimSpace(req.Username), models.RoleSystemAdmin, true).First(&user).Error
	if err != nil {
		fail(c, http.StatusUnauthorized, "invalid_reset_code", "the reset code is invalid or expired")
		return
	}

	var challenge models.PasswordResetChallenge
	err = s.db.WithContext(ctx).First(&challenge, "user_id = ?", user.ID).Error
	if err != nil || time.Now().UTC().After(challenge.ExpiresAt) ||
		challenge.FailedAttempts >= passwordResetMaxAttempts {
		_ = s.db.WithContext(ctx).Where("user_id = ?", user.ID).Delete(&models.PasswordResetChallenge{}).Error
		fail(c, http.StatusUnauthorized, "invalid_reset_code", "the reset code is invalid or expired")
		return
	}

	wanted := []byte(challenge.CodeHash)
	got := []byte(auth.HashCode(req.Code))
	if len(wanted) != len(got) || subtle.ConstantTimeCompare(wanted, got) != 1 {
		challenge.FailedAttempts++
		query := s.db.WithContext(ctx).Model(&challenge).Update("failed_attempts", challenge.FailedAttempts)
		if challenge.FailedAttempts >= passwordResetMaxAttempts {
			query = s.db.WithContext(ctx).Delete(&challenge)
		}
		if query.Error != nil {
			serverError(c, query.Error)
			return
		}
		fail(c, http.StatusUnauthorized, "invalid_reset_code", "the reset code is invalid or expired")
		return
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"password_hash": passwordHash, "must_set_password": false,
			"setup_code_hash": "", "setup_code_expires_at": nil,
			"session_version": gorm.Expr("session_version + 1"),
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&models.PasswordResetChallenge{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&models.PendingAuth{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", user.ID).Delete(&models.Session{}).Error
	})
	if err != nil {
		serverError(c, err)
		return
	}
	s.audit.Log(c.Request.Context(), audit.Actor{UserID: &user.ID, Username: user.Username, IPAddress: c.ClientIP()},
		audit.Entry{Action: audit.ActionPasswordReset, EntityType: "user", EntityID: &user.ID})
	c.Status(http.StatusNoContent)
}

func (s *Server) outgoingMailSettings(ctx context.Context) (mailer.Settings, error) {
	settings, err := s.platformSettings(ctx)
	if err != nil {
		return mailer.Settings{}, err
	}
	password := ""
	if settings.SMTPPasswordEncrypted != "" {
		password, err = s.auth.Cipher().Decrypt(settings.SMTPPasswordEncrypted)
		if err != nil {
			return mailer.Settings{}, err
		}
	}
	return mailer.Settings{
		Enabled: settings.SMTPEnabled, Host: settings.SMTPHost, Port: settings.SMTPPort,
		Security: settings.SMTPSecurity, Username: settings.SMTPUsername, Password: password,
		From: settings.SMTPFrom, Timeout: time.Duration(settings.SMTPTimeoutSeconds) * time.Second,
	}, nil
}

func normalizeEmail(value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" && allowEmpty {
		return "", nil
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || parsed.Name != "" || len(value) > 254 {
		return "", errors.New("enter a valid email address")
	}
	return value, nil
}
