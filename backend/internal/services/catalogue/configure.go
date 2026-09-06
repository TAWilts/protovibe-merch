package catalogue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// OptionValueInput is one selectable value in a saved configuration. A zero ID
// means the band just added it.
type OptionValueInput struct {
	ID    int64  `json:"id"`
	Value string `json:"value"`
}

// OptionGroupInput is one option column in a saved configuration.
type OptionGroupInput struct {
	ID     int64              `json:"id"`
	Name   string             `json:"name"`
	Values []OptionValueInput `json:"values"`
}

// VariantInput carries the per-variant overrides the management page edits.
// Only variants the client actually sent are touched.
type VariantInput struct {
	ID                        int64  `json:"id"`
	SalePriceCents            *int64 `json:"sale_price_cents"`
	DefaultPurchasePriceCents *int64 `json:"default_purchase_price_cents"`
	// MinimumStock is tri-state: absent leaves it alone, null clears the
	// warning, a number sets the threshold. An explicit 0 means "warn only
	// once sold out", which is why null and 0 must stay distinguishable.
	MinimumStock *int  `json:"minimum_stock"`
	ClearMinimum bool  `json:"clear_minimum_stock"`
	IsOffered    *bool `json:"is_offered"`
	NoReorder    *bool `json:"no_reorder"`
}

// ArticleConfiguration is a complete save of the article management page.
type ArticleConfiguration struct {
	Name                      *string            `json:"name"`
	DefaultSalePriceCents     *int64             `json:"default_sale_price_cents"`
	DefaultPurchasePriceCents *int64             `json:"default_purchase_price_cents"`
	IsOffered                 *bool              `json:"is_offered"`
	OptionGroups              []OptionGroupInput `json:"option_groups"`
	Variants                  []VariantInput     `json:"variants"`
}

// ErrUnknownEntity is returned when a save references a group, value or
// variant that does not belong to this article.
var ErrUnknownEntity = errors.New("catalogue: referenced entity does not belong to this article")

// ApplyConfiguration saves an article's options, variants and prices in one
// transaction.
//
// The central rule is preserved from the original: options that disappear from
// the payload are deactivated, never deleted, so historic receipts keep
// resolving their names — and renaming a value updates old receipts, which is
// the behaviour the band relies on. A newly added option dimension first maps
// the existing variants onto its first value, so stock, prices and photos
// survive the change.
func (s *Service) ApplyConfiguration(ctx context.Context, articleID int64, cfg ArticleConfiguration) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txService := s.WithTx(tx)

		var article models.Article
		if err := tx.WithContext(ctx).First(&article, articleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrArticleNotFound
			}
			return err
		}

		if err := applyArticleFields(ctx, tx, &article, cfg); err != nil {
			return err
		}

		newGroupFirstValues, err := txService.applyOptionGroups(ctx, tx, articleID, cfg.OptionGroups)
		if err != nil {
			return err
		}

		// Map existing variants onto each new dimension before regenerating,
		// otherwise they would all go inactive and take their history with them.
		if err := txService.PreserveVariantsForNewOptionGroups(ctx, articleID, newGroupFirstValues); err != nil {
			return err
		}
		if err := txService.SyncVariants(ctx, articleID); err != nil {
			return err
		}

		return txService.applyVariantOverrides(ctx, tx, articleID, cfg.Variants)
	})
}

func applyArticleFields(ctx context.Context, tx *gorm.DB, article *models.Article, cfg ArticleConfiguration) error {
	updates := map[string]any{}

	if cfg.Name != nil {
		name := strings.TrimSpace(*cfg.Name)
		if name == "" || len(name) > 200 {
			return fmt.Errorf("%w: 1 to 200 characters required", ErrInvalidName)
		}
		updates["name"] = name
	}
	// A changed standard price follows through to every variant that still
	// carries the old one. Without this the field is a one-shot template: the
	// combinations of a fresh article are generated before the price is set,
	// and would keep their initial zero forever. Genuinely customised prices
	// differ from the old default and stay untouched. It mirrors the rule in
	// _old/app.py:11826.
	cascades := map[string]int64{}
	if cfg.DefaultSalePriceCents != nil {
		if *cfg.DefaultSalePriceCents < 0 {
			return ErrNegativePrice
		}
		if *cfg.DefaultSalePriceCents != article.DefaultSalePriceCents {
			cascades["sale_price_cents"] = article.DefaultSalePriceCents
		}
		updates["default_sale_price_cents"] = *cfg.DefaultSalePriceCents
	}
	if cfg.DefaultPurchasePriceCents != nil {
		if *cfg.DefaultPurchasePriceCents < 0 {
			return ErrNegativePrice
		}
		if *cfg.DefaultPurchasePriceCents != article.DefaultPurchasePriceCents {
			cascades["default_purchase_price_cents"] = article.DefaultPurchasePriceCents
		}
		updates["default_purchase_price_cents"] = *cfg.DefaultPurchasePriceCents
	}
	if cfg.IsOffered != nil {
		// Withdrawing an article from the assortment is not a deletion: its
		// bookings, stock and future purchases stay fully available.
		updates["is_offered"] = *cfg.IsOffered
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now().UTC()

	now := updates["updated_at"]
	for column, previousDefault := range cascades {
		var replacement int64
		switch column {
		case "sale_price_cents":
			replacement = *cfg.DefaultSalePriceCents
		default:
			replacement = *cfg.DefaultPurchasePriceCents
		}
		if err := tx.WithContext(ctx).Model(&models.Variant{}).
			Where("article_id = ? AND "+column+" = ?", article.ID, previousDefault).
			Updates(map[string]any{column: replacement, "updated_at": now}).Error; err != nil {
			return err
		}
	}

	err := tx.WithContext(ctx).Model(&models.Article{}).Where("id = ?", article.ID).Updates(updates).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return fmt.Errorf("%w: an article with this name already exists", ErrInvalidName)
	}
	return err
}

// applyOptionGroups reconciles the option configuration and returns the first
// value ID of every group that did not exist before.
func (s *Service) applyOptionGroups(ctx context.Context, tx *gorm.DB, articleID int64, groups []OptionGroupInput) ([]int64, error) {
	// A payload without an option_groups key leaves the configuration alone;
	// an explicitly empty list means the article has no options at all.
	if groups == nil {
		return nil, nil
	}

	var existingGroups []models.OptionGroup
	if err := tx.WithContext(ctx).Where("article_id = ?", articleID).Find(&existingGroups).Error; err != nil {
		return nil, err
	}
	existingGroupIDs := make(map[int64]bool, len(existingGroups))
	for _, group := range existingGroups {
		existingGroupIDs[group.ID] = true
	}

	now := time.Now().UTC()
	keptGroups := make(map[int64]bool, len(groups))
	newFirstValues := make([]int64, 0)

	for position, input := range groups {
		name := strings.TrimSpace(input.Name)
		if name == "" || len(name) > 120 {
			return nil, fmt.Errorf("%w: option name must be 1 to 120 characters", ErrInvalidName)
		}

		isNewGroup := input.ID == 0
		var groupID int64

		if isNewGroup {
			group := &models.OptionGroup{ArticleID: articleID, Name: name, Position: position, IsActive: true}
			if err := tx.WithContext(ctx).Create(group).Error; err != nil {
				return nil, err
			}
			groupID = group.ID
		} else {
			if !existingGroupIDs[input.ID] {
				return nil, fmt.Errorf("%w: option group %d", ErrUnknownEntity, input.ID)
			}
			groupID = input.ID
			err := tx.WithContext(ctx).Model(&models.OptionGroup{}).Where("id = ?", groupID).
				Updates(map[string]any{
					"name": name, "position": position, "is_active": true, "updated_at": now,
				}).Error
			if err != nil {
				return nil, err
			}
		}
		keptGroups[groupID] = true

		firstValueID, err := s.applyOptionValues(ctx, tx, groupID, input.Values)
		if err != nil {
			return nil, err
		}
		if isNewGroup && firstValueID != 0 {
			newFirstValues = append(newFirstValues, firstValueID)
		}
	}

	// Whatever the payload dropped is deactivated, so old receipts can still
	// resolve the names they were booked with.
	for _, group := range existingGroups {
		if keptGroups[group.ID] || !group.IsActive {
			continue
		}
		err := tx.WithContext(ctx).Model(&models.OptionGroup{}).Where("id = ?", group.ID).
			Updates(map[string]any{"is_active": false, "updated_at": now}).Error
		if err != nil {
			return nil, err
		}
	}

	return newFirstValues, nil
}

// applyOptionValues reconciles one group's values and returns the ID of its
// first value, which is the mapping target for a newly added dimension.
func (s *Service) applyOptionValues(ctx context.Context, tx *gorm.DB, groupID int64, values []OptionValueInput) (int64, error) {
	var existing []models.OptionValue
	if err := tx.WithContext(ctx).Where("option_group_id = ?", groupID).Find(&existing).Error; err != nil {
		return 0, err
	}
	existingIDs := make(map[int64]bool, len(existing))
	for _, value := range existing {
		existingIDs[value.ID] = true
	}

	now := time.Now().UTC()
	kept := make(map[int64]bool, len(values))
	var firstValueID int64

	for position, input := range values {
		text := strings.TrimSpace(input.Value)
		if text == "" || len(text) > 120 {
			return 0, fmt.Errorf("%w: option value must be 1 to 120 characters", ErrInvalidName)
		}

		var valueID int64
		if input.ID == 0 {
			value := &models.OptionValue{OptionGroupID: groupID, Value: text, Position: position, IsActive: true}
			if err := tx.WithContext(ctx).Create(value).Error; err != nil {
				return 0, err
			}
			valueID = value.ID
		} else {
			if !existingIDs[input.ID] {
				return 0, fmt.Errorf("%w: option value %d", ErrUnknownEntity, input.ID)
			}
			valueID = input.ID
			// Renaming a value updates every historic receipt that shows it,
			// which is exactly the retroactive behaviour that was asked for.
			err := tx.WithContext(ctx).Model(&models.OptionValue{}).Where("id = ?", valueID).
				Updates(map[string]any{
					"value": text, "position": position, "is_active": true, "updated_at": now,
				}).Error
			if err != nil {
				return 0, err
			}
		}
		kept[valueID] = true
		if position == 0 {
			firstValueID = valueID
		}
	}

	for _, value := range existing {
		if kept[value.ID] || !value.IsActive {
			continue
		}
		err := tx.WithContext(ctx).Model(&models.OptionValue{}).Where("id = ?", value.ID).
			Updates(map[string]any{"is_active": false, "updated_at": now}).Error
		if err != nil {
			return 0, err
		}
	}

	return firstValueID, nil
}

// applyVariantOverrides stores the per-variant prices, thresholds and
// assortment flags.
func (s *Service) applyVariantOverrides(ctx context.Context, tx *gorm.DB, articleID int64, inputs []VariantInput) error {
	if len(inputs) == 0 {
		return nil
	}

	var existing []models.Variant
	if err := tx.WithContext(ctx).Where("article_id = ?", articleID).Find(&existing).Error; err != nil {
		return err
	}
	known := make(map[int64]bool, len(existing))
	for _, variant := range existing {
		known[variant.ID] = true
	}

	now := time.Now().UTC()
	for _, input := range inputs {
		if !known[input.ID] {
			return fmt.Errorf("%w: variant %d", ErrUnknownEntity, input.ID)
		}

		updates := map[string]any{}
		if input.SalePriceCents != nil {
			if *input.SalePriceCents < 0 {
				return ErrNegativePrice
			}
			updates["sale_price_cents"] = *input.SalePriceCents
		}
		if input.DefaultPurchasePriceCents != nil {
			if *input.DefaultPurchasePriceCents < 0 {
				return ErrNegativePrice
			}
			updates["default_purchase_price_cents"] = *input.DefaultPurchasePriceCents
		}
		switch {
		case input.ClearMinimum:
			updates["minimum_stock"] = nil
		case input.MinimumStock != nil:
			if *input.MinimumStock < 0 {
				return fmt.Errorf("catalogue: minimum stock cannot be negative")
			}
			updates["minimum_stock"] = *input.MinimumStock
		}
		if input.IsOffered != nil {
			updates["is_offered"] = *input.IsOffered
		}
		if input.NoReorder != nil {
			updates["no_reorder"] = *input.NoReorder
		}
		if len(updates) == 0 {
			continue
		}
		updates["updated_at"] = now

		if err := tx.WithContext(ctx).Model(&models.Variant{}).Where("id = ?", input.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}
