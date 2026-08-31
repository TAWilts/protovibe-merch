package sales_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sales"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var counter atomic.Int64

type fixture struct {
	t         *testing.T
	db        *gorm.DB
	svc       *sales.Service
	catalogue *catalogue.Service
	ctx       context.Context
	bandID    int64
	variants  []models.Variant
	today     models.Date
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set; skipping sales integration test")
	}
	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("SECRET_KEY", "test-secret-key-that-is-long-enough-for-config")
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	suffix := strconv.FormatInt(counter.Add(1), 10) + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
	band := &models.Band{Slug: "sales-" + suffix, Name: "Sales Test", IsActive: true}
	if err := database.WithContext(tenant.WithCrossBandAccess(context.Background())).Create(band).Error; err != nil {
		t.Fatalf("create band: %v", err)
	}
	ctx := tenant.WithBand(context.Background(), band.ID)

	cat := catalogue.NewService(database)
	article, err := cat.CreateArticle(ctx, "Shirt "+suffix, 1800, 900)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}

	var variants []models.Variant
	if err := database.WithContext(ctx).
		Where("article_id = ? AND is_active = ?", article.ID, true).
		Order("combination_key").Limit(3).Find(&variants).Error; err != nil {
		t.Fatalf("read variants: %v", err)
	}

	t.Cleanup(func() {
		for _, stmt := range []string{
			"DELETE FROM sync_events WHERE band_id = ?",
			"DELETE FROM payment_qr_intents WHERE band_id = ?",
			"DELETE FROM sales WHERE band_id = ?",
			"DELETE FROM purchases WHERE band_id = ?",
			"DELETE FROM variants WHERE band_id = ?",
			"DELETE FROM option_values WHERE band_id = ?",
			"DELETE FROM option_groups WHERE band_id = ?",
			"DELETE FROM articles WHERE band_id = ?",
			"DELETE FROM bands WHERE id = ?",
		} {
			_ = database.Exec(stmt, band.ID).Error
		}
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	return &fixture{
		t: t, db: database, svc: sales.NewService(database), catalogue: cat,
		ctx: ctx, bandID: band.ID, variants: variants,
		today: models.NewDate(2026, time.August, 27),
	}
}

func (f *fixture) request(items ...sales.BasketItem) sales.Request {
	return sales.Request{
		Items:         items,
		PaymentMethod: models.PaymentMethodCash,
		IsPaid:        true,
		IsReceived:    true,
		SoldOn:        f.today,
	}
}

func (f *fixture) actor() sales.Actor {
	return sales.Actor{UserID: 1, Username: "seller"}
}

func (f *fixture) rows(receiptID string) []models.Sale {
	f.t.Helper()
	var rows []models.Sale
	if err := f.db.WithContext(f.ctx).Where("receipt_id = ?", receiptID).Order("id").Find(&rows).Error; err != nil {
		f.t.Fatalf("read sales: %v", err)
	}
	return rows
}

// TestBookMultiItemReceipt pins that a basket becomes one receipt with several
// rows, and that the rows add back up to what the customer paid.
func TestBookMultiItemReceipt(t *testing.T) {
	f := newFixture(t)
	given := int64(4000)

	req := f.request(
		sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1},
		sales.BasketItem{VariantID: f.variants[1].ID, Quantity: 1},
	)
	req.AmountGivenCents = &given

	result, err := f.svc.Book(f.ctx, req, f.actor(), nil)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if result.TotalDueCents != 3600 || result.DonationCents != 400 {
		t.Fatalf("expected 3600 due and 400 donated, got %d and %d", result.TotalDueCents, result.DonationCents)
	}

	rows := f.rows(result.ReceiptID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 ledger rows under one receipt, got %d", len(rows))
	}

	var givenSum, donation int64
	for _, row := range rows {
		if row.AmountGivenCents == nil {
			t.Fatalf("a paid row must record the amount given: %+v", row)
		}
		givenSum += *row.AmountGivenCents
		donation += row.DonationCents
		if row.CreatedByUsername != "seller" {
			t.Fatalf("the username snapshot must be stored, got %q", row.CreatedByUsername)
		}
	}
	if givenSum != given || donation != 400 {
		t.Fatalf("rows must sum to the payment: given %d, donation %d", givenSum, donation)
	}
}

// TestSaleReducesDerivedStock ties the booking back to the stock calculation.
func TestSaleReducesDerivedStock(t *testing.T) {
	f := newFixture(t)
	variant := f.variants[0]

	purchase := &models.Purchase{
		ReceiptID: "E-1", VariantID: variant.ID, Quantity: 10,
		UnitCostCents: 900, PurchasedOn: f.today,
	}
	if err := f.db.WithContext(f.ctx).Create(purchase).Error; err != nil {
		t.Fatalf("create purchase: %v", err)
	}

	if _, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: variant.ID, Quantity: 4}), f.actor(), nil); err != nil {
		t.Fatalf("book: %v", err)
	}

	stock, err := f.catalogue.StockMap(f.ctx)
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	if stock[variant.ID].OnHand != 6 {
		t.Fatalf("expected 6 on hand, got %+v", stock[variant.ID])
	}
}

// TestSellingMoreThanStockIsAllowed pins the deliberate rule that the till
// takes money even when the recorded stock has run out.
func TestSellingMoreThanStockIsAllowed(t *testing.T) {
	f := newFixture(t)
	variant := f.variants[0]

	if _, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: variant.ID, Quantity: 3}), f.actor(), nil); err != nil {
		t.Fatalf("a sale must never be blocked by stock: %v", err)
	}

	stock, err := f.catalogue.StockMap(f.ctx)
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	if stock[variant.ID].OnHand != -3 {
		t.Fatalf("the shortfall must be visible as negative stock, got %+v", stock[variant.ID])
	}
}

// TestReceiptIDsAreSequential pins that consecutive baskets get consecutive,
// non-colliding IDs.
func TestReceiptIDsAreSequential(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1}), f.actor(), nil)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	second, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1}), f.actor(), nil)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if first.ReceiptID == second.ReceiptID {
		t.Fatalf("two baskets share the receipt ID %q", first.ReceiptID)
	}
	if first.ReceiptID != "V-20260827-001" || second.ReceiptID != "V-20260827-002" {
		t.Fatalf("unexpected IDs %q and %q", first.ReceiptID, second.ReceiptID)
	}
}

// TestOfflineSyncIsIdempotent is the property a phone at a gig depends on:
// resending a queued sale after a lost response must not book it twice.
func TestOfflineSyncIsIdempotent(t *testing.T) {
	f := newFixture(t)
	req := f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 2})
	event := &sales.OfflineEvent{
		EventID:   "evt-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		DeviceID:  "phone-1",
		CreatedAt: time.Now().UTC(),
	}

	first, err := f.svc.Book(f.ctx, req, f.actor(), event)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Replayed {
		t.Fatal("the first submission is not a replay")
	}

	second, err := f.svc.Book(f.ctx, req, f.actor(), event)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !second.Replayed {
		t.Fatal("a retry must be marked as replayed")
	}
	if second.ReceiptID != first.ReceiptID {
		t.Fatalf("the retry must return the original receipt, got %q vs %q", second.ReceiptID, first.ReceiptID)
	}

	var total int64
	if err := f.db.WithContext(f.ctx).Model(&models.Sale{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 1 {
		t.Fatalf("a retry must not create a second booking, found %d rows", total)
	}
}

// TestOfflineSyncRejectsAReusedIDWithDifferentData pins that a mismatched
// retry is surfaced as a conflict instead of silently discarding or
// duplicating the sale.
func TestOfflineSyncRejectsAReusedIDWithDifferentData(t *testing.T) {
	f := newFixture(t)
	event := &sales.OfflineEvent{
		EventID:   "evt-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		DeviceID:  "phone-1",
		CreatedAt: time.Now().UTC(),
	}

	if _, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1}), f.actor(), event); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	changed := f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 5})
	if _, err := f.svc.Book(f.ctx, changed, f.actor(), event); !errors.Is(err, sales.ErrSyncConflict) {
		t.Fatalf("expected a sync conflict, got %v", err)
	}
}

// TestOfflineRetryMatchesDespiteADifferentPreview pins that the fingerprint
// ignores the receipt-ID preview, which a device may have guessed differently.
func TestOfflineRetryMatchesDespiteADifferentPreview(t *testing.T) {
	f := newFixture(t)
	event := &sales.OfflineEvent{
		EventID:   "evt-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		DeviceID:  "phone-1",
		CreatedAt: time.Now().UTC(),
	}

	first := f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1})
	first.ReceiptID = "V-20260827-042"
	booked, err := f.svc.Book(f.ctx, first, f.actor(), event)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	retry := f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1})
	retry.ReceiptID = "V-20260827-099"
	replayed, err := f.svc.Book(f.ctx, retry, f.actor(), event)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !replayed.Replayed || replayed.ReceiptID != booked.ReceiptID {
		t.Fatalf("the retry must replay the original booking: %+v", replayed)
	}
}

// TestBookingIsAtomic pins that a rejected basket leaves nothing behind — no
// half-written receipt and no consumed receipt number.
func TestBookingIsAtomic(t *testing.T) {
	f := newFixture(t)

	// The second position is unpayable, so the whole basket must be rejected.
	negative := int64(-1)
	req := f.request(
		sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1},
		sales.BasketItem{VariantID: f.variants[1].ID, Quantity: 1, UnitPriceCents: &negative},
	)
	if _, err := f.svc.Book(f.ctx, req, f.actor(), nil); err == nil {
		t.Fatal("the basket must be rejected")
	}

	var total int64
	if err := f.db.WithContext(f.ctx).Model(&models.Sale{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 0 {
		t.Fatalf("a rejected basket must leave no rows, found %d", total)
	}

	ok, err := f.svc.Book(f.ctx, f.request(sales.BasketItem{VariantID: f.variants[0].ID, Quantity: 1}), f.actor(), nil)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if ok.ReceiptID != "V-20260827-001" {
		t.Fatalf("a rejected basket must not consume a receipt number, got %q", ok.ReceiptID)
	}
}

// TestWithdrawnVariantsCannotBeSold pins that taking an article out of the
// assortment actually stops new sales, while leaving its history intact.
func TestWithdrawnVariantsCannotBeSold(t *testing.T) {
	f := newFixture(t)
	variant := f.variants[0]

	if err := f.db.WithContext(f.ctx).Model(&models.Variant{}).
		Where("id = ?", variant.ID).Update("is_offered", false).Error; err != nil {
		t.Fatalf("withdraw variant: %v", err)
	}

	req := f.request(sales.BasketItem{VariantID: variant.ID, Quantity: 1})
	if _, err := f.svc.Book(f.ctx, req, f.actor(), nil); !errors.Is(err, sales.ErrVariantNotOffered) {
		t.Fatalf("expected the variant to be refused, got %v", err)
	}
}
