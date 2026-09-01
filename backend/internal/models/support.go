package models

import "time"

// AdminMessageType distinguishes a bug report from a usage question.
type AdminMessageType string

const (
	AdminMessageIssue    AdminMessageType = "issue"
	AdminMessageQuestion AdminMessageType = "question"
)

// AdminMessage is a support request a band member sent from inside the app.
//
// It keeps the sender's name even after that account is deleted, so the inbox
// stays understandable, and it survives an operational data reset.
type AdminMessage struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	SenderUserID   *int64           `json:"sender_user_id,omitempty"`
	SenderUsername string           `gorm:"size:150;not null" json:"sender_username"`
	SenderEmail    string           `gorm:"size:254;not null;default:''" json:"sender_email"`
	MessageType    AdminMessageType `gorm:"size:20;not null" json:"message_type"`
	Subject        string           `gorm:"size:200;not null" json:"subject"`
	Body           string           `gorm:"type:text;not null" json:"body"`
	// Assignment is optional and keeps a username snapshot so the inbox stays
	// understandable even if the platform account is later deactivated.
	AssignedToUserID   *int64 `gorm:"index" json:"assigned_to_user_id"`
	AssignedToUsername string `gorm:"size:150;not null;default:''" json:"assigned_to_username"`

	IsResolved         bool       `gorm:"not null;index" json:"is_resolved"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	ResolvedByUserID   *int64     `json:"resolved_by_user_id,omitempty"`
	ResolvedByUsername string     `gorm:"size:150;not null;default:''" json:"resolved_by_username"`

	CreatedAt time.Time `gorm:"not null;index" json:"created_at"`
}

func (AdminMessage) TableName() string { return "admin_messages" }

// AuditLog records every security-relevant and every booking-relevant action.
//
// Unlike the original, account and operational entries share one table. BandID
// is nil for platform-level actions. ActingGrantID is set whenever the action
// was performed by platform staff under a support-access grant, which is what
// makes "who looked at our data, when, and why" answerable.
type AuditLog struct {
	ID     int64  `gorm:"primaryKey" json:"id"`
	BandID *int64 `gorm:"index" json:"band_id,omitempty"`

	UserID   *int64 `json:"user_id,omitempty"`
	Username string `gorm:"size:150;not null;default:''" json:"username"`

	ActingGrantID *int64 `gorm:"index" json:"acting_grant_id,omitempty"`

	Action     string  `gorm:"size:80;not null;index" json:"action"`
	EntityType string  `gorm:"size:60;not null" json:"entity_type"`
	EntityID   *int64  `json:"entity_id,omitempty"`
	Details    JSONMap `gorm:"type:json;not null" json:"details"`

	IPAddress string    `gorm:"size:45;not null;default:''" json:"ip_address"`
	CreatedAt time.Time `gorm:"not null;index" json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_log" }
