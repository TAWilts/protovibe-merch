package models

import "time"

// PurchasePriceMode records whether the receipt was entered using individual
// unit prices or one goods total for the complete basket.
type PurchasePriceMode string

const (
	PurchasePriceUnit   PurchasePriceMode = "unit"
	PurchasePriceBasket PurchasePriceMode = "basket"
)

// Purchase is one line of a goods-receipt receipt. Like sales, several lines
// can share a ReceiptID. Booked purchases are never hard-deleted; corrections
// remain visible and are removed from stock/finance totals by cancellation.
type Purchase struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ReceiptID string `gorm:"size:40;not null;index" json:"receipt_id"`
	VariantID int64  `gorm:"not null;index" json:"variant_id"`

	Quantity int `gorm:"not null" json:"quantity"`
	// UnitCostCents is canonical gross cost. Net user input is converted before storage.
	UnitCostCents int64             `gorm:"not null" json:"unit_cost_cents"`
	PriceMode     PurchasePriceMode `gorm:"size:20;not null;default:'unit'" json:"price_mode"`
	// LineTotalCostCents is the exact gross goods cost allocated to this line.
	// It avoids losing rounding cents when one basket total spans many units.
	LineTotalCostCents int64 `gorm:"not null;default:0" json:"line_total_cost_cents"`

	// Receipt-level price metadata is repeated on each line, like supplier/date/reference.
	PricesIncludeVAT   bool  `gorm:"not null" json:"prices_include_vat"`
	VATRateBasisPoints int   `gorm:"not null;default:1900" json:"vat_rate_basis_points"`
	ShippingCostCents  int64 `gorm:"not null;default:0" json:"shipping_cost_cents"`

	PurchasedOn Date   `gorm:"not null;index" json:"purchased_on"`
	Supplier    string `gorm:"size:200;not null;default:''" json:"supplier"`
	// InvoiceReference is a typed invoice number and stays useful even when a
	// document is attached as well.
	InvoiceReference string `gorm:"size:200;not null;default:''" json:"invoice_reference"`
	// InvoiceFilePath is the opaque managed filename of a per-line attachment.
	InvoiceFilePath         string `gorm:"size:255;default:null" json:"-"`
	InvoiceOriginalFilename string `gorm:"size:255;not null;default:''" json:"invoice_original_filename"`
	InvoiceSizeBytes        int64  `gorm:"not null;default:0" json:"invoice_size_bytes"`

	Comment string `gorm:"size:1000;not null;default:''" json:"comment"`

	IsCancelled         bool       `gorm:"not null;index" json:"is_cancelled"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	CancelledByUserID   *int64     `json:"cancelled_by_user_id,omitempty"`
	CancelledByUsername string     `gorm:"size:150;not null;default:''" json:"cancelled_by_username"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
	Actor
}

func (Purchase) TableName() string { return "purchases" }

// PurchaseReceiptAttachment is a document that belongs to the whole receipt
// rather than to a single line, for example one invoice covering four items.
type PurchaseReceiptAttachment struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ReceiptID        string `gorm:"size:40;not null;index" json:"receipt_id"`
	FilePath         string `gorm:"size:255;uniqueIndex;not null" json:"-"`
	OriginalFilename string `gorm:"size:255;not null" json:"original_filename"`
	SizeBytes        int64  `gorm:"not null;default:0" json:"size_bytes"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	Actor
}

func (PurchaseReceiptAttachment) TableName() string { return "purchase_receipt_attachments" }
