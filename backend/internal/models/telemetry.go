package models

import "time"

// TelemetryDaily is intentionally unlinkable. It contains no band/user
// identifier and no pseudonymous hash: only a day, a coarse dimension and
// aggregate counters from users who explicitly opted in.
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
