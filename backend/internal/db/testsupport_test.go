package db_test

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
)

// openTestDB connects to the MariaDB named by TEST_DATABASE_DSN and applies
// the migrations. Without that variable the integration tests skip, so
// `go test ./...` still works on a machine without a database.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set; skipping database integration test")
	}

	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("SECRET_KEY", "test-secret-key-that-is-long-enough-for-config")
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	gdb, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return gdb
}

// createBand inserts a tenant directly; bands are not themselves band-scoped.
func createBand(t *testing.T, gdb *gorm.DB, slug string) *models.Band {
	t.Helper()

	band := &models.Band{
		Slug:         slug + "-" + time.Now().UTC().Format("150405.000000"),
		Name:         slug,
		FeatureFlags: models.FeatureFlags{},
	}
	if err := gdb.WithContext(context.Background()).Create(band).Error; err != nil {
		t.Fatalf("create band %s: %v", slug, err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec("DELETE FROM bands WHERE id = ?", band.ID).Error
	})
	return band
}

// countOf counts rows of one model through the scoped handle. It is generic so
// the case table stays a list of models rather than a list of closures.
func countOf[T any](ctx context.Context, gdb *gorm.DB) (int64, error) {
	var total int64
	var model T
	err := gdb.WithContext(ctx).Model(&model).Count(&total).Error
	return total, err
}

// uniqueCounter backs uniquePath. A counter rather than a timestamp, because
// two fixtures created inside the same clock tick would otherwise collide on a
// unique key and make the isolation tests flaky.
var uniqueCounter atomic.Int64

// uniquePath produces a collision-free value for the globally unique file-path,
// token and combination-key columns.
func uniquePath(prefix string) string {
	return prefix + "-" + strconv.FormatInt(uniqueCounter.Add(1), 10) +
		"-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}

// newFixtures creates the article, variant, band transaction and sale event a
// case may depend on, all inside the given band's scope.
func newFixtures(t *testing.T, gdb *gorm.DB, ctx context.Context, band *models.Band) *fixtures {
	t.Helper()
	now := time.Now().UTC()
	today := models.NewDate(now.Year(), now.Month(), now.Day())

	article := &models.Article{Name: "Shirt " + uniquePath(""), IsActive: true, IsOffered: true}
	if err := gdb.WithContext(ctx).Create(article).Error; err != nil {
		t.Fatalf("fixture article: %v", err)
	}

	optionGroup := &models.OptionGroup{ArticleID: article.ID, Name: "Farbe", IsActive: true}
	if err := gdb.WithContext(ctx).Create(optionGroup).Error; err != nil {
		t.Fatalf("fixture option group: %v", err)
	}

	variant := &models.Variant{
		ArticleID:      article.ID,
		OptionValueIDs: models.JSONInt64Slice{},
		CombinationKey: uniquePath("combo"),
		SalePriceCents: 1800,
		IsActive:       true,
		IsOffered:      true,
	}
	if err := gdb.WithContext(ctx).Create(variant).Error; err != nil {
		t.Fatalf("fixture variant: %v", err)
	}

	txn := &models.BandTransaction{
		TransactionType: models.BandIncome,
		TransactionOn:   today,
		Category:        "Gage",
		Description:     "Fixture",
		AmountCents:     5000,
	}
	if err := gdb.WithContext(ctx).Create(txn).Error; err != nil {
		t.Fatalf("fixture band transaction: %v", err)
	}

	event := &models.SaleEvent{Name: "Gig " + uniquePath(""), LastSelectedAt: now}
	if err := gdb.WithContext(ctx).Create(event).Error; err != nil {
		t.Fatalf("fixture sale event: %v", err)
	}

	return &fixtures{
		band:        band,
		article:     article,
		optionGroup: optionGroup,
		variant:     variant,
		txn:         txn,
		event:       event,
	}
}
