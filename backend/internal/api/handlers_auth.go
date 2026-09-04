package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/rbac"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerAuthRoutes(g *gin.RouterGroup) {
	a := g.Group("/auth")
	a.POST("/login", s.login)
	a.POST("/mfa", s.completeMFALogin)
	a.POST("/password-setup", s.completePasswordSetup)
	a.POST("/password-reset/request", s.requestSystemAdminPasswordReset)
	a.POST("/password-reset/confirm", s.confirmSystemAdminPasswordReset)
	a.POST("/logout", requireAuth(), s.logout)

	// Enrolment is reachable both from a pending login (a platform account
	// that must set up 2FA) and from an authenticated profile session.
	m := g.Group("/mfa")
	m.POST("/enrollment/start", s.startMFAEnrollment)
	m.POST("/enrollment/confirm", s.confirmMFAEnrollment)

	g.GET("/me", requireAuth(), s.me)
}

// bandLookup resolves the band a login attempt belongs to.
//
// Usernames are unique per band, so the client sends the band slug. An empty
// slug addresses the platform accounts, which belong to no band.
func (s *Server) bandLookup(c *gin.Context, slug string) (*int64, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, true
	}

	var band models.Band
	err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		First(&band, "slug = ?", slug).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Do not confirm which bands exist on this instance.
			fail(c, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
			return nil, false
		}
		serverError(c, err)
		return nil, false
	}
	if !band.IsActive || band.DeletedAt != nil {
		fail(c, http.StatusForbidden, "band_inactive", "this band is deactivated")
		return nil, false
	}
	return &band.ID, true
}

type loginRequest struct {
	// Band is the band's slug. Empty means a platform account.
	Band     string `json:"band"`
	Username string `json:"username" binding:"required"`
	// Secret is either the password or the one-time setup code.
	Secret string `json:"secret" binding:"required"`
}

// loginResponse tells the client which step comes next. Exactly one of the
// three "needs" flags is set, or none when the session is already usable.
type loginResponse struct {
	NeedsPasswordSetup bool        `json:"needs_password_setup"`
	NeedsMFA           bool        `json:"needs_mfa"`
	NeedsMFAEnrollment bool        `json:"needs_mfa_enrollment"`
	PendingToken       string      `json:"pending_token,omitempty"`
	Session            *meResponse `json:"session,omitempty"`
	CSRFToken          string      `json:"csrf_token,omitempty"`
}

func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	bandID, ok := s.bandLookup(c, req.Band)
	if !ok {
		return
	}

	result, err := s.auth.Authenticate(c.Request.Context(), bandID, req.Username, req.Secret)
	if err != nil {
		s.audit.Log(c.Request.Context(), audit.Actor{IPAddress: c.ClientIP(), Username: req.Username},
			audit.Entry{Action: audit.ActionLoginFailed, EntityType: "user"})
		s.reportAuthError(c, err)
		return
	}

	switch {
	case result.NeedsPasswordSetup:
		c.JSON(http.StatusOK, loginResponse{NeedsPasswordSetup: true, PendingToken: result.PendingToken})
	case result.NeedsMFA:
		c.JSON(http.StatusOK, loginResponse{NeedsMFA: true, PendingToken: result.PendingToken})
	case result.NeedsMFAEnrollment:
		c.JSON(http.StatusOK, loginResponse{NeedsMFAEnrollment: true, PendingToken: result.PendingToken})
	default:
		s.establishSession(c, result.User)
	}
}

type mfaLoginRequest struct {
	PendingToken string `json:"pending_token" binding:"required"`
	Code         string `json:"code" binding:"required"`
}

func (s *Server) completeMFALogin(c *gin.Context) {
	var req mfaLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	user, err := s.auth.PeekPendingAuth(ctx, req.PendingToken, models.PendingAuthMFALogin)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	if err := s.auth.VerifySecondFactor(user, req.Code); err != nil {
		s.reportAuthError(c, err)
		return
	}
	// A consumed recovery code must be persisted, otherwise it would stay
	// usable after a failed follow-up step.
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Model(user).Update("mfa_recovery_code_hashes", user.MFARecoveryCodeHashes).Error; err != nil {
		serverError(c, err)
		return
	}
	if _, err := s.auth.ConsumePendingAuth(ctx, req.PendingToken, models.PendingAuthMFALogin); err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.establishSession(c, user)
}

type passwordSetupRequest struct {
	PendingToken string `json:"pending_token" binding:"required"`
	Password     string `json:"password" binding:"required"`
}

func (s *Server) completePasswordSetup(c *gin.Context) {
	var req passwordSetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	// Validate before consuming the one-time token. A typing error must not
	// strand the account and force an administrator to issue a new setup code.
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.reportAuthError(c, err)
		return
	}
	user, err := s.auth.ConsumePendingAuth(ctx, req.PendingToken, models.PendingAuthPasswordSetup)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}
	if err := s.auth.SetPassword(ctx, user, req.Password); err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.audit.Log(ctx, audit.Actor{UserID: &user.ID, Username: user.Username, IPAddress: c.ClientIP()},
		audit.Entry{Action: audit.ActionPasswordChanged, EntityType: "user", EntityID: &user.ID})

	// A platform account still has to enrol its second factor before it gets
	// a usable session, unless the explicit local-development bypass is active.
	if user.MFARequired() && !user.MFAEnabled && !s.auth.PlatformMFABypassed(user) {
		token, err := s.auth.NewEnrollmentPending(ctx, user.ID)
		if err != nil {
			serverError(c, err)
			return
		}
		c.JSON(http.StatusOK, loginResponse{NeedsMFAEnrollment: true, PendingToken: token})
		return
	}
	s.establishSession(c, user)
}

func (s *Server) logout(c *gin.Context) {
	state := stateFrom(c)
	if err := s.auth.DeleteSession(c.Request.Context(), state.Session.ID); err != nil {
		serverError(c, err)
		return
	}
	s.clearSessionCookie(c)
	s.audit.Log(c.Request.Context(), actorFrom(c),
		audit.Entry{Action: audit.ActionLogout, EntityType: "session"})
	c.Status(http.StatusNoContent)
}

// establishSession creates the cookie session and returns the identity payload
// the frontend bootstraps from.
func (s *Server) establishSession(c *gin.Context, user *models.User) {
	ctx := c.Request.Context()
	sessionToken, csrfToken, err := s.auth.CreateSession(ctx, user, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		serverError(c, err)
		return
	}
	s.setSessionCookie(c, sessionToken)
	s.setCSRFCookie(c, csrfToken)

	s.audit.Log(ctx, audit.Actor{UserID: &user.ID, Username: user.Username, IPAddress: c.ClientIP()},
		audit.Entry{Action: audit.ActionLogin, EntityType: "user", EntityID: &user.ID})

	c.JSON(http.StatusOK, loginResponse{
		Session:   s.identityPayload(ctx, user, nil),
		CSRFToken: csrfToken,
	})
}

// reportAuthError maps the auth package's errors onto stable API codes without
// leaking which part of a credential was wrong.
func (s *Server) reportAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials),
		errors.Is(err, auth.ErrNoSession):
		fail(c, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	case errors.Is(err, auth.ErrInvalidMFACode):
		fail(c, http.StatusUnauthorized, "invalid_mfa_code", "invalid authentication code")
	case errors.Is(err, auth.ErrMFANotEnrolled):
		fail(c, http.StatusBadRequest, "mfa_not_enrolled", "no second factor is enrolled")
	case errors.Is(err, auth.ErrNoPendingSecret):
		fail(c, http.StatusBadRequest, "no_pending_enrollment", "no pending enrolment to confirm")
	case errors.Is(err, auth.ErrSessionExpired):
		fail(c, http.StatusUnauthorized, "expired", "this step expired; start again")
	case errors.Is(err, auth.ErrAccountInactive):
		fail(c, http.StatusForbidden, "account_inactive", "this account is deactivated")
	case errors.Is(err, auth.ErrBandInactive):
		fail(c, http.StatusForbidden, "band_inactive", "this band is deactivated")
	case errors.Is(err, auth.ErrSetupCodeExpired):
		fail(c, http.StatusForbidden, "setup_code_expired", "the setup code has expired; ask for a new one")
	case errors.Is(err, auth.ErrWeakPassword):
		fail(c, http.StatusBadRequest, "weak_password", err.Error())
	case errors.Is(err, auth.ErrUsernameTaken):
		fail(c, http.StatusConflict, "username_taken", "this username is already taken")
	case errors.Is(err, auth.ErrInvalidUsername):
		fail(c, http.StatusBadRequest, "invalid_username", err.Error())
	case errors.Is(err, auth.ErrDecrypt):
		fail(c, http.StatusInternalServerError, "secret_unreadable",
			"stored second-factor secret cannot be read; SECRET_KEY may have changed")
	default:
		serverError(c, err)
	}
}

// meResponse is the identity bootstrap the Vue app reads on start.
type meResponse struct {
	User struct {
		ID                int64       `json:"id"`
		Username          string      `json:"username"`
		Role              models.Role `json:"role"`
		UITheme           string      `json:"ui_theme"`
		UILanguage        string      `json:"ui_language"`
		ShowVariantPhotos bool        `json:"show_variant_photos"`
		MFAEnabled        bool        `json:"mfa_enabled"`
		ContactEmail      string      `json:"contact_email"`
	} `json:"user"`
	Band         *bandSummary        `json:"band,omitempty"`
	Capabilities rbac.Capabilities   `json:"capabilities"`
	POSMode      bool                `json:"pos_mode"`
	SupportGrant *supportGrantBanner `json:"support_grant,omitempty"`
}

type bandSummary struct {
	ID                 int64               `json:"id"`
	Slug               string              `json:"slug"`
	Name               string              `json:"name"`
	FeatureFlags       featureFlagsPayload `json:"feature_flags"`
	MaintenanceMessage string              `json:"maintenance_message,omitempty"`
}

// supportGrantBanner drives the persistent notice both sides see while a
// support session is live.
type supportGrantBanner struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at"`
	Username  string `json:"username"`
}

func (s *Server) me(c *gin.Context) {
	state := stateFrom(c)
	payload := s.identityPayload(c.Request.Context(), state.User, state.Grant)
	payload.POSMode = state.Session.POSMode
	c.JSON(http.StatusOK, payload)
}

// identityPayload assembles the bootstrap object. The band summary is only
// filled in when the account actually has band access — a platform account
// without a live grant deliberately sees no band at all.
func (s *Server) identityPayload(ctx context.Context, user *models.User, grant *models.SupportAccessGrant) *meResponse {
	caps := rbac.For(user)
	if s.auth.PlatformMFABypassed(user) {
		caps.MFARequired = false
		caps.SensitiveActionMFARequired = false
	}
	payload := &meResponse{Capabilities: caps}
	payload.User.ID = user.ID
	payload.User.Username = user.Username
	payload.User.Role = user.Role
	payload.User.UITheme = user.UITheme
	payload.User.UILanguage = user.UILanguage
	payload.User.ShowVariantPhotos = user.ShowVariantPhotos
	payload.User.MFAEnabled = user.MFAEnabled
	payload.User.ContactEmail = user.ContactEmail

	bandID := user.BandID
	if grant != nil {
		bandID = &grant.BandID
	} else if user.BandID != nil {
		// The band's own members have no acting grant on their session, so the
		// live grant on their band has to be looked up. Without this the notice
		// only ever appeared to support, which is the side that already knows.
		if live, err := s.platform.LiveGrantForBand(ctx, *user.BandID); err == nil {
			grant = live
		}
	}
	if grant != nil {
		payload.SupportGrant = &supportGrantBanner{
			ID:       grant.ID,
			Scope:    string(grant.Scope),
			Reason:   grant.Reason,
			Username: grant.RequestedByUsername,
		}
		if grant.ExpiresAt != nil {
			payload.SupportGrant.ExpiresAt = grant.ExpiresAt.Format(time.RFC3339)
		}
	}

	if bandID != nil {
		var band models.Band
		if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).First(&band, *bandID).Error; err == nil {
			payload.Band = &bandSummary{
				ID: band.ID, Slug: band.Slug, Name: band.Name,
				FeatureFlags: featureFlagsPayload{
					Slideshow:    band.FeatureFlags.SlideshowEnabled(),
					BandFinances: band.FeatureFlags.BandFinancesEnabled(),
					PaymentQR:    band.FeatureFlags.PaymentQREnabled(),
					OfflineSales: band.FeatureFlags.OfflineSalesEnabled(),
					CSVImport:    band.FeatureFlags.CSVImportEnabled(),
				},
				MaintenanceMessage: band.MaintenanceMessage,
			}
		}
	}
	return payload
}
