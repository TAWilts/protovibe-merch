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
		// Revenue counts both sales; collected is the actual paid amount,
		// including the donation that also stays visible separately.
		"revenue_cents":   5400 + 1800,
		"collected_cents": 6000,
		"donation_cents":  600,
		// 6000 actually collected − 18000 spent.
		"cash_balance_cents": 6000 - 18000,
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

func TestBalancesSeparateDiscountRevenueDonationAndCollectedCash(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Discount Balance Shirt")

	res := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 1000, "discount_confirmed": true, "sold_on": "2026-09-07",
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("sale: %d %v", res.Status, res.Body)
	}
	summary := jsonObject(h.do(http.MethodGet, "/api/v1/balances", nil).Body["summary"])
	if summary["revenue_cents"] != float64(1000) || summary["collected_cents"] != float64(1000) ||
		summary["discount_cents"] != float64(800) || summary["donation_cents"] != float64(0) {
		t.Fatalf("discount accounting must stay separated: %v", summary)
	}
}

func TestNoReorderOnlyBecomesObsoleteAtZeroStock(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("Retiring Shirt")
	h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 500}},
		"purchased_on": "2026-09-07",
	})
	if saved := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{map[string]any{"id": variants[0], "no_reorder": true}},
	}); saved.Status != http.StatusOK {
		t.Fatalf("set no-reorder: %d %v", saved.Status, saved.Body)
	}

	balances := h.do(http.MethodGet, "/api/v1/balances", nil).Body
	if len(jsonList(balances, "obsolete_rows")) != 0 {
		t.Fatalf("stocked goods must remain in the normal balance: %v", balances["obsolete_rows"])
	}
	if got := jsonObject(jsonList(balances, "reorder_rows")[0])["stock_mode"]; got != "clearance" {
		t.Fatalf("no-reorder offered goods should report clearance, got %v", got)
	}
	h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-09-07",
	})
	balances = h.do(http.MethodGet, "/api/v1/balances", nil).Body
	found := false
	for _, raw := range jsonList(balances, "obsolete_rows") {
		if int64(jsonObject(raw)["variant_id"].(float64)) == variants[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("depleted no-reorder goods must be obsolete: %v", balances["obsolete_rows"])
	}
}

func TestBalanceStockModeUsesCurrentVariantStateForDateRanges(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("On-demand Shirt")
	if saved := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{map[string]any{"id": variants[0], "target_stock": 0}},
	}); saved.Status != http.StatusOK {
		t.Fatalf("set on-demand mode: %d %v", saved.Status, saved.Body)
	}

	for _, path := range []string{"/api/v1/balances", "/api/v1/balances?from=2020-01-01&to=2030-01-01"} {
		body := h.do(http.MethodGet, path, nil).Body
		found := false
		for _, raw := range jsonList(body, "reorder_rows") {
			row := jsonObject(raw)
			if int64(row["variant_id"].(float64)) == variants[0] {
				found = true
				if row["stock_mode"] != "on_demand" {
					t.Fatalf("%s used %v instead of current on-demand state", path, row["stock_mode"])
				}
			}
		}
		if !found {
			t.Fatalf("variant missing from %s", path)
		}
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

func TestEventTimelineSplitsOnlyAfterMoreThanThirtyOneDaysAndKeepsStableStart(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Timeline Shirt")
	h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 10, "unit_cost_cents": 800}},
		"purchased_on": "2025-12-20",
	})
	for _, sale := range []map[string]any{
		{"sold_on": "2026-01-01", "event_name": "Clubnacht"},
		{"sold_on": "2026-02-01", "event_name": "clubnacht"},
		{"sold_on": "2026-03-05", "event_name": "Clubnacht"},
	} {
		response := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
			"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
			"payment_method": "Bar", "is_paid": true, "is_received": true,
			"sold_on": sale["sold_on"], "event_name": sale["event_name"],
		})
		if response.Status != http.StatusCreated {
			t.Fatalf("book timeline sale: %d %v", response.Status, response.Body)
		}
	}

	points := jsonList(h.do(http.MethodGet, "/api/v1/balances", nil).Body, "event_timeline")
	if len(points) != 2 {
		t.Fatalf("expected two event occurrences, got %v", points)
	}
	first := jsonObject(points[0])
	if first["date"] != "2026-01-01" || first["quantity"] != float64(2) || first["income_cents"] != float64(3600) || first["profit_cents"] != float64(2000) {
		t.Fatalf("unexpected first occurrence: %v", first)
	}
	if second := jsonObject(points[1]); second["date"] != "2026-03-05" || second["quantity"] != float64(1) {
		t.Fatalf("unexpected second occurrence: %v", second)
	}

	filtered := jsonList(h.do(http.MethodGet, "/api/v1/balances?from=2026-02-01&to=2026-02-28", nil).Body, "event_timeline")
	if len(filtered) != 1 {
		t.Fatalf("expected one filtered occurrence, got %v", filtered)
	}
	visible := jsonObject(filtered[0])
	if visible["date"] != "2026-01-01" || visible["quantity"] != float64(1) || visible["income_cents"] != float64(1800) {
		t.Fatalf("filter must preserve occurrence start while limiting values: %v", visible)
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

// TestBandFinanceRoles pins that members may add entries while management of
// existing entries remains a manager responsibility.
func TestBandFinanceRoles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)

	if res := h.do(http.MethodGet, "/api/v1/band-finances", nil); res.Status != http.StatusOK {
		t.Fatalf("a member must read the ledger: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "category": "Gage",
		"description": "Mitgliedsbuchung", "amount_cents": 100,
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("a member must be able to book income and expenses: %d %v", res.Status, res.Body)
	}
	if expense := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "expense", "category": "Sonstiges",
		"description": "Mitgliedsausgabe", "amount_cents": 50,
	}); expense.Status != http.StatusCreated {
		t.Fatalf("a member must also be able to book expenses: %d %v", expense.Status, expense.Body)
	}
	entryID := int64(res.Body["id"].(float64))
	if changed := h.do(http.MethodPatch, "/api/v1/band-finances/"+itoa(entryID), map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-07", "category": "Gage",
		"description": "Manipuliert", "amount_cents": 200,
	}); changed.Status != http.StatusForbidden {
		t.Fatalf("a member must not edit existing entries: %d %v", changed.Status, changed.Body)
	}
}
