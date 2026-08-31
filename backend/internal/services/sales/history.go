package sales

import (
	"context"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

// Position is one line of a receipt as the history and the work queues show it.
type Position struct {
	ID int64 `json:"id"`
	// The receipt, the customer and the date are what identify the case a
	// worklist row belongs to. Without them a pending parcel is an article
	// name and a quantity, which names no order and no address to send it to.
	ReceiptID       string      `json:"receipt_id"`
	SoldOn          models.Date `json:"sold_on"`
	PaymentMethod   string      `json:"payment_method"`
	CustomerName    string      `json:"customer_name"`
	CustomerAddress string      `json:"customer_address"`
	EventName       string      `json:"event_name"`
	Comment         string      `json:"comment"`

	VariantID        int64  `json:"variant_id"`
	ArticleName      string `json:"article_name"`
	VariantLabel     string `json:"variant_label"`
	Quantity         int    `json:"quantity"`
	UnitPriceCents   int64  `json:"unit_price_cents"`
	AmountDueCents   int64  `json:"amount_due_cents"`
	AmountGivenCents *int64 `json:"amount_given_cents"`
	DonationCents    int64  `json:"donation_cents"`

	IsPaid          bool                  `json:"is_paid"`
	PaymentFollowUp bool                  `json:"payment_follow_up"`
	IsReceived      bool                  `json:"is_received"`
	DeliveryStatus  models.DeliveryStatus `json:"delivery_status"`
	IsCancelled     bool                  `json:"is_cancelled"`
}

// Receipt groups a basket's positions the way the history displays them: one
// purchase, expandable into its lines.
type Receipt struct {
	ReceiptID       string      `json:"receipt_id"`
	SoldOn          models.Date `json:"sold_on"`
	PaymentMethod   string      `json:"payment_method"`
	CustomerName    string      `json:"customer_name"`
	CustomerAddress string      `json:"customer_address"`
	EventName       string      `json:"event_name"`
	SoldBy          string      `json:"sold_by"`
	Comment         string      `json:"comment"`

	TotalDueCents   int64 `json:"total_due_cents"`
	TotalGivenCents int64 `json:"total_given_cents"`
	DonationCents   int64 `json:"donation_cents"`
	// IsFullyCancelled is true when every position was cancelled, which is what
	// lets the history grey out a whole receipt rather than each line.
	IsFullyCancelled bool `json:"is_fully_cancelled"`

	Positions []Position `json:"positions"`
}

// History returns the receipts of the scoped band, newest first.
//
// Cancelled positions are included: the point of a cancellation is that it
// stays visible and explainable, unlike a deletion.
func (s *Service) History(ctx context.Context, limit int) ([]Receipt, error) {
	rows, err := s.loadPositions(ctx, nil)
	if err != nil {
		return nil, err
	}
	receipts := groupIntoReceipts(rows)

	if limit > 0 && len(receipts) > limit {
		receipts = receipts[:limit]
	}
	return receipts, nil
}

// Queues are the four work lists of the operations page.
type Queues struct {
	// OpenShipments are parcels still to be sent or on their way.
	OpenShipments []Position `json:"open_shipments"`
	// DeliveredShipments are the completed ones, kept as a separate history.
	DeliveredShipments []Position `json:"delivered_shipments"`
	// OpenPayments are sales still to be collected.
	OpenPayments []Position `json:"open_payments"`
	// SettledPayments are the ones that were chased successfully. An ordinary
	// counter sale never appears here.
	SettledPayments []Position `json:"settled_payments"`
}

// Operations returns the four queues, excluding cancelled positions — a
// cancelled sale is nobody's outstanding work.
func (s *Service) Operations(ctx context.Context) (*Queues, error) {
	rows, err := s.loadPositions(ctx, func(sale *saleRow) bool { return !sale.IsCancelled })
	if err != nil {
		return nil, err
	}

	queues := &Queues{
		OpenShipments:      []Position{},
		DeliveredShipments: []Position{},
		OpenPayments:       []Position{},
		SettledPayments:    []Position{},
	}
	for _, row := range rows {
		position := row.position()
		switch row.DeliveryStatus {
		case models.DeliveryPending, models.DeliveryShipped:
			queues.OpenShipments = append(queues.OpenShipments, position)
		case models.DeliveryReceived:
			queues.DeliveredShipments = append(queues.DeliveredShipments, position)
		}
		if !row.IsPaid {
			queues.OpenPayments = append(queues.OpenPayments, position)
		} else if row.PaymentFollowUp {
			queues.SettledPayments = append(queues.SettledPayments, position)
		}
	}
	return queues, nil
}

// saleRow is a sale joined with the labels needed to display it.
type saleRow struct {
	models.Sale
	ArticleName  string
	VariantLabel string
}

func (r *saleRow) position() Position {
	return Position{
		ID:              r.ID,
		ReceiptID:       r.ReceiptID,
		SoldOn:          r.SoldOn,
		PaymentMethod:   r.PaymentMethod,
		CustomerName:    r.CustomerName,
		CustomerAddress: r.CustomerAddress,
		EventName:       r.EventName,
		Comment:         r.Comment,

		VariantID:        r.VariantID,
		ArticleName:      r.ArticleName,
		VariantLabel:     r.VariantLabel,
		Quantity:         r.Quantity,
		UnitPriceCents:   r.UnitPriceCents,
		AmountDueCents:   r.AmountDueCents,
		AmountGivenCents: r.AmountGivenCents,
		DonationCents:    r.DonationCents,
		IsPaid:           r.IsPaid,
		PaymentFollowUp:  r.PaymentFollowUp,
		IsReceived:       r.IsReceived,
		DeliveryStatus:   r.DeliveryStatus,
		IsCancelled:      r.IsCancelled,
	}
}

// loadPositions reads sales together with their article and option names.
//
// The labels are resolved from the live catalogue rather than from a snapshot,
// which is what makes renaming an option apply retroactively to old receipts.
func (s *Service) loadPositions(ctx context.Context, keep func(*saleRow) bool) ([]saleRow, error) {
	var sales []models.Sale
	err := s.db.WithContext(ctx).
		Order("sold_on DESC, receipt_id DESC, id ASC").
		Find(&sales).Error
	if err != nil {
		return nil, err
	}
	if len(sales) == 0 {
		return nil, nil
	}

	labels, err := catalogue.NewService(s.db).VariantLabels(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]saleRow, 0, len(sales))
	for _, sale := range sales {
		label := labels[sale.VariantID]
		row := saleRow{Sale: sale, ArticleName: label.ArticleName, VariantLabel: label.VariantLabel}
		if keep != nil && !keep(&row) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// groupIntoReceipts folds positions into baskets, preserving the order the
// query produced.
func groupIntoReceipts(rows []saleRow) []Receipt {
	receipts := make([]Receipt, 0)
	index := map[string]int{}

	for _, row := range rows {
		at, seen := index[row.ReceiptID]
		if !seen {
			receipts = append(receipts, Receipt{
				ReceiptID:        row.ReceiptID,
				SoldOn:           row.SoldOn,
				PaymentMethod:    row.PaymentMethod,
				CustomerName:     row.CustomerName,
				CustomerAddress:  row.CustomerAddress,
				EventName:        row.EventName,
				SoldBy:           row.SoldBy,
				Comment:          row.Comment,
				IsFullyCancelled: true,
				Positions:        []Position{},
			})
			at = len(receipts) - 1
			index[row.ReceiptID] = at
		}

		receipt := &receipts[at]
		receipt.Positions = append(receipt.Positions, row.position())

		if !row.IsCancelled {
			receipt.IsFullyCancelled = false
			receipt.TotalDueCents += row.AmountDueCents
			receipt.DonationCents += row.DonationCents
			if row.AmountGivenCents != nil {
				receipt.TotalGivenCents += *row.AmountGivenCents
			}
		}
	}
	return receipts
}
