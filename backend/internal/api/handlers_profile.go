package api

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// availableThemes mirror the CSS themes shipped by the frontend
// (frontend/src/assets/theme.css, ported from _old/static/app.css).
var availableThemes = []string{"aurora", "ocean", "sunset", "forest", "midnight"}

// availableLanguages are the locales the frontend ships a catalogue for.
var availableLanguages = []string{"de", "en"}

func (s *Server) registerProfileRoutes(g *gin.RouterGroup) {
	// The step-up confirmation itself must be reachable without a fresh
	// confirmation, otherwise there would be no way to obtain one.
	g.POST("/profile/reauth", requireAuth(), s.reauthenticate)

	p := g.Group("/profile", requireAuth(), s.requireFreshReauth())
	p.GET("", s.getProfile)
	p.PATCH("/personalization", s.updatePersonalization)
	p.POST("/password", s.changeOwnPassword)
	p.POST("/username", s.changeOwnUsername)
	p.PUT("/contact-email", s.updateOwnContactEmail)
	p.POST("/mfa/disable", s.disableMFA)
	p.POST("/mfa/recovery-codes", s.regenerateRecoveryCodes)

	// POS mode is a per-session switch, not a profile setting. Entering stays
	// quick; leaving verifies live credentials inside togglePOSMode.
	g.POST("/session/pos-mode", requireAuth(), s.togglePOSMode)
}

type reauthRequest struct {
	Password string `json:"password" binding:"required"`
	// Code is required when the account has a second factor, matching the
	// original's verify_admin_sensitive_action.
	Code string `json:"code"`
}

// reauthenticate opens the step-up window that guards the profile and every
// destructive administrative action.
func (s *Server) reauthenticate(c *gin.Context) {
	var req reauthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	if !s.confirmCurrentCredentials(c, req.Password, req.Code) {
		return
	}

	if err := s.auth.MarkReauthenticated(c.Request.Context(), state.Session); err != nil {
		serverError(c, err)
		return
	}
	s.audit.Log(c.Request.Context(), actorFrom(c),
		audit.Entry{Action: audit.ActionReauthenticated, EntityType: "session"})

	c.JSON(http.StatusOK, gin.H{"valid_for_seconds": int(s.auth.ReauthWindow().Seconds())})
}

// confirmCurrentCredentials verifies a live password (and, where configured,
// the second factor). It deliberately does not honour an existing reauth
// window: leaving POS mode must always require the person holding the device
// to unlock it, matching the predecessor's protected exit.
func (s *Server) confirmCurrentCredentials(c *gin.Context, password, code string) bool {
	state := stateFrom(c)
	if !auth.VerifyPassword(password, state.User.PasswordHash) {
		fail(c, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
		return false
	}

	if !state.Caps.SensitiveActionMFARequired {
		return true
	}
	if code == "" {
		fail(c, http.StatusUnauthorized, "mfa_required", "an authentication code is required")
		return false
	}
	if err := s.auth.VerifySecondFactor(state.User, code); err != nil {
		s.reportAuthError(c, err)
		return false
	}
	if err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Model(state.User).Update("mfa_recovery_code_hashes", state.User.MFARecoveryCodeHashes).Error; err != nil {
		serverError(c, err)
		return false
	}
	return true
}

func (s *Server) getProfile(c *gin.Context) {
	state := stateFrom(c)
	payload := s.identityPayload(c.Request.Context(), state.User, state.Grant)
	payload.POSMode = state.Session.POSMode
	c.JSON(http.StatusOK, gin.H{
		"profile":             payload,
		"available_themes":    availableThemes,
		"available_languages": availableLanguages,
		"last_login_at":       state.User.LastLoginAt,
		"mfa_enrolled_at":     state.User.MFAEnrolledAt,
		"recovery_codes_left": len(state.User.MFARecoveryCodeHashes),
	})
}

type personalizationRequest struct {
	UITheme           *string `json:"ui_theme"`
	UILanguage        *string `json:"ui_language"`
	ShowVariantPhotos *bool   `json:"show_variant_photos"`
}

// updatePersonalization stores the presentation preferences. They belong to
// the person, not the band, so one seller enabling product photos never
// changes what anyone else sees.
func (s *Server) updatePersonalization(c *gin.Context) {
	var req personalizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	updates := map[string]any{}

	if req.UITheme != nil {
		if !slices.Contains(availableThemes, *req.UITheme) {
			fail(c, http.StatusBadRequest, "unknown_theme", "unknown theme")
			return
		}
		updates["ui_theme"] = *req.UITheme
		state.User.UITheme = *req.UITheme
	}
	if req.UILanguage != nil {
		if !slices.Contains(availableLanguages, *req.UILanguage) {
			fail(c, http.StatusBadRequest, "unknown_language", "unknown language")
			return
		}
		updates["ui_language"] = *req.UILanguage
		state.User.UILanguage = *req.UILanguage
	}
	if req.ShowVariantPhotos != nil {
		updates["show_variant_photos"] = *req.ShowVariantPhotos
		state.User.ShowVariantPhotos = *req.ShowVariantPhotos
	}
	if len(updates) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	if err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Model(state.User).Updates(updates).Error; err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ui_theme":            state.User.UITheme,
		"ui_language":         state.User.UILanguage,
		"show_variant_photos": state.User.ShowVariantPhotos,
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// changeOwnPassword sets a new password and signs the account out everywhere,
// which is what makes a suspected leak actually recoverable.
func (s *Server) changeOwnPassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	if !auth.VerifyPassword(req.CurrentPassword, state.User.PasswordHash) {
		fail(c, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
		return
	}
	if err := s.auth.SetPassword(c.Request.Context(), state.User, req.NewPassword); err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.audit.Log(c.Request.Context(), actorFrom(c),
		audit.Entry{Action: audit.ActionPasswordChanged, EntityType: "user", EntityID: &state.User.ID})
	s.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"signed_out": true})
}

type changeUsernameRequest struct {
	Username string `json:"username" binding:"required"`
}

func (s *Server) changeOwnUsername(c *gin.Context) {
	var req changeUsernameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	name, err := auth.NormalizeUsername(req.Username)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	state := stateFrom(c)
	previous := state.User.Username
	if name == previous {
		c.JSON(http.StatusOK, gin.H{"username": name})
		return
	}

	ctx := c.Request.Context()
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Model(state.User).Update("username", name).Error; err != nil {
		s.reportAuthError(c, err)
		return
	}
	state.User.Username = name

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action:     audit.ActionUsernameChanged,
		EntityType: "user",
		EntityID:   &state.User.ID,
		// Historic bookings keep the old name as a snapshot, so recording the
		// rename here is what ties the two together later.
		Details: map[string]any{"from": previous, "to": name},
	})
	c.JSON(http.StatusOK, gin.H{"username": name})
}

type contactEmailRequest struct {
	ContactEmail string `json:"contact_email" binding:"required"`
}

func (s *Server) updateOwnContactEmail(c *gin.Context) {
	state := stateFrom(c)
	if !state.User.Role.IsPlatformRole() {
		forbidden(c, "platform_only", "only platform accounts have a recovery address")
		return
	}
	var req contactEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	email, err := normalizeEmail(req.ContactEmail, false)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_email", err.Error())
		return
	}
	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(state.User).Update("contact_email", email).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", state.User.ID).Delete(&models.PasswordResetChallenge{}).Error
	})
	if err != nil {
		serverError(c, err)
		return
	}
	state.User.ContactEmail = email
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{Action: "user.contact_email_changed",
		EntityType: "user", EntityID: &state.User.ID})
	c.JSON(http.StatusOK, gin.H{"contact_email": email})
}

type posModeRequest struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

// togglePOSMode switches the restricted point-of-sale mode for this session
// only, so one phone on the merch table can be locked down without affecting
// anyone else's login.
func (s *Server) togglePOSMode(c *gin.Context) {
	var req posModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	if state.Session.POSMode && !req.Enabled {
		if !s.confirmCurrentCredentials(c, req.Password, req.Code) {
			return
		}
		if err := s.auth.MarkReauthenticated(c.Request.Context(), state.Session); err != nil {
			serverError(c, err)
			return
		}
		s.audit.Log(c.Request.Context(), actorFrom(c),
			audit.Entry{Action: audit.ActionReauthenticated, EntityType: "session"})
	}
	if err := s.auth.SetPOSMode(c.Request.Context(), state.Session, req.Enabled); err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pos_mode": state.Session.POSMode})
}
