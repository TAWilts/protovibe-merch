package models

import "time"

// Purchase is one line of a goods-receipt receipt. Like sales, several lines
// can share a ReceiptID, but unlike sales a purchase may be edited and deleted
// by a manager rather than only cancelled.
type Purchase struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ReceiptID string `gorm:"size:40;not null;index" json:"receipt_id"`
	VariantID int64  `gorm:"not null;index" json:"variant_id"`

	Quantity      int   `gorm:"not null" json:"quantity"`
	UnitCostCents int64 `gorm:"not null" json:"unit_cost_cents"`

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
