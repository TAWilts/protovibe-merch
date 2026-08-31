package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerBandAdminRoutes(g *gin.RouterGroup) {
	// Managing accounts is a band admin's job, and every change here is
	// sensitive enough to need a fresh password confirmation — the same rule
	// the original applied.
	u := g.Group("/band-admin/users",
		requireAuth(), requireBandAccount(), requireBandRole(models.RoleBandAdmin))
	u.GET("", s.listBandUsers)

	guarded := u.Group("", s.requireFreshReauth())
	guarded.POST("", s.createBandUser)
	guarded.POST("/:id/reset-password", s.resetBandUserPassword)
	guarded.PATCH("/:id/role", s.changeBandUserRole)
	guarded.PATCH("/:id/active", s.setBandUserActive)
	guarded.POST("/:id/reset-mfa", s.resetBandUserMFA)
	guarded.DELETE("/:id", s.deleteBandUser)
}

type bandUserPayload struct {
	ID              int64       `json:"id"`
	Username        string      `json:"username"`
	Role            models.Role `json:"role"`
	RoleLabel       string      `json:"role_label"`
	IsActive        bool        `json:"is_active"`
	MFAEnabled      bool        `json:"mfa_enabled"`
	MustSetPassword bool        `json:"must_set_password"`
	LastLoginAt     *time.Time  `json:"last_login_at"`
	CreatedAt       time.Time   `json:"created_at"`
	// IsSelf marks the signed-in admin, whose own account has extra guards.
	IsSelf bool `json:"is_self"`
}

func (s *Server) listBandUsers(c *gin.Context) {
	state := stateFrom(c)
	ctx := c.Request.Context()

	var users []models.User
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Where("band_id = ?", *state.User.BandID).
		Order("username").Find(&users).Error
	if err != nil {
		serverError(c, err)
		return
	}

	payload := make([]bandUserPayload, 0, len(users))
	for _, user := range users {
		payload = append(payload, bandUserPayload{
			ID: user.ID, Username: user.Username, Role: user.Role,
			RoleLabel: roleLabel(user.Role), IsActive: user.IsActive,
			MFAEnabled: user.MFAEnabled, MustSetPassword: user.MustSetPassword,
			LastLoginAt: user.LastLoginAt, CreatedAt: user.CreatedAt,
			IsSelf: user.ID == state.User.ID,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"users": payload,
		// The roles a band admin may assign, so the client does not hard-code
		// a list that could drift from the server's.
		"assignable_roles": models.ManagedBandRoles,
	})
}

type createUserRequest struct {
	Username string      `json:"username" binding:"required"`
	Role     models.Role `json:"role" binding:"required"`
}

// createBandUser adds an account and returns its one-time setup code, which is
// shown exactly once.
func (s *Server) createBandUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !isAssignableBandRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid_role", "this role cannot be assigned")
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	if err := s.checkUserQuota(ctx, *state.User.BandID); err != nil {
		s.reportBandAdminError(c, err)
		return
	}

	user, code, err := s.auth.CreateUser(ctx, state.User.BandID, req.Username, req.Role)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionUserCreated, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username, "role": string(user.Role)},
	})
	c.JSON(http.StatusCreated, gin.H{
		"id": user.ID, "username": user.Username, "role": user.Role,
		// Shown once; it is never retrievable afterwards.
		"setup_code": code,
	})
}

// ErrUserQuotaReached is returned when a band is at its account limit.
var ErrUserQuotaReached = errors.New("band: the account limit for this band is reached")

// checkUserQuota enforces the per-band limit the admin center can set.
func (s *Server) checkUserQuota(ctx context.Context, bandID int64) error {
	var band models.Band
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).First(&band, bandID).Error; err != nil {
		return err
	}
	// Zero means no limit, which is the default for a new band.
	if band.UserQuota <= 0 {
		return nil
	}

	var existing int64
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(&models.User{}).
		Where("band_id = ?", bandID).Count(&existing).Error
	if err != nil {
		return err
	}
	if existing >= int64(band.UserQuota) {
		return ErrUserQuotaReached
	}
	return nil
}

func (s *Server) resetBandUserPassword(c *gin.Context) {
	user, ok := s.bandUser(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	code, err := s.auth.ResetPassword(ctx, user)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionPasswordReset, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username},
	})
	c.JSON(http.StatusOK, gin.H{"username": user.Username, "setup_code": code})
}

type changeRoleRequest struct {
	Role models.Role `json:"role" binding:"required"`
}

// changeBandUserRole moves an account between band roles.
//
// A band admin cannot demote themselves: doing so on the last admin account
// would leave the band with nobody able to manage it.
func (s *Server) changeBandUserRole(c *gin.Context) {
	user, ok := s.bandUser(c)
	if !ok {
		return
	}
	var req changeRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !isAssignableBandRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid_role", "this role cannot be assigned")
		return
	}

	state := stateFrom(c)
	if user.ID == state.User.ID && req.Role != models.RoleBandAdmin {
		forbidden(c, "cannot_demote_self", "you cannot take your own band admin role away")
		return
	}
	if user.Role == models.RoleBandAdmin && req.Role != models.RoleBandAdmin {
		if err := s.ensureAnotherBandAdmin(c, user); err != nil {
			return
		}
	}

	ctx := c.Request.Context()
	previous := user.Role
	// A role change invalidates the account's sessions, so the new rights take
	// effect immediately rather than at the next login.
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(&models.User{}).
		Where("id = ?", user.ID).Updates(map[string]any{
		"role":            req.Role,
		"session_version": gorm.Expr("session_version + 1"),
	}).Error
	if err != nil {
		serverError(c, err)
		return
	}
	if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionUserRoleChanged, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"from": string(previous), "to": string(req.Role)},
	})
	c.Status(http.StatusNoContent)
}

type setActiveRequest struct {
	Active bool `json:"active"`
}

func (s *Server) setBandUserActive(c *gin.Context) {
	user, ok := s.bandUser(c)
	if !ok {
		return
	}
	var req setActiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	if user.ID == state.User.ID && !req.Active {
		forbidden(c, "cannot_deactivate_self", "you cannot deactivate your own account")
		return
	}
	if user.Role == models.RoleBandAdmin && !req.Active {
		if err := s.ensureAnotherBandAdmin(c, user); err != nil {
			return
		}
	}

	ctx := c.Request.Context()
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(&models.User{}).
		Where("id = ?", user.ID).Updates(map[string]any{
		"is_active":       req.Active,
		"session_version": gorm.Expr("session_version + 1"),
	}).Error
	if err != nil {
		serverError(c, err)
		return
	}
	if !req.Active {
		if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
			serverError(c, err)
			return
		}
	}

	action := audit.ActionUserDeactivated
	if req.Active {
		action = audit.ActionUserActivated
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: action, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username},
	})
	c.Status(http.StatusNoContent)
}

// resetBandUserMFA clears a second factor for someone who lost their device.
func (s *Server) resetBandUserMFA(c *gin.Context) {
	user, ok := s.bandUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	auth.DisableMFA(user)
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(&models.User{}).
		Where("id = ?", user.ID).Updates(map[string]any{
		"mfa_enabled":                  false,
		"mfa_secret_encrypted":         "",
		"mfa_pending_secret_encrypted": "",
		"mfa_recovery_code_hashes":     models.JSONSlice{},
		"mfa_enrolled_at":              nil,
		"session_version":              gorm.Expr("session_version + 1"),
	}).Error
	if err != nil {
		serverError(c, err)
		return
	}
	if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionMFAReset, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username},
	})
	c.Status(http.StatusNoContent)
}

// deleteBandUser removes an account.
//
// Its bookings stay: every sale and purchase carries an immutable username
// snapshot, so deleting the person never makes their history unreadable.
func (s *Server) deleteBandUser(c *gin.Context) {
	user, ok := s.bandUser(c)
	if !ok {
		return
	}

	state := stateFrom(c)
	if user.ID == state.User.ID {
		forbidden(c, "cannot_delete_self", "you cannot delete your own account")
		return
	}
	if user.Role == models.RoleBandAdmin {
		if err := s.ensureAnotherBandAdmin(c, user); err != nil {
			return
		}
	}

	ctx := c.Request.Context()
	if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
		serverError(c, err)
		return
	}
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Delete(&models.User{}, user.ID).Error; err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionUserDeleted, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username},
	})
	c.Status(http.StatusNoContent)
}

// bandUser loads a user of the caller's own band. A user from another band is
// reported as missing rather than as forbidden, so the endpoint does not
// confirm which accounts exist elsewhere.
func (s *Server) bandUser(c *gin.Context) (*models.User, bool) {
	id, ok := pathID(c)
	if !ok {
		return nil, false
	}
	state := stateFrom(c)

	var user models.User
	err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Where("id = ? AND band_id = ?", id, *state.User.BandID).First(&user).Error
	if err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such account")
		return nil, false
	}
	return &user, true
}

// ensureAnotherBandAdmin refuses a change that would leave the band without
// anyone able to manage it.
func (s *Server) ensureAnotherBandAdmin(c *gin.Context, subject *models.User) error {
	state := stateFrom(c)

	var others int64
	err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).Model(&models.User{}).
		Where("band_id = ? AND role = ? AND is_active = ? AND id <> ?",
			*state.User.BandID, models.RoleBandAdmin, true, subject.ID).
		Count(&others).Error
	if err != nil {
		serverError(c, err)
		return err
	}
	if others == 0 {
		forbidden(c, "last_band_admin", "the band would be left without an administrator")
		return errors.New("last band admin")
	}
	return nil
}

func isAssignableBandRole(role models.Role) bool {
	for _, candidate := range models.ManagedBandRoles {
		if candidate == role {
			return true
		}
	}
	return false
}

func (s *Server) reportBandAdminError(c *gin.Context, err error) {
	if errors.Is(err, ErrUserQuotaReached) {
		fail(c, http.StatusConflict, "user_quota_reached", err.Error())
		return
	}
	serverError(c, err)
}
