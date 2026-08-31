package models

import "time"

// BandTransactionType is either money coming in or going out.
type BandTransactionType string

const (
	BandIncome  BandTransactionType = "income"
	BandExpense BandTransactionType = "expense"
)

// DefaultBandCategories are the presets offered in the UI. The field itself is
// free text, so a band can add its own.
var DefaultBandCategories = []string{
	"Gage", "Tantiemen", "Fahrgeld", "Equipment", "Unterkunft", "Verpflegung", "Sonstiges",
}

// BandTransaction is the band's own ledger for gigs, royalties and equipment.
// It is deliberately separate from merch purchases and sales so a historic
// merch balance never changes when band money is booked.
type BandTransaction struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	TransactionType BandTransactionType `gorm:"size:20;not null;index" json:"transaction_type"`
	TransactionOn   Date                `gorm:"not null;index" json:"transaction_on"`
	Category        string              `gorm:"size:120;not null" json:"category"`
	Description     string              `gorm:"size:500;not null" json:"description"`
	AmountCents     int64               `gorm:"not null" json:"amount_cents"`

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
