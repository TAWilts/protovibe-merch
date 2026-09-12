package balances

import (
	"context"
	"sort"
	"strings"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// AccountHolderTotal compares settled band income and expenses by the account
// through which the money actually moved. A nil user ID is the band cash
// account. DifferenceCents is expenses minus income, so a positive value for a
// person means they advanced private funds for the band.
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
	var totals []AccountHolderTotal
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
		Scan(&totals).Error; err != nil {
		return nil, err
	}

	for i := range totals {
		totals[i].DifferenceCents = totals[i].ExpenseCents - totals[i].IncomeCents
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].AccountHolderUserID == nil || totals[j].AccountHolderUserID == nil {
			return totals[i].AccountHolderUserID == nil && totals[j].AccountHolderUserID != nil
		}
		if totals[i].DifferenceCents != totals[j].DifferenceCents {
			return totals[i].DifferenceCents > totals[j].DifferenceCents
		}
		return strings.ToLower(totals[i].AccountHolderUsername) < strings.ToLower(totals[j].AccountHolderUsername)
	})
	if totals == nil {
		totals = []AccountHolderTotal{}
	}
	return totals, nil
}
