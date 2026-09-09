package balances

import (
	"context"
	"sort"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// FinanceReport is the auditable source data for the printable finance report.
// It deliberately does not calculate tax/VAT because tax rates are not yet
// modelled in Merch Manager.
type FinanceReport struct {
	BandName    string               `json:"band_name"`
	From        string               `json:"from"`
	To          string               `json:"to"`
	StockAsOf   string               `json:"stock_as_of"`
	GeneratedAt time.Time            `json:"generated_at"`
	Summary     FinanceReportSummary `json:"summary"`

	PaymentMethods []FinancePaymentMethod `json:"payment_methods"`
	Categories     []FinanceCategory      `json:"categories"`
	Inventory      []FinanceInventoryRow  `json:"inventory"`
	Assets         []FinanceAssetRow      `json:"assets"`
}

type FinanceReportSummary struct {
	MerchRevenueCents        int64 `json:"merch_revenue_cents"`
	MiscIncomeCents          int64 `json:"misc_income_cents"`
	TotalRevenueCents        int64 `json:"total_revenue_cents"`
	MerchCollectedCents      int64 `json:"merch_collected_cents"`
	DiscountCents            int64 `json:"discount_cents"`
	DonationCents            int64 `json:"donation_cents"`
	MerchPurchaseCostCents   int64 `json:"merch_purchase_cost_cents"`
	BandIncomeCents          int64 `json:"band_income_cents"`
	BandExpenseCents         int64 `json:"band_expense_cents"`
	BandOpenIncomeCents      int64 `json:"band_open_income_cents"`
	BandOpenExpenseCents     int64 `json:"band_open_expense_cents"`
	OutstandingCustomerCents int64 `json:"outstanding_customer_cents"`
	CashResultCents          int64 `json:"cash_result_cents"`
	StockValueCents          int64 `json:"stock_value_cents"`
	AssetAcquisitionCents    int64 `json:"asset_acquisition_cents"`
}

type FinancePaymentMethod struct {
	PaymentMethod  string `json:"payment_method"`
	ReceiptCount   int64  `json:"receipt_count"`
	BookedCents    int64  `json:"booked_cents"`
	CollectedCents int64  `json:"collected_cents"`
}

type FinanceCategory struct {
	TransactionType models.BandTransactionType `json:"transaction_type"`
	Category        string                     `json:"category"`
	AmountCents     int64                      `json:"amount_cents"`
}

type FinanceInventoryRow struct {
	ArticleName   string `json:"article_name"`
	VariantLabel  string `json:"variant_label"`
	OnHand        int64  `json:"on_hand"`
	UnitCostCents int64  `json:"unit_cost_cents"`
	ValueCents    int64  `json:"value_cents"`
}

type FinanceAssetRow struct {
	Date        models.Date `json:"date"`
	Category    string      `json:"category"`
	Description string      `json:"description"`
	AmountCents int64       `json:"amount_cents"`
}

// FinanceReport builds the detailed report source for a selected period.
func (s *Service) FinanceReport(ctx context.Context, period Period) (*FinanceReport, error) {
	payload, err := s.ComputePeriod(ctx, period)
	if err != nil {
		return nil, err
	}

	report := &FinanceReport{
		GeneratedAt:    time.Now().UTC(),
		PaymentMethods: []FinancePaymentMethod{},
		Categories:     []FinanceCategory{},
		Inventory:      []FinanceInventoryRow{},
		Assets:         []FinanceAssetRow{},
	}
	if period.From != nil {
		report.From = period.From.String()
	}
	if period.To != nil {
		report.To = period.To.String()
		report.StockAsOf = period.To.String()
	} else {
		now := time.Now().UTC()
		report.StockAsOf = models.NewDate(now.Year(), now.Month(), now.Day()).String()
	}

	if bandID, err := tenant.BandID(ctx); err == nil {
		var band models.Band
		if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).First(&band, bandID).Error; err == nil {
			report.BandName = band.Name
		}
	}

	report.Summary.MerchRevenueCents = payload.Summary.RevenueCents - payload.Summary.MiscIncomeCents
	report.Summary.MiscIncomeCents = payload.Summary.MiscIncomeCents
	report.Summary.TotalRevenueCents = payload.Summary.RevenueCents
	report.Summary.MerchCollectedCents = payload.Summary.CollectedCents
	report.Summary.DiscountCents = payload.Summary.DiscountCents
	report.Summary.DonationCents = payload.Summary.DonationCents
	report.Summary.MerchPurchaseCostCents = payload.Summary.PurchaseCostCents
	report.Summary.BandIncomeCents = payload.Summary.BandIncomeCents
	report.Summary.BandExpenseCents = payload.Summary.BandExpenseCents
	report.Summary.OutstandingCustomerCents = payload.Summary.OutstandingCents
	report.Summary.CashResultCents = payload.Summary.OverallBalanceCents

	type openBandTotals struct {
		Income  int64
		Expense int64
	}
	var open openBandTotals
	openQuery := s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Where("is_cancelled = ? AND is_settled = ?", false, false)
	openQuery = period.apply(openQuery, "transaction_on")
	if err := openQuery.
		Select(`COALESCE(SUM(CASE WHEN transaction_type = 'income' THEN amount_cents ELSE 0 END), 0) AS income,
			COALESCE(SUM(CASE WHEN transaction_type = 'expense' THEN amount_cents ELSE 0 END), 0) AS expense`).
		Scan(&open).Error; err != nil {
		return nil, err
	}
	report.Summary.BandOpenIncomeCents = open.Income
	report.Summary.BandOpenExpenseCents = open.Expense

	paymentQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("is_cancelled = ?", false)
	paymentQuery = period.apply(paymentQuery, "sold_on")
	if err := paymentQuery.
		Select(`payment_method,
			COUNT(DISTINCT receipt_id) AS receipt_count,
			COALESCE(SUM(amount_due_cents - discount_cents + donation_cents), 0) AS booked_cents,
			COALESCE(SUM(CASE WHEN is_paid = 1 THEN amount_due_cents - discount_cents + donation_cents ELSE 0 END), 0) AS collected_cents`).
		Group("payment_method").
		Order("payment_method").
		Scan(&report.PaymentMethods).Error; err != nil {
		return nil, err
	}

	categoryQuery := s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Where("is_cancelled = ? AND is_settled = ?", false, true)
	categoryQuery = period.apply(categoryQuery, "transaction_on")
	if err := categoryQuery.
		Select("transaction_type, category, COALESCE(SUM(amount_cents), 0) AS amount_cents").
		Group("transaction_type, category").
		Order("transaction_type, category").
		Scan(&report.Categories).Error; err != nil {
		return nil, err
	}

	basis, err := s.costBasisAt(ctx, period.To)
	if err != nil {
		return nil, err
	}
	allRows := append(append([]Row{}, payload.ReorderRows...), payload.ObsoleteRows...)
	for _, row := range allRows {
		if row.OnHand == 0 {
			continue
		}
		unitCost := basis[row.VariantID]
		value := row.OnHand * unitCost
		report.Inventory = append(report.Inventory, FinanceInventoryRow{
			ArticleName:   row.ArticleName,
			VariantLabel:  row.VariantLabel,
			OnHand:        row.OnHand,
			UnitCostCents: unitCost,
			ValueCents:    value,
		})
		report.Summary.StockValueCents += value
	}
	sort.SliceStable(report.Inventory, func(i, j int) bool {
		if report.Inventory[i].ArticleName != report.Inventory[j].ArticleName {
			return report.Inventory[i].ArticleName < report.Inventory[j].ArticleName
		}
		return report.Inventory[i].VariantLabel < report.Inventory[j].VariantLabel
	})

	assetQuery := s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Where("transaction_type = ? AND is_asset = ? AND is_cancelled = ? AND is_settled = ?",
			models.BandExpense, true, false, true)
	assetQuery = period.apply(assetQuery, "transaction_on")
	if err := assetQuery.
		Select("transaction_on AS date, category, description, amount_cents").
		Order("transaction_on, id").
		Scan(&report.Assets).Error; err != nil {
		return nil, err
	}
	for _, row := range report.Assets {
		report.Summary.AssetAcquisitionCents += row.AmountCents
	}
	return report, nil
}
