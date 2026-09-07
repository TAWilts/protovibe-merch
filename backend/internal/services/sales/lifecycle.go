package sales

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
)

// Lifecycle errors.
var (
	ErrSaleNotFound      = errors.New("sales: no such sale")
	ErrAlreadyCancelled  = errors.New("sales: this sale is already cancelled")
	ErrNoDeliveryFlow    = errors.New("sales: this sale was handed over at the counter and has no delivery workflow")
	ErrInvalidTransition = errors.New("sales: this status change is not allowed")
	ErrAlreadyPaid       = errors.New("sales: this sale is already marked as paid")
	ErrShippingClosed    = errors.New("sales: shipping costs can only be changed for an open shipment")
)

// ShippingAdjustment describes the receipt-level result of a shipping edit.
type ShippingAdjustment struct {
	ReceiptID            string  `json:"receipt_id"`
	SaleIDs              []int64 `json:"sale_ids"`
	OldShippingCostCents int64   `json:"old_shipping_cost_cents"`
	ShippingCostCents    int64   `json:"shipping_cost_cents"`
	TotalDueCents        int64   `json:"total_due_cents"`
	TotalPaidCents       int64   `json:"total_paid_cents"`
	DiscountCents        int64   `json:"discount_cents"`
	DonationCents        int64   `json:"donation_cents"`
}

// CancelScope selects how much of a receipt a cancellation covers.
type CancelScope string

const (
	// CancelItem removes one position; the rest of the basket stays valid.
	CancelItem CancelScope = "item"
	// CancelReceipt removes the whole basket.
	CancelReceipt CancelScope = "receipt"
)

// Cancel marks a sale, or its whole receipt, as cancelled.
//
// Nothing is deleted: the booking stays readable in the history and the audit
// trail, it simply stops counting towards stock, balances and the work queues.
// That is what keeps a cancelled sale explainable months later.
func (s *Service) Cancel(ctx context.Context, saleID int64, scope CancelScope) ([]int64, error) {
	var cancelled []int64

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sale models.Sale
		if err := tx.WithContext(ctx).First(&sale, saleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSaleNotFound
			}
			return err
		}

		// Cancelling a whole receipt when one position is already cancelled is
		// legitimate; cancelling a single position twice is not.
		if sale.IsCancelled && scope == CancelItem {
			return ErrAlreadyCancelled
		}

		open := tx.WithContext(ctx).Model(&models.Sale{}).Where("is_cancelled = ?", false)
		if scope == CancelReceipt {
			open = open.Where("receipt_id = ?", sale.ReceiptID)
		} else {
			open = open.Where("id = ?", sale.ID)
		}

		// The affected IDs are collected first so the caller can name them in
		// the audit entry.
		if err := open.Session(&gorm.Session{}).Pluck("id", &cancelled).Error; err != nil {
			return err
		}
		if len(cancelled) == 0 {
			return ErrAlreadyCancelled
		}

		return open.Updates(map[string]any{
			"is_cancelled": true,
			"cancelled_at": time.Now().UTC(),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return cancelled, nil
}

// deliveryWorkflowStatuses are the states a shipment can be in.
//
// Any of them may be set from any other. The status is a record of what a
// person did with a parcel, and people mis-tap: a forward-only workflow turns
// one wrong tap into a case that can never be told the truth again. The
// original allowed the correction for exactly that reason
// (_old/app.py:11078, "Advance or correct").
//
// What stays closed is the way out of the workflow: a shipment cannot become
// a counter sale, because that would erase the fact that something was owed.
var deliveryWorkflowStatuses = map[models.DeliveryStatus]bool{
	models.DeliveryPending:  true,
	models.DeliveryShipped:  true,
	models.DeliveryReceived: true,
}

// SetDeliveryStatus moves a sale through the shipping workflow, in either
// direction.
func (s *Service) SetDeliveryStatus(ctx context.Context, saleID int64, next models.DeliveryStatus) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sale models.Sale
		if err := tx.WithContext(ctx).First(&sale, saleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSaleNotFound
			}
			return err
		}
		if sale.IsCancelled {
			return ErrAlreadyCancelled
		}
		// A counter sale never entered the workflow, so it has no status to
		// advance.
		if sale.DeliveryStatus == models.DeliveryNotApplicable {
			return ErrNoDeliveryFlow
		}

		if !deliveryWorkflowStatuses[next] {
			return ErrInvalidTransition
		}

		// is_received follows the status rather than latching, so correcting a
		// premature "received" also puts the sale back on the worklist.
		updates := map[string]any{
			"delivery_status": next,
			"is_received":     next == models.DeliveryReceived,
		}
		return tx.WithContext(ctx).Model(&models.Sale{}).Where("id = ?", saleID).Updates(updates).Error
	})
}

// MarkPaid settles the complete outstanding basket containing saleID.
//
// The original booking is not rewritten: the row keeps payment_follow_up set,
// which is what moves it into the separate "paid later" history rather than
// making it look like an ordinary counter sale.
func (s *Service) MarkPaid(ctx context.Context, saleID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sale models.Sale
		if err := tx.WithContext(ctx).First(&sale, saleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSaleNotFound
			}
			return err
		}
		if sale.IsCancelled {
			return ErrAlreadyCancelled
		}

		var open []models.Sale
		if err := tx.WithContext(ctx).
			Where("receipt_id = ? AND is_cancelled = ? AND is_paid = ?", sale.ReceiptID, false, false).
			Find(&open).Error; err != nil {
			return err
		}
		if len(open) == 0 {
			return ErrAlreadyPaid
		}

		// What was owed is what was received; a late payment carries no
		// donation. Every position is updated inside this transaction so a
		// receipt can never be left half paid.
		for _, position := range open {
			if err := tx.WithContext(ctx).Model(&models.Sale{}).Where("id = ?", position.ID).
				Updates(map[string]any{
					"is_paid":            true,
					"payment_follow_up":  true,
					"discount_cents":     0,
					"donation_cents":     0,
					"amount_given_cents": position.AmountDueCents,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateShippingCost changes the gross receipt-level shipping charge while an
// order is still open. Paid receipts keep the amount actually collected; the
// resulting difference is represented as a discount or donation on the goods.
func (s *Service) UpdateShippingCost(ctx context.Context, receiptID string, shippingCostCents int64) (*ShippingAdjustment, error) {
	if shippingCostCents < 0 {
		return nil, ErrNegativeShipping
	}

	var result *ShippingAdjustment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []models.Sale
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("receipt_id = ?", receiptID).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrSaleNotFound
		}

		active := make([]models.Sale, 0, len(rows))
		for _, row := range rows {
			if row.IsCancelled {
				continue
			}
			if row.DeliveryStatus != models.DeliveryPending && row.DeliveryStatus != models.DeliveryShipped {
				return ErrShippingClosed
			}
			active = append(active, row)
		}
		if len(active) == 0 {
			return ErrAlreadyCancelled
		}

		weights := make([]int64, len(active))
		var oldShipping, collected int64
		allPaid := active[0].IsPaid
		ids := make([]int64, 0, len(active))
		for i, row := range active {
			weights[i] = row.AmountDueCents - row.ShippingCostCents
			oldShipping += row.ShippingCostCents
			ids = append(ids, row.ID)
			if row.IsPaid != allPaid {
				return ErrInvalidTransition
			}
			if !allPaid {
				continue
			}
			if row.AmountGivenCents != nil {
				collected += *row.AmountGivenCents
			} else {
				collected += row.AmountDueCents - row.DiscountCents + row.DonationCents
			}
		}

		shippingShares := money.Distribute(shippingCostCents, weights)
		newTotal := shippingCostCents
		for _, value := range weights {
			newTotal += value
		}

		var discount, donation int64
		if allPaid {
			if collected < newTotal {
				discount = newTotal - collected
			} else {
				donation = collected - newTotal
			}
		}
		discountShares := money.Distribute(discount, weights)
		donationShares := money.Distribute(donation, weights)

		for i, row := range active {
			due := weights[i] + shippingShares[i]
			updates := map[string]any{
				"shipping_cost_cents": shippingShares[i],
				"amount_due_cents":    due,
				"discount_cents":      discountShares[i],
				"donation_cents":      donationShares[i],
			}
			if allPaid {
				updates["amount_given_cents"] = due - discountShares[i] + donationShares[i]
			} else {
				updates["amount_given_cents"] = nil
			}
			if err := tx.WithContext(ctx).Model(&models.Sale{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
		}

		result = &ShippingAdjustment{
			ReceiptID: receiptID, SaleIDs: ids, OldShippingCostCents: oldShipping,
			ShippingCostCents: shippingCostCents, TotalDueCents: newTotal,
			DiscountCents: discount, DonationCents: donation,
		}
		if allPaid {
			result.TotalPaidCents = collected
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
