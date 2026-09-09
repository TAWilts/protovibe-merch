package catalogue

import (
	"context"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Stock is one variant's derived stock position.
type Stock struct {
	VariantID int64 `json:"variant_id"`
	Purchased int64 `json:"purchased"`
	Sold      int64 `json:"sold"`
	OnHand    int64 `json:"on_hand"`
}

// StockMap calculates the current stock for every variant.
func (s *Service) StockMap(ctx context.Context) (map[int64]Stock, error) {
	return s.StockMapAt(ctx, nil)
}

// StockMapAt calculates cumulative stock through an optional end date.
// Cancelled purchases and sales never contribute.
func (s *Service) StockMapAt(ctx context.Context, to *models.Date) (map[int64]Stock, error) {
	var variantIDs []int64
	if err := s.db.WithContext(ctx).Model(&models.Variant{}).Pluck("id", &variantIDs).Error; err != nil {
		return nil, err
	}

	stock := make(map[int64]Stock, len(variantIDs))
	for _, id := range variantIDs {
		stock[id] = Stock{VariantID: id}
	}

	type movement struct {
		VariantID int64
		Quantity  int64
	}

	var purchases []movement
	purchaseQuery := s.db.WithContext(ctx).Model(&models.Purchase{}).
		Where("is_cancelled = ?", false)
	if to != nil {
		purchaseQuery = purchaseQuery.Where("purchased_on <= ?", *to)
	}
	if err := purchaseQuery.
		Select("variant_id, COALESCE(SUM(quantity), 0) AS quantity").
		Group("variant_id").
		Scan(&purchases).Error; err != nil {
		return nil, err
	}
	for _, row := range purchases {
		entry := stock[row.VariantID]
		entry.VariantID = row.VariantID
		entry.Purchased = row.Quantity
		stock[row.VariantID] = entry
	}

	var sales []movement
	saleQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_cancelled = ? AND line_type = ? AND variant_id IS NOT NULL", false, models.SaleLineMerchandise)
	if to != nil {
		saleQuery = saleQuery.Where("sold_on <= ?", *to)
	}
	if err := saleQuery.
		Select("variant_id, COALESCE(SUM(quantity), 0) AS quantity").
		Group("variant_id").
		Scan(&sales).Error; err != nil {
		return nil, err
	}
	for _, row := range sales {
		entry := stock[row.VariantID]
		entry.VariantID = row.VariantID
		entry.Sold = row.Quantity
		stock[row.VariantID] = entry
	}

	for id, entry := range stock {
		entry.OnHand = entry.Purchased - entry.Sold
		stock[id] = entry
	}
	return stock, nil
}

// IsAtOrBelowMinimum reports whether a variant should raise a low-stock
// warning.
//
// A nil threshold means no warning is configured. An explicit zero stays
// meaningful and warns only once the variant is actually sold out — the
// distinction the original encoded with a nullable minimum_stock column.
func IsAtOrBelowMinimum(onHand int64, minimum *int) bool {
	if minimum == nil {
		return false
	}
	return onHand <= int64(*minimum)
}
