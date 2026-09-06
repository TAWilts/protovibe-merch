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

// DefaultNewArticleOptions is what a freshly created article starts with, so a
// band can sell a shirt immediately and adjust the options afterwards. It
// matches DEFAULT_NEW_ARTICLE_OPTIONS in the original.
var DefaultNewArticleOptions = []struct {
	Name   string
	Values []string
}{
	{Name: "Farbe", Values: []string{"Schwarz", "Weiß"}},
	{Name: "Größe", Values: []string{"S", "M", "L", "XL", "XXL"}},
}

// Errors returned by the catalogue service.
var (
	ErrArticleNotFound = errors.New("catalogue: article not found")
	ErrInvalidName     = errors.New("catalogue: invalid name")
	ErrNegativePrice   = errors.New("catalogue: prices cannot be negative")
)

// Service owns article, option and variant persistence.
//
// Every method expects a band scope in the context; the GORM tenant callback
// applies the band filter, so no query here spells out band_id.
type Service struct {
	db *gorm.DB
}

// NewService builds the catalogue service.
func NewService(database *gorm.DB) *Service { return &Service{db: database} }

// WithTx returns a service bound to an open transaction, so callers can group
// an article edit and its variant synchronisation into one atomic change.
func (s *Service) WithTx(tx *gorm.DB) *Service { return &Service{db: tx} }

// DB exposes the handle so callers can start a transaction around several
// catalogue operations.
func (s *Service) DB() *gorm.DB { return s.db }

// ActiveOptionConfig reads the active option groups and their active values in
// display order, ready for Cartesian variant generation.
func (s *Service) ActiveOptionConfig(ctx context.Context, articleID int64) ([]OptionGroupValues, error) {
	var groups []models.OptionGroup
	err := s.db.WithContext(ctx).
		Where("article_id = ? AND is_active = ?", articleID, true).
		Order("position, id").
		Find(&groups).Error
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, nil
	}

	groupIDs := make([]int64, len(groups))
	for i, group := range groups {
		groupIDs[i] = group.ID
	}

	var values []models.OptionValue
	err = s.db.WithContext(ctx).
		Where("option_group_id IN ? AND is_active = ?", groupIDs, true).
		Order("position, id").
		Find(&values).Error
	if err != nil {
		return nil, err
	}

	byGroup := make(map[int64][]int64, len(groups))
	for _, value := range values {
		byGroup[value.OptionGroupID] = append(byGroup[value.OptionGroupID], value.ID)
	}

	config := make([]OptionGroupValues, len(groups))
	for i, group := range groups {
		config[i] = OptionGroupValues{GroupID: group.ID, ValueIDs: byGroup[group.ID]}
	}
	return config, nil
}

// SyncVariants brings an article's variants in line with its active option
// configuration.
//
// Nothing is ever physically deleted. A combination that disappears is
// deactivated, because historic sales still point at it and their option
// values must stay resolvable. A combination that reappears is reactivated
// with its original price, stock history and photos intact.
func (s *Service) SyncVariants(ctx context.Context, articleID int64) error {
	var article models.Article
	if err := s.db.WithContext(ctx).First(&article, articleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrArticleNotFound
		}
		return err
	}

	config, err := s.ActiveOptionConfig(ctx, articleID)
	if err != nil {
		return err
	}
	expectedKeys := ExpectedCombinations(config)

	var existing []models.Variant
	if err := s.db.WithContext(ctx).Where("article_id = ?", articleID).Find(&existing).Error; err != nil {
		return err
	}
	existingByKey := make(map[string]models.Variant, len(existing))
	for _, variant := range existing {
		existingByKey[variant.CombinationKey] = variant
	}

	now := time.Now().UTC()

	for _, key := range expectedKeys {
		if found, ok := existingByKey[key]; ok {
			if !found.IsActive {
				if err := s.db.WithContext(ctx).Model(&models.Variant{}).
					Where("id = ?", found.ID).
					Updates(map[string]any{"is_active": true, "updated_at": now}).Error; err != nil {
					return err
				}
			}
			continue
		}

		optionIDs, err := ParseCombinationKey(key)
		if err != nil {
			return fmt.Errorf("catalogue: malformed combination key %q: %w", key, err)
		}
		variant := &models.Variant{
			ArticleID:                 articleID,
			OptionValueIDs:            optionIDs,
			CombinationKey:            key,
			SalePriceCents:            article.DefaultSalePriceCents,
			DefaultPurchasePriceCents: article.DefaultPurchasePriceCents,
			IsOffered:                 true,
			IsActive:                  true,
		}
		if err := s.db.WithContext(ctx).Create(variant).Error; err != nil {
			return err
		}
	}

	// Deactivate whatever the configuration no longer implies. An article
	// whose options are incomplete has no valid combinations at all, so every
	// variant is parked rather than sold with a half-defined identity.
	query := s.db.WithContext(ctx).Model(&models.Variant{}).
		Where("article_id = ? AND is_active = ?", articleID, true)
	if len(expectedKeys) > 0 {
		query = query.Where("combination_key NOT IN ?", expectedKeys)
	}
	return query.Updates(map[string]any{"is_active": false, "updated_at": now}).Error
}

// PreserveVariantsForNewOptionGroups maps existing variants onto the first
// value of each newly added option dimension.
//
// Adding a dimension changes every combination key. Without this step the old
// variants would merely go inactive and take their stock, prices and photos
// out of the active catalogue — which is precisely the data a band cannot
// afford to lose mid-season. The first value is the explicit mapping target,
// so it can be reordered before or after saving without losing the records.
func (s *Service) PreserveVariantsForNewOptionGroups(ctx context.Context, articleID int64, firstValueIDs []int64) error {
	if len(firstValueIDs) == 0 {
		return nil
	}

	var variants []models.Variant
	err := s.db.WithContext(ctx).
		Where("article_id = ? AND is_active = ?", articleID, true).
		Find(&variants).Error
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, variant := range variants {
		present := make(map[int64]bool, len(variant.OptionValueIDs))
		for _, id := range variant.OptionValueIDs {
			present[id] = true
		}

		migrated := append(models.JSONInt64Slice{}, variant.OptionValueIDs...)
		for _, id := range firstValueIDs {
			if !present[id] {
				migrated = append(migrated, id)
			}
		}
		if len(migrated) == len(variant.OptionValueIDs) {
			continue
		}

		err := s.db.WithContext(ctx).Model(&models.Variant{}).
			Where("id = ?", variant.ID).
			Updates(map[string]any{
				"option_value_ids": migrated,
				"combination_key":  CombinationKey(migrated),
				"updated_at":       now,
			}).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateArticle adds an article together with the default option grid and its
// resulting variants, in one transaction.
func (s *Service) CreateArticle(ctx context.Context, name string, defaultSaleCents, defaultPurchaseCents int64) (*models.Article, error) {
	cleaned := strings.TrimSpace(name)
	if cleaned == "" || len(cleaned) > 200 {
		return nil, fmt.Errorf("%w: 1 to 200 characters required", ErrInvalidName)
	}
	if defaultSaleCents < 0 || defaultPurchaseCents < 0 {
		return nil, ErrNegativePrice
	}

	article := &models.Article{
		Name:                      cleaned,
		DefaultSalePriceCents:     defaultSaleCents,
		DefaultPurchasePriceCents: defaultPurchaseCents,
		IsOffered:                 true,
		IsActive:                  true,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txService := s.WithTx(tx)

		if err := tx.WithContext(ctx).Create(article).Error; err != nil {
			return err
		}

		for position, group := range DefaultNewArticleOptions {
			optionGroup := &models.OptionGroup{
				ArticleID: article.ID,
				Name:      group.Name,
				Position:  position,
				IsActive:  true,
			}
			if err := tx.WithContext(ctx).Create(optionGroup).Error; err != nil {
				return err
			}
			for valuePosition, value := range group.Values {
				optionValue := &models.OptionValue{
					OptionGroupID: optionGroup.ID,
					Value:         value,
					Position:      valuePosition,
					IsActive:      true,
				}
				if err := tx.WithContext(ctx).Create(optionValue).Error; err != nil {
					return err
				}
			}
		}

		return txService.SyncVariants(ctx, article.ID)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("%w: an article with this name already exists", ErrInvalidName)
		}
		return nil, err
	}
	return article, nil
}
