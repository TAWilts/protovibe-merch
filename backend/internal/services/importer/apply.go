package importer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
)

// Preview is what the confirmation screen shows before anything is written.
type Preview struct {
	Kind Kind `json:"kind"`
	// RowCount, NewArticles and NewVariants tell the band how much the import
	// will change before they commit to it.
	RowCount        int      `json:"row_count"`
	NewArticles     []string `json:"new_articles"`
	NewOptionValues []string `json:"new_option_values"`
	NewVariants     int      `json:"new_variants"`
	TotalQuantity   int      `json:"total_quantity"`
	TotalCents      int64    `json:"total_cents"`
}

// Result is what an applied import produced.
type Result struct {
	ReceiptID  string `json:"receipt_id"`
	RowCount   int    `json:"row_count"`
	TotalCents int64  `json:"total_cents"`
}

// Actor is who ran the import.
type Actor struct {
	UserID   int64
	Username string
}

// Service applies transaction imports.
type Service struct {
	db        *gorm.DB
	catalogue *catalogue.Service
	receipts  *receipt.Service
}

// NewService builds the importer.
func NewService(database *gorm.DB) *Service {
	return &Service{
		db:        database,
		catalogue: catalogue.NewService(database),
		receipts:  receipt.NewService(database),
	}
}

// Preflight validates the parsed rows against the existing catalogue and
// reports what would change.
//
// It runs before any write so the band can see, and refuse, an import that
// would silently invent a hundred variants from a typo in an options column.
func (s *Service) Preflight(ctx context.Context, kind Kind, rows []Row) (*Preview, error) {
	preview := &Preview{
		Kind:            kind,
		RowCount:        len(rows),
		NewArticles:     []string{},
		NewOptionValues: []string{},
	}

	byArticle := groupByArticle(rows)
	for _, articleRows := range byArticle {
		articleName := articleRows[0].ArticleName

		article, err := s.findArticle(ctx, articleName)
		if err != nil {
			return nil, err
		}
		if article == nil {
			preview.NewArticles = append(preview.NewArticles, articleName)
		}

		activeGroups, err := s.activeGroups(ctx, article)
		if err != nil {
			return nil, err
		}

		fileGroups := map[string]bool{}
		for _, key := range articleRows[0].GroupKeys {
			fileGroups[key] = true
		}
		// An existing option column missing from the file would make every
		// imported row match a different variant than the band expects.
		for key, group := range activeGroups {
			if !fileGroups[key] {
				return nil, fmt.Errorf(
					"importer: %q is missing the existing option column %q", articleName, group.Name)
			}
		}

		combinations := 1
		for key := range fileGroups {
			values, err := s.activeValues(ctx, activeGroups[key])
			if err != nil {
				return nil, err
			}
			for _, row := range articleRows {
				for _, option := range row.Options {
					if option.GroupKey != key || values[option.ValueKey] {
						continue
					}
					values[option.ValueKey] = true
					preview.NewOptionValues = append(preview.NewOptionValues,
						articleName+" · "+option.GroupName+": "+option.Value)
				}
			}
			combinations *= len(values)
			if combinations > MaxVariantsPerArticle {
				return nil, fmt.Errorf(
					"importer: %q would produce more than %d variants", articleName, MaxVariantsPerArticle)
			}
		}
		preview.NewVariants += combinations
	}

	for _, row := range rows {
		preview.TotalQuantity += row.Quantity
		if row.PriceCents != nil {
			preview.TotalCents += int64(row.Quantity) * *row.PriceCents
		}
	}
	return preview, nil
}

// Apply commits the whole import as one transaction.
//
// Everything lands under a single receipt ID, so a mistaken import can be
// reversed as one unit rather than row by row.
func (s *Service) Apply(ctx context.Context, kind Kind, rows []Row, on models.Date, actor Actor) (*Result, error) {
	if _, err := s.Preflight(ctx, kind, rows); err != nil {
		return nil, err
	}

	var result *Result
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txImporter := &Service{db: tx, catalogue: s.catalogue.WithTx(tx), receipts: s.receipts.WithTx(tx)}

		variantIDs, err := txImporter.resolveVariants(ctx, rows)
		if err != nil {
			return err
		}

		prefix := receipt.PrefixPurchase
		if kind == KindSales {
			prefix = receipt.PrefixSale
		}
		receiptID, err := txImporter.receipts.Allocate(ctx, prefix, "", on, "")
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		var total int64

		for _, row := range rows {
			variantID := variantIDs[variantKey(row)]
			price, err := txImporter.resolvePrice(ctx, kind, variantID, row)
			if err != nil {
				return err
			}
			total += int64(row.Quantity) * price

			if kind == KindPurchases {
				purchase := &models.Purchase{
					ReceiptID: receiptID, VariantID: variantID,
					Quantity: row.Quantity, UnitCostCents: price,
					PurchasedOn: on, Supplier: row.Party,
					CreatedAt: now, UpdatedAt: now,
				}
				purchase.CreatedByUserID = &actor.UserID
				purchase.CreatedByUsername = actor.Username
				if err := tx.WithContext(ctx).Create(purchase).Error; err != nil {
					return err
				}
				continue
			}

			amountDue := int64(row.Quantity) * price
			sale := &models.Sale{
				ReceiptID: receiptID, VariantID: variantID,
				Quantity: row.Quantity, UnitPriceCents: price, AmountDueCents: amountDue,
				AmountGivenCents: &amountDue,
				PaymentMethod:    models.PaymentMethodOther,
				IsPaid:           true,
				IsReceived:       true,
				DeliveryStatus:   models.DeliveryNotApplicable,
				// The fifth column names the customer, which is the only
				// contact detail a backfilled sale carries.
				CustomerName: row.Party,
				SoldOn:       on,
				CreatedAt:    now,
			}
			sale.CreatedByUserID = &actor.UserID
			sale.CreatedByUsername = actor.Username
			if err := tx.WithContext(ctx).Create(sale).Error; err != nil {
				return err
			}
		}

		result = &Result{ReceiptID: receiptID, RowCount: len(rows), TotalCents: total}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// resolveVariants creates whatever the file needs and returns the variant ID
// for every distinct row combination.
func (s *Service) resolveVariants(ctx context.Context, rows []Row) (map[string]int64, error) {
	resolved := map[string]int64{}

	for _, articleRows := range groupByArticle(rows) {
		articleName := articleRows[0].ArticleName

		article, err := s.findArticle(ctx, articleName)
		if err != nil {
			return nil, err
		}
		if article == nil {
			article = &models.Article{Name: articleName, IsActive: true, IsOffered: true}
			if err := s.db.WithContext(ctx).Create(article).Error; err != nil {
				return nil, err
			}
		}

		valueIDs, err := s.ensureOptions(ctx, article.ID, articleRows)
		if err != nil {
			return nil, err
		}
		if err := s.catalogue.SyncVariants(ctx, article.ID); err != nil {
			return nil, err
		}

		var variants []models.Variant
		if err := s.db.WithContext(ctx).Where("article_id = ?", article.ID).Find(&variants).Error; err != nil {
			return nil, err
		}
		byKey := map[string]int64{}
		for _, variant := range variants {
			byKey[variant.CombinationKey] = variant.ID
		}

		for _, row := range articleRows {
			ids := make([]int64, 0, len(row.Options))
			for _, option := range row.Options {
				ids = append(ids, valueIDs[option.GroupKey+"\x00"+option.ValueKey])
			}
			key := catalogue.CombinationKey(ids)
			variantID, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf(
					"importer: line %d: the variant for %q could not be created", row.LineNumber, articleName)
			}
			resolved[variantKey(row)] = variantID
		}
	}
	return resolved, nil
}

// ensureOptions creates the missing option columns and values, returning the
// ID of every value the file references.
func (s *Service) ensureOptions(ctx context.Context, articleID int64, rows []Row) (map[string]int64, error) {
	ids := map[string]int64{}

	var groups []models.OptionGroup
	if err := s.db.WithContext(ctx).
		Where("article_id = ? AND is_active = ?", articleID, true).
		Order("position, id").Find(&groups).Error; err != nil {
		return nil, err
	}
	groupsByKey := map[string]models.OptionGroup{}
	for _, group := range groups {
		groupsByKey[strings.ToLower(group.Name)] = group
	}

	position := len(groups)
	for _, option := range rows[0].Options {
		group, exists := groupsByKey[option.GroupKey]
		if !exists {
			group = models.OptionGroup{
				ArticleID: articleID, Name: option.GroupName, Position: position, IsActive: true,
			}
			if err := s.db.WithContext(ctx).Create(&group).Error; err != nil {
				return nil, err
			}
			groupsByKey[option.GroupKey] = group
			position++
		}

		var values []models.OptionValue
		if err := s.db.WithContext(ctx).
			Where("option_group_id = ?", group.ID).Find(&values).Error; err != nil {
			return nil, err
		}
		valuesByKey := map[string]models.OptionValue{}
		for _, value := range values {
			valuesByKey[strings.ToLower(value.Value)] = value
		}

		valuePosition := len(values)
		for _, row := range rows {
			for _, rowOption := range row.Options {
				if rowOption.GroupKey != option.GroupKey {
					continue
				}
				value, seen := valuesByKey[rowOption.ValueKey]
				if !seen {
					value = models.OptionValue{
						OptionGroupID: group.ID, Value: rowOption.Value,
						Position: valuePosition, IsActive: true,
					}
					if err := s.db.WithContext(ctx).Create(&value).Error; err != nil {
						return nil, err
					}
					valuesByKey[rowOption.ValueKey] = value
					valuePosition++
				} else if !value.IsActive {
					// A value that was retired earlier comes back rather than
					// being duplicated.
					if err := s.db.WithContext(ctx).Model(&models.OptionValue{}).
						Where("id = ?", value.ID).Update("is_active", true).Error; err != nil {
						return nil, err
					}
				}
				ids[rowOption.GroupKey+"\x00"+rowOption.ValueKey] = value.ID
			}
		}
	}
	return ids, nil
}

// resolvePrice falls back to the catalogue when the price column was blank.
func (s *Service) resolvePrice(ctx context.Context, kind Kind, variantID int64, row Row) (int64, error) {
	if row.PriceCents != nil {
		return *row.PriceCents, nil
	}

	var variant models.Variant
	if err := s.db.WithContext(ctx).First(&variant, variantID).Error; err != nil {
		return 0, err
	}
	if kind == KindSales {
		return variant.SalePriceCents, nil
	}
	return variant.DefaultPurchasePriceCents, nil
}

func (s *Service) findArticle(ctx context.Context, name string) (*models.Article, error) {
	var article models.Article
	// The collation is case-insensitive, so this matches "shirt" and "Shirt".
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&article).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &article, nil
}

func (s *Service) activeGroups(ctx context.Context, article *models.Article) (map[string]models.OptionGroup, error) {
	groups := map[string]models.OptionGroup{}
	if article == nil {
		return groups, nil
	}

	var rows []models.OptionGroup
	err := s.db.WithContext(ctx).
		Where("article_id = ? AND is_active = ?", article.ID, true).
		Order("position, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, group := range rows {
		key := strings.ToLower(group.Name)
		if _, duplicate := groups[key]; duplicate {
			return nil, fmt.Errorf(
				"importer: %q has duplicate option columns and cannot be imported safely", article.Name)
		}
		groups[key] = group
	}
	return groups, nil
}

func (s *Service) activeValues(ctx context.Context, group models.OptionGroup) (map[string]bool, error) {
	values := map[string]bool{}
	if group.ID == 0 {
		return values, nil
	}

	var rows []models.OptionValue
	err := s.db.WithContext(ctx).
		Where("option_group_id = ? AND is_active = ?", group.ID, true).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, value := range rows {
		values[strings.ToLower(value.Value)] = true
	}
	return values, nil
}

func groupByArticle(rows []Row) map[string][]Row {
	grouped := map[string][]Row{}
	for _, row := range rows {
		key := strings.ToLower(row.ArticleName)
		grouped[key] = append(grouped[key], row)
	}
	return grouped
}

// variantKey identifies the article-plus-options combination of one row.
func variantKey(row Row) string {
	parts := make([]string, 0, len(row.Options)+1)
	parts = append(parts, strings.ToLower(row.ArticleName))
	for _, option := range row.Options {
		parts = append(parts, option.GroupKey+"="+option.ValueKey)
	}
	sortStrings(parts[1:])
	return strings.Join(parts, "\x00")
}
