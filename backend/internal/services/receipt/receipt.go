// Package receipt allocates the human-readable receipt IDs shown at the stand.
//
// An ID looks like V-20260827-003: a prefix, the booking date and a daily
// sequence. Sales and goods receipts have separate sequences, but a sale's
// sequence is shared with the payment-QR reservations — an already displayed
// QR code must never end up pointing at a different sale.
package receipt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Prefixes distinguish the two ledgers.
const (
	PrefixSale     = "V"
	PrefixPurchase = "E"
)

// ErrUnknownPrefix guards against a typo turning into a wrong sequence.
var ErrUnknownPrefix = errors.New("receipt: unknown prefix")

// suffixPattern extracts the daily sequence number from an ID.
var suffixPattern = regexp.MustCompile(`-(\d+)$`)

// Service allocates receipt IDs within the scoped band.
type Service struct {
	db *gorm.DB
}

// NewService builds the receipt service.
func NewService(database *gorm.DB) *Service { return &Service{db: database} }

// WithTx binds the service to an open transaction, which is what makes the
// allocation and the insert of the rows atomic.
func (s *Service) WithTx(tx *gorm.DB) *Service { return &Service{db: tx} }

// dayKey renders the date part of an ID.
func dayKey(on models.Date) string {
	return on.Format("20060102")
}

// Next returns the next free ID for a prefix and date.
//
// It scans the existing IDs rather than keeping a counter, so a restored
// backup or an imported CSV cannot make the sequence collide. Preview requests
// may race with a concurrent sale, which is why the value is only a proposal —
// Allocate is what settles it at write time.
func (s *Service) Next(ctx context.Context, prefix string, on models.Date) (string, error) {
	day := dayKey(on)
	taken, err := s.takenIDs(ctx, prefix, day)
	if err != nil {
		return "", err
	}

	var highest int64
	for id := range taken {
		if match := suffixPattern.FindStringSubmatch(id); match != nil {
			if value, err := strconv.ParseInt(match[1], 10, 64); err == nil && value > highest {
				highest = value
			}
		}
	}
	return fmt.Sprintf("%s-%s-%03d", prefix, day, highest+1), nil
}

// Allocate settles the final ID for a booking.
//
// A client may send back the preview it displayed; that value is honoured when
// it is well formed and still free, so the ID the seller read out to a customer
// is the one that gets stored. Otherwise a fresh sequential ID is issued.
//
// qrIntentToken names the payment-QR reservation being redeemed, if any. Its
// own reserved ID counts as free for that specific token and as taken for
// everyone else.
func (s *Service) Allocate(ctx context.Context, prefix string, supplied string, on models.Date, qrIntentToken string) (string, error) {
	day := dayKey(on)
	supplied = strings.TrimSpace(supplied)

	if supplied != "" && matchesFormat(prefix, day, supplied) {
		taken, err := s.takenIDsExcludingIntent(ctx, prefix, day, qrIntentToken)
		if err != nil {
			return "", err
		}
		if !taken[supplied] {
			return supplied, nil
		}
	}
	return s.Next(ctx, prefix, on)
}

// matchesFormat checks an ID against the expected prefix and date, so a client
// cannot smuggle in an ID belonging to another day or ledger.
func matchesFormat(prefix, day, id string) bool {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix+"-"+day+"-") + `\d{3,}$`)
	return pattern.MatchString(id)
}

// takenIDs collects every ID already in use for a prefix and day.
func (s *Service) takenIDs(ctx context.Context, prefix, day string) (map[string]bool, error) {
	return s.takenIDsExcludingIntent(ctx, prefix, day, "")
}

// takenIDsExcludingIntent is takenIDs, minus the reservation the caller is
// redeeming.
func (s *Service) takenIDsExcludingIntent(ctx context.Context, prefix, day, qrIntentToken string) (map[string]bool, error) {
	like := prefix + "-" + day + "-%"
	taken := map[string]bool{}

	var ids []string
	switch prefix {
	case PrefixSale:
		if err := s.db.WithContext(ctx).Model(&models.Sale{}).
			Where("receipt_id LIKE ?", like).
			Distinct().Pluck("receipt_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			taken[id] = true
		}

		// Reserved QR receipts share the sale sequence. A reservation that was
		// cancelled or has expired frees its ID again.
		type intent struct {
			Token     string
			ReceiptID string
		}
		var intents []intent
		if err := s.db.WithContext(ctx).Model(&models.PaymentQRIntent{}).
			Select("token, receipt_id").
			Where("receipt_id LIKE ? AND cancelled_at IS NULL AND expires_at > ?", like, time.Now().UTC()).
			Scan(&intents).Error; err != nil {
			return nil, err
		}
		for _, i := range intents {
			if qrIntentToken != "" && i.Token == qrIntentToken {
				continue
			}
			taken[i.ReceiptID] = true
		}

	case PrefixPurchase:
		if err := s.db.WithContext(ctx).Model(&models.Purchase{}).
			Where("receipt_id LIKE ?", like).
			Distinct().Pluck("receipt_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			taken[id] = true
		}

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownPrefix, prefix)
	}

	return taken, nil
}
