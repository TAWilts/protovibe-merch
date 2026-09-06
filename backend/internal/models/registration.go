package models

import "time"

// BandRegistrationStatus is the public request lifecycle. A request is
// control-plane data and does not become tenant data until it is approved.
type BandRegistrationStatus string

const (
	BandRegistrationPending  BandRegistrationStatus = "pending"
	BandRegistrationApproved BandRegistrationStatus = "approved"
	BandRegistrationRejected BandRegistrationStatus = "rejected"
	BandRegistrationExpired  BandRegistrationStatus = "expired"
)

// BandRegistrationRequest stores a public request and its one-time handover.
// TokenHash is the only persisted representation of the status-link secret.
type BandRegistrationRequest struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	PublicID  string `gorm:"size:24;uniqueIndex;not null" json:"reference"`
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`

	RequestedBandName      string `gorm:"size:200;not null" json:"requested_band_name"`
	RequestedBandSlug      string `gorm:"size:64;not null" json:"requested_band_slug"`
	RequestedAdminUsername string `gorm:"size:150;not null" json:"requested_admin_username"`
	RequestedContactEmail  string `gorm:"size:254;not null" json:"requested_contact_email"`

	FinalBandName      string `gorm:"size:200;not null" json:"band_name"`
	FinalBandSlug      string `gorm:"size:64;not null" json:"band_slug"`
	FinalAdminUsername string `gorm:"size:150;not null" json:"admin_username"`
	FinalContactEmail  string `gorm:"size:254;not null" json:"contact_email"`

	Status                    BandRegistrationStatus `gorm:"size:20;not null;index" json:"status"`
	PrivacyAcceptedAt         time.Time              `gorm:"not null" json:"privacy_accepted_at"`
	DecisionNote              string                 `gorm:"size:1000;not null;default:''" json:"decision_note,omitempty"`
	DecidedByUserID           *int64                 `json:"decided_by_user_id,omitempty"`
	DecidedByUsername         string                 `gorm:"size:150;not null;default:''" json:"decided_by_username,omitempty"`
	DecidedAt                 *time.Time             `json:"decided_at,omitempty"`
	BandID                    *int64                 `json:"band_id,omitempty"`
	AdminUserID               *int64                 `json:"admin_user_id,omitempty"`
	SetupCodeEncrypted        string                 `gorm:"type:text" json:"-"`
	CredentialsAvailableUntil *time.Time             `json:"credentials_available_until,omitempty"`
	ClaimedAt                 *time.Time             `json:"claimed_at,omitempty"`
	ExpiresAt                 time.Time              `gorm:"not null;index" json:"expires_at"`

	Timestamps
}

func (BandRegistrationRequest) TableName() string { return "band_registration_requests" }
