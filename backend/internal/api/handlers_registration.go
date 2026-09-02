package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/services/registration"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerPublicRegistrationRoutes(g *gin.RouterGroup) {
	r := g.Group("/public/registrations")
	r.GET("/config", s.registrationConfig)
	r.POST("", s.registrationLimit(s.registrationCreateLimiter), s.createRegistration)
	r.POST("/status", s.registrationLimit(s.registrationAccessLimiter), s.registrationStatus)
	r.POST("/claim", s.registrationLimit(s.registrationAccessLimiter), s.claimRegistration)
}

func (s *Server) registerPlatformRegistrationRoutes(g *gin.RouterGroup) {
	r := g.Group("/platform/registration-requests", requireAuth(), requireSystemAdmin())
	r.GET("", s.listRegistrationRequests)
	r.GET("/:id", s.getRegistrationRequest)
	r.POST("/:id/approve", s.approveRegistrationRequest)
	r.POST("/:id/reject", s.rejectRegistrationRequest)
}

func (s *Server) registrationLimit(limiter *requestLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, retryAfter := limiter.allow(c.ClientIP(), time.Now())
		if allowed {
			c.Next()
			return
		}
		seconds := int(retryAfter.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many registration requests; try again later")
	}
}

func (s *Server) registrationConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"registration_enabled": s.cfg.PublicRegistrationEnabled})
}

type publicRegistrationRequest struct {
	BandName        string `json:"band_name" binding:"required"`
	BandSlug        string `json:"band_slug" binding:"required"`
	AdminUsername   string `json:"admin_username" binding:"required"`
	ContactEmail    string `json:"contact_email" binding:"required"`
	PrivacyAccepted bool   `json:"privacy_accepted"`
	Website         string `json:"website"`
}

func (s *Server) createRegistration(c *gin.Context) {
	if !s.cfg.PublicRegistrationEnabled {
		fail(c, http.StatusForbidden, "registration_disabled", "public registration is disabled")
		return
	}
	var req publicRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Website) != "" {
		fail(c, http.StatusBadRequest, "invalid_request", "invalid registration request")
		return
	}
	if !req.PrivacyAccepted {
		failWithDetails(c, http.StatusBadRequest, "privacy_required", "privacy consent is required",
			map[string]string{"privacy_accepted": "required"})
		return
	}
	email, err := normalizeEmail(req.ContactEmail, false)
	if err != nil {
		failWithDetails(c, http.StatusBadRequest, "invalid_email", err.Error(),
			map[string]string{"contact_email": err.Error()})
		return
	}
	created, err := s.registrations.Create(c.Request.Context(), registration.CreateInput{
		BandName: req.BandName, BandSlug: req.BandSlug,
		AdminUsername: req.AdminUsername, ContactEmail: email,
	})
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"reference":  created.Request.PublicID,
		"status":     created.Request.Status,
		"status_url": created.StatusURL,
		"expires_at": created.Request.ExpiresAt,
	})
}

type registrationTokenRequest struct {
	Token string `json:"token" binding:"required"`
}

func (s *Server) registrationStatus(c *gin.Context) {
	var req registrationTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request, err := s.registrations.Status(c.Request.Context(), req.Token)
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, publicRegistrationPayload(request))
}

func publicRegistrationPayload(request *models.BandRegistrationRequest) gin.H {
	available := request.Status == models.BandRegistrationApproved && request.ClaimedAt == nil &&
		request.SetupCodeEncrypted != "" && request.CredentialsAvailableUntil != nil &&
		time.Now().UTC().Before(*request.CredentialsAvailableUntil)
	return gin.H{
		"reference":                   request.PublicID,
		"status":                      request.Status,
		"band_name":                   request.FinalBandName,
		"band_slug":                   request.FinalBandSlug,
		"admin_username":              request.FinalAdminUsername,
		"contact_email":               request.FinalContactEmail,
		"decision_note":               request.DecisionNote,
		"credentials_available":       available,
		"credentials_retrieved":       request.ClaimedAt != nil,
		"credentials_available_until": request.CredentialsAvailableUntil,
		"expires_at":                  request.ExpiresAt,
	}
}

func (s *Server) claimRegistration(c *gin.Context) {
	var req registrationTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	credentials, err := s.registrations.Claim(c.Request.Context(), req.Token)
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"band_slug":             credentials.BandSlug,
		"username":              credentials.Username,
		"setup_code":            credentials.SetupCode,
		"setup_code_expires_at": credentials.ExpiresAt,
	})
}

func (s *Server) listRegistrationRequests(c *gin.Context) {
	status := models.BandRegistrationStatus(strings.TrimSpace(c.Query("status")))
	if status != "" && !validRegistrationStatus(status) {
		fail(c, http.StatusBadRequest, "invalid_status", "unknown registration status")
		return
	}
	requests, err := s.registrations.List(c.Request.Context(), status)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"requests": requests})
}

func (s *Server) getRegistrationRequest(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	request, err := s.registrations.Get(c.Request.Context(), id)
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, request)
}

type approveRegistrationRequest struct {
	BandName      string `json:"band_name" binding:"required"`
	BandSlug      string `json:"band_slug" binding:"required"`
	AdminUsername string `json:"admin_username" binding:"required"`
	ContactEmail  string `json:"contact_email" binding:"required"`
}

func (s *Server) approveRegistrationRequest(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req approveRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	email, err := normalizeEmail(req.ContactEmail, false)
	if err != nil {
		failWithDetails(c, http.StatusBadRequest, "invalid_email", err.Error(),
			map[string]string{"contact_email": err.Error()})
		return
	}
	state := stateFrom(c)
	request, err := s.registrations.Approve(c.Request.Context(), id, registration.ApproveInput{
		BandName: req.BandName, BandSlug: req.BandSlug,
		AdminUsername: req.AdminUsername, ContactEmail: email,
		DecidedByID: state.User.ID, DecidedByName: state.User.Username,
	})
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	actor := actorFrom(c)
	s.audit.Log(c.Request.Context(), actor, audit.Entry{
		Action: audit.ActionRegistrationApproved, EntityType: "band_registration_request", EntityID: &request.ID,
		Details: map[string]any{"reference": request.PublicID, "band_id": request.BandID},
	})
	if request.BandID != nil {
		s.audit.Log(c.Request.Context(), actor, audit.Entry{
			Action: audit.ActionBandCreated, EntityType: "band", EntityID: request.BandID,
			Details: map[string]any{"slug": request.FinalBandSlug, "name": request.FinalBandName, "registration": request.PublicID},
		})
	}
	if request.BandID != nil && request.AdminUserID != nil {
		s.audit.Log(tenant.WithBand(c.Request.Context(), *request.BandID), actor, audit.Entry{
			Action: audit.ActionUserCreated, EntityType: "user", EntityID: request.AdminUserID,
			Details: map[string]any{"username": request.FinalAdminUsername, "role": string(models.RoleBandAdmin), "bootstrap": true, "registration": request.PublicID},
		})
	}
	c.JSON(http.StatusOK, request)
}

type rejectRegistrationRequest struct {
	Note string `json:"note"`
}

func (s *Server) rejectRegistrationRequest(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req rejectRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state := stateFrom(c)
	request, err := s.registrations.Reject(c.Request.Context(), id, state.User.ID, state.User.Username, req.Note)
	if err != nil {
		s.reportRegistrationError(c, err)
		return
	}
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: audit.ActionRegistrationRejected, EntityType: "band_registration_request", EntityID: &request.ID,
		Details: map[string]any{"reference": request.PublicID},
	})
	c.JSON(http.StatusOK, request)
}

func validRegistrationStatus(status models.BandRegistrationStatus) bool {
	switch status {
	case models.BandRegistrationPending, models.BandRegistrationApproved,
		models.BandRegistrationRejected, models.BandRegistrationExpired:
		return true
	default:
		return false
	}
}

func (s *Server) reportRegistrationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, registration.ErrNotFound):
		fail(c, http.StatusNotFound, "registration_not_found", err.Error())
	case errors.Is(err, registration.ErrNotPending):
		fail(c, http.StatusConflict, "registration_decided", err.Error())
	case errors.Is(err, registration.ErrExpired):
		fail(c, http.StatusGone, "registration_expired", err.Error())
	case errors.Is(err, registration.ErrNotApproved):
		fail(c, http.StatusConflict, "registration_not_approved", err.Error())
	case errors.Is(err, registration.ErrCredentialsClaimed):
		fail(c, http.StatusGone, "credentials_claimed", err.Error())
	case errors.Is(err, registration.ErrCredentialsUnavailable):
		fail(c, http.StatusGone, "credentials_unavailable", err.Error())
	case errors.Is(err, registration.ErrDecisionNoteTooLong):
		fail(c, http.StatusBadRequest, "invalid_decision_note", err.Error())
	case errors.Is(err, platform.ErrSlugTaken):
		failWithDetails(c, http.StatusConflict, "slug_taken", err.Error(), map[string]string{"band_slug": "taken"})
	case errors.Is(err, platform.ErrInvalidSlug):
		failWithDetails(c, http.StatusBadRequest, "invalid_slug", err.Error(), map[string]string{"band_slug": "invalid"})
	case errors.Is(err, platform.ErrInvalidName):
		failWithDetails(c, http.StatusBadRequest, "invalid_name", err.Error(), map[string]string{"band_name": "invalid"})
	case errors.Is(err, auth.ErrInvalidUsername):
		failWithDetails(c, http.StatusBadRequest, "invalid_username", err.Error(), map[string]string{"admin_username": "invalid"})
	case errors.Is(err, auth.ErrUsernameTaken):
		failWithDetails(c, http.StatusConflict, "username_taken", err.Error(), map[string]string{"admin_username": "taken"})
	default:
		serverError(c, err)
	}
}
