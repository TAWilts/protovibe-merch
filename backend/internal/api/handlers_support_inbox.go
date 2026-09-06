package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/mailer"
)

func (s *Server) registerSupportInboxRoutes(g *gin.RouterGroup) {
	// Anyone in a band may report a problem; that is the point of the button.
	g.POST("/support-messages", requireAuth(), requireBandRole(models.RoleSeller), s.sendSupportMessage)
	// A band admin sees what their own band reported.
	g.GET("/support-messages", requireAuth(), requireBandRole(models.RoleBandAdmin), s.listBandSupportMessages)
	g.GET("/announcement", requireAuth(), s.announcement)
}

type supportMessageRequest struct {
	MessageType string `json:"message_type" binding:"required"`
	SenderEmail string `json:"sender_email"`
	Subject     string `json:"subject" binding:"required"`
	Body        string `json:"body" binding:"required"`
}

// sendSupportMessage files an issue or a question for the platform's inbox.
//
// The message is stored first and mailed second: if SMTP is down or
// misconfigured, the band's report must still reach the operator, just later.
func (s *Server) sendSupportMessage(c *gin.Context) {
	var req supportMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	messageType := models.AdminMessageType(req.MessageType)
	if messageType != models.AdminMessageIssue && messageType != models.AdminMessageQuestion {
		fail(c, http.StatusBadRequest, "invalid_message_type", "message type must be issue or question")
		return
	}

	subject := strings.TrimSpace(req.Subject)
	body := strings.TrimSpace(req.Body)
	email, err := normalizeEmail(req.SenderEmail, false)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_email", err.Error())
		return
	}
	if subject == "" || body == "" {
		fail(c, http.StatusBadRequest, "invalid_request", "subject and body are required")
		return
	}
	if len(subject) > 120 {
		fail(c, http.StatusBadRequest, "invalid_subject", "subject must not exceed 120 characters")
		return
	}
	if len(body) > 4000 {
		fail(c, http.StatusBadRequest, "invalid_message", "message must not exceed 4000 characters")
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	message := &models.AdminMessage{
		SenderUserID:   &state.User.ID,
		SenderUsername: state.User.Username,
		SenderEmail:    email,
		MessageType:    messageType,
		Subject:        subject,
		Body:           body,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(message).Error; err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "support_message.sent", EntityType: "admin_message", EntityID: &message.ID,
		Details: map[string]any{"type": string(messageType)},
	})
	s.sendSupportNotification(ctx, message)
	c.JSON(http.StatusCreated, gin.H{"id": message.ID})
}

// sendSupportNotification mirrors the legacy mailbox notification. The
// database inbox remains authoritative: an SMTP problem is logged but never
// makes a successfully stored support request disappear for the sender.
func (s *Server) sendSupportNotification(ctx context.Context, message *models.AdminMessage) {
	settings, err := s.platformSettings(ctx)
	if err != nil {
		slog.Warn("support notification settings unavailable", "message_id", message.ID, "error", err)
		return
	}
	recipient := strings.TrimSpace(settings.NotificationEmail)
	if recipient == "" {
		return
	}
	mailSettings, err := s.outgoingMailSettings(ctx)
	if err != nil {
		slog.Warn("support notification settings invalid", "message_id", message.ID, "error", err)
		return
	}

	bandName := fmt.Sprintf("Band #%d", message.BandID)
	var band models.Band
	if err := s.db.WithContext(ctx).First(&band, message.BandID).Error; err == nil && band.Name != "" {
		bandName = band.Name
	}
	typeLabel := "Issue / Problem"
	if message.MessageType == models.AdminMessageQuestion {
		typeLabel = "Frage"
	}
	err = mailer.Send(ctx, mailSettings, mailer.Message{
		To:      recipient,
		Subject: "[Merch Manager] " + typeLabel + ": " + message.Subject,
		Body: "Band: " + bandName + "\n" +
			"Von: " + message.SenderUsername + "\n" +
			"E-Mail: " + message.SenderEmail + "\n" +
			"Art: " + typeLabel + "\n" +
			"Betreff: " + message.Subject + "\n\n" + message.Body + "\n",
	})
	if err != nil && !errors.Is(err, mailer.ErrNotConfigured) && !errors.Is(err, mailer.ErrNoRecipient) {
		slog.Warn("support notification failed", "message_id", message.ID, "error", err)
	}
}

// listBandSupportMessages lets a band see what it reported and what came back.
func (s *Server) listBandSupportMessages(c *gin.Context) {
	var messages []models.AdminMessage
	err := s.db.WithContext(c.Request.Context()).
		Order("created_at DESC, id DESC").Limit(200).Find(&messages).Error
	if err != nil {
		serverError(c, err)
		return
	}
	if messages == nil {
		messages = []models.AdminMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

// announcementBanner is the instance-wide notice shown to every band.
type announcementBanner struct {
	Text  string `json:"text"`
	Level string `json:"level"`
}

type maintenanceBanner struct {
	Message string `json:"message"`
}

// announcement returns the current banner, if any. It is public to any signed
// in account, because a planned maintenance window concerns everybody.
func (s *Server) announcement(c *gin.Context) {
	settings, err := s.platformSettings(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}

	maintenanceEnabled, maintenanceMessage, err := s.maintenanceStatus(c.Request.Context(), stateFrom(c))
	if err != nil {
		serverError(c, err)
		return
	}

	var announcement *announcementBanner
	if settings.AnnouncementText != "" &&
		(settings.AnnouncementExpiresAt == nil || time.Now().UTC().Before(*settings.AnnouncementExpiresAt)) {
		announcement = &announcementBanner{Text: settings.AnnouncementText, Level: settings.AnnouncementLevel}
	}
	var maintenance *maintenanceBanner
	if maintenanceEnabled {
		maintenance = &maintenanceBanner{Message: maintenanceMessage}
	}
	c.JSON(http.StatusOK, gin.H{"announcement": announcement, "maintenance": maintenance})
}
