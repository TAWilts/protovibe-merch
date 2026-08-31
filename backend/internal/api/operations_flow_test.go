package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// bookCounterSale books a paid, handed-over sale and returns its receipt.
func (h *harness) bookCounterSale(variantID int64, quantity int) map[string]any {
	h.t.Helper()
	res := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variantID, "quantity": quantity}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	if res.Status != http.StatusCreated {
		h.t.Fatalf("book: %d %v", res.Status, res.Body)
	}
	return res.Body
}

// TestHistoryGroupsBasketsIntoReceipts pins that a multi-position basket shows
// as one purchase that expands into its lines.
func TestHistoryGroupsBasketsIntoReceipts(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("History Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2},
			map[string]any{"variant_id": variants[1], "quantity": 1},
		},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 6000, "sold_on": "2026-08-27",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", booked.Status, booked.Body)
	}

	history := h.do(http.MethodGet, "/api/v1/history", nil)
	if history.Status != http.StatusOK {
		t.Fatalf("history: %d %v", history.Status, history.Body)
	}
	receipts := jsonList(history.Body, "receipts")
	if len(receipts) != 1 {
		t.Fatalf("expected one receipt, got %d", len(receipts))
	}

	receipt := jsonObject(receipts[0])
	if len(jsonList(receipt, "positions")) != 2 {
		t.Fatalf("expected two positions: %v", receipt)
	}
	if receipt["total_due_cents"] != float64(5400) || receipt["donation_cents"] != float64(600) {
		t.Fatalf("unexpected totals: %v", receipt)
	}

	// The option names come from the live catalogue, which is what makes a
	// later rename apply retroactively.
	first := jsonObject(jsonList(receipt, "positions")[0])
	if first["article_name"] != "History Shirt" || first["variant_label"] == "" {
		t.Fatalf("positions must carry readable labels: %v", first)
	}
}

// TestCancellingKeepsHistoryButFreesStock is the core difference between a
// cancellation and a deletion.
func TestCancellingKeepsHistoryButFreesStock(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Cancel Shirt")

	sale := h.bookCounterSale(variants[0], 3)
	saleID := int64(jsonList(sale, "sale_ids")[0].(float64))
	if h.onHand(variants[0]) != -3 {
		t.Fatalf("expected -3 on hand, got %d", h.onHand(variants[0]))
	}

	cancelled := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/cancel", map[string]any{"scope": "item"})
	if cancelled.Status != http.StatusOK {
		t.Fatalf("cancel: %d %v", cancelled.Status, cancelled.Body)
	}
	if h.onHand(variants[0]) != 0 {
		t.Fatalf("a cancelled sale must not consume stock, got %d", h.onHand(variants[0]))
	}

	// The receipt stays in the history, flagged rather than removed.
	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipt := jsonObject(jsonList(history.Body, "receipts")[0])
	if receipt["is_fully_cancelled"] != true {
		t.Fatalf("the receipt should be marked cancelled: %v", receipt)
	}
	position := jsonObject(jsonList(receipt, "positions")[0])
	if position["is_cancelled"] != true {
		t.Fatalf("the position should be marked cancelled: %v", position)
	}

	// Cancelling the same position twice is a conflict, not a silent no-op.
	again := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/cancel", map[string]any{"scope": "item"})
	if again.Status != http.StatusConflict {
		t.Fatalf("expected a conflict, got %d %v", again.Status, again.Body)
	}
}

// TestCancellingOneItemLeavesTheRestOfTheBasket pins that the remaining
// positions keep their own share of the payment.
func TestCancellingOneItemLeavesTheRestOfTheBasket(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Partial Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 1},
			map[string]any{"variant_id": variants[1], "quantity": 1},
		},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 4000, "sold_on": "2026-08-27",
	})
	saleID := int64(jsonList(booked.Body, "sale_ids")[0].(float64))

	if res := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/cancel", map[string]any{"scope": "item"}); res.Status != http.StatusOK {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}

	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipt := jsonObject(jsonList(history.Body, "receipts")[0])
	if receipt["is_fully_cancelled"] != false {
		t.Fatalf("the receipt still has a live position: %v", receipt)
	}
	// Only the surviving position counts towards the totals now.
	if receipt["total_due_cents"] != float64(1800) {
		t.Fatalf("the remaining position must keep its own amount: %v", receipt)
	}

	// Cancelling the whole receipt afterwards must still work.
	if res := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/cancel", map[string]any{"scope": "receipt"}); res.Status != http.StatusOK {
		t.Fatalf("cancel receipt: %d %v", res.Status, res.Body)
	}
	if h.onHand(variants[1]) != 0 {
		t.Fatalf("the whole basket should be cancelled, got %d", h.onHand(variants[1]))
	}
}

// TestShippingWorkflow walks a mail order from booking to delivered.
func TestShippingWorkflow(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Shipping Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Überweisung", "is_paid": true, "is_received": false,
		"customer_name": "Alex Muster", "customer_address": "Musterweg 1", "sold_on": "2026-08-27",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", booked.Status, booked.Body)
	}
	saleID := int64(jsonList(booked.Body, "sale_ids")[0].(float64))

	queues := h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_shipments")) != 1 {
		t.Fatalf("the parcel should be waiting to be sent: %v", queues.Body)
	}

	path := "/api/v1/sales/" + itoa(saleID) + "/delivery-status"
	if res := h.do(http.MethodPatch, path, map[string]any{"status": "shipped"}); res.Status != http.StatusNoContent {
		t.Fatalf("mark shipped: %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodPatch, path, map[string]any{"status": "received"}); res.Status != http.StatusNoContent {
		t.Fatalf("mark received: %d %v", res.Status, res.Body)
	}

	queues = h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_shipments")) != 0 {
		t.Fatalf("the queue should be empty now: %v", queues.Body)
	}
	if len(jsonList(queues.Body, "delivered_shipments")) != 1 {
		t.Fatalf("the parcel should be in the delivered history: %v", queues.Body)
	}

	// A status is a record of what someone did with a parcel, and people
	// mis-tap. Correcting it back has to work, or one wrong tap closes a case
	// that was never finished — the original allowed it for the same reason.
	if res := h.do(http.MethodPatch, path, map[string]any{"status": "pending"}); res.Status != http.StatusNoContent {
		t.Fatalf("a mis-tapped delivery must be correctable, got %d %v", res.Status, res.Body)
	}

	queues = h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_shipments")) != 1 {
		t.Fatalf("the corrected parcel belongs back on the worklist: %v", queues.Body)
	}
	if len(jsonList(queues.Body, "delivered_shipments")) != 0 {
		t.Fatalf("and out of the delivered history: %v", queues.Body)
	}

	// The one door that stays shut: a shipment cannot become a counter sale,
	// which would erase that something was ever owed.
	if res := h.do(http.MethodPatch, path,
		map[string]any{"status": "not_applicable"}); res.Status != http.StatusConflict {
		t.Fatalf("a shipment must not leave the workflow, got %d %v", res.Status, res.Body)
	}
}

// TestCounterSalesHaveNoDeliveryWorkflow pins that an over-the-counter sale
// never enters the shipping queue at all.
func TestCounterSalesHaveNoDeliveryWorkflow(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Counter Shirt")

	sale := h.bookCounterSale(variants[0], 1)
	saleID := int64(jsonList(sale, "sale_ids")[0].(float64))

	queues := h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_shipments")) != 0 {
		t.Fatalf("a counter sale must not appear in the shipping queue: %v", queues.Body)
	}

	res := h.do(http.MethodPatch, "/api/v1/sales/"+itoa(saleID)+"/delivery-status",
		map[string]any{"status": "shipped"})
	if res.Status != http.StatusConflict || res.Body["code"] != "no_delivery_flow" {
		t.Fatalf("expected no_delivery_flow, got %d %v", res.Status, res.Body)
	}
}

// TestPaymentFollowUp pins that a chased payment lands in its own history
// rather than looking like an ordinary counter sale.
func TestPaymentFollowUp(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Unpaid Shirt")

	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": false, "is_received": true,
		"customer_name": "Alex Muster", "customer_address": "Musterweg 1", "sold_on": "2026-08-27",
	})
	saleID := int64(jsonList(booked.Body, "sale_ids")[0].(float64))

	queues := h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_payments")) != 1 {
		t.Fatalf("the sale should be waiting for payment: %v", queues.Body)
	}

	path := "/api/v1/sales/" + itoa(saleID) + "/payment-status"
	if res := h.do(http.MethodPatch, path, nil); res.Status != http.StatusNoContent {
		t.Fatalf("mark paid: %d %v", res.Status, res.Body)
	}

	queues = h.do(http.MethodGet, "/api/v1/operations", nil)
	if len(jsonList(queues.Body, "open_payments")) != 0 {
		t.Fatalf("nothing should be outstanding now: %v", queues.Body)
	}
	if len(jsonList(queues.Body, "settled_payments")) != 1 {
		t.Fatalf("the chased payment should have its own history: %v", queues.Body)
	}
	if res := h.do(http.MethodPatch, path, nil); res.Status != http.StatusConflict {
		t.Fatalf("marking it paid twice must be a conflict, got %d", res.Status)
	}
}

// TestSellersCannotReachTheWorkQueues pins the role split: recording a sale is
// a seller's job, following it up is a member's.
func TestSellersCannotReachTheWorkQueues(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleSeller)

	for _, path := range []string{"/api/v1/history", "/api/v1/operations"} {
		if res := h.do(http.MethodGet, path, nil); res.Status != http.StatusForbidden {
			t.Fatalf("%s must be forbidden for a seller, got %d %v", path, res.Status, res.Body)
		}
	}
}
