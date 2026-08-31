package catalogue_test

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
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var counter atomic.Int64

func unique(prefix string) string {
	return prefix + strconv.FormatInt(counter.Add(1), 10) + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
}

// fixture is a catalogue service bound to a fresh, isolated band.
type fixture struct {
	t   *testing.T
	db  *gorm.DB
	svc *catalogue.Service
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set; skipping catalogue integration test")
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

	band := &models.Band{Slug: unique("cat-"), Name: "Catalogue Test", IsActive: true}
	if err := database.WithContext(tenant.WithCrossBandAccess(context.Background())).Create(band).Error; err != nil {
		t.Fatalf("create band: %v", err)
	}

	t.Cleanup(func() {
		for _, stmt := range []string{
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
		t:   t,
		db:  database,
		svc: catalogue.NewService(database),
		ctx: tenant.WithBand(context.Background(), band.ID),
	}
}

// variants reads an article's variants ordered by key, active first.
func (f *fixture) variants(articleID int64) []models.Variant {
	f.t.Helper()
	var variants []models.Variant
	if err := f.db.WithContext(f.ctx).
		Where("article_id = ?", articleID).
		Order("combination_key").Find(&variants).Error; err != nil {
		f.t.Fatalf("read variants: %v", err)
	}
	return variants
}

func (f *fixture) activeVariants(articleID int64) []models.Variant {
	f.t.Helper()
	out := make([]models.Variant, 0)
	for _, variant := range f.variants(articleID) {
		if variant.IsActive {
			out = append(out, variant)
		}
	}
	return out
}

// addOptionGroup creates a group with values and returns the group and value IDs.
func (f *fixture) addOptionGroup(articleID int64, name string, position int, values ...string) (int64, []int64) {
	f.t.Helper()

	group := &models.OptionGroup{ArticleID: articleID, Name: name, Position: position, IsActive: true}
	if err := f.db.WithContext(f.ctx).Create(group).Error; err != nil {
		f.t.Fatalf("create option group: %v", err)
	}
	ids := make([]int64, 0, len(values))
	for i, value := range values {
		optionValue := &models.OptionValue{OptionGroupID: group.ID, Value: value, Position: i, IsActive: true}
		if err := f.db.WithContext(f.ctx).Create(optionValue).Error; err != nil {
			f.t.Fatalf("create option value: %v", err)
		}
		ids = append(ids, optionValue.ID)
	}
	return group.ID, ids
}

// TestCreateArticleSeedsDefaultGrid pins the shape a band gets when it adds an
// article: Farbe x Größe, ten sellable variants, priced from the article.
func TestCreateArticleSeedsDefaultGrid(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, "Geometry Shirt", 1800, 900)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}

	active := f.activeVariants(article.ID)
	if len(active) != 10 {
		t.Fatalf("expected 10 variants for 2 colours x 5 sizes, got %d", len(active))
	}
	for _, variant := range active {
		if variant.SalePriceCents != 1800 || variant.DefaultPurchasePriceCents != 900 {
			t.Fatalf("variant %d did not inherit the article prices: %+v", variant.ID, variant)
		}
		if len(variant.OptionValueIDs) != 2 {
			t.Fatalf("variant %d should carry one value per group, got %v", variant.ID, variant.OptionValueIDs)
		}
	}
}

// TestArticleNamesAreUniquePerBand pins that the uniqueness is scoped, and
// case-insensitive as it was under SQLite's NOCASE collation.
func TestArticleNamesAreUniquePerBand(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateArticle(f.ctx, "Hoodie", 4500, 2500); err != nil {
		t.Fatalf("create article: %v", err)
	}
	if _, err := f.svc.CreateArticle(f.ctx, "hoodie", 4500, 2500); err == nil {
		t.Fatal("a duplicate article name must be rejected regardless of case")
	}
}

// TestSyncVariantsDeactivatesInsteadOfDeleting is the rule the whole catalogue
// rests on: a retired combination keeps its record so historic receipts stay
// readable, and it comes back with its price and history intact.
func TestSyncVariantsDeactivatesInsteadOfDeleting(t *testing.T) {
	f := newFixture(t)

	article := &models.Article{Name: unique("Shirt "), IsOffered: true, IsActive: true, DefaultSalePriceCents: 1800}
	if err := f.db.WithContext(f.ctx).Create(article).Error; err != nil {
		t.Fatalf("create article: %v", err)
	}
	_, colourIDs := f.addOptionGroup(article.ID, "Farbe", 0, "Schwarz", "Weiß")

	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := len(f.activeVariants(article.ID)); got != 2 {
		t.Fatalf("expected 2 variants, got %d", got)
	}

	// Give the white variant its own price, then retire the colour.
	whiteKey := catalogue.CombinationKey([]int64{colourIDs[1]})
	if err := f.db.WithContext(f.ctx).Model(&models.Variant{}).
		Where("article_id = ? AND combination_key = ?", article.ID, whiteKey).
		Update("sale_price_cents", 2200).Error; err != nil {
		t.Fatalf("set price: %v", err)
	}
	if err := f.db.WithContext(f.ctx).Model(&models.OptionValue{}).
		Where("id = ?", colourIDs[1]).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate value: %v", err)
	}
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync after retiring: %v", err)
	}

	if got := len(f.activeVariants(article.ID)); got != 1 {
		t.Fatalf("expected 1 active variant, got %d", got)
	}
	if got := len(f.variants(article.ID)); got != 2 {
		t.Fatalf("the retired variant must still exist, found %d rows", got)
	}

	// Bringing the colour back must restore the same record, price included.
	if err := f.db.WithContext(f.ctx).Model(&models.OptionValue{}).
		Where("id = ?", colourIDs[1]).Update("is_active", true).Error; err != nil {
		t.Fatalf("reactivate value: %v", err)
	}
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync after restoring: %v", err)
	}

	var restored models.Variant
	if err := f.db.WithContext(f.ctx).
		Where("article_id = ? AND combination_key = ?", article.ID, whiteKey).
		First(&restored).Error; err != nil {
		t.Fatalf("read restored variant: %v", err)
	}
	if !restored.IsActive {
		t.Fatal("the returning combination must be reactivated")
	}
	if restored.SalePriceCents != 2200 {
		t.Fatalf("the individual price must survive, got %d", restored.SalePriceCents)
	}
	if got := len(f.variants(article.ID)); got != 2 {
		t.Fatalf("no duplicate variant may be created, found %d rows", got)
	}
}

// TestIncompleteOptionGroupParksEveryVariant pins the deliberate choice that a
// half-configured article sells nothing, rather than silently dropping the
// empty dimension and creating variants the band never defined.
func TestIncompleteOptionGroupParksEveryVariant(t *testing.T) {
	f := newFixture(t)

	article := &models.Article{Name: unique("Cap "), IsOffered: true, IsActive: true}
	if err := f.db.WithContext(f.ctx).Create(article).Error; err != nil {
		t.Fatalf("create article: %v", err)
	}
	f.addOptionGroup(article.ID, "Farbe", 0, "Schwarz")
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := len(f.activeVariants(article.ID)); got != 1 {
		t.Fatalf("expected 1 variant, got %d", got)
	}

	f.addOptionGroup(article.ID, "Material", 1) // no values yet
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync with incomplete group: %v", err)
	}
	if got := len(f.activeVariants(article.ID)); got != 0 {
		t.Fatalf("an incomplete article must have no active variants, got %d", got)
	}
	if got := len(f.variants(article.ID)); got != 1 {
		t.Fatalf("the existing variant must be parked, not deleted, found %d rows", got)
	}
}

// TestPreserveVariantsForNewOptionGroups is the migration that stops a band
// from losing stock, prices and photos the moment they add a new dimension to
// an article mid-season.
func TestPreserveVariantsForNewOptionGroups(t *testing.T) {
	f := newFixture(t)

	article := &models.Article{Name: unique("Longsleeve "), IsOffered: true, IsActive: true}
	if err := f.db.WithContext(f.ctx).Create(article).Error; err != nil {
		t.Fatalf("create article: %v", err)
	}
	_, colourIDs := f.addOptionGroup(article.ID, "Farbe", 0, "Schwarz", "Weiß")
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}

	blackKey := catalogue.CombinationKey([]int64{colourIDs[0]})
	var black models.Variant
	if err := f.db.WithContext(f.ctx).
		Where("article_id = ? AND combination_key = ?", article.ID, blackKey).
		First(&black).Error; err != nil {
		t.Fatalf("read variant: %v", err)
	}
	if err := f.db.WithContext(f.ctx).Model(&black).Update("sale_price_cents", 2600).Error; err != nil {
		t.Fatalf("set price: %v", err)
	}

	// Book stock against it, so losing the variant would visibly lose history.
	purchase := &models.Purchase{
		ReceiptID: "E-TEST-1", VariantID: black.ID, Quantity: 12, UnitCostCents: 1200,
		PurchasedOn: models.NewDate(2026, time.August, 1),
	}
	if err := f.db.WithContext(f.ctx).Create(purchase).Error; err != nil {
		t.Fatalf("create purchase: %v", err)
	}

	// Add a size dimension and migrate onto its first value before syncing.
	_, sizeIDs := f.addOptionGroup(article.ID, "Größe", 1, "M", "L")
	if err := f.svc.PreserveVariantsForNewOptionGroups(f.ctx, article.ID, []int64{sizeIDs[0]}); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if err := f.svc.SyncVariants(f.ctx, article.ID); err != nil {
		t.Fatalf("sync after adding a dimension: %v", err)
	}

	if got := len(f.activeVariants(article.ID)); got != 4 {
		t.Fatalf("expected 2 colours x 2 sizes = 4 variants, got %d", got)
	}

	migratedKey := catalogue.CombinationKey([]int64{colourIDs[0], sizeIDs[0]})
	var migrated models.Variant
	if err := f.db.WithContext(f.ctx).
		Where("article_id = ? AND combination_key = ?", article.ID, migratedKey).
		First(&migrated).Error; err != nil {
		t.Fatalf("read migrated variant: %v", err)
	}
	if migrated.ID != black.ID {
		t.Fatalf("the original variant record must be reused, got %d instead of %d", migrated.ID, black.ID)
	}
	if migrated.SalePriceCents != 2600 {
		t.Fatalf("the individual price must survive the migration, got %d", migrated.SalePriceCents)
	}

	stock, err := f.svc.StockMap(f.ctx)
	if err != nil {
		t.Fatalf("stock map: %v", err)
	}
	if stock[black.ID].OnHand != 12 {
		t.Fatalf("the booked stock must follow the variant, got %+v", stock[black.ID])
	}
}

// TestStockIsDerivedFromMovements pins that stock is never a stored counter
// and that a cancellation is excluded from it.
func TestStockIsDerivedFromMovements(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Beanie "), 1500, 700)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	variant := f.activeVariants(article.ID)[0]
	today := models.NewDate(2026, time.August, 27)

	create := func(record any) {
		t.Helper()
		if err := f.db.WithContext(f.ctx).Create(record).Error; err != nil {
			t.Fatalf("create record: %v", err)
		}
	}
	create(&models.Purchase{
		ReceiptID: "E-1", VariantID: variant.ID, Quantity: 20, UnitCostCents: 700, PurchasedOn: today,
	})
	create(&models.Sale{
		ReceiptID: "V-1", VariantID: variant.ID, Quantity: 3, UnitPriceCents: 1500,
		AmountDueCents: 4500, PaymentMethod: models.PaymentMethodCash, IsPaid: true, IsReceived: true,
		DeliveryStatus: models.DeliveryNotApplicable, SoldOn: today,
	})
	create(&models.Sale{
		ReceiptID: "V-2", VariantID: variant.ID, Quantity: 5, UnitPriceCents: 1500,
		AmountDueCents: 7500, PaymentMethod: models.PaymentMethodCash, IsPaid: true, IsReceived: true,
		DeliveryStatus: models.DeliveryNotApplicable, SoldOn: today, IsCancelled: true,
	})

	stock, err := f.svc.StockMap(f.ctx)
	if err != nil {
		t.Fatalf("stock map: %v", err)
	}
	got := stock[variant.ID]
	if got.Purchased != 20 || got.Sold != 3 || got.OnHand != 17 {
		t.Fatalf("a cancelled sale must not consume stock: %+v", got)
	}
}

func TestIsAtOrBelowMinimum(t *testing.T) {
	zero, five := 0, 5
	cases := []struct {
		name    string
		onHand  int64
		minimum *int
		want    bool
	}{
		// No threshold configured means the band never wants a warning here.
		{"no threshold", 0, nil, false},
		// An explicit zero stays meaningful: warn only once actually sold out.
		{"zero threshold, sold out", 0, &zero, true},
		{"zero threshold, one left", 1, &zero, false},
		{"below threshold", 4, &five, true},
		{"at threshold", 5, &five, true},
		{"above threshold", 6, &five, false},
		{"negative stock still warns", -2, &zero, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogue.IsAtOrBelowMinimum(tc.onHand, tc.minimum); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
