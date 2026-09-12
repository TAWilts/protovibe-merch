// Package bandfinance is the band's own ledger for gigs, royalties and
// equipment.
//
// It is deliberately separate from the merch books: booking a gig fee must
// never change a historic merch balance, and a merch reorder must never look
// like a band expense. The balances page adds the two up for one headline
// figure without merging them.
package bandfinance

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Errors returned by the ledger.
var (
	ErrNotFound             = errors.New("bandfinance: no such entry")
	ErrAlreadyCancelled     = errors.New("bandfinance: this entry is already cancelled")
	ErrAlreadySettled       = errors.New("bandfinance: this entry is already settled")
	ErrSettledImmutable     = errors.New("bandfinance: settled entries cannot be edited")
	ErrInvalidAmount        = errors.New("bandfinance: the amount must be positive")
	ErrInvalidType          = errors.New("bandfinance: the type must be income or expense")
	ErrInvalidDate          = errors.New("bandfinance: the transaction date is required")
	ErrMissingFields        = errors.New("bandfinance: category and description are required")
	ErrInvalidAccountHolder = errors.New("bandfinance: account holder must be an active user of this band")
)

// Entry is a new or editable ledger line. IsSettled is a pointer so older API
// clients that omit the field keep the historical default: immediately settled.
type Entry struct {
	TransactionType     models.BandTransactionType `json:"transaction_type"`
	TransactionOn       models.Date                `json:"transaction_on"`
	Category            string                     `json:"category"`
	Description         string                     `json:"description"`
	AmountCents         int64                      `json:"amount_cents"`
	AccountHolderUserID *int64                     `json:"account_holder_user_id"`
	IsSettled           *bool                      `json:"is_settled,omitempty"`
	IsAsset             bool                       `json:"is_asset"`
}

// Actor is who booked the entry.
type Actor struct {
	UserID   int64
	Username string
}

// Service owns the band ledger.
type Service struct {
	db *gorm.DB
}

// NewService builds the ledger service.
func NewService(database *gorm.DB) *Service { return &Service{db: database} }

func settledOrDefault(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func validateEntry(entry Entry) (string, string, error) {
	if entry.TransactionType != models.BandIncome && entry.TransactionType != models.BandExpense {
		return "", "", ErrInvalidType
	}
	if entry.TransactionOn.IsZero() {
		return "", "", ErrInvalidDate
	}
	if entry.AmountCents <= 0 {
		return "", "", ErrInvalidAmount
	}
	category := strings.TrimSpace(entry.Category)
	description := strings.TrimSpace(entry.Description)
	if category == "" || description == "" {
		return "", "", ErrMissingFields
	}
	return category, description, nil
}

// Create books a new income or expense.
func (s *Service) Create(ctx context.Context, entry Entry, actor Actor) (*models.BandTransaction, error) {
	category, description, err := validateEntry(entry)
	if err != nil {
		return nil, err
	}
	holderID, holderUsername, err := resolveAccountHolder(ctx, s.db, entry.AccountHolderUserID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	transaction := &models.BandTransaction{
		TransactionType:       entry.TransactionType,
		TransactionOn:         entry.TransactionOn,
		Category:              category,
		Description:           description,
		AmountCents:           entry.AmountCents,
		AccountHolderUserID:   holderID,
		AccountHolderUsername: holderUsername,
		IsSettled:             settledOrDefault(entry.IsSettled),
		IsAsset:               entry.TransactionType == models.BandExpense && entry.IsAsset,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	transaction.CreatedByUserID = &actor.UserID
	transaction.CreatedByUsername = actor.Username
	if transaction.IsSettled {
		transaction.SettledAt = &now
		transaction.SettledByUserID = &actor.UserID
		transaction.SettledByUsername = actor.Username
	}

	if err := s.db.WithContext(ctx).Create(transaction).Error; err != nil {
		return nil, err
	}
	return transaction, nil
}

// Update changes an open entry. A settled entry is intentionally immutable.
func (s *Service) Update(ctx context.Context, id int64, entry Entry) (*models.BandTransaction, error) {
	category, description, err := validateEntry(entry)
	if err != nil {
		return nil, err
	}

	var updated models.BandTransaction
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&updated, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if updated.IsCancelled {
			return ErrAlreadyCancelled
		}
		if updated.IsSettled {
			return ErrSettledImmutable
		}

		holderID := updated.AccountHolderUserID
		holderUsername := updated.AccountHolderUsername
		if !sameAccountHolder(entry.AccountHolderUserID, updated.AccountHolderUserID) {
			var err error
			holderID, holderUsername, err = resolveAccountHolder(ctx, tx, entry.AccountHolderUserID)
			if err != nil {
				return err
			}
		}

		if err := tx.WithContext(ctx).Model(&models.BandTransaction{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"transaction_type":        entry.TransactionType,
				"transaction_on":          entry.TransactionOn,
				"category":                category,
				"description":             description,
				"amount_cents":            entry.AmountCents,
				"account_holder_user_id":  holderID,
				"account_holder_username": holderUsername,
				"is_asset":                entry.TransactionType == models.BandExpense && entry.IsAsset,
				"updated_at":              time.Now().UTC(),
			}).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).First(&updated, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Settle marks an open income as received or an open expense as paid. There is
// deliberately no reverse operation: once money has changed hands, the entry
// is immutable and can only be cancelled.
func (s *Service) Settle(ctx context.Context, id int64, actor Actor) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transaction models.BandTransaction
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&transaction, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if transaction.IsCancelled {
			return ErrAlreadyCancelled
		}
		if transaction.IsSettled {
			return ErrAlreadySettled
		}

		now := time.Now().UTC()
		return tx.WithContext(ctx).Model(&models.BandTransaction{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"is_settled":          true,
				"settled_at":          now,
				"settled_by_user_id":  actor.UserID,
				"settled_by_username": actor.Username,
				"updated_at":          now,
			}).Error
	})
}

// Cancel voids an entry without deleting it, so the ledger stays auditable.
func (s *Service) Cancel(ctx context.Context, id int64, actor Actor) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transaction models.BandTransaction
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&transaction, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if transaction.IsCancelled {
			return ErrAlreadyCancelled
		}

		return tx.WithContext(ctx).Model(&models.BandTransaction{}).Where("id = ?", id).
			Updates(map[string]any{
				"is_cancelled":          true,
				"cancelled_at":          time.Now().UTC(),
				"cancelled_by_user_id":  actor.UserID,
				"cancelled_by_username": actor.Username,
				"updated_at":            time.Now().UTC(),
			}).Error
	})
}

// CategoryTotal is one row of the category breakdown.
type CategoryTotal struct {
	Category     string `json:"category"`
	IncomeCents  int64  `json:"income_cents"`
	ExpenseCents int64  `json:"expense_cents"`
	BalanceCents int64  `json:"balance_cents"`
}

// Ledger is the ledger view: every entry plus the category breakdown.
type Ledger struct {
	Entries        []models.BandTransaction `json:"entries"`
	Categories     []CategoryTotal          `json:"categories"`
	AccountHolders []AccountHolder          `json:"account_holders"`
	// SuggestedCategories remains for older clients. New clients use the
	// type-specific standardised lists and "Sonstiges" as their fallback.
	SuggestedCategories        []string `json:"suggested_categories"`
	SuggestedIncomeCategories  []string `json:"suggested_income_categories"`
	SuggestedExpenseCategories []string `json:"suggested_expense_categories"`

	IncomeCents      int64 `json:"income_cents"`
	ExpenseCents     int64 `json:"expense_cents"`
	BalanceCents     int64 `json:"balance_cents"`
	OpenIncomeCents  int64 `json:"open_income_cents"`
	OpenExpenseCents int64 `json:"open_expense_cents"`
}

// List returns the ledger, newest first. Cancelled entries are excluded from
// every total; open entries are shown separately and do not affect cash totals.
func (s *Service) List(ctx context.Context) (*Ledger, error) {
	var entries []models.BandTransaction
	err := s.db.WithContext(ctx).
		Order("transaction_on DESC, id DESC").
		Preload("Attachments").
		Find(&entries).Error
	if err != nil {
		return nil, err
	}
	holders, err := listAccountHolders(ctx, s.db)
	if err != nil {
		return nil, err
	}

	ledger := &Ledger{
		Entries:                    entries,
		SuggestedCategories:        models.DefaultBandCategories,
		SuggestedIncomeCategories:  models.DefaultBandIncomeCategories,
		SuggestedExpenseCategories: models.DefaultBandExpenseCategories,
		Categories:                 []CategoryTotal{},
		AccountHolders:             holders,
	}
	if ledger.Entries == nil {
		ledger.Entries = []models.BandTransaction{}
	}
	for i := range ledger.Entries {
		if ledger.Entries[i].Attachments == nil {
			ledger.Entries[i].Attachments = []models.BandTransactionAttachment{}
		}
	}

	byCategory := map[string]*CategoryTotal{}
	order := make([]string, 0)
	for _, entry := range entries {
		if entry.IsCancelled {
			continue
		}
		if !entry.IsSettled {
			if entry.TransactionType == models.BandIncome {
				ledger.OpenIncomeCents += entry.AmountCents
			} else {
				ledger.OpenExpenseCents += entry.AmountCents
			}
			continue
		}
		total, seen := byCategory[entry.Category]
		if !seen {
			total = &CategoryTotal{Category: entry.Category}
			byCategory[entry.Category] = total
			order = append(order, entry.Category)
		}
		if entry.TransactionType == models.BandIncome {
			total.IncomeCents += entry.AmountCents
			ledger.IncomeCents += entry.AmountCents
		} else {
			total.ExpenseCents += entry.AmountCents
			ledger.ExpenseCents += entry.AmountCents
		}
		total.BalanceCents = total.IncomeCents - total.ExpenseCents
	}

	for _, category := range order {
		ledger.Categories = append(ledger.Categories, *byCategory[category])
	}
	ledger.BalanceCents = ledger.IncomeCents - ledger.ExpenseCents
	return ledger, nil
}
