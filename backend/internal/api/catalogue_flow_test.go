package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// TestArticleLifecycleOverHTTP walks the management page's happy path: create
// an article, adjust its options, and see the variant grid follow.
func TestArticleLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name":                         "Geometry Shirt",
		"default_sale_price_cents":     1800,
		"default_purchase_price_cents": 900,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create article: %d %v", created.Status, created.Body)
	}
	if got := len(jsonList(created.Body, "variants")); got != 10 {
		t.Fatalf("a new article starts with a 2x5 grid, got %d variants", got)
	}
	articleID := int64(created.Body["id"].(float64))

	// Reduce the sizes to two and give one variant its own price.
	groups := jsonList(created.Body, "option_groups")
	colour := jsonObject(groups[0])
	size := jsonObject(groups[1])
	sizeValues := jsonList(size, "values")

	variant := jsonObject(jsonList(created.Body, "variants")[0])
	saved := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"option_groups": []any{
			map[string]any{"id": colour["id"], "name": colour["name"], "values": jsonList(colour, "values")},
			map[string]any{"id": size["id"], "name": size["name"], "values": sizeValues[:2]},
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
	if active != 4 {
		t.Fatalf("expected 2 colours x 2 sizes = 4 active variants, got %d", active)
	}
	if got := len(jsonList(saved.Body, "variants")); got != 10 {
		t.Fatalf("retired variants must remain readable, got %d rows", got)
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

// TestPOSModeHidesManagementButKeepsSelling pins the restricted mode from the
// client's perspective.
func TestPOSModeHidesManagementButKeepsSelling(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("enable POS mode: %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/articles", nil); res.Status != http.StatusForbidden {
		t.Fatalf("POS mode must block article management, got %d", res.Status)
	}
	if res := h.do(http.MethodGet, "/api/v1/assortment", nil); res.Status != http.StatusOK {
		t.Fatalf("POS mode must keep the assortment readable, got %d %v", res.Status, res.Body)
	}
}
