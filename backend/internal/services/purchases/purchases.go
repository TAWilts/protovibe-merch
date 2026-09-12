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
	"github.com/tawilts/protovibe-merch/backend/internal/services/accountholder"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
)

// Errors returned by the purchases service.
var (
	ErrEmptyReceipt         = errors.New("purchases: the receipt has no positions")
	ErrInvalidQuantity      = errors.New("purchases: quantity must be positive")
	ErrNegativeCost         = errors.New("purchases: costs cannot be negative")
	ErrUnknownVariant       = errors.New("purchases: unknown variant")
	ErrNotFound             = errors.New("purchases: no such purchase")
	ErrAlreadyCancelled     = errors.New("purchases: purchase is already cancelled")
	ErrInvalidVAT           = errors.New("purchases: VAT rate must be between 0 and 100 percent")
	ErrNegativeShipping     = errors.New("purchases: shipping costs cannot be negative")
	ErrInvalidPriceMode     = errors.New("purchases: invalid price mode")
	ErrBasketTotal          = errors.New("purchases: basket price mode requires a non-negative goods total")
	ErrBasketLineEdit       = errors.New("purchases: basket-priced receipts must be edited as a complete receipt")
	ErrInvalidAccountHolder = accountholder.ErrInvalid
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
	ShippingCostCents   int64                    `json:"shipping_cost_cents"`
	PriceMode           models.PurchasePriceMode `json:"price_mode"`
	GoodsTotalCents     *int64                   `json:"goods_total_cents"`
	AccountHolderUserID *int64                   `json:"account_holder_user_id"`
	// ReceiptID is the preview the client displayed.
	ReceiptID string `json:"receipt_id"`
}

type ReceiptEditItem struct {
	ID            int64 `json:"id"`
	Quantity      int   `json:"quantity"`
	UnitCostCents int64 `json:"unit_cost_cents"`
}

type ReceiptUpdateRequest struct {
	Items               []ReceiptEditItem        `json:"items"`
	PurchasedOn         models.Date              `json:"purchased_on"`
	Supplier            string                   `json:"supplier"`
	InvoiceReference    string                   `json:"invoice_reference"`
	PricesIncludeVAT    *bool                    `json:"prices_include_vat"`
	VATRateBasisPoints  *int                     `json:"vat_rate_basis_points"`
	ShippingCostCents   int64                    `json:"shipping_cost_cents"`
	PriceMode           models.PurchasePriceMode `json:"price_mode"`
	GoodsTotalCents     *int64                   `json:"goods_total_cents"`
	AccountHolderUserID *int64                   `json:"account_holder_user_id"`
}

// Actor is who booked the receipt.
type Actor struct {
	UserID   int64
	Username string
}

// Result is the created goods receipt.
type Result struct {
	ReceiptID               string                   `json:"receipt_id"`
	PurchaseIDs             []int64                  `json:"purchase_ids"`
	TotalCostCents          int64                    `json:"total_cost_cents"`
	GoodsTotalCents         int64                    `json:"goods_total_cents"`
	PriceMode               models.PurchasePriceMode `json:"price_mode"`
	AccountHolderUserID     *int64                   `json:"account_holder_user_id"`
	AccountHolderUsername   string                   `json:"account_holder_username"`
	AutoWithdrawnVariantIDs []int64                  `json:"-"`
	AutoWithdrawnArticleIDs []int64                  `json:"-"`
}

// RefillSuggestion is one active, reorderable variant below its configured
// target inventory. LastUnitCostCents is nil until it has been bought before.
type RefillSuggestion struct {
	ArticleID         int64  `json:"article_id"`
	VariantID         int64  `json:"variant_id"`
	ArticleName       string `json:"article_name"`
	VariantLabel      string `json:"variant_label"`
	OnHand            int64  `json:"on_hand"`
	TargetStock       int    `json:"target_stock"`
	SuggestedQuantity int64  `json:"suggested_quantity"`
	LastUnitCostCents *int64 `json:"last_unit_cost_cents"`
}

type preparedCost struct {
	UnitCents int64
	LineCents int64
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

// RefillSuggestions returns the current gap to target for every active,
// reorderable variant. Offered status is deliberately irrelevant: a band may
// replenish an item before making it visible in the sales assortment again.
func (s *Service) RefillSuggestions(ctx context.Context) ([]RefillSuggestion, error) {
	var variants []models.Variant
	if err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Joins("JOIN articles ON articles.id = variants.article_id").
		Where("articles.is_active = ? AND variants.is_active = ? AND variants.no_reorder = ? AND variants.target_stock IS NOT NULL", true, true, false).
		Order("articles.name, variants.id").Find(&variants).Error; err != nil {
		return nil, err
	}
	stock, err := catalogue.NewService(s.db).StockMap(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := catalogue.NewService(s.db).VariantLabels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]RefillSuggestion, 0, len(variants))
	for _, variant := range variants {
		if variant.TargetStock == nil || stock[variant.ID].OnHand >= int64(*variant.TargetStock) {
			continue
		}
		cost, found, err := s.LastUnitCost(ctx, variant.ID)
		if err != nil {
			return nil, err
		}
		var lastCost *int64
		if found {
			lastCost = &cost
		}
		label := labels[variant.ID]
		result = append(result, RefillSuggestion{
			ArticleID: variant.ArticleID, VariantID: variant.ID,
			ArticleName: label.ArticleName, VariantLabel: label.VariantLabel,
			OnHand: stock[variant.ID].OnHand, TargetStock: *variant.TargetStock,
			SuggestedQuantity: int64(*variant.TargetStock) - stock[variant.ID].OnHand,
			LastUnitCostCents: lastCost,
		})
	}
	return result, nil
}

// Create books a goods receipt with one or more positions.
func (s *Service) Create(ctx context.Context, req Request, actor Actor) (*Result, error) {
	if len(req.Items) == 0 {
		return nil, ErrEmptyReceipt
	}
	priceMode, costs, goodsGross, err := prepareCosts(req.Items, req.PriceMode, req.GoodsTotalCents, req.PricesIncludeVAT, req.VATRateBasisPoints)
	if err != nil {
		return nil, err
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
		holderID, holderUsername, err := accountholder.Resolve(ctx, tx, req.AccountHolderUserID)
		if err != nil {
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
			cost := costs[i]

			purchase := &models.Purchase{
				ReceiptID:             receiptID,
				VariantID:             item.VariantID,
				Quantity:              item.Quantity,
				UnitCostCents:         cost.UnitCents,
				PriceMode:             priceMode,
				LineTotalCostCents:    cost.LineCents,
				PricesIncludeVAT:      includeVAT,
				VATRateBasisPoints:    vatRate,
				ShippingCostCents:     shippingGross,
				AccountHolderUserID:   holderID,
				AccountHolderUsername: holderUsername,
				PurchasedOn:           req.PurchasedOn,
				Supplier:              strings.TrimSpace(req.Supplier),
				InvoiceReference:      strings.TrimSpace(req.InvoiceReference),
				Comment:               strings.TrimSpace(item.Comment),
				CreatedAt:             now,
				UpdatedAt:             now,
			}
			purchase.CreatedByUserID = &actor.UserID
			purchase.CreatedByUsername = actor.Username

			if err := tx.WithContext(ctx).Create(purchase).Error; err != nil {
				return err
			}
			ids = append(ids, purchase.ID)
			total += cost.LineCents
		}
		total += shippingGross

		result = &Result{
			ReceiptID: receiptID, PurchaseIDs: ids, TotalCostCents: total,
			GoodsTotalCents: goodsGross, PriceMode: priceMode,
			AccountHolderUserID: holderID, AccountHolderUsername: holderUsername,
		}
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
	_, err := s.UpdateWithWithdrawal(ctx, id, item)
	return err
}

// UpdateWithWithdrawal updates a legacy unit-priced position and reports any
// catalogue entries that became unavailable because stock was reduced.
func (s *Service) UpdateWithWithdrawal(ctx context.Context, id int64, item Item) (catalogue.AutoWithdrawal, error) {
	withdrawal := catalogue.AutoWithdrawal{VariantIDs: []int64{}, ArticleIDs: []int64{}}
	if item.Quantity <= 0 {
		return withdrawal, ErrInvalidQuantity
	}
	if item.UnitCostCents < 0 {
		return withdrawal, ErrNegativeCost
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if purchase.PriceMode == models.PurchasePriceBasket {
			return ErrBasketLineEdit
		}
		if item.VariantID != 0 && item.VariantID != purchase.VariantID {
			if err := validateVariants(ctx, tx, []Item{item}); err != nil {
				return err
			}
			purchase.VariantID = item.VariantID
		}

		if err := tx.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", id).
			Updates(map[string]any{
				"variant_id":            purchase.VariantID,
				"quantity":              item.Quantity,
				"unit_cost_cents":       item.UnitCostCents,
				"line_total_cost_cents": int64(item.Quantity) * item.UnitCostCents,
				"comment":               strings.TrimSpace(item.Comment),
				"updated_at":            time.Now().UTC(),
			}).Error; err != nil {
			return err
		}
		var err error
		withdrawal, err = catalogue.NewService(tx).AutoWithdrawDepleted(ctx, []int64{purchase.VariantID})
		return err
	})
	return withdrawal, err
}

// UpdateReceipt corrects all active positions and shared receipt metadata atomically.
// The API exposes this only while PURCHASE_EDITING_ENABLED=true.
func (s *Service) UpdateReceipt(ctx context.Context, receiptID string, req ReceiptUpdateRequest) (*Result, error) {
	if len(req.Items) == 0 {
		return nil, ErrEmptyReceipt
	}
	costItems := make([]Item, len(req.Items))
	for i, item := range req.Items {
		costItems[i] = Item{Quantity: item.Quantity, UnitCostCents: item.UnitCostCents}
	}
	priceMode, costs, goodsGross, err := prepareCosts(costItems, req.PriceMode, req.GoodsTotalCents, req.PricesIncludeVAT, req.VATRateBasisPoints)
	if err != nil {
		return nil, err
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

		holderID := positions[0].AccountHolderUserID
		holderUsername := positions[0].AccountHolderUsername
		if !accountholder.Same(req.AccountHolderUserID, holderID) {
			var err error
			holderID, holderUsername, err = accountholder.Resolve(ctx, tx, req.AccountHolderUserID)
			if err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		if err := tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("receipt_id = ?", receiptID).
			Updates(map[string]any{
				"purchased_on":            req.PurchasedOn,
				"supplier":                strings.TrimSpace(req.Supplier),
				"invoice_reference":       strings.TrimSpace(req.InvoiceReference),
				"prices_include_vat":      includeVAT,
				"vat_rate_basis_points":   vatRate,
				"shipping_cost_cents":     shippingGross,
				"account_holder_user_id":  holderID,
				"account_holder_username": holderUsername,
				"price_mode":              priceMode,
				"updated_at":              now,
			}).Error; err != nil {
			return err
		}

		seen := make(map[int64]bool)
		ids := make([]int64, 0, len(req.Items))
		var total int64
		for index, item := range req.Items {
			if _, ok := active[item.ID]; !ok || seen[item.ID] {
				return ErrNotFound
			}
			seen[item.ID] = true
			cost := costs[index]
			if err := tx.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", item.ID).
				Updates(map[string]any{
					"quantity":              item.Quantity,
					"unit_cost_cents":       cost.UnitCents,
					"line_total_cost_cents": cost.LineCents,
					"updated_at":            now,
				}).Error; err != nil {
				return err
			}
			ids = append(ids, item.ID)
			total += cost.LineCents
		}
		total += shippingGross
		variantIDs := make([]int64, 0, len(active))
		for _, position := range active {
			variantIDs = append(variantIDs, position.VariantID)
		}
		withdrawal, err := catalogue.NewService(tx).AutoWithdrawDepleted(ctx, variantIDs)
		if err != nil {
			return err
		}
		result = &Result{
			ReceiptID: receiptID, PurchaseIDs: ids, TotalCostCents: total,
			GoodsTotalCents: goodsGross, PriceMode: priceMode,
			AccountHolderUserID: holderID, AccountHolderUsername: holderUsername,
			AutoWithdrawnVariantIDs: withdrawal.VariantIDs,
			AutoWithdrawnArticleIDs: withdrawal.ArticleIDs,
		}
		return nil
	})
	return result, err
}

// Cancel marks one purchase position as cancelled. The original data and all
// invoice/receipt attachments remain available for audit.
func (s *Service) Cancel(ctx context.Context, id int64, actor Actor) error {
	_, err := s.CancelWithWithdrawal(ctx, id, actor)
	return err
}

func (s *Service) CancelWithWithdrawal(ctx context.Context, id int64, actor Actor) (catalogue.AutoWithdrawal, error) {
	withdrawal := catalogue.AutoWithdrawal{VariantIDs: []int64{}, ArticleIDs: []int64{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if err := tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("id = ?", id).
			Updates(updates).Error; err != nil {
			return err
		}
		var err error
		withdrawal, err = catalogue.NewService(tx).AutoWithdrawDepleted(ctx, []int64{purchase.VariantID})
		return err
	})
	return withdrawal, err
}

// CancelReceipt cancels every position of a goods receipt atomically. Files
// stay attached to the receipt and can still be inspected later.
func (s *Service) CancelReceipt(ctx context.Context, receiptID string, actor Actor) error {
	_, err := s.CancelReceiptWithWithdrawal(ctx, receiptID, actor)
	return err
}

func (s *Service) CancelReceiptWithWithdrawal(ctx context.Context, receiptID string, actor Actor) (catalogue.AutoWithdrawal, error) {
	withdrawal := catalogue.AutoWithdrawal{VariantIDs: []int64{}, ArticleIDs: []int64{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if err := tx.WithContext(ctx).Model(&models.Purchase{}).
			Where("receipt_id = ? AND is_cancelled = ?", receiptID, false).
			Updates(updates).Error; err != nil {
			return err
		}
		variantIDs := make([]int64, 0, len(positions))
		for _, position := range positions {
			variantIDs = append(variantIDs, position.VariantID)
		}
		var err error
		withdrawal, err = catalogue.NewService(tx).AutoWithdrawDepleted(ctx, variantIDs)
		return err
	})
	return withdrawal, err
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
	if purchase.Quantity > 0 && purchase.LineTotalCostCents > 0 {
		return roundedUnitCost(purchase.LineTotalCostCents, purchase.Quantity), true, nil
	}
	return purchase.UnitCostCents, true, nil
}

func prepareCosts(items []Item, mode models.PurchasePriceMode, goodsTotal *int64, includeVAT *bool, rate *int) (models.PurchasePriceMode, []preparedCost, int64, error) {
	if mode == "" {
		mode = models.PurchasePriceUnit
	}
	if mode != models.PurchasePriceUnit && mode != models.PurchasePriceBasket {
		return "", nil, 0, ErrInvalidPriceMode
	}
	include, vatRate, _, err := pricingTerms(includeVAT, rate, 0)
	if err != nil {
		return "", nil, 0, err
	}
	weights := make([]int64, len(items))
	for i, item := range items {
		if item.Quantity <= 0 {
			return "", nil, 0, fmt.Errorf("%w: position %d", ErrInvalidQuantity, i+1)
		}
		if mode == models.PurchasePriceUnit && item.UnitCostCents < 0 {
			return "", nil, 0, fmt.Errorf("%w: position %d", ErrNegativeCost, i+1)
		}
		weights[i] = int64(item.Quantity)
	}

	costs := make([]preparedCost, len(items))
	if mode == models.PurchasePriceBasket {
		if goodsTotal == nil || *goodsTotal < 0 {
			return "", nil, 0, ErrBasketTotal
		}
		grossTotal := GrossFromEntered(*goodsTotal, include, vatRate)
		shares := money.Distribute(grossTotal, weights)
		for i, share := range shares {
			costs[i] = preparedCost{UnitCents: roundedUnitCost(share, items[i].Quantity), LineCents: share}
		}
		return mode, costs, grossTotal, nil
	}

	var total int64
	for i, item := range items {
		unit := GrossFromEntered(item.UnitCostCents, include, vatRate)
		line := int64(item.Quantity) * unit
		costs[i] = preparedCost{UnitCents: unit, LineCents: line}
		total += line
	}
	return mode, costs, total, nil
}

func roundedUnitCost(lineTotal int64, quantity int) int64 {
	if quantity <= 0 {
		return 0
	}
	return (lineTotal + int64(quantity)/2) / int64(quantity)
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
