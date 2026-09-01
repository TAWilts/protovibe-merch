package sales

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Lifecycle errors.
var (
	ErrSaleNotFound      = errors.New("sales: no such sale")
	ErrAlreadyCancelled  = errors.New("sales: this sale is already cancelled")
	ErrNoDeliveryFlow    = errors.New("sales: this sale was handed over at the counter and has no delivery workflow")
	ErrInvalidTransition = errors.New("sales: this status change is not allowed")
	ErrAlreadyPaid       = errors.New("sales: this sale is already marked as paid")
)

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
					"amount_given_cents": position.AmountDueCents,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
