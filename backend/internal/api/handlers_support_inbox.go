package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
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

	messageType := models.AdminMessageQuestion
	if req.MessageType == string(models.AdminMessageIssue) {
		messageType = models.AdminMessageIssue
	}

	subject := strings.TrimSpace(req.Subject)
	body := strings.TrimSpace(req.Body)
	if subject == "" || body == "" {
		fail(c, http.StatusBadRequest, "invalid_request", "subject and body are required")
		return
	}
	if len(subject) > 200 {
		subject = subject[:200]
	}
	if len(body) > 4000 {
		body = body[:4000]
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	message := &models.AdminMessage{
		SenderUserID:   &state.User.ID,
		SenderUsername: state.User.Username,
		SenderEmail:    strings.TrimSpace(req.SenderEmail),
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
	c.JSON(http.StatusCreated, gin.H{"id": message.ID})
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

// announcement returns the current banner, if any. It is public to any signed
// in account, because a planned maintenance window concerns everybody.
func (s *Server) announcement(c *gin.Context) {
	settings, err := s.platformSettings(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}

	if settings.AnnouncementText == "" ||
		(settings.AnnouncementExpiresAt != nil && time.Now().UTC().After(*settings.AnnouncementExpiresAt)) {
		c.JSON(http.StatusOK, gin.H{"announcement": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcement": announcementBanner{
		Text: settings.AnnouncementText, Level: settings.AnnouncementLevel,
	}})
}
