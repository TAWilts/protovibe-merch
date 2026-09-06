// Package purchases records goods received.
//
// Booked purchases are audit-relevant inventory movements. They can be
// corrected while active, but they are never hard-deleted; a cancellation
// keeps the original receipt and attachments visible while reversing its stock
// and finance effect.
package purchases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
)

// Errors returned by the purchases service.
var (
	ErrEmptyReceipt     = errors.New("purchases: the receipt has no positions")
	ErrInvalidQuantity  = errors.New("purchases: quantity must be positive")
	ErrNegativeCost     = errors.New("purchases: costs cannot be negative")
	ErrUnknownVariant   = errors.New("purchases: unknown variant")
	ErrNotFound         = errors.New("purchases: no such purchase")
	ErrAlreadyCancelled = errors.New("purchases: purchase is already cancelled")
)

// Item is one position of a goods receipt.
type Item struct {
	VariantID     int64  `json:"variant_id"`
	Quantity      int    `json:"quantity"`
	UnitCostCents int64  `json:"unit_cost_cents"`
	Comment       string `json:"comment"`
}

// Request is a complete goods receipt as the client submits it.
type Request struct {
	Items            []Item      `json:"items"`
	PurchasedOn      models.Date `json:"purchased_on"`
	Supplier         string      `json:"supplier"`
	InvoiceReference string      `json:"invoice_reference"`
	// ReceiptID is the preview the client displayed.
	ReceiptID string `json:"receipt_id"`
}

// Actor is who booked the receipt.
type Actor struct {
	UserID   int64
	Username string
}

// Result is the created goods receipt.
type Result struct {
	ReceiptID      string  `json:"receipt_id"`
	PurchaseIDs    []int64 `json:"purchase_ids"`
	TotalCostCents int64   `json:"total_cost_cents"`
}

// Service books goods receipts.
type Service struct {
	db       *gorm.DB
	receipts *receipt.Service
}

// NewService builds the purchases service.
func NewService(database *gorm.DB) *Service {
	return &Service{db: database, receipts: receipt.NewService(database)}
}

// Create books a goods receipt with one or more positions.
func (s *Service) Create(ctx context.Context, req Request, actor Actor) (*Result, error) {
	if len(req.Items) == 0 {
		return nil, ErrEmptyReceipt
	}

	var result *Result
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateVariants(ctx, tx, req.Items); err != nil {
			return err
		}

		receiptID, err := s.receipts.WithTx(tx).
			Allocate(ctx, receipt.PrefixPurchase, req.ReceiptID, req.PurchasedOn, "")
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		ids := make([]int64, 0, len(req.Items))
		var total int64

		for i, item := range req.Items {
			if item.Quantity <= 0 {
				return fmt.Errorf("%w: position %d", ErrInvalidQuantity, i+1)
			}
			if item.UnitCostCents < 0 {
				return fmt.Errorf("%w: position %d", ErrNegativeCost, i+1)
			}

			purchase := &models.Purchase{
				ReceiptID:        receiptID,
				VariantID:        item.VariantID,
				Quantity:         item.Quantity,
				UnitCostCents:    item.UnitCostCents,
				PurchasedOn:      req.PurchasedOn,
				Supplier:         strings.TrimSpace(req.Supplier),
				InvoiceReference: strings.TrimSpace(req.InvoiceReference),
				Comment:          strings.TrimSpace(item.Comment),
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			purchase.CreatedByUserID = &actor.UserID
			purchase.CreatedByUsername = actor.Username

			if err := tx.WithContext(ctx).Create(purchase).Error; err != nil {
				return err
			}
			ids = append(ids, purchase.ID)
			total += int64(item.Quantity) * item.UnitCostCents
		}

		result = &Result{ReceiptID: receiptID, PurchaseIDs: ids, TotalCostCents: total}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Update corrects one position of a goods receipt.
//
// Correcting rather than cancelling is deliberate: a mistyped order quantity
// is a bookkeeping error, and leaving a phantom position behind would distort
// the stock the band relies on at the next gig.
func (s *Service) Update(ctx context.Context, id int64, item Item) error {
	if item.Quantity <= 0 {
		return ErrInvalidQuantity
	}
	if item.UnitCostCents < 0 {
		return ErrNegativeCost
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var purchase models.Purchase
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&purchase, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if purchase.IsCancelled {
			return ErrAlreadyCancelled
		}
		if item.VariantID != 0 && item.VariantID != purchase.VariantID {
			if err := validateVariants(ctx, tx, []Item{item}); err != nil {
				return err
			}
			purchase.VariantID = item.VariantID
		}

		return tx.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", id).
			Updates(map[string]any{
				"variant_id":      purchase.VariantID,
				"quantity":        item.Quantity,
				"unit_cost_cents": item.UnitCostCents,
				"comment":         strings.TrimSpace(item.Comment),
				"updated_at":      time.Now().UTC(),
			}).Error
	})
}

// Cancel marks one purchase position as cancelled. The original data and all
// invoice/receipt attachments remain available for audit.
func (s *Service) Cancel(ctx context.Context, id int64, actor Actor) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var purchase models.Purchase
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&purchase, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if purchase.IsCancelled {
			return ErrAlreadyCancelled
		}

		now := time.Now().UTC()
		updates := map[string]any{
			"is_cancelled":          true,
			"cancelled_at":          now,
			"cancelled_by_username": actor.Username,
			"updated_at":            now,
		}
		if actor.UserID > 0 {
			updates["cancelled_by_user_id"] = actor.UserID
		}
		return tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("id = ?", id).
			Updates(updates).Error
	})
}

// CancelReceipt cancels every position of a goods receipt atomically. Files
// stay attached to the receipt and can still be inspected later.
func (s *Service) CancelReceipt(ctx context.Context, receiptID string, actor Actor) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var positions []models.Purchase
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("receipt_id = ?", receiptID).
			Find(&positions).Error; err != nil {
			return err
		}
		if len(positions) == 0 {
			return ErrNotFound
		}

		active := false
		for _, position := range positions {
			if !position.IsCancelled {
				active = true
				break
			}
		}
		if !active {
			return ErrAlreadyCancelled
		}

		now := time.Now().UTC()
		updates := map[string]any{
			"is_cancelled":          true,
			"cancelled_at":          now,
			"cancelled_by_username": actor.Username,
			"updated_at":            now,
		}
		if actor.UserID > 0 {
			updates["cancelled_by_user_id"] = actor.UserID
		}
		return tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("receipt_id = ? AND is_cancelled = ?", receiptID, false).
			Updates(updates).Error
	})
}

// Delete and DeleteReceipt remain as compatibility shims for older internal
// callers. Their semantics are intentionally cancellation, never hard delete.
func (s *Service) Delete(ctx context.Context, id int64) (string, error) {
	return "", s.Cancel(ctx, id, Actor{})
}

func (s *Service) DeleteReceipt(ctx context.Context, receiptID string) ([]string, error) {
	return nil, s.CancelReceipt(ctx, receiptID, Actor{})
}

// LastUnitCost returns what a variant cost the last time it was bought, which
// the purchase form pre-fills so a reorder needs no retyping.
func (s *Service) LastUnitCost(ctx context.Context, variantID int64) (int64, bool, error) {
	var purchase models.Purchase
	err := s.db.WithContext(ctx).
		Where("variant_id = ? AND is_cancelled = ?", variantID, false).
		Order("purchased_on DESC, id DESC").
		First(&purchase).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return purchase.UnitCostCents, true, nil
}

// validateVariants rejects positions pointing at a variant the band does not
// have. Unlike a sale, a purchase may target a withdrawn variant: restocking
// something that left the assortment is legitimate bookkeeping.
func validateVariants(ctx context.Context, tx *gorm.DB, items []Item) error {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.VariantID)
	}

	var found []int64
	if err := tx.WithContext(ctx).Model(&models.Variant{}).
		Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		return err
	}
	known := make(map[int64]bool, len(found))
	for _, id := range found {
		known[id] = true
	}
	for _, item := range items {
		if !known[item.VariantID] {
			return fmt.Errorf("%w: %d", ErrUnknownVariant, item.VariantID)
		}
	}
	return nil
}
