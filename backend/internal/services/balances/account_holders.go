package balances

import (
	"context"
	"sort"
	"strings"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// AccountHolderTotal compares settled band income and expenses by the account
// through which the money actually moved. A nil user ID is the band cash
// account. DifferenceCents is income minus expenses: positive values are a
// surplus, while a negative value for a person means they advanced private
// funds for the band.
type AccountHolderTotal struct {
	AccountHolderUserID   *int64 `json:"account_holder_user_id"`
	AccountHolderUsername string `json:"account_holder_username"`
	IncomeCents           int64  `json:"income_cents"`
	ExpenseCents          int64  `json:"expense_cents"`
	DifferenceCents       int64  `json:"difference_cents"`
}

func (s *Service) accountHolderTotalsPeriod(
	ctx context.Context,
	period Period,
) ([]AccountHolderTotal, error) {
	var bandTotals []AccountHolderTotal
	query := s.db.WithContext(ctx).Model(&models.BandTransaction{}).
		Joins(`LEFT JOIN users account_holder
			ON account_holder.id = band_transactions.account_holder_user_id
			AND account_holder.band_id = band_transactions.band_id`).
		Where("band_transactions.is_cancelled = ? AND band_transactions.is_settled = ?", false, true)
	query = period.apply(query, "band_transactions.transaction_on")
	if err := query.
		Select(`band_transactions.account_holder_user_id,
			COALESCE(MAX(account_holder.username), MAX(band_transactions.account_holder_username), '') AS account_holder_username,
			COALESCE(SUM(CASE WHEN band_transactions.transaction_type = 'income' THEN band_transactions.amount_cents ELSE 0 END), 0) AS income_cents,
			COALESCE(SUM(CASE WHEN band_transactions.transaction_type = 'expense' THEN band_transactions.amount_cents ELSE 0 END), 0) AS expense_cents`).
		Group("band_transactions.account_holder_user_id").
		Scan(&bandTotals).Error; err != nil {
		return nil, err
	}

	type holderKey struct {
		bandCash bool
		userID   int64
	}
	keyFor := func(userID *int64) holderKey {
		if userID == nil {
			return holderKey{bandCash: true}
		}
		return holderKey{userID: *userID}
	}
	merged := make(map[holderKey]AccountHolderTotal)
	merge := func(entry AccountHolderTotal) {
		key := keyFor(entry.AccountHolderUserID)
		current := merged[key]
		if current.AccountHolderUserID == nil && entry.AccountHolderUserID != nil {
			userID := *entry.AccountHolderUserID
			current.AccountHolderUserID = &userID
		}
		if current.AccountHolderUsername == "" {
			current.AccountHolderUsername = entry.AccountHolderUsername
		}
		current.IncomeCents += entry.IncomeCents
		current.ExpenseCents += entry.ExpenseCents
		merged[key] = current
	}
	for _, entry := range bandTotals {
		merge(entry)
	}

	// Sales do not have an account-holder selector: money collected at the
	// merch counter belongs to the shared band cash account. Only payments that
	// actually moved and were not cancelled count here, matching the
	// "collected" value in the main balance summary.
	type collectedSaleTotal struct {
		IncomeCents int64
	}
	var collectedSales collectedSaleTotal
	saleQuery := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("sales.is_cancelled = ? AND sales.is_paid = ?", false, true)
	saleQuery = period.apply(saleQuery, "sales.sold_on")
	if err := saleQuery.
		Select(`COALESCE(SUM(sales.amount_due_cents - sales.discount_cents + sales.donation_cents), 0) AS income_cents`).
		Scan(&collectedSales).Error; err != nil {
		return nil, err
	}
	if collectedSales.IncomeCents != 0 {
		merge(AccountHolderTotal{IncomeCents: collectedSales.IncomeCents})
	}

	// Purchase metadata is repeated on every receipt line. Grouping active
	// lines by receipt keeps shipping cent-exact and includes it exactly once,
	// including when only part of a receipt was cancelled.
	type purchaseReceiptTotal struct {
		AccountHolderUserID   *int64
		AccountHolderUsername string
		GoodsCents            int64
		ShippingCents         int64
	}
	var purchaseReceipts []purchaseReceiptTotal
	purchaseQuery := s.db.WithContext(ctx).Model(&models.Purchase{}).
		Joins(`LEFT JOIN users account_holder
			ON account_holder.id = purchases.account_holder_user_id
			AND account_holder.band_id = purchases.band_id`).
		Where("purchases.is_cancelled = ?", false)
	purchaseQuery = period.apply(purchaseQuery, "purchases.purchased_on")
	if err := purchaseQuery.
		Select(`MAX(purchases.account_holder_user_id) AS account_holder_user_id,
			COALESCE(MAX(account_holder.username), MAX(purchases.account_holder_username), '') AS account_holder_username,
			COALESCE(SUM(purchases.line_total_cost_cents), 0) AS goods_cents,
			COALESCE(MAX(purchases.shipping_cost_cents), 0) AS shipping_cents`).
		Group("purchases.receipt_id").
		Scan(&purchaseReceipts).Error; err != nil {
		return nil, err
	}
	for _, receipt := range purchaseReceipts {
		merge(AccountHolderTotal{
			AccountHolderUserID:   receipt.AccountHolderUserID,
			AccountHolderUsername: receipt.AccountHolderUsername,
			ExpenseCents:          receipt.GoodsCents + receipt.ShippingCents,
		})
	}

	totals := make([]AccountHolderTotal, 0, len(merged))
	for _, entry := range merged {
		totals = append(totals, entry)
	}
	for i := range totals {
		totals[i].DifferenceCents = totals[i].IncomeCents - totals[i].ExpenseCents
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].AccountHolderUserID == nil || totals[j].AccountHolderUserID == nil {
			return totals[i].AccountHolderUserID == nil && totals[j].AccountHolderUserID != nil
		}
		if totals[i].DifferenceCents != totals[j].DifferenceCents {
			// A larger private advance is now the more negative difference.
			return totals[i].DifferenceCents < totals[j].DifferenceCents
		}
		return strings.ToLower(totals[i].AccountHolderUsername) < strings.ToLower(totals[j].AccountHolderUsername)
	})
	if totals == nil {
		totals = []AccountHolderTotal{}
	}
	return totals, nil
}
