package api

import (
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

type platformUserPayload struct {
	ID              int64       `json:"id"`
	Username        string      `json:"username"`
	ContactEmail    string      `json:"contact_email"`
	Role            models.Role `json:"role"`
	RoleLabel       string      `json:"role_label"`
	IsActive        bool        `json:"is_active"`
	MFAEnabled      bool        `json:"mfa_enabled"`
	MustSetPassword bool        `json:"must_set_password"`
	LastLoginAt     *time.Time  `json:"last_login_at"`
	CreatedAt       time.Time   `json:"created_at"`
	IsSelf          bool        `json:"is_self"`
}

func (s *Server) registerPlatformUserRoutes(g *gin.RouterGroup) {
	u := g.Group("/platform/users", requireAuth(), requireSystemAdmin())
	u.GET("", s.listPlatformUsers)
	guarded := u.Group("", s.requireFreshReauth())
	guarded.POST("", s.createPlatformUser)
	guarded.POST("/:id/reset-password", s.resetPlatformUserPassword)
	guarded.POST("/:id/reset-mfa", s.resetPlatformUserMFA)
	guarded.PATCH("/:id/role", s.changePlatformUserRole)
	guarded.PATCH("/:id/active", s.setPlatformUserActive)
}

func (s *Server) listPlatformUsers(c *gin.Context) {
	var users []models.User
	if err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Where("band_id IS NULL").Order("username").Find(&users).Error; err != nil {
		serverError(c, err)
		return
	}
	state := stateFrom(c)
	payload := make([]platformUserPayload, 0, len(users))
	for _, user := range users {
		payload = append(payload, platformUserPayload{
			ID: user.ID, Username: user.Username, ContactEmail: user.ContactEmail,
			Role: user.Role, RoleLabel: roleLabel(user.Role), IsActive: user.IsActive,
			MFAEnabled: user.MFAEnabled, MustSetPassword: user.MustSetPassword,
			LastLoginAt: user.LastLoginAt, CreatedAt: user.CreatedAt,
			IsSelf: user.ID == state.User.ID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"users": payload, "assignable_roles": models.ManagedPlatformRoles})
}

type createPlatformUserRequest struct {
	Username     string      `json:"username" binding:"required"`
	ContactEmail string      `json:"contact_email"`
	Role         models.Role `json:"role" binding:"required"`
}

func (s *Server) createPlatformUser(c *gin.Context) {
	var req createPlatformUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !isAssignablePlatformRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid_role", "this role cannot be assigned")
		return
	}
	email, err := normalizeEmail(req.ContactEmail, true)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_email", err.Error())
		return
	}

	ctx := c.Request.Context()
	user, code, err := s.auth.CreateUser(ctx, nil, req.Username, req.Role)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}
	if email != "" {
		if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
			Model(user).Update("contact_email", email).Error; err != nil {
			_ = s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Delete(user).Error
			serverError(c, err)
			return
		}
		user.ContactEmail = email
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{Action: audit.ActionUserCreated, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"username": user.Username, "role": string(user.Role)}})
	c.JSON(http.StatusCreated, gin.H{"id": user.ID, "username": user.Username, "role": user.Role, "setup_code": code})
}

func (s *Server) resetPlatformUserPassword(c *gin.Context) {
	user, ok := s.platformUser(c)
	if !ok {
		return
	}
	code, err := s.auth.ResetPassword(c.Request.Context(), user)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{Action: audit.ActionPasswordReset,
		EntityType: "user", EntityID: &user.ID, Details: map[string]any{"username": user.Username}})
	c.JSON(http.StatusOK, gin.H{"username": user.Username, "setup_code": code})
}

func (s *Server) changePlatformUserRole(c *gin.Context) {
	user, ok := s.platformUser(c)
	if !ok {
		return
	}
	var req changeRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !isAssignablePlatformRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid_role", "this role cannot be assigned")
		return
	}
	if user.ID == stateFrom(c).User.ID && req.Role != models.RoleSystemAdmin {
		forbidden(c, "cannot_demote_self", "you cannot take your own system admin role away")
		return
	}
	if user.Role == models.RoleSystemAdmin && req.Role != models.RoleSystemAdmin && !s.hasOtherSystemAdmin(c, user.ID) {
		return
	}

	previous := user.Role
	ctx := c.Request.Context()
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(user).Updates(map[string]any{
		"role": req.Role, "session_version": gorm.Expr("session_version + 1"),
	}).Error; err != nil {
		serverError(c, err)
		return
	}
	if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
		serverError(c, err)
		return
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{Action: audit.ActionUserRoleChanged, EntityType: "user", EntityID: &user.ID,
		Details: map[string]any{"from": string(previous), "to": string(req.Role)}})
	c.Status(http.StatusNoContent)
}

func (s *Server) setPlatformUserActive(c *gin.Context) {
	user, ok := s.platformUser(c)
	if !ok {
		return
	}
	var req setActiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if user.ID == stateFrom(c).User.ID && !req.Active {
		forbidden(c, "cannot_deactivate_self", "you cannot deactivate your own account")
		return
	}
	if user.Role == models.RoleSystemAdmin && !req.Active && !s.hasOtherSystemAdmin(c, user.ID) {
		return
	}
	ctx := c.Request.Context()
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(user).Updates(map[string]any{
		"is_active": req.Active, "session_version": gorm.Expr("session_version + 1"),
	}).Error; err != nil {
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
	s.audit.Log(ctx, actorFrom(c), audit.Entry{Action: action, EntityType: "user", EntityID: &user.ID})
	c.Status(http.StatusNoContent)
}

func (s *Server) resetPlatformUserMFA(c *gin.Context) {
	user, ok := s.platformUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	auth.DisableMFA(user)
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Model(user).Updates(map[string]any{
		"mfa_enabled": false, "mfa_secret_encrypted": "", "mfa_pending_secret_encrypted": "",
		"mfa_recovery_code_hashes": models.JSONSlice{}, "mfa_enrolled_at": nil,
		"session_version": gorm.Expr("session_version + 1"),
	}).Error; err != nil {
		serverError(c, err)
		return
	}
	if err := s.auth.RevokeUserSessions(ctx, user.ID); err != nil {
		serverError(c, err)
		return
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{Action: audit.ActionMFAReset, EntityType: "user", EntityID: &user.ID})
	c.Status(http.StatusNoContent)
}

func (s *Server) platformUser(c *gin.Context) (*models.User, bool) {
	id, ok := pathID(c)
	if !ok {
		return nil, false
	}
	var user models.User
	err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Where("id = ? AND band_id IS NULL", id).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "not_found", "no such platform account")
		} else {
			serverError(c, err)
		}
		return nil, false
	}
	return &user, true
}

func (s *Server) hasOtherSystemAdmin(c *gin.Context, userID int64) bool {
	var count int64
	err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).Model(&models.User{}).
		Where("band_id IS NULL AND role = ? AND is_active = ? AND id <> ?", models.RoleSystemAdmin, true, userID).
		Count(&count).Error
	if err != nil {
		serverError(c, err)
		return false
	}
	if count == 0 {
		forbidden(c, "last_system_admin", "the platform would be left without an active system administrator")
		return false
	}
	return true
}

func isAssignablePlatformRole(role models.Role) bool {
	for _, candidate := range models.ManagedPlatformRoles {
		if candidate == role {
			return true
		}
	}
	return false
}
