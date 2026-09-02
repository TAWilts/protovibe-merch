// Package audit writes the tamper-evident activity trail.
//
// Every entry carries the acting user, the band it concerned and — crucially
// for a hosted multi-tenant deployment — the support-access grant it ran
// under. That is what lets a band answer "who from support looked at our data,
// when, and under which approval".
package audit

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Action names. They are stable identifiers, not display text; the frontend
// translates them.
const (
	ActionLogin               = "auth.login"
	ActionLoginFailed         = "auth.login_failed"
	ActionLogout              = "auth.logout"
	ActionPasswordChanged     = "auth.password_changed"
	ActionPasswordReset       = "auth.password_reset"
	ActionMFAEnrolled         = "auth.mfa_enrolled"
	ActionMFADisabled         = "auth.mfa_disabled"
	ActionMFAReset            = "auth.mfa_reset"
	ActionRecoveryCodesIssued = "auth.recovery_codes_issued"
	ActionReauthenticated     = "auth.reauthenticated"
	ActionSessionsRevoked     = "auth.sessions_revoked"

	ActionUserCreated     = "user.created"
	ActionUserRoleChanged = "user.role_changed"
	ActionUserActivated   = "user.activated"
	ActionUserDeactivated = "user.deactivated"
	ActionUserDeleted     = "user.deleted"
	ActionUsernameChanged = "user.username_changed"

	ActionBandCreated          = "band.created"
	ActionBandUpdated          = "band.updated"
	ActionBandDeactivated      = "band.deactivated"
	ActionBandDeleted          = "band.deleted"
	ActionBandRestored         = "band.restored"
	ActionRegistrationApproved = "registration.approved"
	ActionRegistrationRejected = "registration.rejected"

	ActionSupportAccessRequested = "support_access.requested"
	ActionSupportAccessApproved  = "support_access.approved"
	ActionSupportAccessDenied    = "support_access.denied"
	ActionSupportAccessActivated = "support_access.activated"
	ActionSupportAccessRevoked   = "support_access.revoked"
	ActionSupportAccessExpired   = "support_access.expired"

	ActionArticleCreated  = "article.created"
	ActionArticleUpdated  = "article.updated"
	ActionSaleCreated     = "sale.created"
	ActionSaleCancelled   = "sale.cancelled"
	ActionSaleStatus      = "sale.status_changed"
	ActionPurchaseCreated = "purchase.created"
	ActionPurchaseUpdated = "purchase.updated"
	ActionPurchaseDeleted = "purchase.deleted"

	ActionBackupStarted   = "backup.started"
	ActionBackupFinished  = "backup.finished"
	ActionBackupRestored  = "backup.restored"
	ActionMaintenanceSet  = "platform.maintenance_changed"
	ActionSettingsChanged = "platform.settings_changed"
)

// Entry is one audit record. Actor and band are filled in from the request
// context by Log, so callers only describe what happened.
type Entry struct {
	Action     string
	EntityType string
	EntityID   *int64
	Details    models.JSONMap
}

// Actor identifies who performed an action.
type Actor struct {
	UserID    *int64
	Username  string
	IPAddress string
}

// Logger writes audit entries.
type Logger struct {
	db *gorm.DB
}

// New builds a Logger.
func New(database *gorm.DB) *Logger { return &Logger{db: database} }

// Log records one entry.
//
// A failure to write the audit trail must never fail the operation the user
// asked for — a sale at a gig is more important than its log line — so the
// error is reported to the process log and swallowed.
func (l *Logger) Log(ctx context.Context, actor Actor, entry Entry) {
	if err := l.LogErr(ctx, actor, entry); err != nil {
		slog.Error("audit write failed",
			"error", err, "action", entry.Action, "entity_type", entry.EntityType)
	}
}

// LogErr is Log for the few callers that want to fail loudly, such as the
// support-access workflow where the trail is the point of the feature.
func (l *Logger) LogErr(ctx context.Context, actor Actor, entry Entry) error {
	record := &models.AuditLog{
		UserID:        actor.UserID,
		Username:      actor.Username,
		ActingGrantID: tenant.GrantID(ctx),
		Action:        entry.Action,
		EntityType:    entry.EntityType,
		EntityID:      entry.EntityID,
		Details:       entry.Details,
		IPAddress:     actor.IPAddress,
	}
	if record.Details == nil {
		record.Details = models.JSONMap{}
	}
	if bandID, err := tenant.BandID(ctx); err == nil {
		record.BandID = &bandID
	}

	// The audit log is a control-plane table and is written for band and
	// platform actions alike, so it runs outside the tenant filter.
	return l.db.WithContext(tenant.WithCrossBandAccess(ctx)).Create(record).Error
}
