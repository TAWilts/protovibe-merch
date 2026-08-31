package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/qrimage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// mfaQRSize is the edge length of the enrolment QR code. The secret makes for
// a denser code than a payment payload, so it gets a little more room.
const mfaQRSize = 360

type mfaEnrollmentStartRequest struct {
	// PendingToken is set when enrolment happens as part of a login, because a
	// platform account may not have a session yet.
	PendingToken string `json:"pending_token"`
}

type mfaEnrollmentStartResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
	// OTPAuthQR is the same URI as a scannable PNG data URI. It is rendered
	// here rather than in the browser so enrolment also works offline.
	OTPAuthQR string `json:"otpauth_qr"`
}

// startMFAEnrollment issues a pending TOTP secret.
//
// The secret only becomes active once a live code proves the authenticator app
// holds it, which is what confirmMFAEnrollment checks. Until then a failed
// setup leaves the account exactly as it was.
func (s *Server) startMFAEnrollment(c *gin.Context) {
	var req mfaEnrollmentStartRequest
	_ = c.ShouldBindJSON(&req)

	user, ok := s.enrollmentSubject(c, req.PendingToken)
	if !ok {
		return
	}

	secret, uri, err := s.auth.BeginEnrollment(user)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}
	if err := s.db.WithContext(tenant.WithCrossBandAccess(c.Request.Context())).
		Model(user).Update("mfa_pending_secret_encrypted", user.MFAPendingSecretEncrypted).Error; err != nil {
		serverError(c, err)
		return
	}

	image, err := qrimage.DataURI(uri, mfaQRSize)
	if err != nil {
		serverError(c, err)
		return
	}

	c.JSON(http.StatusOK, mfaEnrollmentStartResponse{Secret: secret, OTPAuthURI: uri, OTPAuthQR: image})
}

type mfaEnrollmentConfirmRequest struct {
	PendingToken string `json:"pending_token"`
	Code         string `json:"code" binding:"required"`
}

type mfaEnrollmentConfirmResponse struct {
	// RecoveryCodes are shown exactly once and never retrievable afterwards.
	RecoveryCodes []string    `json:"recovery_codes"`
	Session       *meResponse `json:"session,omitempty"`
	CSRFToken     string      `json:"csrf_token,omitempty"`
}

func (s *Server) confirmMFAEnrollment(c *gin.Context) {
	var req mfaEnrollmentConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	user, ok := s.enrollmentSubject(c, req.PendingToken)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	codes, err := s.auth.ConfirmEnrollment(user, req.Code)
	if err != nil {
		s.reportAuthError(c, err)
		return
	}
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Save(user).Error; err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, audit.Actor{UserID: &user.ID, Username: user.Username, IPAddress: c.ClientIP()},
		audit.Entry{Action: audit.ActionMFAEnrolled, EntityType: "user", EntityID: &user.ID})

	// When enrolment finished a login, hand out the session now.
	if req.PendingToken != "" {
		if _, err := s.auth.ConsumePendingAuth(ctx, req.PendingToken, models.PendingAuthMFAEnrollment); err != nil {
			s.reportAuthError(c, err)
			return
		}
		sessionToken, csrfToken, err := s.auth.CreateSession(ctx, user, c.Request.UserAgent(), c.ClientIP())
		if err != nil {
			serverError(c, err)
			return
		}
		s.setSessionCookie(c, sessionToken)
		s.setCSRFCookie(c, csrfToken)
		c.JSON(http.StatusOK, mfaEnrollmentConfirmResponse{
			RecoveryCodes: codes,
			Session:       s.identityPayload(ctx, user, nil),
			CSRFToken:     csrfToken,
		})
		return
	}

	c.JSON(http.StatusOK, mfaEnrollmentConfirmResponse{RecoveryCodes: codes})
}

// enrollmentSubject resolves who is enrolling: either a pending login (no
// session yet) or the signed-in user, who must have confirmed their password
// recently before touching their second factor.
func (s *Server) enrollmentSubject(c *gin.Context, pendingToken string) (*models.User, bool) {
	ctx := c.Request.Context()

	if pendingToken != "" {
		user, err := s.auth.PeekPendingAuth(ctx, pendingToken, models.PendingAuthMFAEnrollment)
		if err != nil {
			s.reportAuthError(c, err)
			return nil, false
		}
		return user, true
	}

	state := stateFrom(c)
	if state == nil {
		unauthorized(c)
		return nil, false
	}
	if !s.auth.HasFreshReauth(state.Session) {
		fail(c, http.StatusForbidden, "reauth_required", "confirm your password again to continue")
		return nil, false
	}
	return state.User, true
}

// disableMFA removes a voluntarily enabled second factor.
//
// Platform accounts cannot use it: their 2FA is mandatory, and letting them
// switch it off would undermine the support-access workflow that depends on a
// fresh code.
func (s *Server) disableMFA(c *gin.Context) {
	state := stateFrom(c)
	if state.User.MFARequired() {
		forbidden(c, "mfa_mandatory", "platform accounts must keep two-factor authentication enabled")
		return
	}
	if !state.User.MFAEnabled {
		fail(c, http.StatusBadRequest, "mfa_not_enrolled", "no second factor is enrolled")
		return
	}

	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.VerifySecondFactor(state.User, req.Code); err != nil {
		s.reportAuthError(c, err)
		return
	}

	auth.DisableMFA(state.User)
	ctx := c.Request.Context()
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Save(state.User).Error; err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c),
		audit.Entry{Action: audit.ActionMFADisabled, EntityType: "user", EntityID: &state.User.ID})
	c.Status(http.StatusNoContent)
}

// regenerateRecoveryCodes issues a fresh set and invalidates the previous one.
func (s *Server) regenerateRecoveryCodes(c *gin.Context) {
	state := stateFrom(c)

	codes, err := s.auth.RegenerateRecoveryCodes(state.User)
	if err != nil {
		if errors.Is(err, auth.ErrMFANotEnrolled) {
			fail(c, http.StatusBadRequest, "mfa_not_enrolled", "no second factor is enrolled")
			return
		}
		serverError(c, err)
		return
	}

	ctx := c.Request.Context()
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Model(state.User).Update("mfa_recovery_code_hashes", state.User.MFARecoveryCodeHashes).Error; err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c),
		audit.Entry{Action: audit.ActionRecoveryCodesIssued, EntityType: "user", EntityID: &state.User.ID})
	c.JSON(http.StatusOK, gin.H{"recovery_codes": codes})
}
