package catalogue_test

import (
	"errors"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

func ptr[T any](v T) *T { return &v }

// groupsOf reads an article's option groups with their values, active first.
func (f *fixture) groupsOf(articleID int64) []models.OptionGroup {
	f.t.Helper()
	var groups []models.OptionGroup
	if err := f.db.WithContext(f.ctx).
		Where("article_id = ?", articleID).Order("position, id").
		Find(&groups).Error; err != nil {
		f.t.Fatalf("read groups: %v", err)
	}
	return groups
}

func (f *fixture) valuesOf(groupID int64) []models.OptionValue {
	f.t.Helper()
	var values []models.OptionValue
	if err := f.db.WithContext(f.ctx).
		Where("option_group_id = ?", groupID).Order("position, id").
		Find(&values).Error; err != nil {
		f.t.Fatalf("read values: %v", err)
	}
	return values
}

// TestRenamingAnOptionValueIsRetroactive pins the behaviour the band asked
// for: renaming "Schwarz" to "Black" also changes what old receipts show,
// because the receipt points at the value rather than copying its text.
func TestRenamingAnOptionValueIsRetroactive(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Shirt "), 1800, 900)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	groups := f.groupsOf(article.ID)
	colour := groups[0]
	values := f.valuesOf(colour.ID)

	cfg := catalogue.ArticleConfiguration{
		OptionGroups: []catalogue.OptionGroupInput{
			{ID: colour.ID, Name: "Farbe", Values: []catalogue.OptionValueInput{
				{ID: values[0].ID, Value: "Black"},
				{ID: values[1].ID, Value: values[1].Value},
			}},
			{ID: groups[1].ID, Name: groups[1].Name, Values: inputsFrom(f.valuesOf(groups[1].ID))},
		},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	renamed := f.valuesOf(colour.ID)
	if renamed[0].Value != "Black" {
		t.Fatalf("expected the rename to apply, got %q", renamed[0].Value)
	}
	if renamed[0].ID != values[0].ID {
		t.Fatal("a rename must keep the same record, otherwise old receipts lose their reference")
	}
	if got := len(f.activeVariants(article.ID)); got != 10 {
		t.Fatalf("a rename must not change the variant grid, got %d", got)
	}
}

// TestDroppingAnOptionValueDeactivatesIt pins that removal is never a delete.
func TestDroppingAnOptionValueDeactivatesIt(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Shirt "), 1800, 900)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	groups := f.groupsOf(article.ID)
	sizes := f.valuesOf(groups[1].ID)

	// Keep only the first two sizes.
	cfg := catalogue.ArticleConfiguration{
		OptionGroups: []catalogue.OptionGroupInput{
			{ID: groups[0].ID, Name: groups[0].Name, Values: inputsFrom(f.valuesOf(groups[0].ID))},
			{ID: groups[1].ID, Name: groups[1].Name, Values: inputsFrom(sizes[:2])},
		},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	stored := f.valuesOf(groups[1].ID)
	if len(stored) != 5 {
		t.Fatalf("dropped values must still exist, found %d", len(stored))
	}
	active := 0
	for _, value := range stored {
		if value.IsActive {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("expected 2 active sizes, got %d", active)
	}
	if got := len(f.activeVariants(article.ID)); got != 4 {
		t.Fatalf("expected 2 colours x 2 sizes = 4 variants, got %d", got)
	}
}

// TestAddingADimensionKeepsExistingVariants is the end-to-end version of the
// preservation migration, driven through the save path the UI uses.
func TestAddingADimensionKeepsExistingVariants(t *testing.T) {
	f := newFixture(t)

	article := &models.Article{Name: unique("Cap "), IsActive: true, IsOffered: true, DefaultSalePriceCents: 1500}
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
		Where("article_id = ? AND combination_key = ?", article.ID, blackKey).First(&black).Error; err != nil {
		t.Fatalf("read variant: %v", err)
	}

	groups := f.groupsOf(article.ID)
	cfg := catalogue.ArticleConfiguration{
		OptionGroups: []catalogue.OptionGroupInput{
			{ID: groups[0].ID, Name: "Farbe", Values: inputsFrom(f.valuesOf(groups[0].ID))},
			{Name: "Größe", Values: []catalogue.OptionValueInput{{Value: "M"}, {Value: "L"}}},
		},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := len(f.activeVariants(article.ID)); got != 4 {
		t.Fatalf("expected 4 variants, got %d", got)
	}

	var reloaded models.Variant
	if err := f.db.WithContext(f.ctx).First(&reloaded, black.ID).Error; err != nil {
		t.Fatalf("read migrated variant: %v", err)
	}
	if !reloaded.IsActive {
		t.Fatal("the existing variant must be migrated onto the new dimension, not parked")
	}
	if len(reloaded.OptionValueIDs) != 2 {
		t.Fatalf("the migrated variant must carry both dimensions, got %v", reloaded.OptionValueIDs)
	}
}

// TestVariantOverrides pins the per-variant fields the management page edits,
// including the tri-state minimum stock.
func TestVariantOverrides(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Hoodie "), 4500, 2500)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	variant := f.activeVariants(article.ID)[0]

	cfg := catalogue.ArticleConfiguration{
		Variants: []catalogue.VariantInput{{
			ID:             variant.ID,
			SalePriceCents: ptr(int64(5200)),
			MinimumStock:   ptr(0),
			NoReorder:      ptr(true),
			IsOffered:      ptr(false),
		}},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var stored models.Variant
	if err := f.db.WithContext(f.ctx).First(&stored, variant.ID).Error; err != nil {
		t.Fatalf("read variant: %v", err)
	}
	if stored.SalePriceCents != 5200 || !stored.NoReorder || stored.IsOffered {
		t.Fatalf("overrides were not applied: %+v", stored)
	}
	// An explicit zero must survive as zero, not collapse into "no warning".
	if stored.MinimumStock == nil || *stored.MinimumStock != 0 {
		t.Fatalf("an explicit zero threshold must be stored: %v", stored.MinimumStock)
	}

	clear := catalogue.ArticleConfiguration{
		Variants: []catalogue.VariantInput{{ID: variant.ID, ClearMinimum: true}},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, clear); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := f.db.WithContext(f.ctx).First(&stored, variant.ID).Error; err != nil {
		t.Fatalf("read variant: %v", err)
	}
	if stored.MinimumStock != nil {
		t.Fatalf("clearing must remove the threshold, got %v", *stored.MinimumStock)
	}
}

// TestConfigurationRejectsForeignEntities stops a client from editing another
// article's options by guessing an ID.
func TestConfigurationRejectsForeignEntities(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.CreateArticle(f.ctx, unique("A "), 1000, 500)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := f.svc.CreateArticle(f.ctx, unique("B "), 1000, 500)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	foreignGroup := f.groupsOf(second.ID)[0]
	cfg := catalogue.ArticleConfiguration{
		OptionGroups: []catalogue.OptionGroupInput{
			{ID: foreignGroup.ID, Name: "Farbe", Values: []catalogue.OptionValueInput{{Value: "Rot"}}},
		},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, first.ID, cfg); !errors.Is(err, catalogue.ErrUnknownEntity) {
		t.Fatalf("expected a foreign option group to be rejected, got %v", err)
	}

	foreignVariant := f.activeVariants(second.ID)[0]
	cfg = catalogue.ArticleConfiguration{
		Variants: []catalogue.VariantInput{{ID: foreignVariant.ID, SalePriceCents: ptr(int64(1))}},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, first.ID, cfg); !errors.Is(err, catalogue.ErrUnknownEntity) {
		t.Fatalf("expected a foreign variant to be rejected, got %v", err)
	}
}

// TestConfigurationIsAtomic pins that a rejected save leaves nothing behind.
func TestConfigurationIsAtomic(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Shirt "), 1800, 900)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before := len(f.groupsOf(article.ID))

	cfg := catalogue.ArticleConfiguration{
		OptionGroups: []catalogue.OptionGroupInput{
			{Name: "Material", Values: []catalogue.OptionValueInput{{Value: "Baumwolle"}}},
			{Name: "", Values: []catalogue.OptionValueInput{{Value: "kaputt"}}},
		},
	}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err == nil {
		t.Fatal("an empty option name must be rejected")
	}
	if got := len(f.groupsOf(article.ID)); got != before {
		t.Fatalf("the rejected save must leave no new groups, had %d now %d", before, got)
	}
}

// TestWithdrawingAnArticleKeepsItsHistory pins that is_offered is an
// assortment switch, not a delete.
func TestWithdrawingAnArticleKeepsItsHistory(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Retired "), 1800, 900)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variantsBefore := len(f.activeVariants(article.ID))

	cfg := catalogue.ArticleConfiguration{IsOffered: ptr(false)}
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var stored models.Article
	if err := f.db.WithContext(f.ctx).First(&stored, article.ID).Error; err != nil {
		t.Fatalf("read article: %v", err)
	}
	if stored.IsOffered {
		t.Fatal("the article should have left the assortment")
	}
	if !stored.IsActive {
		t.Fatal("leaving the assortment must not deactivate the article")
	}
	if got := len(f.activeVariants(article.ID)); got != variantsBefore {
		t.Fatalf("variants must stay available for purchases and balances, got %d", got)
	}
}

// inputsFrom converts stored values into the input shape, keeping their IDs so
// they are updated rather than recreated.
func inputsFrom(values []models.OptionValue) []catalogue.OptionValueInput {
	out := make([]catalogue.OptionValueInput, 0, len(values))
	for _, value := range values {
		if !value.IsActive {
			continue
		}
		out = append(out, catalogue.OptionValueInput{ID: value.ID, Value: value.Value})
	}
	return out
}

// TestStandardPriceFollowsThroughToUntouchedVariants pins what makes the
// article's standard price useful at all.
//
// The combinations of a new article are generated before anyone has typed a
// price, so without this rule they keep their initial zero forever and the
// sales terminal offers every variant for 0,00 while the article claims a
// price. A variant priced by hand differs from the old default and must keep
// its own value. It mirrors _old/app.py:11826.
func TestStandardPriceFollowsThroughToUntouchedVariants(t *testing.T) {
	f := newFixture(t)

	article, err := f.svc.CreateArticle(f.ctx, unique("Cap "), 0, 0)
	if err != nil {
		t.Fatalf("create article: %v", err)
	}

	// One variant gets a price of its own; the rest stay at the default.
	variants := f.activeVariants(article.ID)
	if len(variants) < 2 {
		t.Fatalf("expected the seeded grid, got %d variants", len(variants))
	}
	custom := variants[0]
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, catalogue.ArticleConfiguration{
		Variants: []catalogue.VariantInput{{ID: custom.ID, SalePriceCents: ptr(int64(2500))}},
	}); err != nil {
		t.Fatalf("price one variant: %v", err)
	}

	// Now the article's standard price is set for the first time.
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, catalogue.ArticleConfiguration{
		DefaultSalePriceCents: ptr(int64(500)),
	}); err != nil {
		t.Fatalf("set standard price: %v", err)
	}

	for _, variant := range f.activeVariants(article.ID) {
		want := int64(500)
		if variant.ID == custom.ID {
			want = 2500
		}
		if variant.SalePriceCents != want {
			t.Fatalf("variant %d: got %d, want %d", variant.ID, variant.SalePriceCents, want)
		}
	}

	// A later change moves the followers again and still leaves the custom one.
	if err := f.svc.ApplyConfiguration(f.ctx, article.ID, catalogue.ArticleConfiguration{
		DefaultSalePriceCents: ptr(int64(700)),
	}); err != nil {
		t.Fatalf("raise standard price: %v", err)
	}
	for _, variant := range f.activeVariants(article.ID) {
		want := int64(700)
		if variant.ID == custom.ID {
			want = 2500
		}
		if variant.SalePriceCents != want {
			t.Fatalf("after the raise, variant %d: got %d, want %d", variant.ID, variant.SalePriceCents, want)
		}
	}
}
