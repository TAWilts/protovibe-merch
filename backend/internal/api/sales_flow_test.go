package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// sellableArticle creates an article and returns its ID plus the first two
// active variant IDs, ready to be sold.
func (h *harness) sellableArticle(name string) (int64, []int64) {
	h.t.Helper()

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": name, "default_sale_price_cents": 1800, "default_purchase_price_cents": 900,
	})
	if created.Status != http.StatusCreated {
		h.t.Fatalf("create article: %d %v", created.Status, created.Body)
	}

	ids := make([]int64, 0, 2)
	for _, raw := range jsonList(created.Body, "variants") {
		variant := jsonObject(raw)
		if variant["is_active"] == true && len(ids) < 2 {
			ids = append(ids, int64(variant["id"].(float64)))
		}
	}
	return int64(created.Body["id"].(float64)), ids
}

// TestSellAtTheStand walks the point-of-sale happy path over HTTP: preview the
// receipt ID, book a two-position basket with an overpayment, and see the
// stock and the donation land correctly.
func TestSellAtTheStand(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Geometry Shirt")

	preview := h.do(http.MethodGet, "/api/v1/receipt-preview?date=2026-08-27", nil)
	if preview.Status != http.StatusOK {
		t.Fatalf("preview: %d %v", preview.Status, preview.Body)
	}
	if preview.Body["receipt_id"] != "V-20260827-001" {
		t.Fatalf("unexpected preview %v", preview.Body)
	}

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 1},
			map[string]any{"variant_id": variants[1], "quantity": 1},
		},
		"payment_method":     "Bar",
		"is_paid":            true,
		"is_received":        true,
		"amount_given_cents": 4000,
		"sold_on":            "2026-08-27",
		"receipt_id":         "V-20260827-001",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", booked.Status, booked.Body)
	}
	if booked.Body["receipt_id"] != "V-20260827-001" {
		t.Fatalf("the previewed ID must be honoured, got %v", booked.Body["receipt_id"])
	}
	if booked.Body["total_due_cents"] != float64(3600) || booked.Body["donation_cents"] != float64(400) {
		t.Fatalf("unexpected totals: %v", booked.Body)
	}

	// The sold stock must show up in the catalogue immediately.
	articles := h.do(http.MethodGet, "/api/v1/articles", nil)
	for _, raw := range jsonList(articles.Body, "articles") {
		article := jsonObject(raw)
		for _, rawVariant := range jsonList(article, "variants") {
			variant := jsonObject(rawVariant)
			if int64(variant["id"].(float64)) == variants[0] {
				if variant["sold"] != float64(1) || variant["on_hand"] != float64(-1) {
					t.Fatalf("stock did not follow the sale: %v", variant)
				}
			}
		}
	}
}

// TestUnpaidSaleNeedsContactDetails pins the rule that the band always knows
// who still owes money.
func TestUnpaidSaleNeedsContactDetails(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Unpaid Shirt")

	basket := map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar",
		"is_paid":        false,
		"is_received":    true,
		"sold_on":        "2026-08-27",
	}
	res := h.do(http.MethodPost, "/api/v1/sales", basket)
	if res.Status != http.StatusBadRequest || res.Body["code"] != "contact_required" {
		t.Fatalf("expected contact details to be required, got %d %v", res.Status, res.Body)
	}

	basket["customer_name"] = "Alex Muster"
	basket["customer_address"] = "Musterweg 1, 12345 Musterstadt"
	if res := h.do(http.MethodPost, "/api/v1/sales", basket); res.Status != http.StatusCreated {
		t.Fatalf("book with contact details: %d %v", res.Status, res.Body)
	}
}

// TestOfflineQueueSyncsExactlyOnce is the property a phone at a gig depends
// on: replaying a queued sale must settle it, not duplicate it.
func TestOfflineQueueSyncsExactlyOnce(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Offline Shirt")

	queued := map[string]any{
		"items":            []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method":   "Bar",
		"is_paid":          true,
		"is_received":      true,
		"sold_on":          "2026-08-27",
		"client_event_id":  "evt-offline-1",
		"client_device_id": "phone-1",
	}

	first := h.do(http.MethodPost, "/api/v1/sales", queued)
	if first.Status != http.StatusCreated {
		t.Fatalf("first sync: %d %v", first.Status, first.Body)
	}
	if first.Body["replayed"] != false {
		t.Fatalf("the first submission is not a replay: %v", first.Body)
	}

	second := h.do(http.MethodPost, "/api/v1/sales", queued)
	if second.Status != http.StatusOK {
		t.Fatalf("a retry must be accepted as settled, got %d %v", second.Status, second.Body)
	}
	if second.Body["replayed"] != true || second.Body["receipt_id"] != first.Body["receipt_id"] {
		t.Fatalf("the retry must replay the original receipt: %v", second.Body)
	}

	// A reused ID with different data is a conflict the device has to resolve.
	queued["items"] = []any{map[string]any{"variant_id": variants[0], "quantity": 5}}
	conflict := h.do(http.MethodPost, "/api/v1/sales", queued)
	if conflict.Status != http.StatusConflict || conflict.Body["code"] != "sync_conflict" {
		t.Fatalf("expected a sync conflict, got %d %v", conflict.Status, conflict.Body)
	}
}

// TestSaleEventsAreSharedAcrossTheBand pins that two phones at the same stand
// see and book against the same gig.
func TestSaleEventsAreSharedAcrossTheBand(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{
		"name": "Sommerfest 2026", "select": true,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create event: %d %v", created.Status, created.Body)
	}
	eventID := int64(created.Body["id"].(float64))

	// The same name typed again is the same gig, not a duplicate.
	again := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Sommerfest 2026"})
	if int64(again.Body["id"].(float64)) != eventID {
		t.Fatalf("the same event name must be reused, got %v", again.Body)
	}

	// A second seller on another device sees the selection.
	h.signInAs(band, models.RoleSeller)
	listed := h.do(http.MethodGet, "/api/v1/sale-events", nil)
	if listed.Status != http.StatusOK {
		t.Fatalf("list events: %d %v", listed.Status, listed.Body)
	}
	if int64(listed.Body["selected_event_id"].(float64)) != eventID {
		t.Fatalf("the selection must be shared across the band: %v", listed.Body)
	}
	if got := len(jsonList(listed.Body, "events")); got != 1 {
		t.Fatalf("expected one event, got %d", got)
	}
}

// TestSalesAreBandScoped pins that a receipt booked by one band is invisible
// to another, including the receipt-number sequence.
func TestSalesAreBandScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	_, variantsA := h.sellableArticle("Band A Shirt")
	first := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variantsA[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	if first.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", first.Status, first.Body)
	}

	// Band B starts its own sequence at 001 rather than continuing A's.
	h.signInAs(bandB, models.RoleManager)
	preview := h.do(http.MethodGet, "/api/v1/receipt-preview?date=2026-08-27", nil)
	if preview.Body["receipt_id"] != "V-20260827-001" {
		t.Fatalf("each band has its own receipt sequence, got %v", preview.Body)
	}

	// And it cannot sell band A's variant.
	res := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variantsA[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("band B must not sell band A's variant, got %d %v", res.Status, res.Body)
	}
}

// TestSoldByDefaultsToTheSignedInUser pins the convenience the original had,
// while leaving the field editable for a shared tablet.
func TestSoldByDefaultsToTheSignedInUser(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("SoldBy Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", booked.Status, booked.Body)
	}

	var sale models.Sale
	if err := h.db.WithContext(h.ctx()).
		Where("band_id = ? AND receipt_id = ?", band.ID, booked.Body["receipt_id"]).
		First(&sale).Error; err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if sale.SoldBy != user.Username {
		t.Fatalf("sold_by should default to the signed-in user, got %q", sale.SoldBy)
	}

	explicit := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"sold_on": "2026-08-27", "sold_by": "Jamie",
	})
	if explicit.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", explicit.Status, explicit.Body)
	}
	// A fresh variable on purpose: reusing the populated one would make GORM
	// add its primary key to the WHERE clause.
	var explicitSale models.Sale
	if err := h.db.WithContext(h.ctx()).
		Where("band_id = ? AND receipt_id = ?", band.ID, explicit.Body["receipt_id"]).
		First(&explicitSale).Error; err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if explicitSale.SoldBy != "Jamie" {
		t.Fatalf("an explicit seller must be kept, got %q", explicitSale.SoldBy)
	}
}
