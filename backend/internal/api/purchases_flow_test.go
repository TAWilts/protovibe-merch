package api_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// upload posts a file to a multipart endpoint with the session and CSRF token.
func (h *harness) upload(path, filename, contentType string, content []byte) response {
	h.t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{
		`form-data; name="file"; filename="` + filename + `"`,
	}
	header["Content-Type"] = []string{contentType}

	part, err := writer.CreatePart(header)
	if err != nil {
		h.t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		h.t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		h.t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, &body)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if h.cookie != "" {
		req.Header.Set("Cookie", h.cookie)
	}
	if h.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", h.csrfToken)
	}

	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("perform request: %v", err)
	}
	defer res.Body.Close()

	out := response{Status: res.StatusCode, Body: map[string]any{}}
	_ = decodeInto(res.Body, &out.Body)
	return out
}

// TestGoodsReceiptLifecycle walks booking, correcting and removing a receipt,
// and checks the derived stock follows each step.
func TestGoodsReceiptLifecycle(t *testing.T) {
	t.Setenv("PURCHASE_EDITING_ENABLED", "true")
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Stock Shirt")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 20, "unit_cost_cents": 900},
			map[string]any{"variant_id": variants[1], "quantity": 10, "unit_cost_cents": 850},
		},
		"purchased_on": "2026-08-27",
		"supplier":     "Druckerei Muster",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	if created.Body["receipt_id"] != "E-20260827-001" {
		t.Fatalf("unexpected receipt ID %v", created.Body["receipt_id"])
	}
	if created.Body["total_cost_cents"] != float64(20*900+10*850) {
		t.Fatalf("unexpected total %v", created.Body["total_cost_cents"])
	}
	listed := h.do(http.MethodGet, "/api/v1/purchases", nil)
	if listed.Body["editing_enabled"] != true {
		t.Fatalf("the enabled feature flag must reach the purchase UI payload: %v", listed.Body)
	}
	positionID := int64(jsonList(created.Body, "purchase_ids")[0].(float64))

	if got := h.onHand(variants[0]); got != 20 {
		t.Fatalf("expected 20 in stock, got %d", got)
	}

	// A mistyped quantity is corrected, not cancelled — leaving a phantom
	// position would distort the stock the band relies on.
	corrected := h.do(http.MethodPatch, "/api/v1/purchases/"+itoa(positionID), map[string]any{
		"quantity": 12, "unit_cost_cents": 900,
	})
	if corrected.Status != http.StatusNoContent {
		t.Fatalf("correct: %d %v", corrected.Status, corrected.Body)
	}
	if got := h.onHand(variants[0]); got != 12 {
		t.Fatalf("expected 12 in stock after the correction, got %d", got)
	}

	if res := h.do(http.MethodDelete, "/api/v1/purchases/"+itoa(positionID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", res.Status, res.Body)
	}
	if got := h.onHand(variants[0]); got != 0 {
		t.Fatalf("expected 0 in stock after the removal, got %d", got)
	}
}

func TestReceiptEditingUsesTheFeatureFlagAndUpdatesSharedTerms(t *testing.T) {
	t.Setenv("PURCHASE_EDITING_ENABLED", "true")
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Editable Stock")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 800},
			map[string]any{"variant_id": variants[1], "quantity": 3, "unit_cost_cents": 850},
		},
		"purchased_on": "2026-09-01",
	})
	ids := jsonList(created.Body, "purchase_ids")
	receiptID := created.Body["receipt_id"].(string)

	updated := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, map[string]any{
		"items": []any{
			map[string]any{"id": ids[0], "quantity": 4, "unit_cost_cents": 1000},
			map[string]any{"id": ids[1], "quantity": 1, "unit_cost_cents": 500},
		},
		"purchased_on":          "2026-09-07",
		"supplier":              "Neue Druckerei",
		"invoice_reference":     "R-42",
		"prices_include_vat":    false,
		"vat_rate_basis_points": 1900,
		"shipping_cost_cents":   200,
	})
	if updated.Status != http.StatusOK || updated.Body["total_cost_cents"] != float64(4*1190+595+238) {
		t.Fatalf("edit receipt: %d %v", updated.Status, updated.Body)
	}

	listed := h.do(http.MethodGet, "/api/v1/purchases", nil)
	first := jsonObject(jsonList(listed.Body, "purchases")[0])
	if first["supplier"] != "Neue Druckerei" || first["vat_rate_basis_points"] != float64(1900) ||
		first["shipping_cost_cents"] != float64(238) {
		t.Fatalf("shared purchase terms were not updated: %v", first)
	}
}

func TestPurchaseAccountHoldersAreReceiptScopedAndHistoricalAssignmentsRemainEditable(t *testing.T) {
	t.Setenv("PURCHASE_EDITING_ENABLED", "true")
	h := newHarness(t)
	band := h.makeBand()
	manager := h.signInAs(band, models.RoleManager)
	holder := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")
	inactive := h.makeUser(&band.ID, models.RoleSeller, "ein-langes-passwort")
	otherBand := h.makeBand()
	otherHolder := h.makeUser(&otherBand.ID, models.RoleMember, "ein-langes-passwort")
	_, variants := h.sellableArticle("Privat bezahlter Einkauf")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 900},
			map[string]any{"variant_id": variants[1], "quantity": 3, "unit_cost_cents": 800},
		},
		"purchased_on": "2026-09-10", "supplier": "Privatdruckerei",
		"shipping_cost_cents": 500, "account_holder_user_id": holder.ID,
	})
	if created.Status != http.StatusCreated || created.Body["account_holder_user_id"] != float64(holder.ID) || created.Body["account_holder_username"] != holder.Username {
		t.Fatalf("create attributed purchase: %d %v", created.Status, created.Body)
	}
	receiptID := created.Body["receipt_id"].(string)
	ids := jsonList(created.Body, "purchase_ids")
	currentHolderName := holder.Username + " Neu"
	if err := h.db.WithContext(h.ctx()).Model(holder).Update("username", currentHolderName).Error; err != nil {
		t.Fatalf("rename holder: %v", err)
	}

	listed := h.do(http.MethodGet, "/api/v1/purchases", nil)
	if listed.Status != http.StatusOK {
		t.Fatalf("list purchases: %d %v", listed.Status, listed.Body)
	}
	positions := jsonList(listed.Body, "purchases")
	if len(positions) != 2 {
		t.Fatalf("expected two receipt positions: %v", positions)
	}
	for _, raw := range positions {
		position := jsonObject(raw)
		if position["account_holder_user_id"] != float64(holder.ID) || position["account_holder_username"] != currentHolderName {
			t.Fatalf("all receipt positions must carry the holder's current name: %v", positions)
		}
	}
	assignable := map[int64]bool{}
	for _, raw := range jsonList(listed.Body, "account_holders") {
		assignable[int64(jsonObject(raw)["id"].(float64))] = true
	}
	if !assignable[manager.ID] || !assignable[holder.ID] || !assignable[inactive.ID] || assignable[otherHolder.ID] {
		t.Fatalf("assignable purchase holders must be active users of this band: %v", listed.Body["account_holders"])
	}

	bandCash := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_cost_cents": 700}},
		"purchased_on": "2026-09-10",
	})
	if bandCash.Status != http.StatusCreated || bandCash.Body["account_holder_user_id"] != nil || bandCash.Body["account_holder_username"] != "" {
		t.Fatalf("an omitted holder must mean band cash: %d %v", bandCash.Status, bandCash.Body)
	}

	if err := h.db.WithContext(h.ctx()).Model(inactive).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate holder: %v", err)
	}
	for name, userID := range map[string]int64{"inactive": inactive.ID, "other band": otherHolder.ID} {
		invalid := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
			"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_cost_cents": 100}},
			"purchased_on": "2026-09-10", "account_holder_user_id": userID,
		})
		if invalid.Status != http.StatusBadRequest || invalid.Body["code"] != "invalid_account_holder" {
			t.Fatalf("%s holder must be rejected: %d %v", name, invalid.Status, invalid.Body)
		}
	}

	if err := h.db.WithContext(h.ctx()).Model(holder).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate historical holder: %v", err)
	}
	editPayload := map[string]any{
		"items": []any{
			map[string]any{"id": ids[0], "quantity": 2, "unit_cost_cents": 900},
			map[string]any{"id": ids[1], "quantity": 3, "unit_cost_cents": 800},
		},
		"purchased_on": "2026-09-11", "supplier": "Historisch erhalten",
		"shipping_cost_cents": 500, "account_holder_user_id": holder.ID,
	}
	preserved := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, editPayload)
	if preserved.Status != http.StatusOK || preserved.Body["account_holder_username"] != holder.Username {
		t.Fatalf("editing must preserve a now-inactive holder: %d %v", preserved.Status, preserved.Body)
	}
	if err := h.db.WithContext(h.ctx()).Delete(holder).Error; err != nil {
		t.Fatalf("delete historical holder: %v", err)
	}
	preservedDeleted := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, editPayload)
	if preservedDeleted.Status != http.StatusOK || preservedDeleted.Body["account_holder_username"] != holder.Username {
		t.Fatalf("editing must preserve a deleted holder snapshot: %d %v", preservedDeleted.Status, preservedDeleted.Body)
	}
	listedAfterDelete := h.do(http.MethodGet, "/api/v1/purchases", nil)
	for _, raw := range jsonList(listedAfterDelete.Body, "purchases") {
		position := jsonObject(raw)
		if position["receipt_id"] == receiptID && position["account_holder_username"] != holder.Username {
			t.Fatalf("a deleted holder must fall back to the historical snapshot: %v", position)
		}
	}

	editPayload["supplier"] = "Darf nicht gespeichert werden"
	editPayload["account_holder_user_id"] = otherHolder.ID
	rejected := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, editPayload)
	if rejected.Status != http.StatusBadRequest || rejected.Body["code"] != "invalid_account_holder" {
		t.Fatalf("cross-tenant receipt reassignment must be rejected: %d %v", rejected.Status, rejected.Body)
	}
	var stored []models.Purchase
	if err := h.db.WithContext(h.ctx()).Where("receipt_id = ?", receiptID).Find(&stored).Error; err != nil {
		t.Fatalf("reload receipt: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected two stored positions, got %d", len(stored))
	}
	for _, position := range stored {
		if position.Supplier != "Historisch erhalten" || position.AccountHolderUserID == nil || *position.AccountHolderUserID != holder.ID || position.AccountHolderUsername != holder.Username {
			t.Fatalf("invalid edit must leave every position unchanged: %+v", stored)
		}
	}
}

func TestBasketPricedPurchaseKeepsExactGoodsTotalAndShippingSeparate(t *testing.T) {
	t.Setenv("PURCHASE_EDITING_ENABLED", "true")
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Basket-priced Stock")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 99999},
			map[string]any{"variant_id": variants[1], "quantity": 1, "unit_cost_cents": 99999},
		},
		"purchased_on":          "2026-09-09",
		"prices_include_vat":    false,
		"vat_rate_basis_points": 1900,
		"shipping_cost_cents":   100,
		"price_mode":            "basket",
		"goods_total_cents":     100,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create basket-priced receipt: %d %v", created.Status, created.Body)
	}
	if created.Body["goods_total_cents"] != float64(119) || created.Body["total_cost_cents"] != float64(238) {
		t.Fatalf("goods should be converted once and shipping separately: %v", created.Body)
	}

	listed := h.do(http.MethodGet, "/api/v1/purchases", nil)
	rows := jsonList(listed.Body, "purchases")
	if len(rows) != 2 {
		t.Fatalf("expected two positions: %v", listed.Body)
	}
	first := jsonObject(rows[0])
	second := jsonObject(rows[1])
	if first["price_mode"] != "basket" || first["total_cost_cents"].(float64)+second["total_cost_cents"].(float64) != 119 {
		t.Fatalf("exact basket allocation was not returned: %v", rows)
	}

	ids := jsonList(created.Body, "purchase_ids")
	receiptID := created.Body["receipt_id"].(string)
	updated := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, map[string]any{
		"items": []any{
			map[string]any{"id": ids[0], "quantity": 1, "unit_cost_cents": 0},
			map[string]any{"id": ids[1], "quantity": 1, "unit_cost_cents": 0},
		},
		"purchased_on": "2026-09-09", "prices_include_vat": true,
		"vat_rate_basis_points": 1900, "shipping_cost_cents": 5,
		"price_mode": "basket", "goods_total_cents": 101,
	})
	if updated.Status != http.StatusOK || updated.Body["goods_total_cents"] != float64(101) || updated.Body["total_cost_cents"] != float64(106) {
		t.Fatalf("edit basket receipt: %d %v", updated.Status, updated.Body)
	}

	positionID := int64(ids[0].(float64))
	lineEdit := h.do(http.MethodPatch, "/api/v1/purchases/"+itoa(positionID), map[string]any{
		"quantity": 1, "unit_cost_cents": 1,
	})
	if lineEdit.Status != http.StatusConflict || lineEdit.Body["code"] != "basket_receipt_requires_receipt_edit" {
		t.Fatalf("basket receipt line edit should require receipt editing: %d %v", lineEdit.Status, lineEdit.Body)
	}
}

func TestRefillSuggestionsRespectTargetsAndLastCosts(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("Refill Stock")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 750}},
		"purchased_on": "2026-09-09",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("seed purchase: %d %v", created.Status, created.Body)
	}
	invalidTarget := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{map[string]any{"id": variants[0], "target_stock": -1}},
	})
	if invalidTarget.Status != http.StatusBadRequest || invalidTarget.Body["code"] != "invalid_target_stock" {
		t.Fatalf("negative target must be rejected: %d %v", invalidTarget.Status, invalidTarget.Body)
	}
	configured := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{
			map[string]any{"id": variants[0], "target_stock": 5},
			map[string]any{"id": variants[1], "target_stock": 4, "no_reorder": true},
		},
	})
	if configured.Status != http.StatusOK {
		t.Fatalf("configure targets: %d %v", configured.Status, configured.Body)
	}

	res := h.do(http.MethodGet, "/api/v1/purchases/refill-suggestions", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("list refill suggestions: %d %v", res.Status, res.Body)
	}
	items := jsonList(res.Body, "items")
	if len(items) != 1 {
		t.Fatalf("no-reorder variants must be excluded: %v", res.Body)
	}
	item := jsonObject(items[0])
	if item["variant_id"] != float64(variants[0]) || item["on_hand"] != float64(2) ||
		item["target_stock"] != float64(5) || item["suggested_quantity"] != float64(3) ||
		item["last_unit_cost_cents"] != float64(750) {
		t.Fatalf("unexpected refill suggestion: %v", item)
	}

	h.signInAs(band, models.RoleMember)
	if forbidden := h.do(http.MethodGet, "/api/v1/purchases/refill-suggestions", nil); forbidden.Status != http.StatusForbidden {
		t.Fatalf("members must not load manager refill suggestions: %d %v", forbidden.Status, forbidden.Body)
	}
}

func TestRefillSuggestionsIncludeOnDemandDeficitsAndPausedStock(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	articleID, variants := h.sellableArticle("Stock Modes")

	stocked := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[1], "quantity": 3, "unit_cost_cents": 500}},
		"purchased_on": "2026-09-09",
	})
	if stocked.Status != http.StatusCreated {
		t.Fatalf("stock paused variant: %d %v", stocked.Status, stocked.Body)
	}

	configured := h.do(http.MethodPut, "/api/v1/articles/"+itoa(articleID), map[string]any{
		"variants": []any{
			map[string]any{"id": variants[0], "target_stock": 0, "is_offered": true, "no_reorder": false},
			map[string]any{"id": variants[1], "target_stock": 10, "is_offered": false, "no_reorder": false},
		},
	})
	if configured.Status != http.StatusOK {
		t.Fatalf("configure on-demand and paused modes: %d %v", configured.Status, configured.Body)
	}

	sold := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 3600, "sold_on": "2026-09-09",
	})
	if sold.Status != http.StatusCreated {
		t.Fatalf("sell on-demand variant into negative stock: %d %v", sold.Status, sold.Body)
	}

	res := h.do(http.MethodGet, "/api/v1/purchases/refill-suggestions", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("list mode refill suggestions: %d %v", res.Status, res.Body)
	}
	got := map[int64]map[string]any{}
	for _, raw := range jsonList(res.Body, "items") {
		item := jsonObject(raw)
		got[int64(item["variant_id"].(float64))] = item
	}
	if item := got[variants[0]]; item == nil || item["on_hand"] != float64(-2) ||
		item["target_stock"] != float64(0) || item["suggested_quantity"] != float64(2) {
		t.Fatalf("unexpected on-demand suggestion: %v", item)
	}
	if item := got[variants[1]]; item == nil || item["on_hand"] != float64(3) ||
		item["target_stock"] != float64(10) || item["suggested_quantity"] != float64(7) {
		t.Fatalf("unexpected paused suggestion: %v", item)
	}
}

func TestReceiptEditingIsReadOnlyWhenTheFlagIsDisabled(t *testing.T) {
	t.Setenv("PURCHASE_EDITING_ENABLED", "false")
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Read-only Stock")
	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_cost_cents": 800}},
		"purchased_on": "2026-09-07",
	})
	receiptID := created.Body["receipt_id"].(string)
	res := h.do(http.MethodPatch, "/api/v1/purchases/receipt/"+receiptID, map[string]any{})
	if res.Status != http.StatusForbidden || res.Body["code"] != "feature_disabled" {
		t.Fatalf("disabled editing must stay read-only: %d %v", res.Status, res.Body)
	}
}

// TestMembersCanReadButNotBookPurchases pins the role split.
func TestMembersCanReadButNotBookPurchases(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)

	if res := h.do(http.MethodGet, "/api/v1/purchases", nil); res.Status != http.StatusOK {
		t.Fatalf("a member must read the history: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": 1, "quantity": 1, "unit_cost_cents": 100}},
		"purchased_on": "2026-08-27",
	})
	if res.Status != http.StatusForbidden {
		t.Fatalf("a member must not book purchases, got %d %v", res.Status, res.Body)
	}
}

// TestInvoiceUploadAndDownload covers attaching a document to a position.
func TestInvoiceUploadAndDownload(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Invoice Shirt")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 5, "unit_cost_cents": 900}},
		"purchased_on": "2026-08-27",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	positionID := int64(jsonList(created.Body, "purchase_ids")[0].(float64))

	uploaded := h.upload("/api/v1/purchases/"+itoa(positionID)+"/invoice",
		"rechnung.pdf", "application/pdf", []byte("%PDF-1.4 invoice"))
	if uploaded.Status != http.StatusCreated {
		t.Fatalf("upload: %d %v", uploaded.Status, uploaded.Body)
	}

	listed := h.do(http.MethodGet, "/api/v1/purchases", nil)
	first := jsonObject(jsonList(listed.Body, "purchases")[0])
	if first["has_invoice_file"] != true {
		t.Fatalf("the position should report an invoice: %v", first)
	}

	if res := h.do(http.MethodDelete, "/api/v1/purchases/"+itoa(positionID)+"/invoice", nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete invoice: %d %v", res.Status, res.Body)
	}
}

// TestReceiptInvoicesBelongToTheWholeBasket pins the current UI model: several
// invoices can be attached to the receipt, regardless of how many positions it has.
func TestReceiptInvoicesBelongToTheWholeBasket(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Basket Invoice Shirt")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items": []any{
			map[string]any{"variant_id": variants[0], "quantity": 2, "unit_cost_cents": 800},
			map[string]any{"variant_id": variants[1], "quantity": 3, "unit_cost_cents": 850},
		},
		"purchased_on": "2026-08-27",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	receiptID := created.Body["receipt_id"].(string)
	path := "/api/v1/purchase-receipts/" + receiptID + "/attachments"
	firstUpload := h.upload(path, "rechnung-1.pdf", "application/pdf", []byte("%PDF-1.4 first basket invoice"))
	secondUpload := h.upload(path, "rechnung-2.pdf", "application/pdf", []byte("%PDF-1.4 second basket invoice"))
	if firstUpload.Status != http.StatusCreated || secondUpload.Status != http.StatusCreated {
		t.Fatalf("upload receipt invoices: first=%d %v second=%d %v",
			firstUpload.Status, firstUpload.Body, secondUpload.Status, secondUpload.Body)
	}

	listed := h.do(http.MethodGet, path, nil)
	files := jsonList(listed.Body, "attachments")
	if listed.Status != http.StatusOK || len(files) != 2 {
		t.Fatalf("both receipt invoices should be listed: %d %v", listed.Status, listed.Body)
	}
	if jsonObject(files[0])["original_filename"] != "rechnung-1.pdf" ||
		jsonObject(files[1])["original_filename"] != "rechnung-2.pdf" {
		t.Fatalf("unexpected attachments: %v", files)
	}

	if res := h.do(http.MethodDelete, "/api/v1/purchase-receipts/"+receiptID, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete receipt: %d %v", res.Status, res.Body)
	}
	listedPurchases := h.do(http.MethodGet, "/api/v1/purchases", nil)
	purchases := jsonList(listedPurchases.Body, "purchases")
	if len(purchases) != 2 {
		t.Fatalf("cancelling the basket must keep both positions in history: %v", listedPurchases.Body)
	}
	for _, raw := range purchases {
		purchase := jsonObject(raw)
		if purchase["is_cancelled"] != true {
			t.Fatalf("every position in a cancelled basket must be marked cancelled: %v", purchase)
		}
	}
}

// TestUploadRejectsDisguisedFiles pins that neither an unsupported type nor a
// mismatched extension reaches the store.
func TestUploadRejectsDisguisedFiles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Upload Shirt")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_cost_cents": 100}},
		"purchased_on": "2026-08-27",
	})
	positionID := int64(jsonList(created.Body, "purchase_ids")[0].(float64))
	path := "/api/v1/purchases/" + itoa(positionID) + "/invoice"

	if res := h.upload(path, "evil.html", "text/html", []byte("<script>alert(1)</script>")); res.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("an unsupported type must be refused, got %d %v", res.Status, res.Body)
	}
	if res := h.upload(path, "evil.html", "application/pdf", []byte("<script>alert(1)</script>")); res.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("a mismatched extension must be refused, got %d %v", res.Status, res.Body)
	}
	if res := h.upload(path, "rechnung.pdf", "application/pdf", []byte("%PDF")); res.Status != http.StatusCreated {
		t.Fatalf("a genuine PDF must be accepted: %d %v", res.Status, res.Body)
	}
}

// TestPurchasesAreBandScoped pins the tenant boundary for goods receipts.
func TestPurchasesAreBandScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	_, variants := h.sellableArticle("Band A Stock")
	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 5, "unit_cost_cents": 900}},
		"purchased_on": "2026-08-27",
	})
	positionID := int64(jsonList(created.Body, "purchase_ids")[0].(float64))

	h.signInAs(bandB, models.RoleManager)
	if res := h.do(http.MethodGet, "/api/v1/purchases", nil); len(jsonList(res.Body, "purchases")) != 0 {
		t.Fatalf("band B must not see band A's receipts: %v", res.Body)
	}
	if res := h.do(http.MethodDelete, "/api/v1/purchases/"+itoa(positionID), nil); res.Status != http.StatusNotFound {
		t.Fatalf("band B must not delete band A's position, got %d %v", res.Status, res.Body)
	}
}
