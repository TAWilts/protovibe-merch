package models

import "time"

// PaymentMethod values as offered on the sales page.
const (
	PaymentMethodCash     = "Bar"
	PaymentMethodPayPal   = "PayPal"
	PaymentMethodTransfer = "Überweisung"
	PaymentMethodCard     = "Karte"
	PaymentMethodOther    = "Sonstiges"
)

// PaymentMethods is the ordered list shown in the UI.
var PaymentMethods = []string{
	PaymentMethodCash,
	PaymentMethodPayPal,
	PaymentMethodTransfer,
	PaymentMethodCard,
	PaymentMethodOther,
}

// QRPaymentMethods are settled by showing a code rather than by handing over
// cash, so the client's "amount given" field is ignored for them.
var QRPaymentMethods = map[string]bool{
	PaymentMethodPayPal:   true,
	PaymentMethodTransfer: true,
}

// DeliveryStatus tracks a sale that was not handed over at the counter.
type DeliveryStatus string

const (
	// DeliveryNotApplicable is an ordinary counter sale.
	DeliveryNotApplicable DeliveryStatus = "not_applicable"
	DeliveryPending       DeliveryStatus = "pending"
	DeliveryShipped       DeliveryStatus = "shipped"
	DeliveryReceived      DeliveryStatus = "received"
)

// Sale is one line of a receipt. A basket with several positions shares one
// ReceiptID, so history shows a single purchase while stock, payment and
// delivery status keep working per item.
type Sale struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ReceiptID string `gorm:"size:40;not null;index" json:"receipt_id"`
	VariantID int64  `gorm:"not null;index" json:"variant_id"`

	Quantity       int   `gorm:"not null" json:"quantity"`
	UnitPriceCents int64 `gorm:"not null" json:"unit_price_cents"`
	AmountDueCents int64 `gorm:"not null" json:"amount_due_cents"`
	// AmountGivenCents is nil while a sale is unpaid.
	AmountGivenCents *int64 `json:"amount_given_cents"`
	// DonationCents is the overpayment, distributed across the basket's lines
	// to the cent so a single position can be cancelled without distorting the
	// rest of the receipt.
	DonationCents int64 `gorm:"not null;default:0" json:"donation_cents"`

	PaymentMethod string `gorm:"size:40;not null" json:"payment_method"`
	IsPaid        bool   `gorm:"not null" json:"is_paid"`
	// PaymentFollowUp separates a sale that had to be chased later from an
	// ordinary counter sale that was paid immediately.
	PaymentFollowUp bool `gorm:"not null" json:"payment_follow_up"`

	IsReceived     bool           `gorm:"not null" json:"is_received"`
	DeliveryStatus DeliveryStatus `gorm:"size:20;not null;default:'not_applicable';index" json:"delivery_status"`

	// A cancellation keeps the booking for audit and history while removing
	// its effect from stock, balances and the work queues.
	IsCancelled bool       `gorm:"not null;index" json:"is_cancelled"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`

	CustomerName    string `gorm:"size:200;not null;default:''" json:"customer_name"`
	CustomerAddress string `gorm:"size:500;not null;default:''" json:"customer_address"`
	// EventName is an immutable snapshot so old receipts, CSV exports and
	// offline clients stay readable independently of catalogue edits.
	EventName string `gorm:"size:200;not null;default:''" json:"event_name"`
	SoldBy    string `gorm:"size:150;not null;default:''" json:"sold_by"`
	Comment   string `gorm:"size:1000;not null;default:''" json:"comment"`

	SoldOn    Date      `gorm:"not null;index" json:"sold_on"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	Actor
}

func (Sale) TableName() string { return "sales" }

// SaleEvent is a gig or market the band sells at.
type SaleEvent struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	Name           string    `gorm:"size:200;not null" json:"name"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	LastSelectedAt time.Time `gorm:"not null;index" json:"last_selected_at"`
}

func (SaleEvent) TableName() string { return "sale_events" }

// SaleEventState is the band's currently selected event. It is shared rather
// than per-user so several phones at the same stand book against one event.
type SaleEventState struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	EventID   int64     `gorm:"not null" json:"event_id"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (SaleEventState) TableName() string { return "sale_event_state" }

// SyncEvent makes an offline sale idempotent. The client generates a durable
// event ID before going offline; the server stores it together with the exact
// response it produced, so a retry after a lost connection replays that
// response instead of booking a second sale.
type SyncEvent struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	EventID string `gorm:"size:64;not null" json:"event_id"`

	EventType     string `gorm:"size:20;not null" json:"event_type"`
	ActorUserID   int64  `gorm:"not null;index" json:"actor_user_id"`
	ActorUsername string `gorm:"size:150;not null;default:''" json:"actor_username"`
	DeviceID      string `gorm:"size:64;not null" json:"device_id"`
	// PayloadHash detects a reused event ID carrying different data, which is
	// answered with 409 rather than silently accepted.
	PayloadHash string `gorm:"size:64;not null" json:"payload_hash"`

	ClientCreatedAt time.Time `gorm:"not null" json:"client_created_at"`
	ResponseJSON    string    `gorm:"type:longtext;not null" json:"-"`
	CreatedAt       time.Time `gorm:"not null;index" json:"created_at"`
}

func (SyncEvent) TableName() string { return "sync_events" }

// PaymentQRSettings holds a band's payment destinations for the QR feature.
type PaymentQRSettings struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	PayPalMeURL        string `gorm:"column:paypal_me_url;size:255;not null;default:''" json:"paypal_me_url"`
	BankAccountHolder  string `gorm:"size:200;not null;default:''" json:"bank_account_holder"`
	BankIBAN           string `gorm:"size:34;not null;default:''" json:"bank_iban"`
	BankBIC            string `gorm:"size:11;not null;default:''" json:"bank_bic"`
	BankRemittanceText string `gorm:"size:140;not null;default:'Merch-Kauf'" json:"bank_remittance_text"`

	UpdatedAt         time.Time `gorm:"not null" json:"updated_at"`
	UpdatedByUserID   *int64    `json:"updated_by_user_id,omitempty"`
	UpdatedByUsername string    `gorm:"size:150;not null;default:''" json:"updated_by_username"`
}

func (PaymentQRSettings) TableName() string { return "payment_qr_settings" }

// PaymentQRIntent reserves a receipt ID and its quoted amount while the
// customer scans a code. Showing a code is deliberately not a sale: no stock
// and no ledger row changes until the seller confirms.
type PaymentQRIntent struct {
	Token string `gorm:"primaryKey;size:64" json:"token"`
	Tenant

	ReceiptID       string `gorm:"size:40;not null" json:"receipt_id"`
	SalePayloadJSON string `gorm:"type:longtext;not null" json:"-"`

	CreatedByUserID int64      `gorm:"not null;index" json:"created_by_user_id"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt       time.Time  `gorm:"not null;index" json:"expires_at"`
	CancelledAt     *time.Time `json:"cancelled_at,omitempty"`
	ConsumedAt      *time.Time `json:"consumed_at,omitempty"`
	ResponseJSON    string     `gorm:"type:longtext" json:"-"`
}

func (PaymentQRIntent) TableName() string { return "payment_qr_intents" }
