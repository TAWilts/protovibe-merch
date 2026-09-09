package catalogue

import (
	"context"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// AutoWithdrawal lists catalogue rows that were automatically removed from
// the sales assortment because they are depleted and will not be reordered.
type AutoWithdrawal struct {
	VariantIDs []int64
	ArticleIDs []int64
}

// AutoWithdrawDepleted marks active, depleted no-reorder variants as not
// offered. An article follows only when every active variant is both depleted
// and marked no-reorder. Nothing is ever re-enabled automatically.
//
// Passing no variant IDs reconciles the complete band catalogue. A service
// bound through WithTx keeps the stock check and updates in the caller's
// transaction.
func (s *Service) AutoWithdrawDepleted(ctx context.Context, variantIDs []int64) (AutoWithdrawal, error) {
	result := AutoWithdrawal{VariantIDs: []int64{}, ArticleIDs: []int64{}}
	stock, err := s.StockMap(ctx)
	if err != nil {
		return result, err
	}

	query := s.db.WithContext(ctx).Where("is_active = ?", true)
	if len(variantIDs) > 0 {
		query = query.Where("id IN ?", uniquePositiveIDs(variantIDs))
	}
	var candidates []models.Variant
	if err := query.Find(&candidates).Error; err != nil {
		return result, err
	}

	articleSet := make(map[int64]bool)
	now := time.Now().UTC()
	for _, variant := range candidates {
		articleSet[variant.ArticleID] = true
		if !variant.NoReorder || stock[variant.ID].OnHand > 0 || !variant.IsOffered {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&models.Variant{}).Where("id = ?", variant.ID).
			Updates(map[string]any{"is_offered": false, "updated_at": now}).Error; err != nil {
			return result, err
		}
		result.VariantIDs = append(result.VariantIDs, variant.ID)
	}

	for articleID := range articleSet {
		var article models.Article
		if err := s.db.WithContext(ctx).First(&article, articleID).Error; err != nil {
			return result, err
		}
		if !article.IsOffered {
			continue
		}
		var variants []models.Variant
		if err := s.db.WithContext(ctx).
			Where("article_id = ? AND is_active = ?", articleID, true).Find(&variants).Error; err != nil {
			return result, err
		}
		if len(variants) == 0 {
			continue
		}
		allDepleted := true
		for _, variant := range variants {
			if !variant.NoReorder || stock[variant.ID].OnHand > 0 {
				allDepleted = false
				break
			}
		}
		if !allDepleted {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&models.Article{}).Where("id = ?", articleID).
			Updates(map[string]any{"is_offered": false, "updated_at": now}).Error; err != nil {
			return result, err
		}
		result.ArticleIDs = append(result.ArticleIDs, articleID)
	}
	return result, nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}
