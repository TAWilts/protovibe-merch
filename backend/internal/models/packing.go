package models

import "time"

type PackingStatus string

const (
	PackingOpen      PackingStatus = "open"
	PackingPacked    PackingStatus = "packed"
	PackingStaysHere PackingStatus = "stays_here"
)

type PackingListState struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant
	Revision   int64 `gorm:"not null;default:0" json:"revision"`
	Generation int64 `gorm:"not null;default:1" json:"generation"`
	Timestamps
}

func (PackingListState) TableName() string { return "packing_list_states" }

type PackingBag struct {
	ID string `gorm:"primaryKey;size:36" json:"id"`
	Tenant
	Name     string        `gorm:"size:200;not null" json:"name"`
	Position int           `gorm:"not null;default:0" json:"position"`
	Status   PackingStatus `gorm:"size:20;not null" json:"status"`
	Timestamps
	Actor
}

func (PackingBag) TableName() string { return "packing_bags" }

type PackingItem struct {
	ID string `gorm:"primaryKey;size:36" json:"id"`
	Tenant
	BagID    string        `gorm:"size:36;not null;index" json:"bag_id"`
	Name     string        `gorm:"size:200;not null" json:"name"`
	Position int           `gorm:"not null;default:0" json:"position"`
	Status   PackingStatus `gorm:"size:20;not null" json:"status"`
	Timestamps
	Actor
}

func (PackingItem) TableName() string { return "packing_items" }

type PackingPhoto struct {
	ID string `gorm:"primaryKey;size:36" json:"id"`
	Tenant
	BagID             *string   `gorm:"size:36;index" json:"bag_id,omitempty"`
	ItemID            *string   `gorm:"size:36;index" json:"item_id,omitempty"`
	FilePath          string    `gorm:"size:255;uniqueIndex;not null" json:"-"`
	OriginalFilename  string    `gorm:"size:255;not null" json:"original_filename"`
	SizeBytes         int64     `gorm:"not null;default:0" json:"size_bytes"`
	Position          int       `gorm:"not null;default:0" json:"position"`
	CreatedAt         time.Time `gorm:"not null" json:"created_at"`
	CreatedByUserID   *int64    `json:"created_by_user_id,omitempty"`
	CreatedByUsername string    `gorm:"size:150;not null;default:''" json:"created_by_username"`
}

func (PackingPhoto) TableName() string { return "packing_photos" }

type PackingSyncEvent struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant
	EventID         string    `gorm:"size:64;not null" json:"event_id"`
	DeviceID        string    `gorm:"size:64;not null" json:"device_id"`
	ActorUserID     int64     `gorm:"not null;index" json:"actor_user_id"`
	ActorUsername   string    `gorm:"size:150;not null;default:''" json:"actor_username"`
	PayloadHash     string    `gorm:"size:64;not null" json:"payload_hash"`
	ResponseJSON    string    `gorm:"type:longtext;not null" json:"-"`
	ClientCreatedAt time.Time `gorm:"not null" json:"client_created_at"`
	CreatedAt       time.Time `gorm:"not null;index" json:"created_at"`
}

func (PackingSyncEvent) TableName() string { return "packing_sync_events" }
