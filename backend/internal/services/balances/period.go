package balances

import (
	"context"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

// Period is the optional date range selected on the balances page.
//
// Flow values (sales, purchases and band transactions) are restricted to the
// range. Stock is different: it is always the cumulative position at To. When
// To is omitted it is the current stock position.
type Period struct {
	From *models.Date
	To   *models.Date
}

func (p Period) apply(query *gorm.DB, column string) *gorm.DB {
	if p.From != nil {
		query = query.Where(column+" >= ?", *p.From)
	}
	if p.To != nil {
		query = query.Where(column+" <= ?", *p.To)
	}
	return query
}

// ComputePeriod assembles the balances page for one optional period.
func (s *Service) ComputePeriod(ctx context.Context, period Period) (*Payload, error) {
	rows, err := s.variantRowsPeriod(ctx, period)
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

	summary, err := s.summaryPeriod(ctx, period, rows)
	if err != nil {
		return nil, err
	}
	payload.Summary = *summary

	if payload.TopSellingItems, payload.TopRevenueItems, err = s.itemRankingsPeriod(ctx, period); err != nil {
		return nil, err
	}
	if payload.TopEvents, err = s.groupRankingPeriod(ctx, period, "event_name"); err != nil {
		return nil, err
	}
	if payload.TopSellers, err = s.groupRankingPeriod(ctx, period, "sold_by"); err != nil {
		return nil, err
	}
	if payload.DailyIncome, err = s.dailyIncomePeriod(ctx, period); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Service) variantRowsPeriod(ctx context.Context, period Period) ([]Row, error) {
	type baseVariant struct {
		ID                        int64
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
	}
	var variants []baseVariant
	err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select(`variants.id, variants.article_id, articles.name AS article_name,
			variants.sale_price_cents, variants.default_purchase_price_cents,
			variants.minimum_stock, variants.is_offered, variants.no_reorder, variants.is_active,
			articles.is_offered AS article_is_offered, articles.is_active AS article_is_active`).
		Joins("JOIN articles ON articles.id = variants.article_id").
		Scan(&variants).Error
	if err != nil {
		return nil, err
	}

	stock, err := s.catalogue.StockMapAt(ctx, period.To)
	if err != nil {
		return nil, err
	}
	labels, err := s.catalogue.VariantLabels(ctx)
	if err != nil {
		return nil, err
	}

	type purchaseAggregate struct {
		VariantID int64
		CostCents int64
	}
	var purchaseRows []purchaseAggregate
	purchaseQuery := s.db.WithContext(ctx).Model(&models.Purchase{}).
		Where("is_cancelled = ?", false)
	purchaseQuery = period.apply(purchaseQuery, "purchased_on")
	if err := purchaseQuery.
		Select("variant_id, COALESCE(SUM(quantity * unit_cost_cents), 0) AS cost_cents").
		Group("variant_id").
		Scan(&purchaseRows).Error; err != nil {
		return nil, err
	}
	purchaseCost := make(map[int64]int64, len(purchaseRows))
	for _, row := range purchaseRows {
		purchaseCost[row.VariantID] = row.CostCents
	}

	type saleAggregate struct {
		VariantID      int64
		RevenueCents   int64
		CollectedCents int64
		DonationCents  int64
	}
	var saleRows []saleAggregate
	saleQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_cancelled = ?", false)
	saleQuery = period.apply(saleQuery, "sold_on")
	if err := saleQuery.
		Select(`variant_id,
			COALESCE(SUM(amount_due_cents), 0) AS revenue_cents,
			COALESCE(SUM(CASE WHEN is_paid = 1 THEN amount_due_cents ELSE 0 END), 0) AS collected_cents,
			COALESCE(SUM(CASE WHEN is_paid = 1 THEN donation_cents ELSE 0 END), 0) AS donation_cents`).
		Group("variant_id").
		Scan(&saleRows).Error; err != nil {
		return nil, err
	}
	sales := make(map[int64]saleAggregate, len(saleRows))
	for _, row := range saleRows {
		sales[row.VariantID] = row
	}

	rows := make([]Row, 0, len(variants))
	for _, variant := range variants {
		position := stock[variant.ID]
		used := position.Purchased > 0 || position.Sold > 0
		if !variant.IsActive && !used {
			continue
		}
		sale := sales[variant.ID]
		rows = append(rows, Row{
			VariantID:                 variant.ID,
			ArticleID:                 variant.ArticleID,
			ArticleName:               variant.ArticleName,
			VariantLabel:              labels[variant.ID].VariantLabel,
			Purchased:                 position.Purchased,
			Sold:                      position.Sold,
			OnHand:                    position.OnHand,
			MinimumStock:              variant.MinimumStock,
			BelowMinimum:              catalogue.IsAtOrBelowMinimum(position.OnHand, variant.MinimumStock),
			PurchaseCostCents:         purchaseCost[variant.ID],
			RevenueCents:              sale.RevenueCents,
			CollectedCents:            sale.CollectedCents,
			DonationCents:             sale.DonationCents,
			SalePriceCents:            variant.SalePriceCents,
			DefaultPurchasePriceCents: variant.DefaultPurchasePriceCents,
			IsOffered:                 variant.IsOffered,
			IsAvailableForSale:        variant.IsActive && variant.ArticleIsActive && variant.IsOffered && variant.ArticleIsOffered,
			NoReorder:                 variant.NoReorder,
			IsActive:                  variant.IsActive,
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

func (s *Service) summaryPeriod(ctx context.Context, period Period, rows []Row) (*Summary, error) {
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

	openQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_paid = ? AND is_cancelled = ?", false, false)
	openQuery = period.apply(openQuery, "sold_on")
	if err := openQuery.
		Select("COALESCE(SUM(amount_due_cents), 0)").
		Scan(&summary.OutstandingCents).Error; err != nil {
		return nil, err
	}

	deliveryQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_received = ? AND is_cancelled = ?", false, false)
	deliveryQuery = period.apply(deliveryQuery, "sold_on")
	if err := deliveryQuery.Count(&summary.PendingDeliveryCount).Error; err != nil {
		return nil, err
	}

	type bandTotals struct {
		Income  int64
		Expense int64
	}
	var totals bandTotals
	bandQuery := s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Where("is_cancelled = ? AND is_settled = ?", false, true)
	bandQuery = period.apply(bandQuery, "transaction_on")
	if err := bandQuery.
		Select(`COALESCE(SUM(CASE WHEN transaction_type = 'income' THEN amount_cents ELSE 0 END), 0) AS income,
			COALESCE(SUM(CASE WHEN transaction_type = 'expense' THEN amount_cents ELSE 0 END), 0) AS expense`).
		Scan(&totals).Error; err != nil {
		return nil, err
	}

	summary.BandIncomeCents = totals.Income
	summary.BandExpenseCents = totals.Expense
	summary.BandBalanceCents = totals.Income - totals.Expense
	summary.OverallBalanceCents = summary.CashBalanceCents + summary.BandBalanceCents
	return summary, nil
}

func (s *Service) costBasisAt(ctx context.Context, to *models.Date) (map[int64]int64, error) {
	type variantDefault struct {
		ID                        int64
		DefaultPurchasePriceCents int64
	}
	var defaults []variantDefault
	if err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select("id, default_purchase_price_cents").
		Scan(&defaults).Error; err != nil {
		return nil, err
	}
	basis := make(map[int64]int64, len(defaults))
	for _, row := range defaults {
		basis[row.ID] = row.DefaultPurchasePriceCents
	}

	type purchaseAggregate struct {
		VariantID int64
		Quantity  int64
		CostCents int64
	}
	var rows []purchaseAggregate
	query := s.db.WithContext(ctx).Model(&models.Purchase{}).
		Where("is_cancelled = ?", false)
	if to != nil {
		query = query.Where("purchased_on <= ?", *to)
	}
	if err := query.
		Select(`variant_id, COALESCE(SUM(quantity), 0) AS quantity,
			COALESCE(SUM(quantity * unit_cost_cents), 0) AS cost_cents`).
		Group("variant_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Quantity > 0 {
			basis[row.VariantID] = (row.CostCents + row.Quantity/2) / row.Quantity
		}
	}
	return basis, nil
}

func (s *Service) itemRankingsPeriod(ctx context.Context, period Period) (bySales, byRevenue []RankingEntry, err error) {
	type row struct {
		ArticleName string
		VariantID   int64
		Quantity    int64
		IncomeCents int64
	}
	var rows []row
	query := s.db.WithContext(ctx).Model(&models.Sale{}).
		Joins("JOIN variants ON variants.id = sales.variant_id").
		Joins("JOIN articles ON articles.id = variants.article_id").
		Where("sales.is_cancelled = ?", false)
	query = period.apply(query, "sales.sold_on")
	if err = query.
		Select(`articles.name AS article_name, sales.variant_id,
			SUM(sales.quantity) AS quantity,
			COALESCE(SUM(CASE WHEN sales.is_paid = 1
				THEN sales.amount_due_cents + sales.donation_cents ELSE 0 END), 0) AS income_cents`).
		Group("articles.name, sales.variant_id").
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}

	basis, err := s.costBasisAt(ctx, period.To)
	if err != nil {
		return nil, nil, err
	}
	byArticle := map[string]*RankingEntry{}
	for _, entry := range rows {
		aggregate := byArticle[entry.ArticleName]
		if aggregate == nil {
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

func (s *Service) groupRankingPeriod(ctx context.Context, period Period, column string) ([]RankingEntry, error) {
	type row struct {
		Label       string
		VariantID   int64
		Quantity    int64
		IncomeCents int64
	}
	var rows []row
	query := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_cancelled = ? AND "+column+" <> ''", false)
	query = period.apply(query, "sold_on")
	if err := query.
		Select(column + ` AS label, variant_id,
			SUM(quantity) AS quantity,
			COALESCE(SUM(CASE WHEN is_paid = 1
				THEN amount_due_cents + donation_cents ELSE 0 END), 0) AS income_cents`).
		Group(column + ", variant_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	basis, err := s.costBasisAt(ctx, period.To)
	if err != nil {
		return nil, err
	}
	grouped := map[string]*RankingEntry{}
	for _, entry := range rows {
		aggregate := grouped[entry.Label]
		if aggregate == nil {
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

func (s *Service) dailyIncomePeriod(ctx context.Context, period Period) ([]DailyIncome, error) {
	var points []DailyIncome
	query := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_cancelled = ? AND is_paid = ?", false, true)
	query = period.apply(query, "sold_on")
	if err := query.
		Select(`sold_on AS date,
			COALESCE(SUM(amount_due_cents + donation_cents), 0) AS income_cents,
			COUNT(*) AS sale_count`).
		Group("sold_on").
		Order("sold_on").
		Scan(&points).Error; err != nil {
		return nil, err
	}
	if points == nil {
		points = []DailyIncome{}
	}
	return points, nil
}
