package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerPlatformRoutes(g *gin.RouterGroup) {
	p := g.Group("/platform", requireAuth(), requirePlatformStaff())
	p.GET("/bands", s.listBands)
	p.GET("/bands/:id", s.getBand)
	p.GET("/support-access", s.listPlatformGrants)
	p.POST("/support-access", s.requestSupportAccess)
	p.POST("/support-access/:id/activate", s.activateSupportAccess)
	p.POST("/support-access/:id/revoke", s.revokeSupportAccess)

	// Creating, renaming and removing a band is the system admin's job; a
	// support admin may look but not reshape the instance.
	admin := p.Group("", requireSystemAdmin())
	admin.POST("/bands", s.createBand)
	admin.PATCH("/bands/:id", s.updateBand)
	admin.POST("/bands/:id/activate", s.activateBand)
	admin.POST("/bands/:id/deactivate", s.deactivateBand)
	admin.DELETE("/bands/:id", s.deleteBand)
	admin.POST("/bands/:id/restore", s.restoreBand)
	// The bootstrap account of a band. Deliberately narrow — see
	// createBandBootstrapAdmin.
	admin.POST("/bands/:id/admins", s.createBandBootstrapAdmin)

	// The band's own side of the support workflow. A band admin sees every
	// request aimed at their band and decides it.
	// requireBandAccount, not just the role: a platform account holding a live
	// grant satisfies requireBandRole, and must never be able to approve its
	// own next request through it.
	b := g.Group("/band-admin/support-access",
		requireAuth(), requireBandAccount(), requireBandRole(models.RoleBandAdmin))
	b.GET("", s.listBandGrants)
	b.POST("/:id/approve", s.approveSupportAccess)
	b.POST("/:id/deny", s.denySupportAccess)
	b.POST("/:id/revoke", s.revokeSupportAccessAsBand)
}

func (s *Server) listBands(c *gin.Context) {
	includeDeleted := c.Query("include_deleted") == "true"

	bands, err := s.platform.ListBands(c.Request.Context(), includeDeleted)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bands": bands})
}

func (s *Server) getBand(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	band, err := s.platform.Band(c.Request.Context(), id)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	c.JSON(http.StatusOK, band)
}

type createBandRequest struct {
	Slug         string `json:"slug" binding:"required"`
	Name         string `json:"name" binding:"required"`
	ContactEmail string `json:"contact_email"`
}

func (s *Server) createBand(c *gin.Context) {
	var req createBandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	band, err := s.platform.CreateBand(ctx, req.Slug, req.Name, req.ContactEmail)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBandCreated, EntityType: "band", EntityID: &band.ID,
		Details: map[string]any{"slug": band.Slug, "name": band.Name},
	})
	c.JSON(http.StatusCreated, band)
}

type createBandAdminRequest struct {
	Username string `json:"username" binding:"required"`
}

// createBandBootstrapAdmin gives a band its first administrator.
//
// Without it a freshly created band is unreachable: only a band admin may add
// accounts, and platform staff have no band access — the support-access flow
// cannot help either, because it needs a band admin to approve it.
//
// The power this grants is real, so it stays fenced in: system admins only —
// who already passed password and second factor to reach this session — and
// the role is fixed to band_admin rather than taken from the request, so this
// can never become a general way to create band accounts. The band sees the
// entry in its own audit log, which is why the log is written in the band's
// scope.
func (s *Server) createBandBootstrapAdmin(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req createBandAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	band, err := s.platform.Band(ctx, id)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	// A band on its way out must not gain new accounts; restore it first.
	if band.DeletedAt != nil {
		fail(c, http.StatusConflict, "band_deleted", "this band is deleted; restore it first")
		return
	}
	if err := s.checkUserQuota(ctx, band.ID); err != nil {
		s.reportBandAdminError(c, err)
		return
	}

	user, code, err := s.auth.CreateUser(ctx, &band.ID, req.Username, models.RoleBandAdmin)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.audit.Log(tenant.WithBand(ctx, band.ID), actorFrom(c), audit.Entry{
		Action: audit.ActionUserCreated, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{
			"username": user.Username, "role": string(user.Role),
			"band_id": band.ID, "band_slug": band.Slug,
			// Marks the entry as the platform-side bootstrap rather than an
			// ordinary account the band created itself.
			"bootstrap": true,
		},
	})
	c.JSON(http.StatusCreated, gin.H{
		"id": user.ID, "username": user.Username, "role": user.Role,
		// Shown once; it is never retrievable afterwards.
		"setup_code": code,
	})
}

func (s *Server) updateBand(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var update platform.BandUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	band, err := s.platform.UpdateBand(ctx, id, update)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBandUpdated, EntityType: "band", EntityID: &id,
	})
	c.JSON(http.StatusOK, band)
}

func (s *Server) activateBand(c *gin.Context)   { s.setBandActive(c, true) }
func (s *Server) deactivateBand(c *gin.Context) { s.setBandActive(c, false) }

// setBandActive flips a band's login switch.
//
// Deactivating touches no band data at all — it only stops sign-ins — which is
// what makes it a safe first response to a billing or abuse question.
func (s *Server) setBandActive(c *gin.Context, active bool) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.platform.SetActive(ctx, id, active); err != nil {
		s.reportPlatformError(c, err)
		return
	}
	if !active {
		// Existing sessions have to go, otherwise a deactivated band keeps
		// working until its cookies happen to expire.
		if err := s.auth.RevokeBandSessions(ctx, id); err != nil {
			serverError(c, err)
			return
		}
	}

	action := audit.ActionBandDeactivated
	if active {
		action = audit.ActionBandRestored
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{Action: action, EntityType: "band", EntityID: &id})
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteBand(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.platform.SoftDelete(ctx, id); err != nil {
		s.reportPlatformError(c, err)
		return
	}
	if err := s.auth.RevokeBandSessions(ctx, id); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBandDeleted, EntityType: "band", EntityID: &id,
		Details: map[string]any{"soft_delete": true},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) restoreBand(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.platform.Restore(ctx, id); err != nil {
		s.reportPlatformError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBandRestored, EntityType: "band", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

// --- support access -------------------------------------------------------

func (s *Server) listPlatformGrants(c *gin.Context) {
	var bandID *int64
	if raw := c.Query("band_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid band identifier")
			return
		}
		bandID = &parsed
	}

	grants, err := s.platform.ListGrants(c.Request.Context(), bandID)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

func (s *Server) listBandGrants(c *gin.Context) {
	state := stateFrom(c)
	grants, err := s.platform.ListGrants(c.Request.Context(), state.User.BandID)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

type requestAccessRequest struct {
	BandID          int64  `json:"band_id" binding:"required"`
	Reason          string `json:"reason" binding:"required"`
	Scope           string `json:"scope"`
	DurationSeconds int    `json:"duration_seconds"`
}

// requestSupportAccess asks a band for permission.
//
// It grants nothing: the band still has to approve, and the requester still
// has to confirm with a second factor afterwards.
func (s *Server) requestSupportAccess(c *gin.Context) {
	var req requestAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	scope := models.SupportScopeReadOnly
	if req.Scope == string(models.SupportScopeReadWrite) {
		scope = models.SupportScopeReadWrite
	}
	duration := time.Duration(req.DurationSeconds) * time.Second

	state := stateFrom(c)
	ctx := c.Request.Context()
	grant, err := s.platform.RequestAccess(ctx, req.BandID, state.User, req.Reason, scope, duration)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSupportAccessRequested, EntityType: "support_access_grant",
		EntityID: &grant.ID,
		Details: map[string]any{
			"band_id": grant.BandID, "scope": string(grant.Scope), "reason": grant.Reason,
		},
	})
	c.JSON(http.StatusCreated, grant)
}

func (s *Server) approveSupportAccess(c *gin.Context) { s.decideSupportAccess(c, true) }
func (s *Server) denySupportAccess(c *gin.Context)    { s.decideSupportAccess(c, false) }

// decideSupportAccess records the band admin's answer.
//
// Approving is a sensitive action: it needs a fresh password confirmation, the
// same as deleting a user, because it hands an outsider a key to the band's
// books.
func (s *Server) decideSupportAccess(c *gin.Context, approve bool) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	state := stateFrom(c)
	if approve && !s.auth.HasFreshReauth(state.Session) {
		fail(c, http.StatusForbidden, "reauth_required", "confirm your password again to continue")
		return
	}

	var body struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&body)

	ctx := c.Request.Context()
	grant, err := s.platform.Decide(ctx, id, state.User, approve, body.Note)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}

	action := audit.ActionSupportAccessDenied
	if approve {
		action = audit.ActionSupportAccessApproved
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: action, EntityType: "support_access_grant", EntityID: &grant.ID,
		Details: map[string]any{"requested_by": grant.RequestedByUsername, "reason": grant.Reason},
	})
	c.JSON(http.StatusOK, grant)
}

type activateAccessRequest struct {
	// Code is a fresh second factor. The approval alone is not enough: it must
	// be the approved person, at the keyboard, right now.
	Code string `json:"code" binding:"required"`
}

func (s *Server) activateSupportAccess(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req activateAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	if err := s.auth.VerifySecondFactor(state.User, req.Code); err != nil {
		s.reportAuthError(c, err)
		return
	}
	if err := s.auth.PersistRecoveryCodes(ctx, state.User); err != nil {
		serverError(c, err)
		return
	}

	grant, err := s.platform.Activate(ctx, id, state.User)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	if err := s.auth.ApplySupportScope(ctx, state.Session, grant); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSupportAccessActivated, EntityType: "support_access_grant",
		EntityID: &grant.ID,
		Details: map[string]any{
			"band_id": grant.BandID, "scope": string(grant.Scope), "expires_at": grant.ExpiresAt,
		},
	})
	c.JSON(http.StatusOK, grant)
}

func (s *Server) revokeSupportAccess(c *gin.Context)       { s.revokeGrant(c) }
func (s *Server) revokeSupportAccessAsBand(c *gin.Context) { s.revokeGrant(c) }

// revokeGrant ends a window early. Either side may do it, and the band must
// never have to wait out access it no longer wants.
func (s *Server) revokeGrant(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	grant, err := s.platform.Grant(ctx, id)
	if err != nil {
		s.reportPlatformError(c, err)
		return
	}
	// A band admin may only revoke a grant aimed at their own band.
	if state.User.Role.IsBandRole() {
		if state.User.BandID == nil || *state.User.BandID != grant.BandID {
			fail(c, http.StatusNotFound, "not_found", "no such support access request")
			return
		}
	}

	if err := s.platform.Revoke(ctx, id, state.User); err != nil {
		s.reportPlatformError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSupportAccessRevoked, EntityType: "support_access_grant", EntityID: &id,
		Details: map[string]any{"band_id": grant.BandID, "by_band": state.User.Role.IsBandRole()},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) reportPlatformError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, platform.ErrBandNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such band")
	case errors.Is(err, platform.ErrGrantNotFound), errors.Is(err, platform.ErrWrongBand):
		fail(c, http.StatusNotFound, "not_found", "no such support access request")
	case errors.Is(err, platform.ErrSlugTaken):
		fail(c, http.StatusConflict, "slug_taken", err.Error())
	case errors.Is(err, platform.ErrInvalidSlug):
		fail(c, http.StatusBadRequest, "invalid_slug", err.Error())
	case errors.Is(err, platform.ErrInvalidName):
		fail(c, http.StatusBadRequest, "invalid_name", err.Error())
	case errors.Is(err, platform.ErrOpenRequest):
		fail(c, http.StatusConflict, "open_request", err.Error())
	case errors.Is(err, platform.ErrGrantNotPending):
		fail(c, http.StatusConflict, "already_decided", err.Error())
	case errors.Is(err, platform.ErrGrantNotApproved):
		fail(c, http.StatusConflict, "not_approved", err.Error())
	case errors.Is(err, platform.ErrGrantNotLive):
		fail(c, http.StatusConflict, "not_active", err.Error())
	case errors.Is(err, platform.ErrReasonRequired):
		fail(c, http.StatusBadRequest, "reason_required", err.Error())
	case errors.Is(err, platform.ErrAlreadyActive):
		fail(c, http.StatusConflict, "already_active", err.Error())
	default:
		serverError(c, err)
	}
}
