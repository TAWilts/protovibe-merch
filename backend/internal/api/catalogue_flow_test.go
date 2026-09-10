package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// TestArticleLifecycleOverHTTP walks the management page's happy path: create
// an article, adjust its options, and see the variant grid follow.
func TestArticleLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name":                     "Geometry Shirt",
		"default_sale_price_cents": 1800,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create article: %d %v", created.Status, created.Body)
	}
	if got := len(jsonList(created.Body, "variants")); got != 10 {
		t.Fatalf("a new article starts with a 2x5 grid, got %d variants", got)
	}
	articleID := int64(created.Body["id"].(float64))

	// Rename one option, add a size and give one variant its own price. A
	// generated configuration may be extended but no longer reduced.
	groups := jsonList(created.Body, "option_groups")
	colour := jsonObject(groups[0])
	size := jsonObject(groups[1])
	sizeValues := jsonList(size, "values")
	sizeValues = append(sizeValues, map[string]any{"id": 0, "value": "XXL"})

	variant := jsonObject(jsonList(created.Body, "variants")[0])
	saved := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"option_groups": []any{
			map[string]any{"id": colour["id"], "name": "Colour", "values": jsonList(colour, "values")},
			map[string]any{"id": size["id"], "name": size["name"], "values": sizeValues},
		},
		"variants": []any{
			map[string]any{"id": variant["id"], "sale_price_cents": 2200, "minimum_stock": 3},
		},
	})
	if saved.Status != http.StatusOK {
		t.Fatalf("save article: %d %v", saved.Status, saved.Body)
	}

	active := 0
	for _, raw := range jsonList(saved.Body, "variants") {
		if jsonObject(raw)["is_active"] == true {
			active++
		}
	}
	if active != 12 {
		t.Fatalf("expected 2 colours x 6 sizes = 12 active variants, got %d", active)
	}
	if got := len(jsonList(saved.Body, "variants")); got != 12 {
		t.Fatalf("all variants must remain readable, got %d rows", got)
	}
	if saved.Body["configuration_locked"] != true {
		t.Fatalf("a generated configuration must be locked: %v", saved.Body)
	}
}

// TestArticleDraftDefersVariants pins the article-management workflow: the
// option grid is editable immediately, but its derived variants only exist
// after the manager confirms the configuration for the first time.
func TestArticleDraftDefersVariants(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name":                     "Draft Shirt",
		"default_sale_price_cents": 1800,
		"defer_variants":           true,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create draft: %d %v", created.Status, created.Body)
	}
	if got := len(jsonList(created.Body, "variants")); got != 0 {
		t.Fatalf("an unconfirmed draft must not have variants, got %d", got)
	}
	if created.Body["configuration_complete"] != false {
		t.Fatalf("an unconfirmed draft must be incomplete: %v", created.Body)
	}
	if created.Body["configuration_locked"] != false {
		t.Fatalf("an unconfirmed draft must remain structurally editable: %v", created.Body)
	}

	if res := h.do(http.MethodGet, "/api/v1/assortment", nil); len(jsonList(res.Body, "articles")) != 0 {
		t.Fatalf("an unconfirmed draft must not be sellable: %v", res.Body)
	}

	articleID := int64(created.Body["id"].(float64))
	saved := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"option_groups": jsonList(created.Body, "option_groups"),
	})
	if saved.Status != http.StatusOK {
		t.Fatalf("confirm draft: %d %v", saved.Status, saved.Body)
	}
	if got := len(jsonList(saved.Body, "variants")); got != 10 {
		t.Fatalf("the first confirmation must generate the 2x5 grid, got %d", got)
	}
	if saved.Body["configuration_complete"] != true {
		t.Fatalf("the confirmed article must be complete: %v", saved.Body)
	}
	if saved.Body["configuration_locked"] != true {
		t.Fatalf("the first generated grid must lock structural removals: %v", saved.Body)
	}
	for _, raw := range jsonList(saved.Body, "variants") {
		if jsonObject(raw)["is_active"] != true {
			t.Fatalf("the first generated grid must not contain retired variants: %v", saved.Body)
		}
	}
}

func TestLockedArticleRejectsOptionRemovalAtomically(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": "Locked HTTP Shirt", "default_sale_price_cents": 1800,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	articleID := int64(created.Body["id"].(float64))
	groups := jsonList(created.Body, "option_groups")
	colour := jsonObject(groups[0])
	size := jsonObject(groups[1])
	sizeValues := jsonList(size, "values")

	blocked := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"name": "Must Not Persist",
		"option_groups": []any{
			map[string]any{"id": colour["id"], "name": colour["name"], "values": jsonList(colour, "values")},
			map[string]any{"id": size["id"], "name": size["name"], "values": sizeValues[:len(sizeValues)-1]},
		},
	})
	if blocked.Status != http.StatusConflict || blocked.Body["code"] != "option_removal_locked" {
		t.Fatalf("expected locked removal conflict, got %d %v", blocked.Status, blocked.Body)
	}

	reloaded := h.do(http.MethodGet, "/api/v1/articles/"+itoa(articleID), nil)
	if reloaded.Status != http.StatusOK || reloaded.Body["name"] != "Locked HTTP Shirt" {
		t.Fatalf("a rejected save must not partially update the article: %d %v", reloaded.Status, reloaded.Body)
	}
	if got := len(jsonList(reloaded.Body, "variants")); got != 10 {
		t.Fatalf("a rejected save must preserve all variants, got %d", got)
	}
}

func TestIncompleteArticleAlwaysReturnsArraysAndCanBeDeleted(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	article := &models.Article{Name: "Legacy Draft", IsOffered: true, IsActive: true}
	bandCtx := tenant.WithBand(h.ctx(), band.ID)
	if err := h.db.WithContext(bandCtx).Create(article).Error; err != nil {
		t.Fatalf("create legacy article: %v", err)
	}
	group := &models.OptionGroup{ArticleID: article.ID, Name: "Legacy Option", IsActive: true}
	if err := h.db.WithContext(bandCtx).Create(group).Error; err != nil {
		t.Fatalf("create empty option group: %v", err)
	}

	loaded := h.do(http.MethodGet, "/api/v1/articles/"+itoa(article.ID), nil)
	if loaded.Status != http.StatusOK || loaded.Body["configuration_complete"] != false {
		t.Fatalf("legacy draft must remain editable: %d %v", loaded.Status, loaded.Body)
	}
	groups := jsonList(loaded.Body, "option_groups")
	if len(groups) != 1 {
		t.Fatalf("expected one option group: %v", loaded.Body)
	}
	if values, ok := jsonObject(groups[0])["values"].([]any); !ok || len(values) != 0 {
		t.Fatalf("an empty option group must be encoded as [], got %T %v", jsonObject(groups[0])["values"], jsonObject(groups[0])["values"])
	}
	if variants, ok := loaded.Body["variants"].([]any); !ok || len(variants) != 0 {
		t.Fatalf("an empty variant list must be encoded as [], got %T %v", loaded.Body["variants"], loaded.Body["variants"])
	}

	deleted := h.do(http.MethodDelete, "/api/v1/articles/"+itoa(article.ID), nil)
	if deleted.Status != http.StatusNoContent {
		t.Fatalf("delete legacy draft: %d %v", deleted.Status, deleted.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/articles/"+itoa(article.ID), nil); res.Status != http.StatusNotFound {
		t.Fatalf("deleted draft must be gone: %d %v", res.Status, res.Body)
	}
	var logged int64
	if err := h.db.Raw("SELECT COUNT(*) FROM audit_log WHERE band_id = ? AND action = ? AND entity_id = ?",
		band.ID, audit.ActionArticleDeleted, article.ID).Scan(&logged).Error; err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if logged != 1 {
		t.Fatalf("expected one article deletion audit entry, got %d", logged)
	}
}

func TestArticleDeletionRejectsCompleteAndUsedDrafts(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	complete, _ := h.sellableArticle("Complete Article")
	if res := h.do(http.MethodDelete, "/api/v1/articles/"+itoa(complete), nil); res.Status != http.StatusConflict || res.Body["code"] != "article_not_incomplete" {
		t.Fatalf("complete article must not be deleted: %d %v", res.Status, res.Body)
	}

	article := &models.Article{Name: "Used Draft", IsOffered: true, IsActive: true}
	bandCtx := tenant.WithBand(h.ctx(), band.ID)
	if err := h.db.WithContext(bandCtx).Create(article).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}
	variant := &models.Variant{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{}, CombinationKey: "", IsActive: false}
	if err := h.db.WithContext(bandCtx).Create(variant).Error; err != nil {
		t.Fatalf("create retired variant: %v", err)
	}
	purchase := &models.Purchase{
		ReceiptID: "E-DRAFT", VariantID: variant.ID, Quantity: 1, UnitCostCents: 100,
		LineTotalCostCents: 100,
		PurchasedOn:        models.NewDate(2026, time.September, 9),
	}
	if err := h.db.WithContext(bandCtx).Create(purchase).Error; err != nil {
		t.Fatalf("create purchase reference: %v", err)
	}
	if res := h.do(http.MethodDelete, "/api/v1/articles/"+itoa(article.ID), nil); res.Status != http.StatusConflict || res.Body["code"] != "article_in_use" {
		t.Fatalf("used draft must not be deleted: %d %v", res.Status, res.Body)
	}

	h.signInAs(band, models.RoleSeller)
	if res := h.do(http.MethodDelete, "/api/v1/articles/"+itoa(article.ID), nil); res.Status != http.StatusForbidden {
		t.Fatalf("seller must not delete drafts: %d %v", res.Status, res.Body)
	}
}

// TestSellersCannotChangeTheCatalogue pins the role split: reading the
// assortment is a seller's job, editing it is a manager's.
func TestSellersCannotChangeTheCatalogue(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleSeller)

	if res := h.do(http.MethodGet, "/api/v1/assortment", nil); res.Status != http.StatusOK {
		t.Fatalf("a seller must be able to read the assortment: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/articles", map[string]any{"name": "Nope"})
	if res.Status != http.StatusForbidden {
		t.Fatalf("a seller must not create articles, got %d %v", res.Status, res.Body)
	}
}

// TestAssortmentHidesWithdrawnAndIncompleteArticles pins that the sales page
// only ever offers what can actually be sold.
func TestAssortmentHidesWithdrawnAndIncompleteArticles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": "Withdrawn Shirt", "default_sale_price_cents": 1800,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	articleID := int64(created.Body["id"].(float64))

	if res := h.do(http.MethodGet, "/api/v1/assortment", nil); len(jsonList(res.Body, "articles")) != 1 {
		t.Fatalf("the new article should be on offer: %v", res.Body)
	}

	withdrawn := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{"is_offered": false})
	if withdrawn.Status != http.StatusOK {
		t.Fatalf("withdraw: %d %v", withdrawn.Status, withdrawn.Body)
	}

	res := h.do(http.MethodGet, "/api/v1/assortment", nil)
	if got := len(jsonList(res.Body, "articles")); got != 0 {
		t.Fatalf("a withdrawn article must leave the assortment, got %d", got)
	}
	// It must still be visible to management, with its history intact.
	if res := h.do(http.MethodGet, "/api/v1/articles", nil); len(jsonList(res.Body, "articles")) != 1 {
		t.Fatalf("management must still see the article: %v", res.Body)
	}
}

// TestCatalogueIsBandScopedOverHTTP is the tenant boundary seen from outside:
// one band's articles must be invisible and unreachable to another.
func TestCatalogueIsBandScopedOverHTTP(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": "Band A Shirt", "default_sale_price_cents": 1800,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	articleID := int64(created.Body["id"].(float64))

	h.signInAs(bandB, models.RoleManager)
	if res := h.do(http.MethodGet, "/api/v1/articles", nil); len(jsonList(res.Body, "articles")) != 0 {
		t.Fatalf("band B must not see band A's catalogue: %v", res.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/articles/"+itoa(articleID), nil); res.Status != http.StatusNotFound {
		t.Fatalf("band B must not read band A's article by ID, got %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{"name": "hijacked"})
	if res.Status != http.StatusNotFound {
		t.Fatalf("band B must not edit band A's article, got %d %v", res.Status, res.Body)
	}

	// Band A's article is untouched.
	h.signInAs(bandA, models.RoleManager)
	reloaded := h.do(http.MethodGet, "/api/v1/articles/"+itoa(articleID), nil)
	if reloaded.Body["name"] != "Band A Shirt" {
		t.Fatalf("band A's article was modified: %v", reloaded.Body)
	}
}
