// Package sales implements the point-of-sale booking rules.
//
// The rules are ported from create_sale in the Flask original. Two of them are
// load-bearing and easy to get wrong:
//
//   - Stock never blocks a sale. A band that sold more shirts than it recorded
//     buying still has to be able to book the sale; the balance sheet shows
//     the negative rather than the till refusing money.
//   - A basket's positions must always add back up to what the customer paid,
//     to the cent, so cancelling one position leaves the rest correct.
package sales

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
)

// Validation errors. They map onto stable API codes in the HTTP layer.
var (
	ErrEmptyBasket                  = errors.New("sales: the basket is empty")
	ErrInvalidQuantity              = errors.New("sales: quantity must be positive")
	ErrNegativePrice                = errors.New("sales: prices cannot be negative")
	ErrNegativeShipping             = errors.New("sales: shipping costs cannot be negative")
	ErrShippingOnCounter            = errors.New("sales: shipping costs require a shipment")
	ErrContactRequired              = errors.New("sales: name and address are required when a sale is unpaid or not handed over")
	ErrUnknownPayment               = errors.New("sales: unknown payment method")
	ErrNegativeAmount               = errors.New("sales: the amount given cannot be negative")
	ErrDiscountConfirmationRequired = errors.New("sales: a payment below the amount due requires explicit discount confirmation")
	ErrVariantNotOffered            = errors.New("sales: this variant is not currently offered")
	// ErrUnknownVariant covers both a typo and a variant belonging to another
	// band — the tenant filter simply makes the row invisible here.
	ErrUnknownVariant = errors.New("sales: unknown variant")
)

// BasketItem is one position a seller added at the stand.
type BasketItem struct {
	VariantID int64 `json:"variant_id"`
	Quantity  int   `json:"quantity"`
	// UnitPriceCents overrides the variant's price for this sale only. A nil
	// value means "use the catalogue price", which is the normal case.
	UnitPriceCents *int64 `json:"unit_price_cents"`
}

// Request is a complete booking as the client submits it.
type Request struct {
	Items         []BasketItem `json:"items"`
	PaymentMethod string       `json:"payment_method"`
	IsPaid        bool         `json:"is_paid"`
	IsReceived    bool         `json:"is_received"`
	// AmountGivenCents is what the band actually keeps. Anything above the
	// amount due becomes a donation; anything below it is a confirmed discount.
	AmountGivenCents  *int64 `json:"amount_given_cents"`
	DiscountConfirmed bool   `json:"discount_confirmed"`
	// ShippingCostCents is the gross amount charged for a shipment.
	ShippingCostCents int64 `json:"shipping_cost_cents"`

	CustomerName    string `json:"customer_name"`
	CustomerAddress string `json:"customer_address"`
	EventName       string `json:"event_name"`
	SoldBy          string `json:"sold_by"`
	Comment         string `json:"comment"`

	SoldOn models.Date `json:"sold_on"`
	// ReceiptID is the preview the seller already showed the customer.
	ReceiptID string `json:"receipt_id"`
	// PaymentQRIntentToken redeems a displayed payment code.
	PaymentQRIntentToken string `json:"payment_qr_intent_token"`
}

// Line is one prepared ledger row.
type Line struct {
	VariantID         int64
	Quantity          int
	UnitPriceCents    int64
	AmountDueCents    int64
	ShippingCostCents int64
	DiscountCents     int64
	AmountGivenCents  *int64
	DonationCents     int64
}

// Prepared is a booking that passed every rule and is ready to persist.
type Prepared struct {
	Lines           []Line
	TotalDueCents   int64
	TotalPaidCents  int64
	DiscountCents   int64
	DonationCents   int64
	PaymentMethod   string
	IsPaid          bool
	IsReceived      bool
	PaymentFollowUp bool
	DeliveryStatus  models.DeliveryStatus
	CustomerName    string
	CustomerAddress string
	EventName       string
	SoldBy          string
	Comment         string
}

// VariantPrice is what the catalogue says about one variant at booking time.
type VariantPrice struct {
	SalePriceCents int64
	IsOffered      bool
}

// Prepare applies every booking rule and computes the ledger rows.
//
// It is deliberately pure: the caller resolves the catalogue prices, Prepare
// decides what gets written. That keeps the money arithmetic testable without
// a database and keeps the rules in one readable place.
func Prepare(req Request, prices map[int64]VariantPrice) (*Prepared, error) {
	if len(req.Items) == 0 {
		return nil, ErrEmptyBasket
	}
	if !isKnownPaymentMethod(req.PaymentMethod) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPayment, req.PaymentMethod)
	}

	name := strings.TrimSpace(req.CustomerName)
	address := strings.TrimSpace(req.CustomerAddress)
	// Anything the band still has to chase — money or a parcel — needs someone
	// to chase it from.
	if (!req.IsPaid || !req.IsReceived) && (name == "" || address == "") {
		return nil, ErrContactRequired
	}

	lines := make([]Line, 0, len(req.Items))
	var totalDue int64
	for i, item := range req.Items {
		if item.Quantity <= 0 {
			return nil, fmt.Errorf("%w: position %d", ErrInvalidQuantity, i+1)
		}
		price, ok := prices[item.VariantID]
		if !ok {
			return nil, fmt.Errorf("%w: %d", ErrUnknownVariant, item.VariantID)
		}
		if !price.IsOffered {
			return nil, fmt.Errorf("%w: variant %d", ErrVariantNotOffered, item.VariantID)
		}

		unit := price.SalePriceCents
		if item.UnitPriceCents != nil {
			unit = *item.UnitPriceCents
		}
		if unit < 0 {
			return nil, fmt.Errorf("%w: position %d", ErrNegativePrice, i+1)
		}

		due := unit * int64(item.Quantity)
		totalDue += due
		lines = append(lines, Line{
			VariantID:      item.VariantID,
			Quantity:       item.Quantity,
			UnitPriceCents: unit,
			AmountDueCents: due,
		})
	}

	if req.ShippingCostCents < 0 {
		return nil, ErrNegativeShipping
	}
	if req.IsReceived && req.ShippingCostCents != 0 {
		return nil, ErrShippingOnCounter
	}

	goodsWeights := make([]int64, len(lines))
	for i, line := range lines {
		goodsWeights[i] = line.AmountDueCents
	}
	shippingShares := money.Distribute(req.ShippingCostCents, goodsWeights)
	for i := range lines {
		lines[i].ShippingCostCents = shippingShares[i]
		lines[i].AmountDueCents += shippingShares[i]
	}
	totalDue += req.ShippingCostCents

	given, discount, donation, err := resolveAmounts(req, totalDue)
	if err != nil {
		return nil, err
	}

	discountShares := money.Distribute(discount, goodsWeights)
	donationShares := money.Distribute(donation, goodsWeights)

	for i := range lines {
		lines[i].DiscountCents = discountShares[i]
		lines[i].DonationCents = donationShares[i]
		if given != nil {
			// Each row carries its own adjustment shares, so cancelling one
			// position removes exactly its part of the discount or donation too.
			rowGiven := lines[i].AmountDueCents - discountShares[i] + donationShares[i]
			lines[i].AmountGivenCents = &rowGiven
		}
	}

	// An ordinary counter sale has no delivery workflow at all; anything not
	// handed over starts the shipping queue instead.
	deliveryStatus := models.DeliveryPending
	if req.IsReceived {
		deliveryStatus = models.DeliveryNotApplicable
	}

	return &Prepared{
		Lines:           lines,
		TotalDueCents:   totalDue,
		TotalPaidCents:  totalDue - discount + donation,
		DiscountCents:   discount,
		DonationCents:   donation,
		PaymentMethod:   req.PaymentMethod,
		IsPaid:          req.IsPaid,
		IsReceived:      req.IsReceived,
		PaymentFollowUp: !req.IsPaid,
		DeliveryStatus:  deliveryStatus,
		CustomerName:    name,
		CustomerAddress: address,
		EventName:       strings.TrimSpace(req.EventName),
		SoldBy:          strings.TrimSpace(req.SoldBy),
		Comment:         strings.TrimSpace(req.Comment),
	}, nil
}

// resolveAmounts decides what was actually handed over and how much of it is a
// donation.
func resolveAmounts(req Request, totalDue int64) (given *int64, discount, donation int64, err error) {
	// An unpaid booking must never count as a donation just because a stale
	// cash field was still filled in on the client.
	if !req.IsPaid {
		return nil, 0, 0, nil
	}

	if req.AmountGivenCents == nil {
		exact := totalDue
		return &exact, 0, 0, nil
	}
	if *req.AmountGivenCents < 0 {
		return nil, 0, 0, ErrNegativeAmount
	}
	if *req.AmountGivenCents < totalDue && !req.DiscountConfirmed {
		return nil, 0, 0, ErrDiscountConfirmationRequired
	}

	value := *req.AmountGivenCents
	if value < totalDue {
		return &value, totalDue - value, 0, nil
	}
	return &value, 0, value - totalDue, nil
}

func isKnownPaymentMethod(method string) bool {
	for _, known := range models.PaymentMethods {
		if known == method {
			return true
		}
	}
	return false
}
