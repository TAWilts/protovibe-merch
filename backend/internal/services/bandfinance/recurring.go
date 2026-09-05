package bandfinance

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// ErrInvalidInterval means a recurring rule cannot be scheduled.
var ErrInvalidInterval = errors.New("bandfinance: interval must be a positive number of days, weeks, months or years")

// RecurringEntry is a new repeating ledger rule.
type RecurringEntry struct {
	TransactionType models.BandTransactionType `json:"transaction_type"`
	StartOn         models.Date                `json:"start_on"`
	Category        string                     `json:"category"`
	Description     string                     `json:"description"`
	AmountCents     int64                      `json:"amount_cents"`
	IntervalValue   int                        `json:"interval_value"`
	IntervalUnit    models.RecurrenceUnit      `json:"interval_unit"`
}

// CreateRecurring stores a schedule. Its first occurrence is due on StartOn.
func (s *Service) CreateRecurring(ctx context.Context, entry RecurringEntry, actor Actor) (*models.RecurringBandTransaction, error) {
	if entry.TransactionType != models.BandIncome && entry.TransactionType != models.BandExpense {
		return nil, ErrInvalidType
	}
	if entry.AmountCents <= 0 {
		return nil, ErrInvalidAmount
	}
	if strings.TrimSpace(entry.Category) == "" || strings.TrimSpace(entry.Description) == "" {
		return nil, ErrMissingFields
	}
	if entry.StartOn.IsZero() || !validInterval(entry.IntervalValue, entry.IntervalUnit) {
		return nil, ErrInvalidInterval
	}

	now := time.Now().UTC()
	rule := &models.RecurringBandTransaction{
		TransactionType: entry.TransactionType,
		StartOn:         entry.StartOn,
		NextRunOn:       entry.StartOn,
		Category:        strings.TrimSpace(entry.Category),
		Description:     strings.TrimSpace(entry.Description),
		AmountCents:     entry.AmountCents,
		IntervalValue:   entry.IntervalValue,
		IntervalUnit:    entry.IntervalUnit,
		IsActive:        true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	rule.CreatedByUserID = &actor.UserID
	rule.CreatedByUsername = actor.Username
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return nil, err
	}
	return rule, nil
}

// ListRecurring returns active and paused rules.
func (s *Service) ListRecurring(ctx context.Context) ([]models.RecurringBandTransaction, error) {
	var rules []models.RecurringBandTransaction
	if err := s.db.WithContext(ctx).
		Order("is_active DESC, next_run_on, id").
		Find(&rules).Error; err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []models.RecurringBandTransaction{}
	}
	return rules, nil
}

// SetRecurringActive pauses or resumes a rule. Dates elapsed while explicitly
// paused are skipped instead of being booked retroactively.
func (s *Service) SetRecurringActive(ctx context.Context, id int64, active bool, today models.Date) error {
	var rule models.RecurringBandTransaction
	if err := s.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	next := rule.NextRunOn
	if active {
		for next.Before(today.Time) {
			next = nextOccurrence(rule.StartOn, next, rule.IntervalValue, rule.IntervalUnit)
		}
	}

	return s.db.WithContext(ctx).Model(&models.RecurringBandTransaction{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"is_active":   active,
			"next_run_on": next,
			"updated_at":  time.Now().UTC(),
		}).Error
}

// MaterializeDueForBand catches one band up through the supplied date.
func (s *Service) MaterializeDueForBand(ctx context.Context, through models.Date) (int, error) {
	bandID, err := tenant.BandID(ctx)
	if err != nil {
		return 0, err
	}

	var rules []models.RecurringBandTransaction
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND next_run_on <= ?", true, through).
		Order("next_run_on, id").
		Find(&rules).Error; err != nil {
		return 0, err
	}

	total := 0
	for _, rule := range rules {
		count, err := s.materializeRule(ctx, bandID, rule.ID, through)
		if err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

// MaterializeDueAll is the scheduler entry point. Discovery is cross-band;
// every write re-enters an ordinary single-band tenant scope.
func (s *Service) MaterializeDueAll(ctx context.Context, through models.Date) (int, error) {
	type ruleRef struct {
		ID     int64
		BandID int64
	}
	var refs []ruleRef
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Model(&models.RecurringBandTransaction{}).
		Select("recurring_band_transactions.id, recurring_band_transactions.band_id").
		Joins("JOIN bands ON bands.id = recurring_band_transactions.band_id").
		Where("recurring_band_transactions.is_active = ? AND recurring_band_transactions.next_run_on <= ?", true, through).
		Where("bands.is_active = ? AND bands.deleted_at IS NULL", true).
		Scan(&refs).Error
	if err != nil {
		return 0, err
	}

	total := 0
	for _, ref := range refs {
		count, err := s.materializeRule(tenant.WithBand(ctx, ref.BandID), ref.BandID, ref.ID, through)
		if err != nil {
			return total, err
		}
		total += count
	}
	return total, nil
}

func (s *Service) materializeRule(ctx context.Context, bandID, id int64, through models.Date) (int, error) {
	generated := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rule models.RecurringBandTransaction
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&rule, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if !rule.IsActive {
			return nil
		}

		due := rule.NextRunOn
		for !due.After(through.Time) {
			if generated >= 1000 {
				return errors.New("bandfinance: recurring rule produced more than 1000 overdue occurrences")
			}

			var existing models.RecurringBandTransactionRun
			err := tx.WithContext(ctx).
				Where("recurring_transaction_id = ? AND occurrence_on = ?", rule.ID, due).
				First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}

			if errors.Is(err, gorm.ErrRecordNotFound) {
				now := time.Now().UTC()
				booking := &models.BandTransaction{
					Tenant:          models.Tenant{BandID: bandID},
					TransactionType: rule.TransactionType,
					TransactionOn:   due,
					Category:        rule.Category,
					Description:     rule.Description,
					AmountCents:     rule.AmountCents,
					CreatedAt:       now,
					UpdatedAt:       now,
					Actor: models.Actor{
						CreatedByUserID:   rule.CreatedByUserID,
						CreatedByUsername: rule.CreatedByUsername,
					},
				}
				if err := tx.WithContext(ctx).Create(booking).Error; err != nil {
					return err
				}
				run := &models.RecurringBandTransactionRun{
					Tenant:                 models.Tenant{BandID: bandID},
					RecurringTransactionID: rule.ID,
					OccurrenceOn:           due,
					TransactionID:          booking.ID,
					CreatedAt:              now,
				}
				if err := tx.WithContext(ctx).Create(run).Error; err != nil {
					return err
				}
				generated++
			}

			due = nextOccurrence(rule.StartOn, due, rule.IntervalValue, rule.IntervalUnit)
		}

		return tx.WithContext(ctx).Model(&models.RecurringBandTransaction{}).
			Where("id = ?", rule.ID).
			Updates(map[string]any{
				"next_run_on": due,
				"updated_at":  time.Now().UTC(),
			}).Error
	})
	return generated, err
}

func validInterval(value int, unit models.RecurrenceUnit) bool {
	if value <= 0 {
		return false
	}
	switch unit {
	case models.RecurrenceDay, models.RecurrenceWeek, models.RecurrenceMonth, models.RecurrenceYear:
		return true
	default:
		return false
	}
}

// nextOccurrence preserves the original day-of-month anchor. For example,
// 31 Jan -> 28/29 Feb -> 31 Mar instead of drifting permanently to the 28th.
func nextOccurrence(start, current models.Date, value int, unit models.RecurrenceUnit) models.Date {
	switch unit {
	case models.RecurrenceDay:
		next := current.AddDate(0, 0, value)
		return models.NewDate(next.Year(), next.Month(), next.Day())
	case models.RecurrenceWeek:
		next := current.AddDate(0, 0, 7*value)
		return models.NewDate(next.Year(), next.Month(), next.Day())
	case models.RecurrenceMonth:
		year, month := addMonths(current.Year(), current.Month(), value)
		return models.NewDate(year, month, clampDay(year, month, start.Day()))
	case models.RecurrenceYear:
		year := current.Year() + value
		return models.NewDate(year, start.Month(), clampDay(year, start.Month(), start.Day()))
	default:
		return current
	}
}

func addMonths(year int, month time.Month, delta int) (int, time.Month) {
	zeroBased := year*12 + int(month) - 1 + delta
	return zeroBased / 12, time.Month(zeroBased%12 + 1)
}

func clampDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}
