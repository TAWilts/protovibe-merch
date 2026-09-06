package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// TestBalancesReflectTheLedger walks the numbers the band actually reads off
// the balances page: what was bought, what was sold, what is still owed, and
// the resulting cash position.
func TestBalancesReflectTheLedger(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Balance Shirt")

	// 20 shirts at 9,00 € = 180,00 € spent.
	if res := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 20, "unit_cost_cents": 900}},
		"purchased_on": "2026-08-27",
	}); res.Status != http.StatusCreated {
		t.Fatalf("purchase: %d %v", res.Status, res.Body)
	}

	// 3 sold at 18,00 € with 6,00 € donated = 60,00 € collected.
	if res := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 3}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 6000, "sold_on": "2026-08-27",
	}); res.Status != http.StatusCreated {
		t.Fatalf("sale: %d %v", res.Status, res.Body)
	}

	// One more sold on credit; it counts as revenue but not as collected cash.
	if res := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": false, "is_received": true,
		"customer_name": "Alex Muster", "customer_address": "Musterweg 1", "sold_on": "2026-08-27",
	}); res.Status != http.StatusCreated {
		t.Fatalf("unpaid sale: %d %v", res.Status, res.Body)
	}

	res := h.do(http.MethodGet, "/api/v1/balances", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("balances: %d %v", res.Status, res.Body)
	}
	summary := jsonObject(res.Body["summary"])

	checks := map[string]float64{
		"purchase_cost_cents": 18000,
		// Revenue counts both sales; collected counts only the paid one.
		"revenue_cents":   5400 + 1800,
		"collected_cents": 5400,
		"donation_cents":  600,
		// 5400 collected + 600 donated − 18000 spent.
		"cash_balance_cents": 5400 + 600 - 18000,
		"outstanding_cents":  1800,
		"stock_count":        16,
	}
	for key, want := range checks {
		if summary[key] != want {
			t.Errorf("%s = %v, want %v", key, summary[key], want)
		}
	}
}

// TestCancelledSalesLeaveTheBalances pins that a cancellation disappears from
// the numbers just as it does from stock.
func TestCancelledSalesLeaveTheBalances(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Cancelled Balance Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	saleID := int64(jsonList(booked.Body, "sale_ids")[0].(float64))

	before := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if before["collected_cents"] != float64(3600) {
		t.Fatalf("expected 3600 collected, got %v", before["collected_cents"])
	}

	if res := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/cancel", map[string]any{"scope": "item"}); res.Status != http.StatusOK {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}

	after := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if after["collected_cents"] != float64(0) || after["revenue_cents"] != float64(0) {
		t.Fatalf("a cancelled sale must leave the balances: %v", after)
	}
}

// TestMinimumStockWarnings pins the tri-state threshold end to end.
func TestMinimumStockWarnings(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("Threshold Shirt")

	// Five in stock, warn below three.
	h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 5, "unit_cost_cents": 900}},
		"purchased_on": "2026-08-27",
	})
	if res := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{map[string]any{"id": variants[0], "minimum_stock": 3}},
	}); res.Status != http.StatusOK {
		t.Fatalf("set threshold: %d %v", res.Status, res.Body)
	}

	summary := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if summary["minimum_stock_warning_count"] != float64(0) {
		t.Fatalf("five in stock is above the threshold: %v", summary)
	}

	// Sell three, leaving two — now below.
	h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 3}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	summary = jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if summary["minimum_stock_warning_count"] != float64(1) {
		t.Fatalf("two in stock is below the threshold of three: %v", summary)
	}
}

// TestBalanceDefaultOrderFollowsConfiguredOptions keeps the neutral table
// order useful. Alphabetical sorting would put L before M and S; the default
// must instead follow the order the band configured on the article.
func TestBalanceDefaultOrderFollowsConfiguredOptions(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	h.sellableArticle("Configured Order Shirt")

	rows := jsonList(h.do(http.MethodGet, "/api/v1/balances", nil).Body, "reorder_rows")
	want := []string{
		"Farbe: Schwarz · Größe: S",
		"Farbe: Schwarz · Größe: M",
		"Farbe: Schwarz · Größe: L",
		"Farbe: Schwarz · Größe: XL",
		"Farbe: Schwarz · Größe: XXL",
		"Farbe: Weiß · Größe: S",
	}
	if len(rows) < len(want) {
		t.Fatalf("not enough balance rows: %v", rows)
	}
	for index, label := range want {
		if got := jsonObject(rows[index])["variant_label"]; got != label {
			t.Fatalf("row %d = %q, want %q; default order must follow option positions", index, got, label)
		}
	}
}

// TestRankingsFoldVariantsIntoArticles pins how the band reads "which shirt
// sells", and that profit uses the weighted average purchase price.
func TestRankingsFoldVariantsIntoArticles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Ranking Shirt")

	h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 10, "unit_cost_cents": 800},
			map[string]any{"variant_id": variants[1], "quantity": 10, "unit_cost_cents": 800},
		},
		"purchased_on": "2026-08-27",
	})
	h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2},
			map[string]any{"variant_id": variants[1], "quantity": 3},
		},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"sold_on": "2026-08-27", "event_name": "Sommerfest", "sold_by": "Jamie",
	})

	body := h.do(http.MethodGet, "/api/v1/balances", nil).Body
	selling := jsonList(body, "top_selling_items")
	if len(selling) != 1 {
		t.Fatalf("two variants of one article must fold into one entry: %v", selling)
	}
	top := jsonObject(selling[0])
	if top["label"] != "Ranking Shirt" || top["quantity"] != float64(5) {
		t.Fatalf("unexpected ranking entry: %v", top)
	}
	// 5 sold at 18,00 € minus 5 × 8,00 € weighted average cost.
	if top["profit_cents"] != float64(5*1800-5*800) {
		t.Fatalf("profit should use the weighted average cost: %v", top)
	}

	if len(jsonList(body, "top_events")) != 1 {
		t.Fatalf("the event ranking should have one entry: %v", body["top_events"])
	}
	if len(jsonList(body, "top_sellers")) != 1 {
		t.Fatalf("the seller ranking should have one entry: %v", body["top_sellers"])
	}
	if len(jsonList(body, "daily_income")) != 1 {
		t.Fatalf("the income chart should have one day: %v", body["daily_income"])
	}
}

// TestBandLedgerIsSeparateButAddsUp pins the deliberate split: gig money never
// touches the merch balance, yet both appear in one headline figure.
func TestBandLedgerIsSeparateButAddsUp(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	created := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "transaction_on": "2026-08-27",
		"category": "Gage", "description": "Sommerfest", "amount_cents": 45000,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create income: %d %v", created.Status, created.Body)
	}
	entryID := int64(created.Body["id"].(float64))

	if res := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "expense", "transaction_on": "2026-08-27",
		"category": "Equipment", "description": "Kabel", "amount_cents": 5000,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create expense: %d %v", res.Status, res.Body)
	}

	ledger := h.do(http.MethodGet, "/api/v1/band-finances", nil)
	if ledger.Body["balance_cents"] != float64(40000) {
		t.Fatalf("expected a 400,00 € band balance: %v", ledger.Body)
	}
	if len(jsonList(ledger.Body, "categories")) != 2 {
		t.Fatalf("expected two categories: %v", ledger.Body["categories"])
	}

	summary := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	// The merch side is untouched by band bookings.
	if summary["cash_balance_cents"] != float64(0) {
		t.Fatalf("band money must not change the merch balance: %v", summary)
	}
	if summary["overall_balance_cents"] != float64(40000) {
		t.Fatalf("the headline figure should add both ledgers: %v", summary)
	}

	// Cancelling voids the entry without deleting it.
	if res := h.do(http.MethodPost, "/api/v1/band-finances/"+itoa(entryID)+"/cancel", nil); res.Status != http.StatusNoContent {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}
	ledger = h.do(http.MethodGet, "/api/v1/band-finances", nil)
	if ledger.Body["balance_cents"] != float64(-5000) {
		t.Fatalf("the cancelled income must leave the total: %v", ledger.Body)
	}
	if len(jsonList(ledger.Body, "entries")) != 2 {
		t.Fatalf("the cancelled entry must stay readable: %v", ledger.Body["entries"])
	}
}

// TestBandFinanceRoles pins that members read and managers write.
func TestBandFinanceRoles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)

	if res := h.do(http.MethodGet, "/api/v1/band-finances", nil); res.Status != http.StatusOK {
		t.Fatalf("a member must read the ledger: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "category": "Gage",
		"description": "Nope", "amount_cents": 100,
	})
	if res.Status != http.StatusForbidden {
		t.Fatalf("a member must not book: %d %v", res.Status, res.Body)
	}
}
