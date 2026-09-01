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

// TestReceiptInvoiceBelongsToTheWholeBasket pins the current UI model: one
// invoice is attached to the receipt, regardless of how many positions it has.
func TestReceiptInvoiceBelongsToTheWholeBasket(t *testing.T) {
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
	uploaded := h.upload(path, "warenkorb.pdf", "application/pdf", []byte("%PDF-1.4 basket invoice"))
	if uploaded.Status != http.StatusCreated {
		t.Fatalf("upload receipt invoice: %d %v", uploaded.Status, uploaded.Body)
	}

	listed := h.do(http.MethodGet, path, nil)
	files := jsonList(listed.Body, "attachments")
	if listed.Status != http.StatusOK || len(files) != 1 {
		t.Fatalf("receipt invoice should be listed once: %d %v", listed.Status, listed.Body)
	}
	if jsonObject(files[0])["original_filename"] != "warenkorb.pdf" {
		t.Fatalf("unexpected attachment: %v", files[0])
	}

	if res := h.do(http.MethodDelete, "/api/v1/purchase-receipts/"+receiptID, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete receipt: %d %v", res.Status, res.Body)
	}
	if listed := h.do(http.MethodGet, "/api/v1/purchases", nil); len(jsonList(listed.Body, "purchases")) != 0 {
		t.Fatalf("deleting the basket must remove all positions: %v", listed.Body)
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
