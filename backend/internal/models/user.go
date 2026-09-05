package models

import "time"

// Role is either a cumulative band role or a non-cumulative platform role.
// The two families are disjoint: a platform account belongs to no band and has
// no access to band data except through a live SupportAccessGrant.
type Role string

const (
	RoleSeller       Role = "seller"
	RoleMember       Role = "member"
	RoleManager      Role = "manager"
	RoleBandAdmin    Role = "band_admin"
	RoleSupportAdmin Role = "support_admin"
	RoleSystemAdmin  Role = "system_admin"
)

// BandRoleLevels mirrors BAND_ROLE_LEVELS from the Flask original. Access
// checks compare levels, so "manager or above" stays a single comparison.
var BandRoleLevels = map[Role]int{
	RoleSeller:    1,
	RoleMember:    2,
	RoleManager:   3,
	RoleBandAdmin: 4,
}

// IsBandRole reports whether the role belongs to a band tenant.
func (r Role) IsBandRole() bool {
	_, ok := BandRoleLevels[r]
	return ok
}

// IsPlatformRole reports whether the role belongs to the control plane.
func (r Role) IsPlatformRole() bool {
	return r == RoleSupportAdmin || r == RoleSystemAdmin
}

// Level returns the cumulative band level, or 0 for platform roles.
func (r Role) Level() int { return BandRoleLevels[r] }

// Valid reports whether the value is one of the six known roles.
func (r Role) Valid() bool { return r.IsBandRole() || r.IsPlatformRole() }

// AtLeast reports whether r satisfies a required band role.
func (r Role) AtLeast(required Role) bool {
	if !r.IsBandRole() || !required.IsBandRole() {
		return false
	}
	return r.Level() >= required.Level()
}

// ManagedBandRoles are the roles a band admin may assign within their band.
var ManagedBandRoles = []Role{RoleSeller, RoleMember, RoleManager, RoleBandAdmin}

// ManagedPlatformRoles are the roles a system admin may assign.
var ManagedPlatformRoles = []Role{RoleSupportAdmin, RoleSystemAdmin}

// User is an account. Platform accounts have a nil BandID; band accounts are
// unique per band, so two bands may each have a user called "merch".
type User struct {
	ID     int64  `gorm:"primaryKey" json:"id"`
	BandID *int64 `gorm:"index" json:"band_id,omitempty"`

	Username     string `gorm:"size:150;not null" json:"username"`
	ContactEmail string `gorm:"size:254;not null;default:''" json:"contact_email"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`
	Role         Role   `gorm:"size:20;not null;index" json:"role"`
	IsActive     bool   `gorm:"not null" json:"is_active"`

	// A freshly created account signs in once with a one-time setup code and
	// must then choose its own password.
	MustSetPassword    bool       `gorm:"not null" json:"must_set_password"`
	SetupCodeHash      string     `gorm:"size:255;not null;default:''" json:"-"`
	SetupCodeExpiresAt *time.Time `json:"setup_code_expires_at,omitempty"`

	// TOTP secrets are encrypted with a key derived from SECRET_KEY, which is
	// why SECRET_KEY must never change once anyone has enrolled.
	MFASecretEncrypted        string     `gorm:"type:text" json:"-"`
	MFAPendingSecretEncrypted string     `gorm:"type:text" json:"-"`
	MFARecoveryCodeHashes     JSONSlice  `gorm:"type:json;not null" json:"-"`
	MFAEnabled                bool       `gorm:"not null" json:"mfa_enabled"`
	MFAEnrolledAt             *time.Time `json:"mfa_enrolled_at,omitempty"`

	// Bumping SessionVersion invalidates every existing session for this user.
	// Password changes, role changes, deactivation and the admin center's
	// session-kill all use it.
	SessionVersion int        `gorm:"not null;default:0" json:"-"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`

	// Presentation preferences belong to the person, not to the band.
	UITheme           string `gorm:"size:20;not null;default:'aurora'" json:"ui_theme"`
	UILanguage        string `gorm:"size:5;not null;default:'de'" json:"ui_language"`
	ShowVariantPhotos bool   `gorm:"not null" json:"show_variant_photos"`

	// Anonymous telemetry is an individual decision. A nil decision timestamp
	// means the person has not answered the first-login prompt yet; until then
	// absolutely no telemetry sample is collected for this account.
	TelemetryEnabled   bool       `gorm:"not null;default:false" json:"telemetry_enabled"`
	TelemetryDecidedAt *time.Time `json:"telemetry_decided_at,omitempty"`

	Timestamps
}

func (User) TableName() string { return "users" }

// MFARequired reports whether this account must use TOTP. Platform staff have
// no opt-out; band roles may enable it voluntarily.
func (u *User) MFARequired() bool { return u.Role.IsPlatformRole() }

// Session is the server-side session record. Keeping sessions in the database
// rather than in a signed cookie is what makes session_version revocation and
// the support-access banner work across every running instance.
type Session struct {
	// ID is the opaque token stored in the cookie, hashed before persisting.
	ID     string `gorm:"primaryKey;size:64" json:"-"`
	UserID int64  `gorm:"not null;index" json:"user_id"`
	// BandID is the band this session currently operates on. For a band user
	// it equals the user's own band; for platform staff it is only set while a
	// support grant is live.
	BandID *int64 `gorm:"index" json:"band_id,omitempty"`
	// ActingGrantID marks a session that is operating under support access.
	// Every audit entry written by this session carries it.
	ActingGrantID *int64 `gorm:"index" json:"acting_grant_id,omitempty"`

	SessionVersion int    `gorm:"not null" json:"-"`
	CSRFTokenHash  string `gorm:"size:64;not null" json:"-"`

	// POSMode restricts the session to the sales workflow, matching the
	// original's session['pos_mode'].
	POSMode bool `gorm:"not null" json:"pos_mode"`
	// ReauthAt stamps the last successful step-up confirmation.
	ReauthAt *time.Time `json:"reauth_at,omitempty"`

	UserAgent string `gorm:"size:255;not null;default:''" json:"user_agent"`
	IPAddress string `gorm:"size:45;not null;default:''" json:"ip_address"`

	CreatedAt  time.Time `gorm:"not null" json:"created_at"`
	LastSeenAt time.Time `gorm:"not null;index" json:"last_seen_at"`
	ExpiresAt  time.Time `gorm:"not null;index" json:"expires_at"`
}

func (Session) TableName() string { return "sessions" }

// PendingAuth holds the short-lived state between a correct password and the
// second factor, or between a setup code and the new password. Keeping it
// server-side means a stolen cookie cannot skip a step.
type PendingAuth struct {
	ID        string    `gorm:"primaryKey;size:64" json:"-"`
	UserID    int64     `gorm:"not null;index" json:"user_id"`
	Purpose   string    `gorm:"size:30;not null" json:"purpose"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
}

func (PendingAuth) TableName() string { return "pending_auth" }

// Purposes for PendingAuth.
const (
	PendingAuthMFALogin      = "mfa_login"
	PendingAuthMFAEnrollment = "mfa_enrollment"
	PendingAuthPasswordSetup = "password_setup"
)

// PasswordResetChallenge is the short-lived, mail-delivered recovery code for
// a system administrator. Only the hash is persisted.
type PasswordResetChallenge struct {
	UserID         int64     `gorm:"primaryKey" json:"-"`
	CodeHash       string    `gorm:"size:64;not null" json:"-"`
	ExpiresAt      time.Time `gorm:"not null" json:"-"`
	RequestedAt    time.Time `gorm:"not null" json:"-"`
	FailedAttempts int       `gorm:"not null" json:"-"`
}

func (PasswordResetChallenge) TableName() string { return "password_reset_challenges" }
