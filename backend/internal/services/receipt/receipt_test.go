package receipt_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var counter atomic.Int64

type fixture struct {
	t       *testing.T
	db      *gorm.DB
	svc     *receipt.Service
	ctx     context.Context
	variant *models.Variant
	today   models.Date
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set; skipping receipt integration test")
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
	crossBand := tenant.WithCrossBandAccess(context.Background())

	band := &models.Band{Slug: "receipt-" + suffix, Name: "Receipt Test", IsActive: true}
	if err := database.WithContext(crossBand).Create(band).Error; err != nil {
		t.Fatalf("create band: %v", err)
	}
	ctx := tenant.WithBand(context.Background(), band.ID)

	article := &models.Article{Name: "Shirt " + suffix, IsActive: true, IsOffered: true}
	if err := database.WithContext(ctx).Create(article).Error; err != nil {
		t.Fatalf("create article: %v", err)
	}
	variant := &models.Variant{
		ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{},
		CombinationKey: "", SalePriceCents: 1800, IsActive: true, IsOffered: true,
	}
	if err := database.WithContext(ctx).Create(variant).Error; err != nil {
		t.Fatalf("create variant: %v", err)
	}

	t.Cleanup(func() {
		for _, stmt := range []string{
			"DELETE FROM payment_qr_intents WHERE band_id = ?",
			"DELETE FROM sales WHERE band_id = ?",
			"DELETE FROM purchases WHERE band_id = ?",
			"DELETE FROM variants WHERE band_id = ?",
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
		t:       t,
		db:      database,
		svc:     receipt.NewService(database),
		ctx:     ctx,
		variant: variant,
		today:   models.NewDate(2026, time.August, 27),
	}
}

func (f *fixture) addSale(receiptID string) {
	f.t.Helper()
	sale := &models.Sale{
		ReceiptID: receiptID, VariantID: f.variant.ID, Quantity: 1,
		UnitPriceCents: 1800, AmountDueCents: 1800,
		PaymentMethod: models.PaymentMethodCash, IsPaid: true, IsReceived: true,
		DeliveryStatus: models.DeliveryNotApplicable, SoldOn: f.today,
	}
	if err := f.db.WithContext(f.ctx).Create(sale).Error; err != nil {
		f.t.Fatalf("create sale: %v", err)
	}
}

func (f *fixture) addQRIntent(token, receiptID string, expires time.Time, cancelled bool) {
	f.t.Helper()
	intent := &models.PaymentQRIntent{
		Token: token, ReceiptID: receiptID, SalePayloadJSON: "{}",
		CreatedByUserID: 1, ExpiresAt: expires,
	}
	if cancelled {
		now := time.Now().UTC()
		intent.CancelledAt = &now
	}
	if err := f.db.WithContext(f.ctx).Create(intent).Error; err != nil {
		f.t.Fatalf("create intent: %v", err)
	}
}

func TestNextStartsAtOneAndCounts(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if first != "V-20260827-001" {
		t.Fatalf("expected V-20260827-001, got %q", first)
	}

	f.addSale(first)
	second, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if second != "V-20260827-002" {
		t.Fatalf("expected V-20260827-002, got %q", second)
	}
}

// TestSequencesAreSeparatePerLedgerAndDay pins that goods receipts do not
// consume sale numbers and that each day starts fresh.
func TestSequencesAreSeparatePerLedgerAndDay(t *testing.T) {
	f := newFixture(t)
	f.addSale("V-20260827-001")

	purchaseID, err := f.svc.Next(f.ctx, receipt.PrefixPurchase, f.today)
	if err != nil {
		t.Fatalf("next purchase: %v", err)
	}
	if purchaseID != "E-20260827-001" {
		t.Fatalf("goods receipts have their own sequence, got %q", purchaseID)
	}

	tomorrow := models.NewDate(2026, time.August, 28)
	nextDay, err := f.svc.Next(f.ctx, receipt.PrefixSale, tomorrow)
	if err != nil {
		t.Fatalf("next day: %v", err)
	}
	if nextDay != "V-20260828-001" {
		t.Fatalf("each day restarts at 001, got %q", nextDay)
	}
}

// TestMultiItemReceiptCountsOnce pins that a basket with several positions
// occupies exactly one number, which is what makes the ID a basket identity
// rather than a row identity.
func TestMultiItemReceiptCountsOnce(t *testing.T) {
	f := newFixture(t)
	f.addSale("V-20260827-001")
	f.addSale("V-20260827-001")
	f.addSale("V-20260827-001")

	next, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next != "V-20260827-002" {
		t.Fatalf("a three-position basket must consume one number, got %q", next)
	}
}

// TestQRReservationBlocksTheNumber is the reason the sequences are shared: a
// code already shown to a customer must never be reused for a different sale.
func TestQRReservationBlocksTheNumber(t *testing.T) {
	f := newFixture(t)
	f.addQRIntent("token-live", "V-20260827-001", time.Now().UTC().Add(20*time.Minute), false)

	next, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next != "V-20260827-002" {
		t.Fatalf("a live reservation must hold its number, got %q", next)
	}

	// The seller who owns that reservation may still redeem it.
	allocated, err := f.svc.Allocate(f.ctx, receipt.PrefixSale, "V-20260827-001", f.today, "token-live")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if allocated != "V-20260827-001" {
		t.Fatalf("the reserving seller must keep the number, got %q", allocated)
	}

	// Anyone else is pushed to the next free one.
	other, err := f.svc.Allocate(f.ctx, receipt.PrefixSale, "V-20260827-001", f.today, "")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if other == "V-20260827-001" {
		t.Fatal("a foreign reservation must not be taken over")
	}
}

// TestExpiredAndCancelledReservationsFreeTheNumber keeps abandoned QR displays
// from permanently burning receipt numbers.
func TestExpiredAndCancelledReservationsFreeTheNumber(t *testing.T) {
	f := newFixture(t)
	f.addQRIntent("token-expired", "V-20260827-001", time.Now().UTC().Add(-time.Minute), false)
	f.addQRIntent("token-cancelled", "V-20260827-002", time.Now().UTC().Add(20*time.Minute), true)

	next, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next != "V-20260827-001" {
		t.Fatalf("expired and cancelled reservations must free their numbers, got %q", next)
	}
}

// TestAllocateHonoursAFreePreview keeps the number a seller read out to a
// customer, instead of quietly issuing a different one.
func TestAllocateHonoursAFreePreview(t *testing.T) {
	f := newFixture(t)

	allocated, err := f.svc.Allocate(f.ctx, receipt.PrefixSale, "V-20260827-007", f.today, "")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if allocated != "V-20260827-007" {
		t.Fatalf("a free preview must be honoured, got %q", allocated)
	}
}

// TestAllocateRejectsUnusableSuppliedIDs pins that a client cannot smuggle in
// an ID from another day, another ledger or an arbitrary string.
func TestAllocateRejectsUnusableSuppliedIDs(t *testing.T) {
	f := newFixture(t)
	f.addSale("V-20260827-001")

	cases := map[string]string{
		"taken":        "V-20260827-001",
		"other day":    "V-20260101-005",
		"other ledger": "E-20260827-005",
		"free text":    "meine-id",
		"too short":    "V-20260827-1",
		"empty":        "",
	}
	for name, supplied := range cases {
		t.Run(name, func(t *testing.T) {
			allocated, err := f.svc.Allocate(f.ctx, receipt.PrefixSale, supplied, f.today, "")
			if err != nil {
				t.Fatalf("allocate: %v", err)
			}
			if allocated != "V-20260827-002" {
				t.Fatalf("expected a fresh sequential ID, got %q", allocated)
			}
		})
	}
}

// TestSequenceSurvivesAGapAboveThreeDigits pins that an imported or restored
// history with high numbers keeps counting up rather than colliding.
func TestSequenceSurvivesAGapAboveThreeDigits(t *testing.T) {
	f := newFixture(t)
	f.addSale("V-20260827-0999")

	next, err := f.svc.Next(f.ctx, receipt.PrefixSale, f.today)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next != "V-20260827-1000" {
		t.Fatalf("expected V-20260827-1000, got %q", next)
	}
}

func TestUnknownPrefixIsRejected(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Next(f.ctx, "X", f.today); err == nil {
		t.Fatal("an unknown prefix must be rejected rather than silently share a sequence")
	}
}
