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

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Errors returned by the ledger.
var (
	ErrNotFound         = errors.New("bandfinance: no such entry")
	ErrAlreadyCancelled = errors.New("bandfinance: this entry is already cancelled")
	ErrInvalidAmount    = errors.New("bandfinance: the amount must be positive")
	ErrInvalidType      = errors.New("bandfinance: the type must be income or expense")
	ErrMissingFields    = errors.New("bandfinance: category and description are required")
)

// Entry is a new ledger line.
type Entry struct {
	TransactionType models.BandTransactionType `json:"transaction_type"`
	TransactionOn   models.Date                `json:"transaction_on"`
	Category        string                     `json:"category"`
	Description     string                     `json:"description"`
	AmountCents     int64                      `json:"amount_cents"`
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

// Create books a new income or expense.
func (s *Service) Create(ctx context.Context, entry Entry, actor Actor) (*models.BandTransaction, error) {
	if entry.TransactionType != models.BandIncome && entry.TransactionType != models.BandExpense {
		return nil, ErrInvalidType
	}
	if entry.AmountCents <= 0 {
		return nil, ErrInvalidAmount
	}

	category := strings.TrimSpace(entry.Category)
	description := strings.TrimSpace(entry.Description)
	if category == "" || description == "" {
		return nil, ErrMissingFields
	}

	now := time.Now().UTC()
	transaction := &models.BandTransaction{
		TransactionType: entry.TransactionType,
		TransactionOn:   entry.TransactionOn,
		Category:        category,
		Description:     description,
		AmountCents:     entry.AmountCents,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	transaction.CreatedByUserID = &actor.UserID
	transaction.CreatedByUsername = actor.Username

	if err := s.db.WithContext(ctx).Create(transaction).Error; err != nil {
		return nil, err
	}
	return transaction, nil
}

// Cancel voids an entry without deleting it, so the ledger stays auditable.
func (s *Service) Cancel(ctx context.Context, id int64, actor Actor) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transaction models.BandTransaction
		if err := tx.WithContext(ctx).First(&transaction, id).Error; err != nil {
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
	Entries    []models.BandTransaction `json:"entries"`
	Categories []CategoryTotal          `json:"categories"`
	// SuggestedCategories are the presets offered in the form; the field
	// itself stays free text so a band can add its own.
	SuggestedCategories []string `json:"suggested_categories"`

	IncomeCents  int64 `json:"income_cents"`
	ExpenseCents int64 `json:"expense_cents"`
	BalanceCents int64 `json:"balance_cents"`
}

// List returns the ledger, newest first, with cancelled entries included but
// excluded from every total.
func (s *Service) List(ctx context.Context) (*Ledger, error) {
	var entries []models.BandTransaction
	err := s.db.WithContext(ctx).
		Order("transaction_on DESC, id DESC").
		Preload("Attachments").
		Find(&entries).Error
	if err != nil {
		return nil, err
	}

	ledger := &Ledger{
		Entries:             entries,
		SuggestedCategories: models.DefaultBandCategories,
		Categories:          []CategoryTotal{},
	}
	if ledger.Entries == nil {
		ledger.Entries = []models.BandTransaction{}
	}

	byCategory := map[string]*CategoryTotal{}
	order := make([]string, 0)
	for _, entry := range entries {
		if entry.IsCancelled {
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
