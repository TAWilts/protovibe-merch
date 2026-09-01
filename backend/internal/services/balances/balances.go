// Package balances produces the stock and money overview.
//
// Two conventions carry over from the original and shape everything here:
//
//   - "Saldo" means collected payments plus donations minus recorded goods
//     received. It is deliberately not called profit, because a reorder would
//     make a profit figure swing wildly for a week.
//   - Cancelled sales disappear from every total, exactly as they already do
//     from stock. A cancellation must not linger in the numbers.
package balances

import (
	"context"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

// Row is one variant's balance line.
type Row struct {
	VariantID    int64  `json:"variant_id"`
	ArticleID    int64  `json:"article_id"`
	ArticleName  string `json:"article_name"`
	VariantLabel string `json:"variant_label"`

	Purchased int64 `json:"purchased"`
	Sold      int64 `json:"sold"`
	OnHand    int64 `json:"on_hand"`

	MinimumStock *int `json:"minimum_stock"`
	BelowMinimum bool `json:"below_minimum"`

	PurchaseCostCents         int64 `json:"purchase_cost_cents"`
	RevenueCents              int64 `json:"revenue_cents"`
	CollectedCents            int64 `json:"collected_cents"`
	DonationCents             int64 `json:"donation_cents"`
	SalePriceCents            int64 `json:"sale_price_cents"`
	DefaultPurchasePriceCents int64 `json:"default_purchase_price_cents"`

	IsOffered bool `json:"is_offered"`
	// IsAvailableForSale also includes the article-level flags. A variant may
	// still be marked offered while its whole article is withdrawn.
	IsAvailableForSale bool `json:"is_available_for_sale"`
	NoReorder          bool `json:"no_reorder"`
	IsActive           bool `json:"is_active"`
}

// Summary is the headline metric row.
type Summary struct {
	PurchaseCostCents int64 `json:"purchase_cost_cents"`
	RevenueCents      int64 `json:"revenue_cents"`
	CollectedCents    int64 `json:"collected_cents"`
	DonationCents     int64 `json:"donation_cents"`
	// CashBalanceCents is collected + donations − goods received.
	CashBalanceCents int64 `json:"cash_balance_cents"`
	// OutstandingCents is what customers still owe.
	OutstandingCents     int64 `json:"outstanding_cents"`
	PendingDeliveryCount int64 `json:"pending_delivery_count"`
	StockCount           int64 `json:"stock_count"`
	MinimumStockWarnings int64 `json:"minimum_stock_warning_count"`
	BandIncomeCents      int64 `json:"band_income_cents"`
	BandExpenseCents     int64 `json:"band_expense_cents"`
	BandBalanceCents     int64 `json:"band_balance_cents"`
	// OverallBalanceCents adds the band's own ledger to the merch balance.
	OverallBalanceCents int64 `json:"overall_balance_cents"`
}

// RankingEntry is one line of a top-five list.
type RankingEntry struct {
	Label       string `json:"label"`
	Quantity    int64  `json:"quantity"`
	IncomeCents int64  `json:"income_cents"`
	ProfitCents int64  `json:"profit_cents"`
}

// DailyIncome is one point of the income chart.
type DailyIncome struct {
	Date        models.Date `json:"date"`
	IncomeCents int64       `json:"income_cents"`
	SaleCount   int64       `json:"sale_count"`
}

// Payload is the whole balances page.
type Payload struct {
	Summary Summary `json:"summary"`
	// ReorderRows and ObsoleteRows split the table the way the original does:
	// variants still worth restocking, and those explicitly retired.
	ReorderRows  []Row `json:"reorder_rows"`
	ObsoleteRows []Row `json:"obsolete_rows"`

	TopSellingItems []RankingEntry `json:"top_selling_items"`
	TopRevenueItems []RankingEntry `json:"top_revenue_items"`
	TopEvents       []RankingEntry `json:"top_events"`
	TopSellers      []RankingEntry `json:"top_sellers"`
	DailyIncome     []DailyIncome  `json:"daily_income"`
}

// Service computes the balances payload.
type Service struct {
	db        *gorm.DB
	catalogue *catalogue.Service
}

// NewService builds the balances service.
func NewService(database *gorm.DB) *Service {
	return &Service{db: database, catalogue: catalogue.NewService(database)}
}

// Compute assembles the full payload for the scoped band.
func (s *Service) Compute(ctx context.Context) (*Payload, error) {
	rows, err := s.variantRows(ctx)
	if err != nil {
		return nil, err
	}

	payload := &Payload{ReorderRows: []Row{}, ObsoleteRows: []Row{}}
	for _, row := range rows {
		if row.NoReorder {
			payload.ObsoleteRows = append(payload.ObsoleteRows, row)
		} else {
			payload.ReorderRows = append(payload.ReorderRows, row)
		}
	}

	summary, err := s.summary(ctx, rows)
	if err != nil {
		return nil, err
	}
	payload.Summary = *summary

	if payload.TopSellingItems, payload.TopRevenueItems, err = s.itemRankings(ctx); err != nil {
		return nil, err
	}
	if payload.TopEvents, err = s.groupRanking(ctx, "event_name"); err != nil {
		return nil, err
	}
	if payload.TopSellers, err = s.groupRanking(ctx, "sold_by"); err != nil {
		return nil, err
	}
	if payload.DailyIncome, err = s.dailyIncome(ctx); err != nil {
		return nil, err
	}
	return payload, nil
}

// variantRows builds one balance line per variant that has any history or is
// still active. A retired variant that was never used is left out entirely, so
// the table does not fill up with noise.
func (s *Service) variantRows(ctx context.Context) ([]Row, error) {
	type aggregate struct {
		VariantID                 int64
		ArticleID                 int64
		ArticleName               string
		SalePriceCents            int64
		DefaultPurchasePriceCents int64
		MinimumStock              *int
		IsOffered                 bool
		ArticleIsOffered          bool
		ArticleIsActive           bool
		NoReorder                 bool
		IsActive                  bool
		PurchaseCostCents         int64
		RevenueCents              int64
		CollectedCents            int64
		DonationCents             int64
	}

	var aggregates []aggregate
	err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select(`variants.id AS variant_id, variants.article_id, articles.name AS article_name,
			variants.sale_price_cents, variants.default_purchase_price_cents,
			variants.minimum_stock, variants.is_offered, variants.no_reorder, variants.is_active,
			articles.is_offered AS article_is_offered, articles.is_active AS article_is_active,
			COALESCE((SELECT SUM(p.quantity * p.unit_cost_cents) FROM purchases p
				WHERE p.variant_id = variants.id), 0) AS purchase_cost_cents,
			COALESCE((SELECT SUM(s.amount_due_cents) FROM sales s
				WHERE s.variant_id = variants.id AND s.is_cancelled = 0), 0) AS revenue_cents,
			COALESCE((SELECT SUM(s.amount_due_cents) FROM sales s
				WHERE s.variant_id = variants.id AND s.is_cancelled = 0 AND s.is_paid = 1), 0) AS collected_cents,
			COALESCE((SELECT SUM(s.donation_cents) FROM sales s
				WHERE s.variant_id = variants.id AND s.is_cancelled = 0 AND s.is_paid = 1), 0) AS donation_cents`).
		Joins("JOIN articles ON articles.id = variants.article_id").
		Scan(&aggregates).Error
	if err != nil {
		return nil, err
	}

	stock, err := s.catalogue.StockMap(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := s.catalogue.VariantLabels(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]Row, 0, len(aggregates))
	for _, entry := range aggregates {
		position := stock[entry.VariantID]
		used := position.Purchased > 0 || position.Sold > 0
		if !entry.IsActive && !used {
			continue
		}

		rows = append(rows, Row{
			VariantID:                 entry.VariantID,
			ArticleID:                 entry.ArticleID,
			ArticleName:               entry.ArticleName,
			VariantLabel:              labels[entry.VariantID].VariantLabel,
			Purchased:                 position.Purchased,
			Sold:                      position.Sold,
			OnHand:                    position.OnHand,
			MinimumStock:              entry.MinimumStock,
			BelowMinimum:              catalogue.IsAtOrBelowMinimum(position.OnHand, entry.MinimumStock),
			PurchaseCostCents:         entry.PurchaseCostCents,
			RevenueCents:              entry.RevenueCents,
			CollectedCents:            entry.CollectedCents,
			DonationCents:             entry.DonationCents,
			SalePriceCents:            entry.SalePriceCents,
			DefaultPurchasePriceCents: entry.DefaultPurchasePriceCents,
			IsOffered:                 entry.IsOffered,
			IsAvailableForSale:        entry.IsActive && entry.ArticleIsActive && entry.IsOffered && entry.ArticleIsOffered,
			NoReorder:                 entry.NoReorder,
			IsActive:                  entry.IsActive,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		leftName, rightName := strings.ToLower(rows[i].ArticleName), strings.ToLower(rows[j].ArticleName)
		if leftName != rightName {
			return leftName < rightName
		}
		left, right := labels[rows[i].VariantID].OptionPositions, labels[rows[j].VariantID].OptionPositions
		for position := 0; position < len(left) && position < len(right); position++ {
			if left[position] != right[position] {
				return left[position] < right[position]
			}
		}
		if len(left) != len(right) {
			return len(left) < len(right)
		}
		return rows[i].VariantID < rows[j].VariantID
	})
	return rows, nil
}

// summary totals the rows and adds the band's own ledger.
func (s *Service) summary(ctx context.Context, rows []Row) (*Summary, error) {
	summary := &Summary{}
	for _, row := range rows {
		summary.PurchaseCostCents += row.PurchaseCostCents
		summary.RevenueCents += row.RevenueCents
		summary.CollectedCents += row.CollectedCents
		summary.DonationCents += row.DonationCents
		summary.StockCount += row.OnHand
		if row.BelowMinimum {
			summary.MinimumStockWarnings++
		}
	}
	summary.CashBalanceCents = summary.CollectedCents + summary.DonationCents - summary.PurchaseCostCents

	err := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_paid = ? AND is_cancelled = ?", false, false).
		Select("COALESCE(SUM(amount_due_cents), 0)").Scan(&summary.OutstandingCents).Error
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_received = ? AND is_cancelled = ?", false, false).
		Count(&summary.PendingDeliveryCount).Error
	if err != nil {
		return nil, err
	}

	type bandTotals struct {
		Income  int64
		Expense int64
	}
	var totals bandTotals
	err = s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Where("is_cancelled = ?", false).
		Select(`COALESCE(SUM(CASE WHEN transaction_type = 'income' THEN amount_cents ELSE 0 END), 0) AS income,
			COALESCE(SUM(CASE WHEN transaction_type = 'expense' THEN amount_cents ELSE 0 END), 0) AS expense`).
		Scan(&totals).Error
	if err != nil {
		return nil, err
	}

	summary.BandIncomeCents = totals.Income
	summary.BandExpenseCents = totals.Expense
	summary.BandBalanceCents = totals.Income - totals.Expense
	// The two ledgers stay separate but are added up for the headline figure.
	summary.OverallBalanceCents = summary.CashBalanceCents + summary.BandBalanceCents
	return summary, nil
}

// costBasis is the weighted average purchase price per variant.
//
// A variant that was never bought falls back to its maintained standard
// purchase price, which keeps pre-order rankings useful without pretending the
// current stock is consumed in strict FIFO order.
func (s *Service) costBasis(ctx context.Context) (map[int64]int64, error) {
	type row struct {
		VariantID     int64
		UnitCostCents int64
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select(`variants.id AS variant_id,
			CASE WHEN COALESCE((SELECT SUM(p.quantity) FROM purchases p WHERE p.variant_id = variants.id), 0) > 0
				-- The division yields a DECIMAL; money stays integer cents, so
				-- it is rounded here rather than silently truncated later.
				THEN CAST(ROUND(
					(SELECT SUM(p.quantity * p.unit_cost_cents) FROM purchases p WHERE p.variant_id = variants.id)
					/ (SELECT SUM(p.quantity) FROM purchases p WHERE p.variant_id = variants.id)
				) AS SIGNED)
				ELSE variants.default_purchase_price_cents
			END AS unit_cost_cents`).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	basis := make(map[int64]int64, len(rows))
	for _, entry := range rows {
		basis[entry.VariantID] = entry.UnitCostCents
	}
	return basis, nil
}

// itemRankings returns the best-selling and the highest-earning articles.
func (s *Service) itemRankings(ctx context.Context) (bySales, byRevenue []RankingEntry, err error) {
	type row struct {
		ArticleName string
		VariantID   int64
		Quantity    int64
		IncomeCents int64
	}

	var rows []row
	err = s.db.WithContext(ctx).Model(&models.Sale{}).
		Select(`articles.name AS article_name, sales.variant_id,
			SUM(sales.quantity) AS quantity,
			COALESCE(SUM(CASE WHEN sales.is_paid = 1
				THEN sales.amount_due_cents + sales.donation_cents ELSE 0 END), 0) AS income_cents`).
		Joins("JOIN variants ON variants.id = sales.variant_id").
		Joins("JOIN articles ON articles.id = variants.article_id").
		Where("sales.is_cancelled = ?", false).
		Group("articles.name, sales.variant_id").
		Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}

	basis, err := s.costBasis(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Variants are folded into their article, which is how the band thinks
	// about "which shirt sells".
	byArticle := map[string]*RankingEntry{}
	for _, entry := range rows {
		aggregate, seen := byArticle[entry.ArticleName]
		if !seen {
			aggregate = &RankingEntry{Label: entry.ArticleName}
			byArticle[entry.ArticleName] = aggregate
		}
		aggregate.Quantity += entry.Quantity
		aggregate.IncomeCents += entry.IncomeCents
		aggregate.ProfitCents += entry.IncomeCents - entry.Quantity*basis[entry.VariantID]
	}

	entries := make([]RankingEntry, 0, len(byArticle))
	for _, entry := range byArticle {
		entries = append(entries, *entry)
	}

	bySales = topFive(entries, func(a, b RankingEntry) bool {
		if a.Quantity != b.Quantity {
			return a.Quantity > b.Quantity
		}
		return a.IncomeCents > b.IncomeCents
	})
	byRevenue = topFive(entries, func(a, b RankingEntry) bool {
		if a.IncomeCents != b.IncomeCents {
			return a.IncomeCents > b.IncomeCents
		}
		return a.Quantity > b.Quantity
	})
	return bySales, byRevenue, nil
}

// groupRanking ranks by an immutable snapshot column such as event_name or
// sold_by, which is why these still work after a rename or a deleted account.
func (s *Service) groupRanking(ctx context.Context, column string) ([]RankingEntry, error) {
	type row struct {
		Label       string
		VariantID   int64
		Quantity    int64
		IncomeCents int64
	}

	var rows []row
	err := s.db.WithContext(ctx).Model(&models.Sale{}).
		Select(column+` AS label, variant_id,
			SUM(quantity) AS quantity,
			COALESCE(SUM(CASE WHEN is_paid = 1
				THEN amount_due_cents + donation_cents ELSE 0 END), 0) AS income_cents`).
		Where("is_cancelled = ? AND "+column+" <> ''", false).
		Group(column + ", variant_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	basis, err := s.costBasis(ctx)
	if err != nil {
		return nil, err
	}

	grouped := map[string]*RankingEntry{}
	for _, entry := range rows {
		aggregate, seen := grouped[entry.Label]
		if !seen {
			aggregate = &RankingEntry{Label: entry.Label}
			grouped[entry.Label] = aggregate
		}
		aggregate.Quantity += entry.Quantity
		aggregate.IncomeCents += entry.IncomeCents
		aggregate.ProfitCents += entry.IncomeCents - entry.Quantity*basis[entry.VariantID]
	}

	entries := make([]RankingEntry, 0, len(grouped))
	for _, entry := range grouped {
		entries = append(entries, *entry)
	}
	return topFive(entries, func(a, b RankingEntry) bool {
		if a.IncomeCents != b.IncomeCents {
			return a.IncomeCents > b.IncomeCents
		}
		return a.Quantity > b.Quantity
	}), nil
}

// dailyIncome feeds the income chart: paid sales plus donations per sale date.
func (s *Service) dailyIncome(ctx context.Context) ([]DailyIncome, error) {
	var points []DailyIncome
	err := s.db.WithContext(ctx).Model(&models.Sale{}).
		Select(`sold_on AS date,
			COALESCE(SUM(amount_due_cents + donation_cents), 0) AS income_cents,
			COUNT(*) AS sale_count`).
		Where("is_cancelled = ? AND is_paid = ?", false, true).
		Group("sold_on").
		Order("sold_on").
		Scan(&points).Error
	if err != nil {
		return nil, err
	}
	if points == nil {
		points = []DailyIncome{}
	}
	return points, nil
}

// topFive sorts a copy and keeps the leading entries, matching the original's
// five-item ranking cards.
func topFive(entries []RankingEntry, less func(a, b RankingEntry) bool) []RankingEntry {
	sorted := make([]RankingEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })

	if len(sorted) > 5 {
		sorted = sorted[:5]
	}
	if sorted == nil {
		sorted = []RankingEntry{}
	}
	return sorted
}
