package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Band is a tenant: one band's catalogue, bookings, accounts and settings.
type Band struct {
	ID            int64      `gorm:"primaryKey" json:"id"`
	Slug          string     `gorm:"size:64;uniqueIndex;not null" json:"slug"`
	Name          string     `gorm:"size:200;not null" json:"name"`
	ContactEmail  string     `gorm:"size:254;not null;default:''" json:"contact_email"`
	IsActive      bool       `gorm:"not null" json:"is_active"`
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`
	// DeletedAt is a grace period, not a hard delete. Rows stay in place so a
	// band that was removed by mistake can be restored without a backup.
	DeletedAt *time.Time `gorm:"index" json:"deleted_at,omitempty"`
	// MaintenanceMessage, when set, puts this single band into maintenance
	// mode without affecting the rest of the instance.
	MaintenanceMessage string `gorm:"size:500;not null;default:''" json:"maintenance_message"`

	StorageQuotaBytes int64        `gorm:"not null;default:0" json:"storage_quota_bytes"`
	UserQuota         int          `gorm:"not null;default:0" json:"user_quota"`
	FeatureFlags      FeatureFlags `gorm:"type:json;not null" json:"feature_flags"`

	Timestamps
}

func (Band) TableName() string { return "bands" }

// FeatureFlags switches optional areas off for a band. Absent keys mean
// enabled, so adding a new flag never silently disables an existing band.
type FeatureFlags struct {
	Slideshow    *bool `json:"slideshow,omitempty"`
	BandFinances *bool `json:"band_finances,omitempty"`
	PaymentQR    *bool `json:"payment_qr,omitempty"`
	OfflineSales *bool `json:"offline_sales,omitempty"`
	CSVImport    *bool `json:"csv_import,omitempty"`
}

// Enabled reports a flag's effective state; nil means enabled.
func enabled(flag *bool) bool { return flag == nil || *flag }

func (f FeatureFlags) SlideshowEnabled() bool    { return enabled(f.Slideshow) }
func (f FeatureFlags) BandFinancesEnabled() bool { return enabled(f.BandFinances) }
func (f FeatureFlags) PaymentQREnabled() bool    { return enabled(f.PaymentQR) }
func (f FeatureFlags) OfflineSalesEnabled() bool { return enabled(f.OfflineSales) }
func (f FeatureFlags) CSVImportEnabled() bool    { return enabled(f.CSVImport) }

func (f FeatureFlags) Value() (driver.Value, error) {
	return json.Marshal(f)
}

func (f *FeatureFlags) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*f = FeatureFlags{}
		return nil
	case []byte:
		return json.Unmarshal(v, f)
	case string:
		return json.Unmarshal([]byte(v), f)
	default:
		return fmt.Errorf("cannot scan %T into FeatureFlags", value)
	}
}

// SupportAccessScope limits what a granted support session may do.
type SupportAccessScope string

const (
	SupportScopeReadOnly  SupportAccessScope = "read_only"
	SupportScopeReadWrite SupportAccessScope = "read_write"
)

// SupportAccessStatus is the grant's lifecycle.
//
// The flow is fixed and has no bypass: a platform admin requests access with a
// reason, a band admin approves or denies it, the platform admin then activates
// it with a fresh TOTP code, and it expires on its own.
type SupportAccessStatus string

const (
	SupportStatusPending  SupportAccessStatus = "pending"
	SupportStatusApproved SupportAccessStatus = "approved"
	SupportStatusDenied   SupportAccessStatus = "denied"
	SupportStatusActive   SupportAccessStatus = "active"
	SupportStatusExpired  SupportAccessStatus = "expired"
	SupportStatusRevoked  SupportAccessStatus = "revoked"
)

// SupportAccessGrant is the only path from a platform account to band data.
type SupportAccessGrant struct {
	ID     int64 `gorm:"primaryKey" json:"id"`
	BandID int64 `gorm:"not null;index" json:"band_id"`

	RequestedByUserID   int64              `gorm:"not null;index" json:"requested_by_user_id"`
	RequestedByUsername string             `gorm:"size:150;not null" json:"requested_by_username"`
	Reason              string             `gorm:"size:1000;not null" json:"reason"`
	Scope               SupportAccessScope `gorm:"size:20;not null" json:"scope"`
	// RequestedDuration is how long the grant should last once activated.
	RequestedDurationSeconds int `gorm:"not null" json:"requested_duration_seconds"`

	Status SupportAccessStatus `gorm:"size:20;not null;index" json:"status"`

	DecidedByUserID   *int64     `json:"decided_by_user_id,omitempty"`
	DecidedByUsername string     `gorm:"size:150;not null;default:''" json:"decided_by_username"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	DecisionNote      string     `gorm:"size:1000;not null;default:''" json:"decision_note"`

	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ExpiresAt   *time.Time `gorm:"index" json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	// RevokedByUserID is set both when a band admin pulls the plug and when a
	// platform admin ends their own session early.
	RevokedByUserID *int64 `json:"revoked_by_user_id,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (SupportAccessGrant) TableName() string { return "support_access_grants" }

// IsLive reports whether the grant currently authorises band access.
func (g *SupportAccessGrant) IsLive(now time.Time) bool {
	return g.Status == SupportStatusActive &&
		g.ExpiresAt != nil && now.Before(*g.ExpiresAt) &&
		g.RevokedAt == nil
}

// AllowsWrite reports whether a non-GET request is permitted under this grant.
func (g *SupportAccessGrant) AllowsWrite() bool { return g.Scope == SupportScopeReadWrite }

// ErrGrantNotLive is returned when a request references a grant that has
// expired, been revoked or was never activated.
var ErrGrantNotLive = errors.New("support access grant is not live")

// PlatformSettings is the single-row instance configuration edited from the
// admin center. Values here override the corresponding environment defaults
// so an operator does not have to redeploy to change them.
type PlatformSettings struct {
	ID int64 `gorm:"primaryKey;check:id = 1" json:"id"`

	// Global maintenance mode. Platform staff can always sign in.
	MaintenanceEnabled bool   `gorm:"not null" json:"maintenance_enabled"`
	MaintenanceMessage string `gorm:"size:500;not null;default:''" json:"maintenance_message"`

	// Announcement banner shown to every band, for example before a planned
	// update window.
	AnnouncementText      string     `gorm:"size:1000;not null;default:''" json:"announcement_text"`
	AnnouncementLevel     string     `gorm:"size:20;not null;default:'info'" json:"announcement_level"`
	AnnouncementExpiresAt *time.Time `json:"announcement_expires_at,omitempty"`

	// Outgoing mail. The password is stored encrypted with a key derived from
	// SECRET_KEY and is never returned by the API.
	SMTPEnabled           bool   `gorm:"not null" json:"smtp_enabled"`
	SMTPHost              string `gorm:"size:255;not null;default:''" json:"smtp_host"`
	SMTPPort              int    `gorm:"not null;default:465" json:"smtp_port"`
	SMTPSecurity          string `gorm:"size:20;not null;default:'ssl'" json:"smtp_security"`
	SMTPUsername          string `gorm:"size:255;not null;default:''" json:"smtp_username"`
	SMTPPasswordEncrypted string `gorm:"type:text" json:"-"`
	SMTPFrom              string `gorm:"size:254;not null;default:''" json:"smtp_from"`
	SMTPTimeoutSeconds    int    `gorm:"not null;default:8" json:"smtp_timeout_seconds"`
	// Where support notifications go when no band-specific address applies.
	NotificationEmail string `gorm:"size:254;not null;default:''" json:"notification_email"`

	UpdatedAt         time.Time `gorm:"not null" json:"updated_at"`
	UpdatedByUserID   *int64    `json:"updated_by_user_id,omitempty"`
	UpdatedByUsername string    `gorm:"size:150;not null;default:''" json:"updated_by_username"`
}

func (PlatformSettings) TableName() string { return "platform_settings" }

// BackupStatus is the lifecycle of one scheduled or manual backup run.
type BackupStatus string

const (
	BackupStatusRunning   BackupStatus = "running"
	BackupStatusSucceeded BackupStatus = "succeeded"
	BackupStatusFailed    BackupStatus = "failed"
)

// BackupRun records one dump. A NULL BandID means a full-instance backup.
type BackupRun struct {
	ID     int64  `gorm:"primaryKey" json:"id"`
	BandID *int64 `gorm:"index" json:"band_id,omitempty"`

	Status BackupStatus `gorm:"size:20;not null;index" json:"status"`
	// Trigger distinguishes a scheduled run from a manual one and from the
	// safety point taken automatically before a restore. The column is named
	// trigger_kind because TRIGGER is reserved in MariaDB.
	Trigger string `gorm:"column:trigger_kind;size:20;not null" json:"trigger"`

	Path      string `gorm:"size:500;not null;default:''" json:"path"`
	SizeBytes int64  `gorm:"not null;default:0" json:"size_bytes"`
	Error     string `gorm:"type:text" json:"error,omitempty"`

	StartedAt         time.Time  `gorm:"not null;index" json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	StartedByUserID   *int64     `json:"started_by_user_id,omitempty"`
	StartedByUsername string     `gorm:"size:150;not null;default:''" json:"started_by_username"`
}

func (BackupRun) TableName() string { return "backup_runs" }
