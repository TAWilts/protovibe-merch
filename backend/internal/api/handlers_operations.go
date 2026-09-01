package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/mailer"
	"github.com/tawilts/protovibe-merch/backend/internal/services/updates"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerPlatformOpsRoutes(g *gin.RouterGroup) {
	p := g.Group("/platform", requireAuth(), requirePlatformStaff())
	p.GET("/audit", s.auditLog)
	p.GET("/settings", s.getPlatformSettings)
	p.GET("/messages", s.listSupportMessages)
	p.GET("/message-assignees", s.listSupportMessageAssignees)
	p.POST("/messages/:id/resolve", s.resolveSupportMessage)
	p.PATCH("/messages/:id/assignment", s.assignSupportMessage)

	p.GET("/updates", s.checkUpdates)

	admin := p.Group("", requireSystemAdmin())
	admin.PUT("/settings", s.updatePlatformSettings)
	admin.POST("/settings/test-mail", s.sendTestMail)
	admin.POST("/bands/:id/revoke-sessions", s.revokeBandSessions)
	admin.POST("/users/:id/revoke-sessions", s.revokeUserSessions)
}

// auditEntry is one row of the cross-band audit viewer.
type auditEntry struct {
	models.AuditLog
	// BandName saves the reader from resolving IDs by hand.
	BandName string `json:"band_name"`
}

// auditLog is the cross-band activity trail.
//
// It is the answer to "who touched our data and when", so it is filterable by
// band, user, action and the support grant an action ran under.
func (s *Server) auditLog(c *gin.Context) {
	ctx := tenant.WithCrossBandAccess(c.Request.Context())

	query := s.db.WithContext(ctx).Model(&models.AuditLog{}).
		Select("audit_log.*, COALESCE(bands.name, '') AS band_name").
		Joins("LEFT JOIN bands ON bands.id = audit_log.band_id").
		Order("audit_log.created_at DESC, audit_log.id DESC")

	if raw := c.Query("band_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid band identifier")
			return
		}
		query = query.Where("audit_log.band_id = ?", id)
	}
	if raw := c.Query("user_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid user identifier")
			return
		}
		query = query.Where("audit_log.user_id = ?", id)
	}
	if action := c.Query("action"); action != "" {
		query = query.Where("audit_log.action LIKE ?", action+"%")
	}
	if raw := c.Query("grant_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid grant identifier")
			return
		}
		query = query.Where("audit_log.acting_grant_id = ?", id)
	}
	if raw := c.Query("since"); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_date", "since must be an RFC3339 timestamp")
			return
		}
		query = query.Where("audit_log.created_at >= ?", since.UTC())
	}

	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	var entries []auditEntry
	if err := query.Limit(limit).Scan(&entries).Error; err != nil {
		serverError(c, err)
		return
	}
	if entries == nil {
		entries = []auditEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries, "limit": limit})
}

// platformSettingsPayload never exposes the stored SMTP password; it only says
// whether one is configured.
type platformSettingsPayload struct {
	MaintenanceEnabled    bool       `json:"maintenance_enabled"`
	MaintenanceMessage    string     `json:"maintenance_message"`
	AnnouncementText      string     `json:"announcement_text"`
	AnnouncementLevel     string     `json:"announcement_level"`
	AnnouncementExpiresAt *time.Time `json:"announcement_expires_at"`
	SMTPEnabled           bool       `json:"smtp_enabled"`
	SMTPHost              string     `json:"smtp_host"`
	SMTPPort              int        `json:"smtp_port"`
	SMTPSecurity          string     `json:"smtp_security"`
	SMTPUsername          string     `json:"smtp_username"`
	SMTPPasswordSet       bool       `json:"smtp_password_set"`
	SMTPFrom              string     `json:"smtp_from"`
	NotificationEmail     string     `json:"notification_email"`
}

// checkUpdates reports whether a newer release is published.
//
// It never changes anything: a self-hosted band server must not update itself
// under its owner's feet, so this only tells an operator that something exists.
func (s *Server) checkUpdates(c *gin.Context) {
	release, err := s.updates.Latest(c.Request.Context(), c.Query("force") == "true")
	if err != nil {
		if errors.Is(err, updates.ErrNotConfigured) {
			fail(c, http.StatusConflict, "updates_not_configured",
				"no repository is configured for the update check")
			return
		}
		// Reaching GitHub is not something the instance controls, so the
		// reason is passed on rather than hidden behind a 500.
		fail(c, http.StatusBadGateway, "update_check_failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, release)
}

// sendTestMail proves the SMTP settings actually work.
//
// Without it an operator only finds out that outgoing mail is misconfigured
// when a support notification silently fails to arrive.
func (s *Server) sendTestMail(c *gin.Context) {
	var req struct {
		To string `json:"to"`
	}
	_ = c.ShouldBindJSON(&req)

	ctx := c.Request.Context()
	settings, err := s.platformSettings(ctx)
	if err != nil {
		serverError(c, err)
		return
	}

	recipient := strings.TrimSpace(req.To)
	if recipient == "" {
		recipient = settings.NotificationEmail
	}

	password := ""
	if settings.SMTPPasswordEncrypted != "" {
		password, err = s.auth.Cipher().Decrypt(settings.SMTPPasswordEncrypted)
		if err != nil {
			serverError(c, err)
			return
		}
	}

	err = mailer.Send(ctx, mailer.Settings{
		Enabled:  settings.SMTPEnabled,
		Host:     settings.SMTPHost,
		Port:     settings.SMTPPort,
		Security: settings.SMTPSecurity,
		Username: settings.SMTPUsername,
		Password: password,
		From:     settings.SMTPFrom,
		Timeout:  time.Duration(settings.SMTPTimeoutSeconds) * time.Second,
	}, mailer.Message{
		To:      recipient,
		Subject: "Merch Manager: Testnachricht",
		Body: "Diese Nachricht bestätigt, dass der Mailversand dieser Instanz funktioniert." +
			"\n\nAusgelöst von: " + stateFrom(c).User.Username + "\n",
	})
	switch {
	case errors.Is(err, mailer.ErrNotConfigured):
		fail(c, http.StatusConflict, "smtp_not_configured",
			"outgoing mail is not configured")
	case errors.Is(err, mailer.ErrNoRecipient):
		fail(c, http.StatusBadRequest, "no_recipient",
			"give a recipient or set a notification address")
	case err != nil:
		// The reason names the failing step, which is the whole point of a
		// test send, so it is passed through instead of a generic message.
		fail(c, http.StatusBadGateway, "smtp_failed", err.Error())
	default:
		s.audit.Log(ctx, actorFrom(c), audit.Entry{
			Action: "platform.test_mail_sent", EntityType: "platform_settings",
			Details: map[string]any{"to": recipient},
		})
		c.JSON(http.StatusOK, gin.H{"to": recipient})
	}
}

func (s *Server) getPlatformSettings(c *gin.Context) {
	settings, err := s.platformSettings(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, platformSettingsPayload{
		MaintenanceEnabled:    settings.MaintenanceEnabled,
		MaintenanceMessage:    settings.MaintenanceMessage,
		AnnouncementText:      settings.AnnouncementText,
		AnnouncementLevel:     settings.AnnouncementLevel,
		AnnouncementExpiresAt: settings.AnnouncementExpiresAt,
		SMTPEnabled:           settings.SMTPEnabled,
		SMTPHost:              settings.SMTPHost,
		SMTPPort:              settings.SMTPPort,
		SMTPSecurity:          settings.SMTPSecurity,
		SMTPUsername:          settings.SMTPUsername,
		SMTPPasswordSet:       settings.SMTPPasswordEncrypted != "",
		SMTPFrom:              settings.SMTPFrom,
		NotificationEmail:     settings.NotificationEmail,
	})
}

type updateSettingsRequest struct {
	MaintenanceEnabled    *bool      `json:"maintenance_enabled"`
	MaintenanceMessage    *string    `json:"maintenance_message"`
	AnnouncementText      *string    `json:"announcement_text"`
	AnnouncementLevel     *string    `json:"announcement_level"`
	AnnouncementExpiresAt *time.Time `json:"announcement_expires_at"`
	SMTPEnabled           *bool      `json:"smtp_enabled"`
	SMTPHost              *string    `json:"smtp_host"`
	SMTPPort              *int       `json:"smtp_port"`
	SMTPSecurity          *string    `json:"smtp_security"`
	SMTPUsername          *string    `json:"smtp_username"`
	// SMTPPassword is write-only; an omitted field keeps the stored one.
	SMTPPassword      *string `json:"smtp_password"`
	SMTPFrom          *string `json:"smtp_from"`
	NotificationEmail *string `json:"notification_email"`
}

// updatePlatformSettings changes the instance-wide levers: maintenance mode,
// the announcement banner and outgoing mail.
func (s *Server) updatePlatformSettings(c *gin.Context) {
	var req updateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	settings, err := s.platformSettings(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}

	updates := map[string]any{}
	if req.MaintenanceEnabled != nil {
		updates["maintenance_enabled"] = *req.MaintenanceEnabled
	}
	if req.MaintenanceMessage != nil {
		updates["maintenance_message"] = *req.MaintenanceMessage
	}
	if req.AnnouncementText != nil {
		updates["announcement_text"] = *req.AnnouncementText
	}
	if req.AnnouncementLevel != nil {
		switch *req.AnnouncementLevel {
		case "info", "warning", "critical":
			updates["announcement_level"] = *req.AnnouncementLevel
		default:
			fail(c, http.StatusBadRequest, "invalid_level", "the level must be info, warning or critical")
			return
		}
	}
	if req.AnnouncementExpiresAt != nil {
		updates["announcement_expires_at"] = req.AnnouncementExpiresAt.UTC()
	}
	if req.SMTPEnabled != nil {
		updates["smtp_enabled"] = *req.SMTPEnabled
	}
	if req.SMTPHost != nil {
		updates["smtp_host"] = *req.SMTPHost
	}
	if req.SMTPPort != nil {
		updates["smtp_port"] = *req.SMTPPort
	}
	if req.SMTPSecurity != nil {
		switch *req.SMTPSecurity {
		case "ssl", "starttls", "none":
			updates["smtp_security"] = *req.SMTPSecurity
		default:
			fail(c, http.StatusBadRequest, "invalid_security", "the security must be ssl, starttls or none")
			return
		}
	}
	if req.SMTPUsername != nil {
		updates["smtp_username"] = *req.SMTPUsername
	}
	if req.SMTPPassword != nil {
		// Stored encrypted with the same key derivation as the TOTP secrets,
		// so a database dump alone never yields a usable mail login.
		encrypted, err := s.auth.Cipher().Encrypt(*req.SMTPPassword)
		if err != nil {
			serverError(c, err)
			return
		}
		updates["smtp_password_encrypted"] = encrypted
	}
	if req.SMTPFrom != nil {
		updates["smtp_from"] = *req.SMTPFrom
	}
	if req.NotificationEmail != nil {
		updates["notification_email"] = *req.NotificationEmail
	}
	if len(updates) == 0 {
		s.getPlatformSettings(c)
		return
	}

	state := stateFrom(c)
	updates["updated_at"] = time.Now().UTC()
	updates["updated_by_user_id"] = state.User.ID
	updates["updated_by_username"] = state.User.Username

	if err := s.db.WithContext(ctx).Model(&models.PlatformSettings{}).
		Where("id = ?", settings.ID).Updates(updates).Error; err != nil {
		serverError(c, err)
		return
	}
	s.invalidateSettings()

	action := audit.ActionSettingsChanged
	if req.MaintenanceEnabled != nil {
		action = audit.ActionMaintenanceSet
	}
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: action, EntityType: "platform_settings",
		Details: map[string]any{"fields": len(updates) - 3},
	})
	s.getPlatformSettings(c)
}

// revokeBandSessions signs out every member of a band at once.
func (s *Server) revokeBandSessions(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.auth.RevokeBandSessions(ctx, id); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSessionsRevoked, EntityType: "band", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

// revokeUserSessions signs one account out everywhere.
func (s *Server) revokeUserSessions(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	// Bumping the version is what makes the revocation stick even for a
	// session that is mid-request on another instance.
	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).
		UpdateColumn("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
		serverError(c, err)
		return
	}
	if err := s.auth.RevokeUserSessions(c.Request.Context(), id); err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: audit.ActionSessionsRevoked, EntityType: "user", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

// --- support inbox --------------------------------------------------------

type supportMessagePayload struct {
	models.AdminMessage
	BandName string `json:"band_name"`
}

// listSupportMessages is the cross-band inbox.
func (s *Server) listSupportMessages(c *gin.Context) {
	ctx := tenant.WithCrossBandAccess(c.Request.Context())

	query := s.db.WithContext(ctx).Model(&models.AdminMessage{}).
		Select("admin_messages.*, COALESCE(bands.name, '') AS band_name").
		Joins("LEFT JOIN bands ON bands.id = admin_messages.band_id").
		Order("admin_messages.created_at DESC, admin_messages.id DESC")

	if c.Query("open") == "true" {
		query = query.Where("admin_messages.is_resolved = ?", false)
	}

	var messages []supportMessagePayload
	if err := query.Limit(500).Scan(&messages).Error; err != nil {
		serverError(c, err)
		return
	}
	if messages == nil {
		messages = []supportMessagePayload{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

type supportMessageAssignee struct {
	ID       int64       `json:"id"`
	Username string      `json:"username"`
	Role     models.Role `json:"role"`
}

// listSupportMessageAssignees returns active platform staff. Support admins
// may use this narrow list without gaining access to system-only account
// management.
func (s *Server) listSupportMessageAssignees(c *gin.Context) {
	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	var users []supportMessageAssignee
	if err := s.db.WithContext(ctx).Model(&models.User{}).
		Select("id, username, role").
		Where("band_id IS NULL AND is_active = ? AND role IN ?", true,
			[]models.Role{models.RoleSupportAdmin, models.RoleSystemAdmin}).
		Order("username, id").Scan(&users).Error; err != nil {
		serverError(c, err)
		return
	}
	if users == nil {
		users = []supportMessageAssignee{}
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (s *Server) assignSupportMessage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var body struct {
		AssigneeUserID *int64 `json:"assignee_user_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := tenant.WithCrossBandAccess(c.Request.Context())
	updates := map[string]any{
		"assigned_to_user_id":  nil,
		"assigned_to_username": "",
	}
	if body.AssigneeUserID != nil {
		var user models.User
		if err := s.db.WithContext(ctx).
			Where("id = ? AND band_id IS NULL AND is_active = ? AND role IN ?", *body.AssigneeUserID, true,
				[]models.Role{models.RoleSupportAdmin, models.RoleSystemAdmin}).
			First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				fail(c, http.StatusBadRequest, "invalid_assignee", "assignee must be active platform staff")
				return
			}
			serverError(c, err)
			return
		}
		updates["assigned_to_user_id"] = user.ID
		updates["assigned_to_username"] = user.Username
	}

	result := s.db.WithContext(ctx).Model(&models.AdminMessage{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		serverError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such message")
		return
	}
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: "support_message.assigned", EntityType: "admin_message", EntityID: &id,
		Details: map[string]any{"assignee_user_id": body.AssigneeUserID},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) resolveSupportMessage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	var body struct {
		Resolved bool `json:"resolved"`
	}
	_ = c.ShouldBindJSON(&body)

	state := stateFrom(c)
	ctx := tenant.WithCrossBandAccess(c.Request.Context())

	updates := map[string]any{"is_resolved": body.Resolved}
	if body.Resolved {
		updates["resolved_at"] = time.Now().UTC()
		updates["resolved_by_user_id"] = state.User.ID
		updates["resolved_by_username"] = state.User.Username
	} else {
		updates["resolved_at"] = nil
		updates["resolved_by_user_id"] = nil
		updates["resolved_by_username"] = ""
	}

	result := s.db.WithContext(ctx).Model(&models.AdminMessage{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		serverError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such message")
		return
	}
	c.Status(http.StatusNoContent)
}
