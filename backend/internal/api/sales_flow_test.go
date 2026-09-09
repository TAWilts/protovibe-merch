package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// sellableArticle creates an article and returns its ID plus the first two
// active variant IDs, ready to be sold.
func (h *harness) sellableArticle(name string) (int64, []int64) {
	h.t.Helper()

	created := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": name, "default_sale_price_cents": 1800,
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

func TestSellingLastNoReorderUnitAutomaticallyWithdrawsVariantAndArticle(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	createdArticle := h.do(http.MethodPost, "/api/v1/articles", map[string]any{
		"name": "Last-run Shirt", "default_sale_price_cents": 1800,
	})
	articleID := int64(createdArticle.Body["id"].(float64))
	allVariants := jsonList(createdArticle.Body, "variants")
	variantID := int64(jsonObject(allVariants[0])["id"].(float64))

	stocked := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variantID, "quantity": 1, "unit_cost_cents": 500}},
		"purchased_on": "2026-09-09",
	})
	if stocked.Status != http.StatusCreated {
		t.Fatalf("stock variant: %d %v", stocked.Status, stocked.Body)
	}
	updates := make([]any, 0, len(allVariants))
	for _, raw := range allVariants {
		updates = append(updates, map[string]any{
			"id": jsonObject(raw)["id"], "no_reorder": true,
		})
	}
	configured := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{"variants": updates})
	if configured.Status != http.StatusOK {
		t.Fatalf("mark variants no-reorder: %d %v", configured.Status, configured.Body)
	}
	if configured.Body["is_offered"] != true {
		t.Fatalf("article must remain offered while one no-reorder unit is in stock: %v", configured.Body)
	}

	sold := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variantID, "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 1800, "sold_on": "2026-09-09",
	})
	if sold.Status != http.StatusCreated {
		t.Fatalf("sell last unit: %d %v", sold.Status, sold.Body)
	}
	reloaded := h.do(http.MethodGet, "/api/v1/articles/"+itoa(articleID), nil)
	if reloaded.Body["is_offered"] != false {
		t.Fatalf("article should be withdrawn after all active variants are depleted and no-reorder: %v", reloaded.Body)
	}
	for _, raw := range jsonList(reloaded.Body, "variants") {
		variant := jsonObject(raw)
		if variant["is_active"] == true && variant["is_offered"] != false {
			t.Fatalf("active depleted no-reorder variant should be withdrawn: %v", variant)
		}
	}

	var auditCount int64
	if err := h.db.WithContext(h.ctx()).Model(&models.AuditLog{}).
		Where("band_id = ? AND action = ?", band.ID, "catalogue.auto_withdrawn").Count(&auditCount).Error; err != nil {
		t.Fatalf("count withdrawal audit: %v", err)
	}
	if auditCount == 0 {
		t.Fatal("automatic withdrawal must be audited")
	}
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

func TestSpecialIncomeSalesAreStockNeutralAndReportedSeparately(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Special Line Stock")

	book := func(lineType, description string, amount int) response {
		return h.do(http.MethodPost, "/api/v1/sales", map[string]any{
			"items": []any{map[string]any{
				"line_type": lineType, "description": description, "amount_cents": amount,
			}},
			"payment_method": "Bar", "is_paid": true, "is_received": true,
			"sold_on": "2026-09-09",
		})
	}

	donation := book("donation", "Hutspende", 750)
	if donation.Status != http.StatusCreated || donation.Body["total_due_cents"] != float64(0) ||
		donation.Body["total_paid_cents"] != float64(750) || donation.Body["donation_cents"] != float64(750) {
		t.Fatalf("unexpected donation result: %d %v", donation.Status, donation.Body)
	}
	misc := book("misc_income", "Pfandbecher", 400)
	if misc.Status != http.StatusCreated || misc.Body["total_due_cents"] != float64(400) ||
		misc.Body["total_paid_cents"] != float64(400) || misc.Body["donation_cents"] != float64(0) {
		t.Fatalf("unexpected misc-income result: %d %v", misc.Status, misc.Body)
	}
	if got := h.onHand(variants[0]); got != 0 {
		t.Fatalf("special lines must not move stock, got %d", got)
	}

	history := h.do(http.MethodGet, "/api/v1/history", nil)
	if history.Status != http.StatusOK || len(jsonList(history.Body, "receipts")) != 2 {
		t.Fatalf("special receipts missing from history: %d %v", history.Status, history.Body)
	}
	seen := map[string]bool{}
	for _, rawReceipt := range jsonList(history.Body, "receipts") {
		position := jsonObject(jsonList(jsonObject(rawReceipt), "positions")[0])
		seen[position["line_type"].(string)] = position["variant_id"] == nil && position["line_description"] != ""
	}
	if !seen["donation"] || !seen["misc_income"] {
		t.Fatalf("history must expose both special types without variants: %v", history.Body)
	}

	summary := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if summary["misc_income_cents"] != float64(400) || summary["revenue_cents"] != float64(400) ||
		summary["donation_cents"] != float64(750) || summary["collected_cents"] != float64(1150) {
		t.Fatalf("special income was not separated correctly: %v", summary)
	}
}

func TestSpecialSaleRejectsMixedReceiptsAndSupportsIdempotentOfflineReplay(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Mixed Receipt")

	mixed := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 1},
			map[string]any{"line_type": "donation", "description": "Spende", "amount_cents": 100},
		},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-09-09",
	})
	if mixed.Status != http.StatusBadRequest || mixed.Body["code"] != "mixed_basket" {
		t.Fatalf("mixed receipt must be rejected: %d %v", mixed.Status, mixed.Body)
	}

	queued := map[string]any{
		"items":          []any{map[string]any{"line_type": "donation", "description": "Spende", "amount_cents": 300}},
		"payment_method": "PayPal", "is_paid": true, "is_received": true, "sold_on": "2026-09-09",
		"client_event_id": "special-offline-1", "client_device_id": "phone-1",
	}
	first := h.do(http.MethodPost, "/api/v1/sales", queued)
	second := h.do(http.MethodPost, "/api/v1/sales", queued)
	if first.Status != http.StatusCreated || second.Status != http.StatusOK || second.Body["replayed"] != true ||
		first.Body["receipt_id"] != second.Body["receipt_id"] {
		t.Fatalf("special offline replay is not idempotent: first=%d %v second=%d %v",
			first.Status, first.Body, second.Status, second.Body)
	}
}

func TestShipmentChargesGrossShippingCosts(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Shipping Shirt")
	h.signInAs(band, models.RoleSeller)

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":               []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method":      "Bar",
		"is_paid":             true,
		"is_received":         false,
		"shipping_cost_cents": 499,
		"customer_name":       "Alex Muster",
		"customer_address":    "Musterweg 1",
		"sold_on":             "2026-09-07",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book shipment: %d %v", booked.Status, booked.Body)
	}
	if booked.Body["total_due_cents"] != float64(2299) {
		t.Fatalf("shipping must be included in the total: %v", booked.Body)
	}

	h.signInAs(band, models.RoleMember)
	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipt := jsonObject(jsonList(history.Body, "receipts")[0])
	if receipt["shipping_cost_cents"] != float64(499) || receipt["total_due_cents"] != float64(2299) {
		t.Fatalf("history must expose gross shipping separately: %v", receipt)
	}
}

func TestDiscountNeedsConfirmationForEveryImmediatePaymentMethod(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Discount Shirt")

	for _, method := range []string{"Bar", "PayPal", "Überweisung"} {
		t.Run(method, func(t *testing.T) {
			basket := map[string]any{
				"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
				"payment_method": method, "is_paid": true, "is_received": true,
				"amount_given_cents": 1000, "sold_on": "2026-09-07",
			}
			unconfirmed := h.do(http.MethodPost, "/api/v1/sales", basket)
			if unconfirmed.Status != http.StatusConflict || unconfirmed.Body["code"] != "discount_confirmation_required" {
				t.Fatalf("discount must require confirmation: %d %v", unconfirmed.Status, unconfirmed.Body)
			}

			basket["discount_confirmed"] = true
			booked := h.do(http.MethodPost, "/api/v1/sales", basket)
			if booked.Status != http.StatusCreated || booked.Body["discount_cents"] != float64(800) || booked.Body["total_paid_cents"] != float64(1000) {
				t.Fatalf("confirmed discount must be booked: %d %v", booked.Status, booked.Body)
			}
		})
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
		"items":              []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method":     "Bar",
		"is_paid":            true,
		"is_received":        true,
		"amount_given_cents": 0,
		"discount_confirmed": true,
		"sold_on":            "2026-08-27",
		"client_event_id":    "evt-offline-1",
		"client_device_id":   "phone-1",
	}

	first := h.do(http.MethodPost, "/api/v1/sales", queued)
	if first.Status != http.StatusCreated {
		t.Fatalf("first sync: %d %v", first.Status, first.Body)
	}
	if first.Body["replayed"] != false {
		t.Fatalf("the first submission is not a replay: %v", first.Body)
	}
	if first.Body["total_paid_cents"] != float64(0) || first.Body["discount_cents"] != float64(3600) {
		t.Fatalf("a confirmed zero-payment discount must survive offline booking: %v", first.Body)
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

func TestMembersCanDeleteEventsWithoutChangingHistoricalSales(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Event Shirt")
	created := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{
		"name": "Sommerfest 2026", "select": true,
	})
	eventID := int64(created.Body["id"].(float64))
	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"event_name": "Sommerfest 2026", "sold_on": "2026-09-07",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book historical sale: %d %v", booked.Status, booked.Body)
	}

	h.signInAs(band, models.RoleMember)
	deleted := h.do(http.MethodDelete, "/api/v1/sale-events/"+itoa(eventID), nil)
	if deleted.Status != http.StatusNoContent {
		t.Fatalf("member delete: %d %v", deleted.Status, deleted.Body)
	}
	listed := h.do(http.MethodGet, "/api/v1/sale-events", nil)
	if len(jsonList(listed.Body, "events")) != 0 || listed.Body["selected_event_id"] != float64(0) {
		t.Fatalf("deleted event must disappear and clear the selection: %v", listed.Body)
	}
	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipt := jsonObject(jsonList(history.Body, "receipts")[0])
	if receipt["event_name"] != "Sommerfest 2026" {
		t.Fatalf("historical event name must remain: %v", receipt)
	}
}

func TestMembersCanRenameEventsWithoutChangingHistoricalSales(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Rename Event Shirt")
	created := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{
		"name": "Alter Gigname", "select": true,
	})
	eventID := int64(created.Body["id"].(float64))
	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"event_name": "Alter Gigname", "sold_on": "2026-09-07",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book historical sale: %d %v", booked.Status, booked.Body)
	}

	h.signInAs(band, models.RoleMember)
	renamed := h.do(http.MethodPatch, "/api/v1/sale-events/"+itoa(eventID), map[string]any{"name": "Neuer Gigname"})
	if renamed.Status != http.StatusOK || renamed.Body["name"] != "Neuer Gigname" || renamed.Body["is_selected"] != true {
		t.Fatalf("member rename: %d %v", renamed.Status, renamed.Body)
	}
	history := h.do(http.MethodGet, "/api/v1/history", nil)
	if got := jsonObject(jsonList(history.Body, "receipts")[0])["event_name"]; got != "Alter Gigname" {
		t.Fatalf("historical event name changed to %v", got)
	}

	var logged int64
	if err := h.db.Raw("SELECT COUNT(*) FROM audit_log WHERE band_id = ? AND action = ? AND entity_id = ?",
		band.ID, audit.ActionSaleEventRenamed, eventID).Scan(&logged).Error; err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if logged != 1 {
		t.Fatalf("expected one rename audit entry, got %d", logged)
	}

	otherBand := h.makeBand()
	h.signInAs(otherBand, models.RoleMember)
	if hidden := h.do(http.MethodPatch, "/api/v1/sale-events/"+itoa(eventID), map[string]any{"name": "Fremder Gig"}); hidden.Status != http.StatusNotFound {
		t.Fatalf("another band must not rename the event, got %d %v", hidden.Status, hidden.Body)
	}

	h.signInAs(band, models.RoleMember)
	h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Zweiter Gig"})
	conflict := h.do(http.MethodPatch, "/api/v1/sale-events/"+itoa(eventID), map[string]any{"name": "Zweiter Gig"})
	if conflict.Status != http.StatusConflict || conflict.Body["code"] != "sale_event_name_conflict" {
		t.Fatalf("duplicate rename must conflict: %d %v", conflict.Status, conflict.Body)
	}

	h.signInAs(band, models.RoleSeller)
	if blocked := h.do(http.MethodPatch, "/api/v1/sale-events/"+itoa(eventID), map[string]any{"name": "Nicht erlaubt"}); blocked.Status != http.StatusForbidden {
		t.Fatalf("seller rename must be forbidden, got %d", blocked.Status)
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

func TestHistoricalSaleBooksWithdrawnVariantsExactlyOnce(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	actor := h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("Archive Shirt")

	if err := h.db.WithContext(h.ctx()).Model(&models.Variant{}).
		Where("id = ? AND band_id = ?", variants[0], band.ID).
		Updates(map[string]any{"is_offered": false, "is_active": false}).Error; err != nil {
		t.Fatalf("withdraw variant: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Model(&models.Article{}).
		Where("id = ? AND band_id = ?", articleID, band.ID).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate article: %v", err)
	}
	archiveCatalogue := h.do(http.MethodGet, "/api/v1/articles?include_inactive=true", nil)
	if archiveCatalogue.Status != http.StatusOK || len(jsonList(archiveCatalogue.Body, "articles")) != 1 {
		t.Fatalf("manager historical catalogue must include inactive articles: %d %v", archiveCatalogue.Status, archiveCatalogue.Body)
	}
	h.signInAs(band, models.RoleMember)
	memberCatalogue := h.do(http.MethodGet, "/api/v1/articles?include_inactive=true", nil)
	if memberCatalogue.Status != http.StatusOK || len(jsonList(memberCatalogue.Body, "articles")) != 0 {
		t.Fatalf("member must not expose inactive articles through the query flag: %d %v", memberCatalogue.Status, memberCatalogue.Body)
	}
	if res := h.signIn(band.Slug, actor.Username, "ein-langes-passwort"); res.Status != http.StatusOK {
		t.Fatalf("sign manager back in: %d %v", res.Status, res.Body)
	}
	normal := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2, "unit_price_cents": 1800}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 3600, "sold_on": "2026-08-27",
	})
	if normal.Status != http.StatusBadRequest || normal.Body["code"] != "variant_not_offered" {
		t.Fatalf("live sale must still reject a withdrawn variant: %d %v", normal.Status, normal.Body)
	}

	createdEvent := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{
		"name": "Archivfestival", "select": false,
	})
	if createdEvent.Status != http.StatusCreated {
		t.Fatalf("create event: %d %v", createdEvent.Status, createdEvent.Body)
	}
	eventID := int64(createdEvent.Body["id"].(float64))
	payload := map[string]any{
		"items":         []any{map[string]any{"variant_id": variants[0], "quantity": 2, "unit_price_cents": 1800}},
		"sale_event_id": eventID, "sold_on": "2026-08-27",
		"amount_given_cents": 3200, "discount_confirmed": true,
		"comment": "Aus alter Abrechnung", "receipt_id": "V-20260827-001",
		"client_event_id": "historical-event-1", "client_device_id": "archive-desktop",
		"client_created_at": "2026-09-08T12:00:00Z",
	}

	first := h.do(http.MethodPost, "/api/v1/sales/historical", payload)
	if first.Status != http.StatusCreated {
		t.Fatalf("historical book: %d %v", first.Status, first.Body)
	}
	if first.Body["total_due_cents"] != float64(3600) || first.Body["total_paid_cents"] != float64(3200) ||
		first.Body["discount_cents"] != float64(400) || first.Body["donation_cents"] != float64(0) {
		t.Fatalf("unexpected historical totals: %v", first.Body)
	}
	if renamed := h.do(http.MethodPatch, "/api/v1/sale-events/"+itoa(eventID), map[string]any{"name": "Archivfestival neu"}); renamed.Status != http.StatusOK {
		t.Fatalf("rename event before retry: %d %v", renamed.Status, renamed.Body)
	}
	second := h.do(http.MethodPost, "/api/v1/sales/historical", payload)
	if second.Status != http.StatusOK || second.Body["replayed"] != true || second.Body["receipt_id"] != first.Body["receipt_id"] {
		t.Fatalf("historical retry must replay: %d %v", second.Status, second.Body)
	}
	payload["amount_given_cents"] = 3100
	conflict := h.do(http.MethodPost, "/api/v1/sales/historical", payload)
	if conflict.Status != http.StatusConflict || conflict.Body["code"] != "sync_conflict" {
		t.Fatalf("changed retry must conflict: %d %v", conflict.Status, conflict.Body)
	}

	if got := h.onHand(variants[0]); got != -2 {
		t.Fatalf("historical sale must reduce stock once, got %d", got)
	}
	var sale models.Sale
	if err := h.db.WithContext(h.ctx()).Where("band_id = ? AND receipt_id = ?", band.ID, first.Body["receipt_id"]).First(&sale).Error; err != nil {
		t.Fatalf("read historical sale: %v", err)
	}
	if sale.EventName != "Archivfestival" || sale.SoldBy != "Historisch / nicht zugeordnet" ||
		sale.PaymentMethod != models.PaymentMethodOther || !sale.IsPaid || !sale.IsReceived ||
		sale.CreatedByUserID == nil || *sale.CreatedByUserID != actor.ID || sale.CustomerName != "" || sale.ShippingCostCents != 0 {
		t.Fatalf("historical metadata was not fixed correctly: %+v", sale)
	}

	balances := h.do(http.MethodGet, "/api/v1/balances", nil)
	foundSeller := false
	for _, raw := range jsonList(balances.Body, "top_sellers") {
		entry := jsonObject(raw)
		if entry["label"] == "Historisch / nicht zugeordnet" && entry["quantity"] == float64(2) {
			foundSeller = true
		}
	}
	if !foundSeller {
		t.Fatalf("historical seller grouping missing: %v", balances.Body["top_sellers"])
	}
	var audited int64
	if err := h.db.Raw("SELECT COUNT(*) FROM audit_log WHERE band_id = ? AND action = ?", band.ID, audit.ActionHistoricalSaleCreated).Scan(&audited).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("expected one audit entry despite replay, got %d", audited)
	}
}

func TestHistoricalSaleValidatesRoleDateEventAndTenant(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Historical Validation Shirt")
	createdEvent := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Eigener Gig", "select": false})
	eventID := int64(createdEvent.Body["id"].(float64))
	valid := func() map[string]any {
		return map[string]any{
			"items":         []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_price_cents": 1800}},
			"sale_event_id": eventID, "sold_on": "2026-08-27", "amount_given_cents": 1800,
			"client_event_id": unique("historical-"), "client_device_id": "validation-device",
			"client_created_at": "2026-09-08T12:00:00Z",
		}
	}

	h.signInAs(band, models.RoleMember)
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", valid()); res.Status != http.StatusForbidden {
		t.Fatalf("member must not book historical sales: %d %v", res.Status, res.Body)
	}
	h.signInAs(band, models.RoleManager)

	missingDate := valid()
	delete(missingDate, "sold_on")
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", missingDate); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_date_required" {
		t.Fatalf("missing date: %d %v", res.Status, res.Body)
	}
	future := valid()
	future["sold_on"] = "2999-01-01"
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", future); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_date_in_future" {
		t.Fatalf("future date: %d %v", res.Status, res.Body)
	}
	missingEvent := valid()
	missingEvent["sale_event_id"] = 0
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", missingEvent); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_event_required" {
		t.Fatalf("missing event: %d %v", res.Status, res.Body)
	}
	missingPrice := valid()
	missingPrice["items"] = []any{map[string]any{"variant_id": variants[0], "quantity": 1}}
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", missingPrice); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_price_required" {
		t.Fatalf("missing price: %d %v", res.Status, res.Body)
	}
	missingAmount := valid()
	delete(missingAmount, "amount_given_cents")
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", missingAmount); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_amount_required" {
		t.Fatalf("missing amount: %d %v", res.Status, res.Body)
	}
	missingRetryID := valid()
	delete(missingRetryID, "client_event_id")
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", missingRetryID); res.Status != http.StatusBadRequest || res.Body["code"] != "historical_idempotency_required" {
		t.Fatalf("missing idempotency metadata: %d %v", res.Status, res.Body)
	}

	otherBand := h.makeBand()
	h.signInAs(otherBand, models.RoleManager)
	foreignEvent := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Fremder Gig", "select": false})
	h.signInAs(band, models.RoleManager)
	foreign := valid()
	foreign["sale_event_id"] = int64(foreignEvent.Body["id"].(float64))
	if res := h.do(http.MethodPost, "/api/v1/sales/historical", foreign); res.Status != http.StatusNotFound || res.Body["code"] != "historical_event_not_found" {
		t.Fatalf("foreign event must stay hidden: %d %v", res.Status, res.Body)
	}
}

func TestHistoricalSaleBooksOverpaymentAsDonation(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	_, variants := h.sellableArticle("Historical Donation Shirt")
	createdEvent := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Spenden-Gig", "select": false})
	eventID := int64(createdEvent.Body["id"].(float64))

	booked := h.do(http.MethodPost, "/api/v1/sales/historical", map[string]any{
		"items":         []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_price_cents": 1800}},
		"sale_event_id": eventID, "sold_on": "2026-08-27", "amount_given_cents": 2000,
		"client_event_id": "historical-donation-1", "client_device_id": "archive-desktop",
		"client_created_at": "2026-09-08T12:00:00Z",
	})
	if booked.Status != http.StatusCreated || booked.Body["total_paid_cents"] != float64(2000) ||
		booked.Body["donation_cents"] != float64(200) || booked.Body["discount_cents"] != float64(0) {
		t.Fatalf("historical overpayment must become a donation: %d %v", booked.Status, booked.Body)
	}
}

func TestHistoricalSpecialIncomeUsesEventAndIdempotencyWithoutStock(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Historical Special Stock")
	createdEvent := h.do(http.MethodPost, "/api/v1/sale-events", map[string]any{"name": "Archivkasse", "select": false})
	eventID := int64(createdEvent.Body["id"].(float64))

	payload := map[string]any{
		"items": []any{map[string]any{
			"line_type": "misc_income", "description": "Pfand", "amount_cents": 900,
		}},
		"sale_event_id": eventID, "sold_on": "2026-08-27", "amount_given_cents": 900,
		"client_event_id": "historical-special-1", "client_device_id": "archive-desktop",
		"client_created_at": "2026-09-08T12:00:00Z",
	}
	first := h.do(http.MethodPost, "/api/v1/sales/historical", payload)
	second := h.do(http.MethodPost, "/api/v1/sales/historical", payload)
	if first.Status != http.StatusCreated || first.Body["total_due_cents"] != float64(900) ||
		second.Status != http.StatusOK || second.Body["replayed"] != true {
		t.Fatalf("historical special replay failed: first=%d %v second=%d %v",
			first.Status, first.Body, second.Status, second.Body)
	}
	if got := h.onHand(variants[0]); got != 0 {
		t.Fatalf("historical special income must not move stock, got %d", got)
	}
	var row models.Sale
	if err := h.db.WithContext(h.ctx()).Where("band_id = ? AND receipt_id = ?", band.ID, first.Body["receipt_id"]).First(&row).Error; err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if row.LineType != models.SaleLineMiscIncome || row.VariantID != nil || row.LineDescription != "Pfand" ||
		row.EventName != "Archivkasse" || row.SoldBy != historicalUnassignedSellerForTest {
		t.Fatalf("historical special metadata mismatch: %+v", row)
	}
}

const historicalUnassignedSellerForTest = "Historisch / nicht zugeordnet"
