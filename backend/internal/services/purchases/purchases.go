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
	ErrInvalidVAT       = errors.New("purchases: VAT rate must be between 0 and 100 percent")
	ErrNegativeShipping = errors.New("purchases: shipping costs cannot be negative")
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
	// Pointer fields preserve gross-price semantics for older clients that omit them.
	PricesIncludeVAT   *bool `json:"prices_include_vat"`
	VATRateBasisPoints *int  `json:"vat_rate_basis_points"`
	// Shipping uses the same net/gross interpretation as the item prices.
	ShippingCostCents int64 `json:"shipping_cost_cents"`
	// ReceiptID is the preview the client displayed.
	ReceiptID string `json:"receipt_id"`
}

type ReceiptEditItem struct {
	ID            int64 `json:"id"`
	Quantity      int   `json:"quantity"`
	UnitCostCents int64 `json:"unit_cost_cents"`
}

type ReceiptUpdateRequest struct {
	Items              []ReceiptEditItem `json:"items"`
	PurchasedOn        models.Date       `json:"purchased_on"`
	Supplier           string            `json:"supplier"`
	InvoiceReference   string            `json:"invoice_reference"`
	PricesIncludeVAT   *bool             `json:"prices_include_vat"`
	VATRateBasisPoints *int              `json:"vat_rate_basis_points"`
	ShippingCostCents  int64             `json:"shipping_cost_cents"`
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
	includeVAT, vatRate, shippingGross, err := pricingTerms(
		req.PricesIncludeVAT, req.VATRateBasisPoints, req.ShippingCostCents,
	)
	if err != nil {
		return nil, err
	}

	var result *Result
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			grossUnitCost := GrossFromEntered(item.UnitCostCents, includeVAT, vatRate)

			purchase := &models.Purchase{
				ReceiptID:          receiptID,
				VariantID:          item.VariantID,
				Quantity:           item.Quantity,
				UnitCostCents:      grossUnitCost,
				PricesIncludeVAT:   includeVAT,
				VATRateBasisPoints: vatRate,
				ShippingCostCents:  shippingGross,
				PurchasedOn:        req.PurchasedOn,
				Supplier:           strings.TrimSpace(req.Supplier),
				InvoiceReference:   strings.TrimSpace(req.InvoiceReference),
				Comment:            strings.TrimSpace(item.Comment),
				CreatedAt:          now,
				UpdatedAt:          now,
			}
			purchase.CreatedByUserID = &actor.UserID
			purchase.CreatedByUsername = actor.Username

			if err := tx.WithContext(ctx).Create(purchase).Error; err != nil {
				return err
			}
			ids = append(ids, purchase.ID)
			total += int64(item.Quantity) * grossUnitCost
		}
		total += shippingGross

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

// UpdateReceipt corrects all active positions and shared receipt metadata atomically.
// The API exposes this only while PURCHASE_EDITING_ENABLED=true.
func (s *Service) UpdateReceipt(ctx context.Context, receiptID string, req ReceiptUpdateRequest) (*Result, error) {
	if len(req.Items) == 0 {
		return nil, ErrEmptyReceipt
	}
	includeVAT, vatRate, shippingGross, err := pricingTerms(
		req.PricesIncludeVAT, req.VATRateBasisPoints, req.ShippingCostCents,
	)
	if err != nil {
		return nil, err
	}

	var result *Result
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

		active := make(map[int64]models.Purchase)
		for _, position := range positions {
			if !position.IsCancelled {
				active[position.ID] = position
			}
		}
		if len(active) == 0 {
			return ErrAlreadyCancelled
		}
		if len(req.Items) != len(active) {
			return ErrEmptyReceipt
		}

		now := time.Now().UTC()
		if err := tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("receipt_id = ?", receiptID).
			Updates(map[string]any{
				"purchased_on":          req.PurchasedOn,
				"supplier":              strings.TrimSpace(req.Supplier),
				"invoice_reference":     strings.TrimSpace(req.InvoiceReference),
				"prices_include_vat":    includeVAT,
				"vat_rate_basis_points": vatRate,
				"shipping_cost_cents":   shippingGross,
				"updated_at":            now,
			}).Error; err != nil {
			return err
		}

		seen := make(map[int64]bool)
		ids := make([]int64, 0, len(req.Items))
		var total int64
		for index, item := range req.Items {
			if item.Quantity <= 0 {
				return fmt.Errorf("%w: position %d", ErrInvalidQuantity, index+1)
			}
			if item.UnitCostCents < 0 {
				return fmt.Errorf("%w: position %d", ErrNegativeCost, index+1)
			}
			if _, ok := active[item.ID]; !ok || seen[item.ID] {
				return ErrNotFound
			}
			seen[item.ID] = true
			gross := GrossFromEntered(item.UnitCostCents, includeVAT, vatRate)
			if err := tx.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", item.ID).
				Updates(map[string]any{
					"quantity":        item.Quantity,
					"unit_cost_cents": gross,
					"updated_at":      now,
				}).Error; err != nil {
				return err
			}
			ids = append(ids, item.ID)
			total += int64(item.Quantity) * gross
		}
		total += shippingGross
		result = &Result{ReceiptID: receiptID, PurchaseIDs: ids, TotalCostCents: total}
		return nil
	})
	return result, err
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

const DefaultVATRateBasisPoints = 1900

func pricingTerms(include *bool, rate *int, shippingEntered int64) (bool, int, int64, error) {
	includeVAT := true
	if include != nil {
		includeVAT = *include
	}
	vatRate := DefaultVATRateBasisPoints
	if rate != nil {
		vatRate = *rate
	}
	if vatRate < 0 || vatRate > 10000 {
		return false, 0, 0, ErrInvalidVAT
	}
	if shippingEntered < 0 {
		return false, 0, 0, ErrNegativeShipping
	}
	return includeVAT, vatRate, GrossFromEntered(shippingEntered, includeVAT, vatRate), nil
}

// GrossFromEntered normalises a user-entered amount to the canonical gross cents.
func GrossFromEntered(cents int64, includesVAT bool, rateBasisPoints int) int64 {
	if includesVAT || cents == 0 {
		return cents
	}
	return (cents*int64(10000+rateBasisPoints) + 5000) / 10000
}

func NetFromGross(cents int64, rateBasisPoints int) int64 {
	if cents == 0 || rateBasisPoints <= 0 {
		return cents
	}
	return (cents*10000 + int64(10000+rateBasisPoints)/2) / int64(10000+rateBasisPoints)
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
