package models

import "time"

// BandTransactionType is either money coming in or going out.
type BandTransactionType string

const (
	BandIncome  BandTransactionType = "income"
	BandExpense BandTransactionType = "expense"
)

// Standardised categories keep finance reports comparable. "Sonstiges" is
// the explicit fallback for anything that does not fit the presets.
var DefaultBandIncomeCategories = []string{
	"Gage", "Tantiemen", "Spende", "Erstattung", "Sonstiges",
}

var DefaultBandExpenseCategories = []string{
	"Equipment", "Fahrtkosten / Sprit", "Verpflegung", "Unterkunft",
	"Proberaum / Miete", "Werbung / Marketing", "Software / Abos",
	"Gebühren", "Versicherung", "Personal / Gage", "Versand", "Sonstiges",
}

// DefaultBandCategories is kept for backwards-compatible API clients.
var DefaultBandCategories = append(
	append([]string{}, DefaultBandIncomeCategories...),
	DefaultBandExpenseCategories...,
)

// BandTransaction is the band's own ledger for gigs, royalties and equipment.
// It is deliberately separate from merch purchases and sales so a historic
// merch balance never changes when band money is booked.
//
// IsSettled has a type-specific label in the UI: an income is "received", an
// expense is "paid". Once settled, the business fields are immutable; only a
// cancellation remains possible.
type BandTransaction struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	TransactionType BandTransactionType `gorm:"size:20;not null;index" json:"transaction_type"`
	TransactionOn   Date                `gorm:"not null;index" json:"transaction_on"`
	Category        string              `gorm:"size:120;not null" json:"category"`
	Description     string              `gorm:"size:500;not null" json:"description"`
	AmountCents     int64               `gorm:"not null" json:"amount_cents"`

	IsSettled         bool       `gorm:"not null;index" json:"is_settled"`
	IsAsset           bool       `gorm:"not null;index" json:"is_asset"`
	SettledAt         *time.Time `json:"settled_at,omitempty"`
	SettledByUserID   *int64     `json:"settled_by_user_id,omitempty"`
	SettledByUsername string     `gorm:"size:150;not null;default:''" json:"settled_by_username"`

	IsCancelled         bool       `gorm:"not null;index" json:"is_cancelled"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	CancelledByUserID   *int64     `json:"cancelled_by_user_id,omitempty"`
	CancelledByUsername string     `gorm:"size:150;not null;default:''" json:"cancelled_by_username"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
	Actor

	Attachments []BandTransactionAttachment `gorm:"foreignKey:TransactionID" json:"attachments,omitempty"`
}

func (BandTransaction) TableName() string { return "band_transactions" }

// BandTransactionAttachment is a receipt or invoice for a band booking.
type BandTransactionAttachment struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	TransactionID    int64  `gorm:"not null;index" json:"transaction_id"`
	FilePath         string `gorm:"size:255;uniqueIndex;not null" json:"-"`
	OriginalFilename string `gorm:"size:255;not null" json:"original_filename"`
	SizeBytes        int64  `gorm:"not null;default:0" json:"size_bytes"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	Actor
}

func (BandTransactionAttachment) TableName() string { return "band_transaction_attachments" }
