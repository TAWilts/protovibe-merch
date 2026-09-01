package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/rbac"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Cookie names. The session cookie is HttpOnly; the CSRF cookie deliberately
// is not, because the frontend has to echo its value back in a header.
const (
	sessionCookieName = "merch_session"
	csrfCookieName    = "merch_csrf"
)

// maintenanceOpenPrefixes stay reachable while the instance is in
// maintenance. Locking these would strand an operator outside their own
// admin center.
var maintenanceOpenPrefixes = []string{
	"/api/v1/auth",
	"/api/v1/mfa",
	"/api/v1/announcement",
	"/api/v1/version",
}

// unsafeMethods require a CSRF token.
var unsafeMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// requestLogger tags every request with an ID and emits one structured line
// per request, which is what makes a hosted deployment debuggable.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		requestID := uuid.NewString()
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)

		c.Next()

		attrs := []any{
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
			"ip", c.ClientIP(),
		}
		if state := stateFrom(c); state != nil && state.User != nil {
			attrs = append(attrs, "user_id", state.User.ID, "role", string(state.User.Role))
			if state.Session.BandID != nil {
				attrs = append(attrs, "band_id", *state.Session.BandID)
			}
			if state.Grant != nil {
				attrs = append(attrs, "acting_grant_id", state.Grant.ID)
			}
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
			slog.Error("request failed", attrs...)
			return
		}
		slog.Info("request", attrs...)
	}
}

// noStore keeps authentication, administration and payment responses out of
// every cache, mirroring the original's prevent_sensitive_page_caching.
func noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

// --- Guard 1: maintenance -------------------------------------------------

// maintenanceGuard blocks band traffic while the instance or a single band is
// in maintenance. Platform staff always get through so they can fix whatever
// caused the maintenance window.
func (s *Server) maintenanceGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		maintenanceEnabled, message, err := s.maintenanceStatus(c.Request.Context(), stateFrom(c))
		if err != nil {
			serverError(c, err)
			return
		}
		if !maintenanceEnabled {
			c.Next()
			return
		}

		// Authentication must never be blocked. An operator has to be able to
		// sign in to switch maintenance back off, and a band member has to be
		// able to sign out of a stuck session. Once signed in, a band account
		// still gets the maintenance response everywhere else.
		for _, open := range maintenanceOpenPrefixes {
			if strings.HasPrefix(c.Request.URL.Path, open) {
				c.Next()
				return
			}
		}

		failWithDetails(c, http.StatusServiceUnavailable, "maintenance",
			"the service is temporarily unavailable",
			map[string]string{"message": message})
	}
}

// maintenanceStatus is shared by the guard and the status endpoint. Keeping
// the decision in one place prevents the UI from promising access that the
// following request then rejects.
func (s *Server) maintenanceStatus(ctx context.Context, state *RequestState) (bool, string, error) {
	if state != nil && state.User != nil && state.User.Role.IsPlatformRole() {
		return false, "", nil
	}
	settings, err := s.platformSettings(ctx)
	if err != nil {
		return false, "", err
	}
	if settings.MaintenanceEnabled {
		return true, strings.TrimSpace(settings.MaintenanceMessage), nil
	}
	if state == nil || state.User == nil || state.User.BandID == nil {
		return false, "", nil
	}
	var band models.Band
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Select("id", "maintenance_message").First(&band, *state.User.BandID).Error; err != nil {
		return false, "", err
	}
	message := strings.TrimSpace(band.MaintenanceMessage)
	return message != "", message, nil
}

// featureGuard enforces tenant feature switches on the API. Hiding a link in
// Vue is only a convenience; without this guard a disabled paid or staged
// feature remained callable by sending requests directly.
func (s *Server) featureGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			c.Next()
			return
		}
		bandID, err := tenant.BandID(c.Request.Context())
		if err != nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		var feature string
		var enabled func(models.FeatureFlags) bool
		switch {
		case strings.HasPrefix(path, "/api/v1/slideshow"):
			feature, enabled = "slideshow", func(f models.FeatureFlags) bool { return f.SlideshowEnabled() }
		case strings.HasPrefix(path, "/api/v1/band-finances"):
			feature, enabled = "band_finances", func(f models.FeatureFlags) bool { return f.BandFinancesEnabled() }
		case strings.HasPrefix(path, "/api/v1/payment-qr"):
			feature, enabled = "payment_qr", func(f models.FeatureFlags) bool { return f.PaymentQREnabled() }
		case strings.HasPrefix(path, "/api/v1/imports"):
			feature, enabled = "csv_import", func(f models.FeatureFlags) bool { return f.CSVImportEnabled() }
		default:
			c.Next()
			return
		}

		var band models.Band
		if err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
			Select("id", "feature_flags").First(&band, bandID).Error; err != nil {
			serverError(c, err)
			return
		}
		if !enabled(band.FeatureFlags) {
			forbidden(c, "feature_disabled", "the "+feature+" feature is disabled for this band")
			return
		}
		c.Next()
	}
}

// --- Guard 2: session, CSRF and session_version ---------------------------

// resolveSession loads the session when a cookie is present. It never rejects
// an anonymous request; requireAuth does that, so public endpoints such as
// login share the same chain.
func (s *Server) resolveSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookieName)
		if err != nil || token == "" {
			c.Next()
			return
		}

		bundle, err := s.auth.LoadSession(c.Request.Context(), token)
		if err != nil {
			// Any invalid session is cleared rather than left to fail again on
			// the next request.
			s.clearSessionCookie(c)
			switch {
			case errors.Is(err, auth.ErrNoSession),
				errors.Is(err, auth.ErrSessionExpired),
				errors.Is(err, auth.ErrStaleSession):
				c.Next()
				return
			default:
				serverError(c, err)
				return
			}
		}

		state := &RequestState{
			Session: bundle.Session,
			User:    bundle.User,
			Grant:   bundle.Grant,
			Caps:    rbac.For(bundle.User),
		}
		c.Set(ctxKeyRequestState, state)

		// The tenant scope is derived from the session, never from a request
		// parameter. A band user gets their own band; platform staff get a
		// band only while a support grant is live, and read-only grants mark
		// the scope so the database layer rejects writes.
		ctx := c.Request.Context()
		switch {
		case bundle.Grant != nil:
			ctx = tenant.WithGrant(ctx, bundle.Grant.BandID, bundle.Grant.ID, !bundle.Grant.AllowsWrite())
		case bundle.User.BandID != nil:
			ctx = tenant.WithBand(ctx, *bundle.User.BandID)
		}
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// csrfGuard validates the double-submit token on unsafe methods.
func (s *Server) csrfGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !unsafeMethods[c.Request.Method] {
			c.Next()
			return
		}
		state := stateFrom(c)
		if state == nil {
			// Anonymous unsafe requests (login, account setup) carry no
			// session to bind a token to; they are rate limited instead.
			c.Next()
			return
		}
		if !s.auth.VerifyCSRF(state.Session, c.GetHeader("X-CSRF-Token")) {
			forbidden(c, "csrf_failed", "missing or invalid CSRF token")
			return
		}
		c.Next()
	}
}

// --- Guard 3: platform-staff boundary -------------------------------------

// platformBoundary keeps support and system admins out of band data unless a
// live support-access grant says otherwise.
//
// This is the tenant boundary's outer wall. The database callback is the inner
// one: even if this guard were bypassed, no band-scoped query would run
// without a scope.
func (s *Server) platformBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil || !state.User.Role.IsPlatformRole() {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		for _, prefix := range rbac.PlatformStaffAllowedPrefixes {
			if strings.HasPrefix(path, prefix) {
				c.Next()
				return
			}
		}

		if state.Grant == nil {
			forbidden(c, "no_support_grant",
				"platform accounts need an approved support access grant to reach band data")
			return
		}
		if !state.Grant.AllowsWrite() && unsafeMethods[c.Request.Method] {
			forbidden(c, "grant_read_only", "this support access grant is read-only")
			return
		}
		c.Next()
	}
}

// --- Guard 4: POS mode ----------------------------------------------------

// posModeGuard enforces the restricted point-of-sale mode server-side, so a
// device left unattended on a merch table cannot be talked into reaching
// purchases, balances or administration.
func posModeGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil || !state.Session.POSMode {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		for _, prefix := range rbac.POSRestrictedPrefixes {
			if strings.HasPrefix(path, prefix) {
				forbidden(c, "pos_mode_restricted", "this area is disabled while POS mode is active")
				return
			}
		}
		c.Next()
	}
}

// --- Authorisation helpers ------------------------------------------------

// requireAuth rejects anonymous requests.
func requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if stateFrom(c) == nil {
			unauthorized(c)
			return
		}
		c.Next()
	}
}

// requireBandRole demands a cumulative band role. Platform staff satisfy it
// only through a live grant, which also has to allow the request's method.
func requireBandRole(required models.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if state.User.Role.AtLeast(required) {
			c.Next()
			return
		}
		if state.Grant != nil {
			// Support access stands in for a band role, bounded by the grant's
			// own scope rather than by a role level.
			c.Next()
			return
		}
		forbidden(c, "insufficient_role", "this action requires the "+string(required)+" role")
	}
}

// requireBandAccount demands a genuine member of the band, not a platform
// account operating under a support grant.
//
// It guards the decisions a band makes about outsiders — approving support
// access above all. Without it, a platform admin holding a live grant could
// approve their own next request through it, which would turn the whole
// approval workflow into a formality.
func requireBandAccount() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if !state.User.Role.IsBandRole() || state.User.BandID == nil {
			forbidden(c, "band_account_required",
				"only a member of the band may perform this action")
			return
		}
		c.Next()
	}
}

// requirePlatformStaff restricts a route to the control plane.
func requirePlatformStaff() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if !state.User.Role.IsPlatformRole() {
			forbidden(c, "platform_only", "this area is reserved for platform accounts")
			return
		}
		c.Next()
	}
}

// requireSystemAdmin restricts a route to the highest platform role.
func requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if state.User.Role != models.RoleSystemAdmin {
			forbidden(c, "system_admin_only", "this action requires the system admin role")
			return
		}
		c.Next()
	}
}

// requireFreshReauth demands a recent password confirmation. It guards the
// profile and every destructive administrative action, matching the original's
// profile_reauth_required.
func (s *Server) requireFreshReauth() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := stateFrom(c)
		if state == nil {
			unauthorized(c)
			return
		}
		if !s.auth.HasFreshReauth(state.Session) {
			fail(c, http.StatusForbidden, "reauth_required",
				"confirm your password again to continue")
			return
		}
		c.Next()
	}
}

// apiPrefix is the single mount point of the JSON API.
const apiPrefix = "/api/v1"

// underPrefix runs a guard only for requests below a path prefix, which is how
// the guard chain can live on the engine (covering unmatched paths) without
// also wrapping the health probes.
func underPrefix(prefix string, guard gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, prefix) {
			guard(c)
			return
		}
		c.Next()
	}
}
