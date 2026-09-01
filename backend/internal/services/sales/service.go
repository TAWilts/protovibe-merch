package sales

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
)

// Errors specific to booking and offline synchronisation.
var (
	// ErrSyncConflict means a client reused an event ID with different data.
	// Replaying the stored answer would be wrong, so the client is told to
	// resolve it rather than silently booking twice or discarding the sale.
	ErrSyncConflict   = errors.New("sales: this offline event ID was already used with different data")
	ErrIntentUnusable = errors.New("sales: the payment code is expired, cancelled or already redeemed")
)

// Actor is who books the sale. The username is stored as an immutable snapshot
// so deleting the account later never makes the booking unreadable.
type Actor struct {
	UserID   int64
	Username string
}

// OfflineEvent identifies a sale that was queued on a device without a
// connection.
type OfflineEvent struct {
	EventID   string    `json:"event_id"`
	DeviceID  string    `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Result is what the client gets back after a successful booking.
type Result struct {
	ReceiptID     string  `json:"receipt_id"`
	SaleIDs       []int64 `json:"sale_ids"`
	TotalDueCents int64   `json:"total_due_cents"`
	DonationCents int64   `json:"donation_cents"`
	// Replayed marks an answer that came from the sync log rather than from a
	// fresh booking, so the device can tell the two apart.
	Replayed bool `json:"replayed"`
}

// Service books sales.
type Service struct {
	db       *gorm.DB
	receipts *receipt.Service
}

// NewService builds the sales service.
func NewService(database *gorm.DB) *Service {
	return &Service{db: database, receipts: receipt.NewService(database)}
}

// Book validates and stores a basket as one receipt.
//
// When offline is set, the booking is idempotent: the client's durable event
// ID is recorded together with a fingerprint of the payload and the exact
// answer produced. A retry after a lost response replays that answer instead
// of creating a second sale, which is what makes a phone at a gig safe to
// synchronise repeatedly.
func (s *Service) Book(ctx context.Context, req Request, actor Actor, offline *OfflineEvent) (*Result, error) {
	if offline != nil {
		if replayed, err := s.replay(ctx, *offline, req); err != nil || replayed != nil {
			return replayed, err
		}
	}

	var result *Result
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		prices, err := loadPrices(ctx, tx, req.Items)
		if err != nil {
			return err
		}
		prepared, err := Prepare(req, prices)
		if err != nil {
			return err
		}

		intent, err := s.redeemIntent(ctx, tx, req.PaymentQRIntentToken)
		if err != nil {
			return err
		}
		supplied := req.ReceiptID
		if intent != nil {
			// The code the customer already scanned decides the ID.
			supplied = intent.ReceiptID
		}

		receiptID, err := s.receipts.WithTx(tx).
			Allocate(ctx, receipt.PrefixSale, supplied, req.SoldOn, req.PaymentQRIntentToken)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		saleIDs := make([]int64, 0, len(prepared.Lines))
		for _, line := range prepared.Lines {
			sale := &models.Sale{
				ReceiptID:        receiptID,
				VariantID:        line.VariantID,
				Quantity:         line.Quantity,
				UnitPriceCents:   line.UnitPriceCents,
				AmountDueCents:   line.AmountDueCents,
				AmountGivenCents: line.AmountGivenCents,
				DonationCents:    line.DonationCents,
				PaymentMethod:    prepared.PaymentMethod,
				IsPaid:           prepared.IsPaid,
				PaymentFollowUp:  prepared.PaymentFollowUp,
				IsReceived:       prepared.IsReceived,
				DeliveryStatus:   prepared.DeliveryStatus,
				CustomerName:     prepared.CustomerName,
				CustomerAddress:  prepared.CustomerAddress,
				EventName:        prepared.EventName,
				SoldBy:           prepared.SoldBy,
				Comment:          prepared.Comment,
				SoldOn:           req.SoldOn,
				CreatedAt:        now,
			}
			sale.CreatedByUserID = &actor.UserID
			sale.CreatedByUsername = actor.Username

			if err := tx.WithContext(ctx).Create(sale).Error; err != nil {
				return err
			}
			saleIDs = append(saleIDs, sale.ID)
		}

		result = &Result{
			ReceiptID:     receiptID,
			SaleIDs:       saleIDs,
			TotalDueCents: prepared.TotalDueCents,
			DonationCents: prepared.DonationCents,
		}

		if intent != nil {
			if err := s.consumeIntent(ctx, tx, intent, result); err != nil {
				return err
			}
		}
		if offline != nil {
			if err := s.recordSyncEvent(ctx, tx, *offline, req, actor, result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// replay returns the stored answer for an already-synchronised event, or an
// error when the same ID arrives carrying different data.
func (s *Service) replay(ctx context.Context, event OfflineEvent, req Request) (*Result, error) {
	var stored models.SyncEvent
	err := s.db.WithContext(ctx).First(&stored, "event_id = ?", event.EventID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	if stored.PayloadHash != payloadHash(req) {
		return nil, ErrSyncConflict
	}

	var result Result
	if err := json.Unmarshal([]byte(stored.ResponseJSON), &result); err != nil {
		return nil, fmt.Errorf("sales: stored sync response is unreadable: %w", err)
	}
	result.Replayed = true
	return &result, nil
}

func (s *Service) recordSyncEvent(ctx context.Context, tx *gorm.DB, event OfflineEvent, req Request, actor Actor, result *Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	record := &models.SyncEvent{
		EventID:         event.EventID,
		EventType:       "sale",
		ActorUserID:     actor.UserID,
		ActorUsername:   actor.Username,
		DeviceID:        event.DeviceID,
		PayloadHash:     payloadHash(req),
		ClientCreatedAt: event.CreatedAt,
		ResponseJSON:    string(encoded),
		CreatedAt:       time.Now().UTC(),
	}
	return tx.WithContext(ctx).Create(record).Error
}

// payloadHash fingerprints the parts of a request that define the booking.
//
// The receipt-ID preview and the QR token are excluded on purpose: a device
// retrying the same sale must match even if the server had already assigned a
// different ID on the first attempt.
func payloadHash(req Request) string {
	fingerprint := struct {
		Items           []BasketItem `json:"items"`
		PaymentMethod   string       `json:"payment_method"`
		IsPaid          bool         `json:"is_paid"`
		IsReceived      bool         `json:"is_received"`
		AmountGiven     *int64       `json:"amount_given_cents"`
		CustomerName    string       `json:"customer_name"`
		CustomerAddress string       `json:"customer_address"`
		EventName       string       `json:"event_name"`
		SoldBy          string       `json:"sold_by"`
		Comment         string       `json:"comment"`
		SoldOn          string       `json:"sold_on"`
	}{
		Items:           req.Items,
		PaymentMethod:   req.PaymentMethod,
		IsPaid:          req.IsPaid,
		IsReceived:      req.IsReceived,
		AmountGiven:     req.AmountGivenCents,
		CustomerName:    req.CustomerName,
		CustomerAddress: req.CustomerAddress,
		EventName:       req.EventName,
		SoldBy:          req.SoldBy,
		Comment:         req.Comment,
		SoldOn:          req.SoldOn.String(),
	}

	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		// Marshalling this struct cannot fail; a hash of nothing would still
		// be safe because it only ever compares equal to itself.
		encoded = []byte(err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// redeemIntent loads a payment-QR reservation and verifies it is still usable.
func (s *Service) redeemIntent(ctx context.Context, tx *gorm.DB, token string) (*models.PaymentQRIntent, error) {
	if token == "" {
		return nil, nil
	}

	var intent models.PaymentQRIntent
	if err := tx.WithContext(ctx).First(&intent, "token = ?", token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIntentUnusable
		}
		return nil, err
	}
	if intent.CancelledAt != nil || intent.ConsumedAt != nil || time.Now().UTC().After(intent.ExpiresAt) {
		return nil, ErrIntentUnusable
	}
	return &intent, nil
}

// consumeIntent marks a reservation as redeemed and blanks its payload, so the
// stored basket cannot be replayed once the sale exists.
func (s *Service) consumeIntent(ctx context.Context, tx *gorm.DB, intent *models.PaymentQRIntent, result *Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return tx.WithContext(ctx).Model(&models.PaymentQRIntent{}).
		Where("token = ?", intent.Token).
		Updates(map[string]any{
			"consumed_at":       now,
			"sale_payload_json": "{}",
			"response_json":     string(encoded),
		}).Error
}

// loadPrices resolves the catalogue prices for the basket's variants.
func loadPrices(ctx context.Context, tx *gorm.DB, items []BasketItem) (map[int64]VariantPrice, error) {
	ids := make([]int64, 0, len(items))
	seen := map[int64]bool{}
	for _, item := range items {
		if !seen[item.VariantID] {
			seen[item.VariantID] = true
			ids = append(ids, item.VariantID)
		}
	}
	if len(ids) == 0 {
		return map[int64]VariantPrice{}, nil
	}

	type row struct {
		ID             int64
		SalePriceCents int64
		IsOffered      bool
		ArticleOffered bool
		IsActive       bool
	}
	var rows []row
	err := tx.WithContext(ctx).Model(&models.Variant{}).
		Select("variants.id, variants.sale_price_cents, variants.is_offered, variants.is_active, articles.is_offered AS article_offered").
		Joins("JOIN articles ON articles.id = variants.article_id").
		Where("variants.id IN ?", ids).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	prices := make(map[int64]VariantPrice, len(rows))
	for _, r := range rows {
		prices[r.ID] = VariantPrice{
			SalePriceCents: r.SalePriceCents,
			// A variant is sellable only when it and its article are both
			// active and still part of the assortment.
			IsOffered: r.IsOffered && r.ArticleOffered && r.IsActive,
		}
	}
	return prices, nil
}

// QuoteTotal computes what a basket costs without booking anything.
//
// The payment-code flow needs the exact amount before a sale exists, and it
// must come from the catalogue rather than from the request — otherwise a
// tampered client could show a customer a code for the wrong total.
func (s *Service) QuoteTotal(ctx context.Context, req Request) (int64, error) {
	prices, err := loadPrices(ctx, s.db.WithContext(ctx), req.Items)
	if err != nil {
		return 0, err
	}
	prepared, err := Prepare(req, prices)
	if err != nil {
		return 0, err
	}
	return prepared.TotalDueCents, nil
}

// PaymentQRDescriptions builds compact, server-owned basket labels for an EPC
// transfer reference. Browser text is deliberately not accepted here: the
// reference must describe the same variants that the server has just priced.
func (s *Service) PaymentQRDescriptions(ctx context.Context, items []BasketItem) ([]string, error) {
	labels, err := catalogue.NewService(s.db).VariantLabels(ctx)
	if err != nil {
		return nil, err
	}

	quantities := make(map[int64]int, len(items))
	order := make([]int64, 0, len(items))
	for _, item := range items {
		if _, seen := quantities[item.VariantID]; !seen {
			order = append(order, item.VariantID)
		}
		quantities[item.VariantID] += item.Quantity
	}

	descriptions := make([]string, 0, len(order))
	for _, variantID := range order {
		label, ok := labels[variantID]
		if !ok {
			return nil, ErrUnknownVariant
		}
		article := strings.Join(strings.Fields(label.ArticleName), " ")
		options := make([]string, 0, len(label.OptionValues))
		for _, value := range label.OptionValues {
			if compact := strings.Join(strings.Fields(value), " "); compact != "" {
				options = append(options, compact)
			}
		}
		description := fmt.Sprintf("%dx %s", quantities[variantID], article)
		if len(options) > 0 {
			description += " " + strings.Join(options, "/")
		}
		descriptions = append(descriptions, description)
	}
	return descriptions, nil
}
