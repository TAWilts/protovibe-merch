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

// StockMap calculates stock for every variant of the scoped band.
//
// Stock is deliberately never stored. It is the sum of goods received minus
// the non-cancelled sales, which is why cancelling a receipt cannot leave a
// stored counter out of step with the ledger, and why a restored backup is
// automatically consistent.
func (s *Service) StockMap(ctx context.Context) (map[int64]Stock, error) {
	type row struct {
		VariantID int64
		Purchased int64
		Sold      int64
	}

	// The two sides are summed separately and combined in one grouped pass, so
	// a variant that was only bought or only sold still appears.
	var rows []row
	err := s.db.WithContext(ctx).
		Model(&models.Variant{}).
		Select(`variants.id AS variant_id,
			COALESCE((SELECT SUM(p.quantity) FROM purchases p
				WHERE p.variant_id = variants.id), 0) AS purchased,
			COALESCE((SELECT SUM(sa.quantity) FROM sales sa
				WHERE sa.variant_id = variants.id AND sa.is_cancelled = 0), 0) AS sold`).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	stock := make(map[int64]Stock, len(rows))
	for _, r := range rows {
		stock[r.VariantID] = Stock{
			VariantID: r.VariantID,
			Purchased: r.Purchased,
			Sold:      r.Sold,
			OnHand:    r.Purchased - r.Sold,
		}
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
