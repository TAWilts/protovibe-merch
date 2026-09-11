package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sandbox"
)

const (
	sandboxAPIPrefix         = "/api/v1/sandbox"
	sandboxSessionCookieName = "merch_sandbox_session"
	sandboxCSRFCookieName    = "merch_sandbox_csrf"
)

type sandboxSessionResponse struct {
	Session   *meResponse `json:"session"`
	CSRFToken string      `json:"csrf_token"`
}

func (s *Server) registerSandboxRoutes(api *gin.RouterGroup) {
	g := api.Group("/sandbox")
	g.POST("/session", s.startSandbox)
	g.GET("/me", requireAuth(), requireSandbox(), s.me)
	g.POST("/reset", requireAuth(), requireSandbox(), s.resetSandbox)
	g.PATCH("/role", requireAuth(), requireSandbox(), s.changeSandboxRole)
	g.PATCH("/tutorial", requireAuth(), requireSandbox(), s.changeSandboxTutorial)
	g.DELETE("/session", requireAuth(), requireSandbox(), s.discardSandbox)
	g.PATCH("/profile/features", requireAuth(), requireSandbox(), requireBandAccount(), requireBandRole(models.RoleMember), s.updateFeatureVisibility)
	g.GET("/payment-qr/availability", requireAuth(), requireSandbox(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"paypal": false, "bank": false})
	})
	blocked := g.Group("", requireAuth(), requireSandbox())
	for _, path := range []string{
		"/payment-qr/intents", "/payment-qr/intents/:token/cancel", "/payment-qr/settings",
		"/support-messages",
		"/platform/*path",
		"/band-admin/*path",
	} {
		blocked.Any(path, sandboxExternalActionBlocked)
	}

	band := g.Group("", requireSandbox(), s.trackSandboxProgress())
	s.registerBandRoutes(band)
	s.registerCatalogueRoutes(band)
	s.registerSalesRoutes(band)
	s.registerPurchaseRoutes(band)
	s.registerUploadRoutes(band)
	s.registerReportRoutes(band)
	s.registerExportRoutes(band)
	s.registerImportRoutes(band)
	s.registerPhotoRoutes(band)
	s.registerPackingRoutes(band)
}

func sandboxExternalActionBlocked(c *gin.Context) {
	fail(c, http.StatusForbidden, "sandbox_external_action_blocked",
		"this function is disabled in the sandbox because it would affect external systems or production administration")
}

func requireSandbox() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if state.Sandbox == nil {
			forbidden(c, "sandbox_required", "a sandbox session is required")
			return
		}
		c.Next()
	}
}

func (s *Server) startSandbox(c *gin.Context) {
	if !s.sandboxes.Enabled() {
		fail(c, http.StatusNotFound, "sandbox_disabled", "the sandbox is disabled")
		return
	}
	current := stateFrom(c)

	var sourceUserID *int64
	if token, err := c.Cookie(sessionCookieName); err == nil && token != "" {
		if bundle, loadErr := s.auth.LoadSession(c.Request.Context(), token); loadErr == nil && bundle.Session.SandboxEnvironmentID == nil {
			id := bundle.User.ID
			sourceUserID = &id
		}
	}
	if current != nil && current.Sandbox != nil && (sourceUserID == nil ||
		(current.Sandbox.SourceUserID != nil && *current.Sandbox.SourceUserID == *sourceUserID)) {
		s.respondSandboxSession(c, current.Sandbox, current.User, "")
		return
	}
	if sourceUserID != nil {
		id := *sourceUserID
		if existing, findErr := s.sandboxes.ActiveForSource(c.Request.Context(), id); findErr == nil {
			user, userErr := s.sandboxes.User(c.Request.Context(), existing)
			if userErr != nil {
				serverError(c, userErr)
				return
			}
			if s.createAndRespondSandboxSession(c, existing, user) && current != nil && current.Sandbox != nil {
				_ = s.sandboxes.MarkPurging(c.Request.Context(), current.Sandbox)
			}
			return
		}
	}

	if !s.allowSandboxCreation(c) {
		return
	}
	env, user, err := s.sandboxes.Create(c.Request.Context(), sourceUserID)
	if err != nil {
		switch {
		case errors.Is(err, sandbox.ErrCapacity):
			fail(c, http.StatusServiceUnavailable, "sandbox_capacity_reached", "all sandbox slots are currently in use")
		case errors.Is(err, sandbox.ErrDisabled):
			fail(c, http.StatusNotFound, "sandbox_disabled", "the sandbox is disabled")
		default:
			serverError(c, err)
		}
		return
	}
	if sourceUserID != nil {
		now := time.Now().UTC()
		_ = s.db.Model(&models.User{}).Where("id = ?", *sourceUserID).Update("sandbox_intro_seen_at", now).Error
	}
	if s.createAndRespondSandboxSession(c, env, user) && current != nil && current.Sandbox != nil {
		_ = s.sandboxes.MarkPurging(c.Request.Context(), current.Sandbox)
	}
}

func (s *Server) createAndRespondSandboxSession(c *gin.Context, env *models.SandboxEnvironment, user *models.User) bool {
	token, csrf, err := s.auth.CreateSandboxSession(c.Request.Context(), user, env.ID, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		serverError(c, err)
		return false
	}
	s.setSandboxCookies(c, token, csrf)
	s.respondSandboxSession(c, env, user, csrf)
	return true
}

func (s *Server) respondSandboxSession(c *gin.Context, env *models.SandboxEnvironment, user *models.User, csrf string) {
	payload := s.identityPayload(c.Request.Context(), user, nil)
	s.attachSandboxIdentity(c.Request.Context(), payload, env)
	c.JSON(http.StatusOK, sandboxSessionResponse{Session: payload, CSRFToken: csrf})
}

func (s *Server) resetSandbox(c *gin.Context) {
	if !s.allowSandboxCreation(c) {
		return
	}
	state := stateFrom(c)
	old := state.Sandbox
	env, user, err := s.sandboxes.Replace(c.Request.Context(), old.SourceUserID, old.ID)
	if err != nil {
		serverError(c, err)
		return
	}
	token, csrf, err := s.auth.CreateSandboxSession(c.Request.Context(), user, env.ID, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		_ = s.sandboxes.MarkPurging(c.Request.Context(), env)
		serverError(c, err)
		return
	}
	if err := s.sandboxes.MarkPurging(c.Request.Context(), old); err != nil {
		_ = s.sandboxes.MarkPurging(c.Request.Context(), env)
		serverError(c, err)
		return
	}
	s.setSandboxCookies(c, token, csrf)
	s.respondSandboxSession(c, env, user, csrf)
}

func (s *Server) allowSandboxCreation(c *gin.Context) bool {
	allowed, retryAfter := s.sandboxStartLimiter.allow(c.ClientIP(), time.Now())
	if allowed {
		return true
	}
	seconds := int(retryAfter.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	fail(c, http.StatusTooManyRequests, "sandbox_rate_limited", "too many sandboxes were created; try again later")
	return false
}

func (s *Server) changeSandboxRole(c *gin.Context) {
	var req struct {
		Role models.Role `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state := stateFrom(c)
	user, err := s.sandboxes.SetRole(c.Request.Context(), state.Sandbox, req.Role)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_sandbox_role", err.Error())
		return
	}
	s.respondSandboxSession(c, state.Sandbox, user, "")
}

func (s *Server) changeSandboxTutorial(c *gin.Context) {
	var req struct {
		Visible bool `json:"visible"`
		Restart bool `json:"restart"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state := stateFrom(c)
	if err := s.sandboxes.SetTutorialVisible(c.Request.Context(), state.Sandbox, req.Visible, req.Restart); err != nil {
		serverError(c, err)
		return
	}
	state.Sandbox.TutorialVisible = req.Visible
	if req.Restart {
		state.Sandbox.TutorialState = models.JSONMap{"catalogue": false, "purchase": false, "sale": false, "balance": false}
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) discardSandbox(c *gin.Context) {
	if err := s.sandboxes.MarkPurging(c.Request.Context(), stateFrom(c).Sandbox); err != nil {
		serverError(c, err)
		return
	}
	s.clearSandboxCookies(c)
	c.Status(http.StatusNoContent)
}

func (s *Server) trackSandboxProgress() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() < 200 || c.Writer.Status() >= 300 {
			return
		}
		state := stateFrom(c)
		if state == nil || state.Sandbox == nil {
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, sandboxAPIPrefix)
		step := ""
		switch {
		case c.Request.Method == http.MethodPut && strings.HasPrefix(path, "/articles/"):
			step = "catalogue"
		case c.Request.Method == http.MethodPost && path == "/purchases":
			step = "purchase"
		case c.Request.Method == http.MethodPost && path == "/sales":
			step = "sale"
		case c.Request.Method == http.MethodGet && (path == "/balances" || strings.HasPrefix(path, "/exports/")):
			step = "balance"
		}
		if step != "" {
			_ = s.sandboxes.MarkProgress(c.Request.Context(), state.Sandbox, step)
		}
	}
}

func (s *Server) setSandboxCookies(c *gin.Context, sessionToken, csrfToken string) {
	maxAge := int(s.cfg.SandboxIdleTTL.Seconds())
	http.SetCookie(c.Writer, &http.Cookie{Name: sandboxSessionCookieName, Value: sessionToken, Path: "/", Domain: s.cfg.CookieDomain, MaxAge: maxAge, Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(c.Writer, &http.Cookie{Name: sandboxCSRFCookieName, Value: csrfToken, Path: "/", Domain: s.cfg.CookieDomain, MaxAge: maxAge, Secure: s.cfg.CookieSecure, HttpOnly: false, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearSandboxCookies(c *gin.Context) {
	for _, item := range []struct {
		name     string
		httpOnly bool
	}{{sandboxSessionCookieName, true}, {sandboxCSRFCookieName, false}} {
		http.SetCookie(c.Writer, &http.Cookie{Name: item.name, Value: "", Path: "/", Domain: s.cfg.CookieDomain, MaxAge: -1, Secure: s.cfg.CookieSecure, HttpOnly: item.httpOnly, SameSite: http.SameSiteLaxMode})
	}
}
