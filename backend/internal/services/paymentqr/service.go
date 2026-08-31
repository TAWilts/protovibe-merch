package paymentqr

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/qrimage"
)

// IntentTTL is how long a displayed code stays valid. It matches the original:
// long enough for a customer to fumble with their banking app, short enough
// that an abandoned code frees its receipt number the same evening.
const IntentTTL = 20 * time.Minute

// Errors from the intent workflow.
var (
	ErrNotConfigured    = errors.New("paymentqr: this payment method is not configured for the band")
	ErrUnknownMethod    = errors.New("paymentqr: unknown payment method")
	ErrIntentNotFound   = errors.New("paymentqr: no such payment code")
	ErrInvalidPayPalURL = errors.New("paymentqr: the PayPal link must be an https URL")
)

// Availability tells the sales page which codes it may offer.
type Availability struct {
	PayPal bool `json:"paypal"`
	Bank   bool `json:"bank"`
}

// Service owns the payment settings and the displayed-code intents.
type Service struct {
	db *gorm.DB
}

// NewService builds the payment QR service.
func NewService(database *gorm.DB) *Service { return &Service{db: database} }

// Settings returns the band's payment destinations, creating the row lazily.
func (s *Service) Settings(ctx context.Context) (*models.PaymentQRSettings, error) {
	var settings models.PaymentQRSettings
	err := s.db.WithContext(ctx).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A band that never configured anything is a normal state, not an error.
		return &models.PaymentQRSettings{BankRemittanceText: "Merch-Kauf"}, nil
	}
	if err != nil {
		return nil, err
	}
	return &settings, nil
}

// SaveSettings validates and stores the payment destinations.
func (s *Service) SaveSettings(ctx context.Context, input models.PaymentQRSettings, actorID int64, actorName string) (*models.PaymentQRSettings, error) {
	paypal := strings.TrimSpace(input.PayPalMeURL)
	if paypal != "" {
		parsed, err := url.Parse(paypal)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, ErrInvalidPayPalURL
		}
	}

	iban := NormalizeIBAN(input.BankIBAN)
	if iban != "" {
		if err := ValidateIBAN(iban); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.BankAccountHolder) == "" {
			return nil, ErrMissingHolder
		}
	}
	if err := ValidateBIC(input.BankBIC); err != nil {
		return nil, err
	}

	remittance := strings.TrimSpace(input.BankRemittanceText)
	if remittance == "" {
		remittance = "Merch-Kauf"
	}

	existing, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	existing.PayPalMeURL = paypal
	existing.BankAccountHolder = strings.TrimSpace(input.BankAccountHolder)
	existing.BankIBAN = iban
	existing.BankBIC = strings.ToUpper(strings.TrimSpace(input.BankBIC))
	existing.BankRemittanceText = remittance
	existing.UpdatedAt = now
	existing.UpdatedByUserID = &actorID
	existing.UpdatedByUsername = actorName

	if existing.ID == 0 {
		if err := s.db.WithContext(ctx).Create(existing).Error; err != nil {
			return nil, err
		}
	} else if err := s.db.WithContext(ctx).Save(existing).Error; err != nil {
		return nil, err
	}
	return existing, nil
}

// Availability reports which codes the band can currently show.
func (s *Service) Availability(ctx context.Context) (Availability, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return Availability{}, err
	}
	return Availability{
		PayPal: settings.PayPalMeURL != "",
		Bank:   settings.BankIBAN != "" && settings.BankAccountHolder != "",
	}, nil
}

// Intent is a reserved receipt number with a rendered code.
type Intent struct {
	Token        string `json:"token"`
	ReceiptID    string `json:"receipt_id"`
	Method       string `json:"method"`
	AmountCents  int64  `json:"amount_cents"`
	ImageDataURI string `json:"image_data_uri"`
	// PayloadHint is what the code encodes, shown so a seller can read the
	// reference aloud when a camera refuses to focus.
	PayloadHint string    `json:"payload_hint"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// CreateIntent reserves a receipt ID and renders the code for it.
//
// Showing a code is deliberately not a sale: no stock moves and no ledger row
// appears until the seller confirms. The reservation only holds the number so
// a code already in a customer's camera can never be reassigned.
func (s *Service) CreateIntent(
	ctx context.Context,
	method string,
	amountCents int64,
	receiptID string,
	salePayloadJSON string,
	actorID int64,
	description string,
) (*Intent, error) {
	if amountCents <= 0 {
		return nil, ErrNoAmount
	}

	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}

	var payload string
	switch method {
	case models.PaymentMethodPayPal:
		if settings.PayPalMeURL == "" {
			return nil, ErrNotConfigured
		}
		// PayPal.me takes the amount in the path; the currency suffix keeps it
		// unambiguous for a customer whose account is not in euros.
		payload = strings.TrimRight(settings.PayPalMeURL, "/") +
			"/" + formatAmount(amountCents) + "EUR"

	case models.PaymentMethodTransfer:
		if settings.BankIBAN == "" || settings.BankAccountHolder == "" {
			return nil, ErrNotConfigured
		}
		remittance := settings.BankRemittanceText
		if receiptID != "" {
			// The receipt ID leads, so it survives any later shortening.
			remittance = receiptID
			if description != "" {
				remittance += ": " + description
			}
		}
		payload, err = EPCPayload(BankAccount{
			Holder:     settings.BankAccountHolder,
			IBAN:       settings.BankIBAN,
			BIC:        settings.BankBIC,
			Remittance: remittance,
		}, amountCents)
		if err != nil {
			return nil, err
		}

	default:
		return nil, ErrUnknownMethod
	}

	image, err := renderQR(payload)
	if err != nil {
		return nil, err
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(IntentTTL)

	if salePayloadJSON == "" {
		salePayloadJSON = "{}"
	}
	record := &models.PaymentQRIntent{
		Token:           token,
		ReceiptID:       receiptID,
		SalePayloadJSON: salePayloadJSON,
		CreatedByUserID: actorID,
		CreatedAt:       time.Now().UTC(),
		ExpiresAt:       expires,
	}
	if err := s.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, err
	}

	return &Intent{
		Token:        token,
		ReceiptID:    receiptID,
		Method:       method,
		AmountCents:  amountCents,
		ImageDataURI: image,
		PayloadHint:  payloadHint(method, payload),
		ExpiresAt:    expires,
	}, nil
}

// CancelIntent releases a reservation so its receipt number is free again.
func (s *Service) CancelIntent(ctx context.Context, token string) error {
	result := s.db.WithContext(ctx).Model(&models.PaymentQRIntent{}).
		Where("token = ? AND consumed_at IS NULL AND cancelled_at IS NULL", token).
		Update("cancelled_at", time.Now().UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrIntentNotFound
	}
	return nil
}

// PurgeExpired removes reservations that were never confirmed. Their receipt
// numbers are already free; this only keeps the table small.
func (s *Service) PurgeExpired(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().UTC().Add(-olderThan)
	return s.db.WithContext(ctx).
		Where("expires_at < ? AND consumed_at IS NULL", cutoff).
		Delete(&models.PaymentQRIntent{}).Error
}

// renderQR encodes a payment payload as a PNG data URI, sized for a customer
// scanning it off the seller's phone at arm's length.
func renderQR(payload string) (string, error) {
	return qrimage.DataURI(payload, 320)
}

// payloadHint is what the seller can read aloud. For a bank transfer that is
// the reference line, not the whole EPC blob.
func payloadHint(method, payload string) string {
	if method != models.PaymentMethodTransfer {
		return payload
	}
	lines := strings.Split(payload, "\n")
	if len(lines) >= 11 {
		return lines[10]
	}
	return ""
}

// formatAmount renders cents as a plain decimal for the PayPal.me path.
func formatAmount(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func randomToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := randRead(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// randRead is a thin indirection over crypto/rand so the token generator stays
// testable without a global.
var randRead = rand.Read
