package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/rbac"
)

// ctxKeyRequestState is where the resolved session lives inside gin.Context.
const ctxKeyRequestState = "merch.request_state"

// RequestState is everything the middleware chain resolved for one request.
type RequestState struct {
	Session *models.Session
	User    *models.User
	// Grant is set only when platform staff operate under a live
	// support-access grant.
	Grant *models.SupportAccessGrant
	Caps  rbac.Capabilities
}

// stateFrom returns the resolved request state, or nil for an anonymous request.
func stateFrom(c *gin.Context) *RequestState {
	value, ok := c.Get(ctxKeyRequestState)
	if !ok {
		return nil
	}
	state, _ := value.(*RequestState)
	return state
}

// actorFrom builds the audit actor for the current request.
func actorFrom(c *gin.Context) audit.Actor {
	actor := audit.Actor{IPAddress: c.ClientIP()}
	if state := stateFrom(c); state != nil && state.User != nil {
		actor.UserID = &state.User.ID
		actor.Username = state.User.Username
	}
	return actor
}

// errorBody is the single JSON error shape the API returns. `code` is a stable
// identifier the frontend maps to a translated message; `message` is a English
// fallback for logs and for developers.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries field-level validation problems.
	Details map[string]string `json:"details,omitempty"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody{Code: code, Message: message})
}

func failWithDetails(c *gin.Context, status int, code, message string, details map[string]string) {
	c.AbortWithStatusJSON(status, errorBody{Code: code, Message: message, Details: details})
}

// Common failures, kept in one place so codes stay consistent.
func unauthorized(c *gin.Context) {
	fail(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
}

func forbidden(c *gin.Context, code, message string) {
	fail(c, http.StatusForbidden, code, message)
}

func serverError(c *gin.Context, err error) {
	_ = c.Error(err)
	fail(c, http.StatusInternalServerError, "internal_error", "unexpected server error")
}

// roleLabel is the display name of a role, kept next to the other response
// helpers so handlers do not each import rbac for one call.
func roleLabel(role models.Role) string { return rbac.Label(role) }
