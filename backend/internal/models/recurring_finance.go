package models

import "time"

// RecurrenceUnit is the calendar unit used by a recurring band-finance rule.
type RecurrenceUnit string

const (
	RecurrenceDay   RecurrenceUnit = "day"
	RecurrenceWeek  RecurrenceUnit = "week"
	RecurrenceMonth RecurrenceUnit = "month"
	RecurrenceYear  RecurrenceUnit = "year"
)

// RecurringBandTransaction describes a schedule. Occurrences are materialised
// as ordinary BandTransaction rows so existing balance/report code sees the
// same data as for a manually booked entry.
type RecurringBandTransaction struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	TransactionType BandTransactionType `gorm:"size:20;not null" json:"transaction_type"`
	StartOn         Date                `gorm:"not null" json:"start_on"`
	NextRunOn       Date                `gorm:"not null;index" json:"next_run_on"`
	Category        string              `gorm:"size:120;not null" json:"category"`
	Description     string              `gorm:"size:500;not null" json:"description"`
	AmountCents     int64               `gorm:"not null" json:"amount_cents"`
	IntervalValue   int                 `gorm:"not null" json:"interval_value"`
	IntervalUnit    RecurrenceUnit      `gorm:"size:10;not null" json:"interval_unit"`
	IsActive        bool                `gorm:"not null;index" json:"is_active"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
	Actor
}

func (RecurringBandTransaction) TableName() string { return "recurring_band_transactions" }

// RecurringBandTransactionRun is the idempotency ledger for generated
// occurrences. Its unique key prevents duplicate bookings.
type RecurringBandTransactionRun struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	RecurringTransactionID int64     `gorm:"not null;index" json:"recurring_transaction_id"`
	OccurrenceOn           Date      `gorm:"not null" json:"occurrence_on"`
	TransactionID          int64     `gorm:"not null;index" json:"transaction_id"`
	CreatedAt              time.Time `gorm:"not null" json:"created_at"`
}

func (RecurringBandTransactionRun) TableName() string {
	return "recurring_band_transaction_runs"
}
