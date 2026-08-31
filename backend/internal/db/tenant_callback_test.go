package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// tenantCase describes one band-scoped model: how to build a fresh record and
// how to read the collection back. Every band-scoped table must appear here —
// that is what makes this a coverage matrix rather than a spot check.
type tenantCase struct {
	name string
	// build returns a record ready to insert, given fixtures already created
	// for the same band.
	build func(f *fixtures) any
	// list reads the model's rows through the scoped handle.
	count func(ctx context.Context, gdb *gorm.DB) (int64, error)
	// needsCatalogue marks cases that require article/variant fixtures.
	needsCatalogue bool
}

// fixtures are per-band prerequisites shared by the cases that need them.
type fixtures struct {
	band        *models.Band
	article     *models.Article
	optionGroup *models.OptionGroup
	variant     *models.Variant
	txn         *models.BandTransaction
	event       *models.SaleEvent
}

func tenantCases() []tenantCase {
	now := time.Now().UTC()
	today := models.NewDate(now.Year(), now.Month(), now.Day())

	return []tenantCase{
		{
			name: "articles",
			build: func(f *fixtures) any {
				return &models.Article{Name: "Hoodie " + uniquePath(""), IsActive: true, IsOffered: true}
			},
			count: countOf[models.Article],
		},
		{
			name: "option_groups",
			build: func(f *fixtures) any {
				return &models.OptionGroup{ArticleID: f.article.ID, Name: "Farbe", IsActive: true}
			},
			count:          countOf[models.OptionGroup],
			needsCatalogue: true,
		},
		{
			name: "variants",
			build: func(f *fixtures) any {
				return &models.Variant{
					ArticleID:      f.article.ID,
					OptionValueIDs: models.JSONInt64Slice{},
					CombinationKey: uniquePath("combo"),
					SalePriceCents: 2400,
					IsActive:       true,
					IsOffered:      true,
				}
			},
			count:          countOf[models.Variant],
			needsCatalogue: true,
		},
		{
			name: "variant_photos",
			build: func(f *fixtures) any {
				return &models.VariantPhoto{
					VariantID:        f.variant.ID,
					FilePath:         uniquePath("variant"),
					OriginalFilename: "shirt.jpg",
				}
			},
			count:          countOf[models.VariantPhoto],
			needsCatalogue: true,
		},
		{
			name: "slideshow_extra_photos",
			build: func(f *fixtures) any {
				return &models.SlideshowExtraPhoto{
					FilePath:         uniquePath("extra"),
					OriginalFilename: "prices.png",
				}
			},
			count: countOf[models.SlideshowExtraPhoto],
		},
		{
			name: "sales",
			build: func(f *fixtures) any {
				return &models.Sale{
					ReceiptID:      "V-TEST-001",
					VariantID:      f.variant.ID,
					Quantity:       1,
					UnitPriceCents: 1800,
					AmountDueCents: 1800,
					PaymentMethod:  models.PaymentMethodCash,
					IsPaid:         true,
					IsReceived:     true,
					DeliveryStatus: models.DeliveryNotApplicable,
					SoldOn:         today,
				}
			},
			count:          countOf[models.Sale],
			needsCatalogue: true,
		},
		{
			name: "purchases",
			build: func(f *fixtures) any {
				return &models.Purchase{
					ReceiptID:     "E-TEST-001",
					VariantID:     f.variant.ID,
					Quantity:      10,
					UnitCostCents: 900,
					PurchasedOn:   today,
				}
			},
			count:          countOf[models.Purchase],
			needsCatalogue: true,
		},
		{
			name: "purchase_receipt_attachments",
			build: func(f *fixtures) any {
				return &models.PurchaseReceiptAttachment{
					ReceiptID:        "E-TEST-001",
					FilePath:         uniquePath("invoice"),
					OriginalFilename: "invoice.pdf",
				}
			},
			count: countOf[models.PurchaseReceiptAttachment],
		},
		{
			name: "band_transactions",
			build: func(f *fixtures) any {
				return &models.BandTransaction{
					TransactionType: models.BandExpense,
					TransactionOn:   today,
					Category:        "Equipment",
					Description:     "Kabel",
					AmountCents:     4200,
				}
			},
			count: countOf[models.BandTransaction],
		},
		{
			name: "band_transaction_attachments",
			build: func(f *fixtures) any {
				return &models.BandTransactionAttachment{
					TransactionID:    f.txn.ID,
					FilePath:         uniquePath("band"),
					OriginalFilename: "receipt.pdf",
				}
			},
			count:          countOf[models.BandTransactionAttachment],
			needsCatalogue: true,
		},
		{
			name: "sale_events",
			build: func(f *fixtures) any {
				return &models.SaleEvent{Name: "Festival " + uniquePath(""), LastSelectedAt: now}
			},
			count: countOf[models.SaleEvent],
		},
		{
			name: "sync_events",
			build: func(f *fixtures) any {
				return &models.SyncEvent{
					EventID:         uniquePath("sync"),
					EventType:       "sale",
					ActorUserID:     1,
					DeviceID:        "device-1",
					PayloadHash:     "hash",
					ClientCreatedAt: time.Now().UTC(),
				}
			},
			count: countOf[models.SyncEvent],
		},
		{
			name: "slideshow_settings",
			build: func(f *fixtures) any {
				return &models.SlideshowSettings{CollageShowPrices: true, UpdatedAt: now}
			},
			count: countOf[models.SlideshowSettings],
		},
		{
			name: "sale_event_state",
			build: func(f *fixtures) any {
				return &models.SaleEventState{EventID: f.event.ID, UpdatedAt: now}
			},
			count:          countOf[models.SaleEventState],
			needsCatalogue: true,
		},
		{
			name: "payment_qr_settings",
			build: func(f *fixtures) any {
				return &models.PaymentQRSettings{BankRemittanceText: "Merch-Kauf", UpdatedAt: now}
			},
			count: countOf[models.PaymentQRSettings],
		},
		{
			name: "admin_messages",
			build: func(f *fixtures) any {
				return &models.AdminMessage{
					SenderUsername: "seller",
					MessageType:    models.AdminMessageQuestion,
					Subject:        "Frage",
					Body:           "Wie storniere ich?",
					CreatedAt:      now,
				}
			},
			count: countOf[models.AdminMessage],
		},
		{
			name: "option_values",
			build: func(f *fixtures) any {
				return &models.OptionValue{OptionGroupID: f.optionGroup.ID, Value: "Rot", IsActive: true}
			},
			count:          countOf[models.OptionValue],
			needsCatalogue: true,
		},
		{
			name: "payment_qr_intents",
			build: func(f *fixtures) any {
				return &models.PaymentQRIntent{
					Token:           uniquePath("qr"),
					ReceiptID:       uniquePath("V"),
					SalePayloadJSON: "{}",
					CreatedByUserID: 1,
					ExpiresAt:       time.Now().UTC().Add(time.Hour),
				}
			},
			count: countOf[models.PaymentQRIntent],
		},
	}
}

// TestTenantIsolation is the load-bearing test of the whole multi-tenant
// design: for every band-scoped model, a record written in band A must be
// invisible, uncountable and unwritable from a scope on band B.
func TestTenantIsolation(t *testing.T) {
	gdb := openTestDB(t)

	bandA := createBand(t, gdb, "band-a")
	bandB := createBand(t, gdb, "band-b")

	ctxA := tenant.WithBand(context.Background(), bandA.ID)
	ctxB := tenant.WithBand(context.Background(), bandB.ID)

	for _, tc := range tenantCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtures(t, gdb, ctxA, bandA)

			record := tc.build(f)
			if err := gdb.WithContext(ctxA).Create(record).Error; err != nil {
				t.Fatalf("create in band A: %v", err)
			}

			gotA, err := tc.count(ctxA, gdb)
			if err != nil {
				t.Fatalf("count in band A: %v", err)
			}
			if gotA == 0 {
				t.Fatalf("band A cannot see its own %s", tc.name)
			}

			gotB, err := tc.count(ctxB, gdb)
			if err != nil {
				t.Fatalf("count in band B: %v", err)
			}
			if gotB != 0 {
				t.Fatalf("band B sees %d %s rows belonging to band A", gotB, tc.name)
			}
		})
	}
}

// TestMissingScopeIsRejected proves the callback fails loudly instead of
// running an unfiltered query — the failure mode that would leak data.
func TestMissingScopeIsRejected(t *testing.T) {
	gdb := openTestDB(t)

	var articles []models.Article
	err := gdb.WithContext(context.Background()).Find(&articles).Error
	if !errors.Is(err, tenant.ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for an unscoped read, got %v", err)
	}

	err = gdb.WithContext(context.Background()).Create(&models.Article{Name: "leak"}).Error
	if !errors.Is(err, tenant.ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for an unscoped create, got %v", err)
	}
}

// TestCrossBandCreateIsRejected proves a record cannot be smuggled into
// another band by presetting band_id.
func TestCrossBandCreateIsRejected(t *testing.T) {
	gdb := openTestDB(t)

	bandA := createBand(t, gdb, "mismatch-a")
	bandB := createBand(t, gdb, "mismatch-b")

	article := &models.Article{Name: "smuggled", IsActive: true, IsOffered: true}
	article.BandID = bandB.ID

	err := gdb.WithContext(tenant.WithBand(context.Background(), bandA.ID)).Create(article).Error
	if !errors.Is(err, tenant.ErrScopeMismatch) {
		t.Fatalf("expected ErrScopeMismatch, got %v", err)
	}
}

// TestReadOnlyGrantBlocksWrites proves a read_only support grant can read but
// never write, enforced at the database layer rather than only in a handler.
func TestReadOnlyGrantBlocksWrites(t *testing.T) {
	gdb := openTestDB(t)

	band := createBand(t, gdb, "readonly")
	ctx := tenant.WithGrant(context.Background(), band.ID, 42, true)

	var articles []models.Article
	if err := gdb.WithContext(ctx).Find(&articles).Error; err != nil {
		t.Fatalf("read under a read-only grant must work: %v", err)
	}

	err := gdb.WithContext(ctx).Create(&models.Article{Name: "nope", IsActive: true}).Error
	if !errors.Is(err, tenant.ErrReadOnlyScope) {
		t.Fatalf("expected ErrReadOnlyScope, got %v", err)
	}
}

// TestUpdateAndDeleteAreScoped proves that a band cannot modify or remove
// another band's rows even when it knows the primary key.
func TestUpdateAndDeleteAreScoped(t *testing.T) {
	gdb := openTestDB(t)

	bandA := createBand(t, gdb, "scoped-a")
	bandB := createBand(t, gdb, "scoped-b")
	ctxA := tenant.WithBand(context.Background(), bandA.ID)
	ctxB := tenant.WithBand(context.Background(), bandB.ID)

	article := &models.Article{Name: "target", IsActive: true, IsOffered: true}
	if err := gdb.WithContext(ctxA).Create(article).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	res := gdb.WithContext(ctxB).Model(&models.Article{}).
		Where("id = ?", article.ID).Update("name", "hijacked")
	if res.Error != nil {
		t.Fatalf("update: %v", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("band B updated %d of band A's articles", res.RowsAffected)
	}

	res = gdb.WithContext(ctxB).Where("id = ?", article.ID).Delete(&models.Article{})
	if res.Error != nil {
		t.Fatalf("delete: %v", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("band B deleted %d of band A's articles", res.RowsAffected)
	}

	var reloaded models.Article
	if err := gdb.WithContext(ctxA).First(&reloaded, article.ID).Error; err != nil {
		t.Fatalf("band A's article should still exist: %v", err)
	}
	if reloaded.Name != "target" {
		t.Fatalf("band A's article was modified to %q", reloaded.Name)
	}
}
