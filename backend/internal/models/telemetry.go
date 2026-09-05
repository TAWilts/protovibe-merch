package models

import "time"

// TelemetryDaily is the unlinkable aggregate layer: one day, one coarse
// dimension and counters from users who explicitly opted in.
type TelemetryDaily struct {
	ID int64 `gorm:"primaryKey" json:"id"`

	Day            Date   `gorm:"not null;uniqueIndex:uq_telemetry_daily_dimension,priority:1" json:"day"`
	EventKind      string `gorm:"column:event_kind;size:40;not null;uniqueIndex:uq_telemetry_daily_dimension,priority:2" json:"event_kind"`
	DimensionValue string `gorm:"column:dimension_value;size:255;not null;uniqueIndex:uq_telemetry_daily_dimension,priority:3" json:"dimension"`

	SampleCount        int64     `gorm:"not null" json:"sample_count"`
	TotalDurationMS    int64     `gorm:"column:total_duration_ms;not null" json:"total_duration_ms"`
	TotalRequestBytes  int64     `gorm:"not null" json:"total_request_bytes"`
	TotalResponseBytes int64     `gorm:"not null" json:"total_response_bytes"`
	UpdatedAt          time.Time `gorm:"not null" json:"updated_at"`
}

func (TelemetryDaily) TableName() string { return "telemetry_daily" }

// TelemetryEvent is the detailed pseudonymised layer. Stable aliases make it
// possible to compare operations from the same band/article without storing
// source IDs or a reverse lookup table.
//
// Location contains only the event/location text the band entered itself. IP
// addresses are never copied into telemetry.
type TelemetryEvent struct {
	ID int64 `gorm:"primaryKey" json:"-"`

	OccurredAt     time.Time `gorm:"not null;index" json:"occurred_at"`
	EventType      string    `gorm:"size:40;not null;index" json:"event_type"`
	BandAlias      string    `gorm:"size:32;not null;default:'';index" json:"band_alias"`
	OperationAlias string    `gorm:"size:32;not null;default:''" json:"operation_alias"`
	SubjectAlias   string    `gorm:"size:32;not null;default:''" json:"subject_alias"`
	FeatureKey     string    `gorm:"size:80;not null;default:'';index" json:"feature_key"`
	Role           string    `gorm:"size:40;not null;default:''" json:"role"`

	Quantity        *int   `json:"quantity,omitempty"`
	UnitPriceCents  *int64 `json:"unit_price_cents,omitempty"`
	AmountCents     *int64 `json:"amount_cents,omitempty"`
	PaymentMethod   string `gorm:"size:40;not null;default:''" json:"payment_method"`
	IsPaid          *bool  `json:"is_paid,omitempty"`
	IsReceived      *bool  `json:"is_received,omitempty"`
	IsOpen          *bool  `json:"is_open,omitempty"`
	Status          string `gorm:"size:40;not null;default:''" json:"status"`
	DeliveryStatus  string `gorm:"size:40;not null;default:''" json:"delivery_status"`
	PaymentFollowUp *bool  `json:"payment_follow_up,omitempty"`
	Location        string `gorm:"size:200;not null;default:'';index" json:"location"`
	StorageBytes    *int64 `json:"storage_bytes,omitempty"`

	HTTPStatus    int   `gorm:"not null;default:0" json:"http_status"`
	RequestBytes  int64 `gorm:"not null;default:0" json:"request_bytes"`
	ResponseBytes int64 `gorm:"not null;default:0" json:"response_bytes"`
	DurationMS    int64 `gorm:"not null;default:0" json:"duration_ms"`
}

func (TelemetryEvent) TableName() string { return "telemetry_events" }
