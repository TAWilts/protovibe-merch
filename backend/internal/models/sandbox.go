package models

import "time"

const (
	SandboxStatusActive  = "active"
	SandboxStatusPurging = "purging"
)

// SandboxEnvironment owns the lifecycle of one disposable tenant. Keeping the
// lifecycle outside Band makes it impossible for normal tenant management to
// accidentally turn a demo into a permanent band.
type SandboxEnvironment struct {
	ID           int64  `gorm:"primaryKey" json:"id"`
	BandID       int64  `gorm:"not null;uniqueIndex" json:"band_id"`
	UserID       int64  `gorm:"not null;uniqueIndex" json:"user_id"`
	SourceUserID *int64 `gorm:"index" json:"-"`

	TemplateVersion int     `gorm:"not null;default:1" json:"template_version"`
	TutorialState   JSONMap `gorm:"type:json;not null" json:"tutorial_state"`
	TutorialVisible bool    `gorm:"not null" json:"tutorial_visible"`
	Status          string  `gorm:"size:20;not null" json:"status"`

	LastActiveAt time.Time `gorm:"not null;index" json:"last_active_at"`
	ExpiresAt    time.Time `gorm:"not null;index" json:"expires_at"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

func (SandboxEnvironment) TableName() string { return "sandbox_environments" }
