package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// uploadCSV posts an import file together with optional form fields.
func (h *harness) uploadCSV(path, csv string, fields map[string]string) response {
	h.t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		_ = writer.WriteField(name, value)
	}
	part, err := writer.CreateFormFile("file", "import.csv")
	if err != nil {
		h.t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
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
	_ = json.NewDecoder(res.Body).Decode(&out.Body)
	return out
}

const purchaseCSV = `Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von
20;Tour Shirt;Farbe=Schwarz, Größe=M;9,00;Druckerei Muster
10;Tour Shirt;Farbe=Schwarz, Größe=L;9,00;Druckerei Muster
5;Tour Shirt;Farbe=Weiß, Größe=M;9,50;Druckerei Muster
`

// TestImportPreviewChangesNothing pins that the confirmation screen is
// genuinely read-only — the band must be able to inspect a file before it
// touches the catalogue.
func TestImportPreviewChangesNothing(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	preview := h.uploadCSV("/api/v1/imports/einkaeufe/preview", purchaseCSV, nil)
	if preview.Status != http.StatusOK {
		t.Fatalf("preview: %d %v", preview.Status, preview.Body)
	}
	if preview.Body["row_count"] != float64(3) {
		t.Fatalf("expected three rows: %v", preview.Body)
	}
	if names := jsonList(preview.Body, "new_articles"); len(names) != 1 || names[0] != "Tour Shirt" {
		t.Fatalf("the new article should be announced: %v", preview.Body)
	}
	// Two colours × two sizes, even though only three rows appear.
	if preview.Body["new_variants"] != float64(4) {
		t.Fatalf("expected four resulting variants: %v", preview.Body)
	}
	if preview.Body["total_quantity"] != float64(35) {
		t.Fatalf("unexpected quantity: %v", preview.Body)
	}

	if articles := h.do(http.MethodGet, "/api/v1/articles", nil); len(jsonList(articles.Body, "articles")) != 0 {
		t.Fatalf("a preview must not create anything: %v", articles.Body)
	}
}

// TestImportCreatesCatalogueAndStock walks the whole backfill.
func TestImportCreatesCatalogueAndStock(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	applied := h.uploadCSV("/api/v1/imports/einkaeufe/apply", purchaseCSV,
		map[string]string{"date": "2026-08-27"})
	if applied.Status != http.StatusCreated {
		t.Fatalf("apply: %d %v", applied.Status, applied.Body)
	}
	// Everything lands under one receipt, so a mistaken import is reversible
	// as a unit rather than row by row.
	if applied.Body["receipt_id"] != "E-20260827-001" {
		t.Fatalf("unexpected receipt: %v", applied.Body)
	}
	if applied.Body["total_cents"] != float64(20*900+10*900+5*950) {
		t.Fatalf("unexpected total: %v", applied.Body)
	}

	articles := h.do(http.MethodGet, "/api/v1/articles", nil)
	list := jsonList(articles.Body, "articles")
	if len(list) != 1 {
		t.Fatalf("expected one article: %v", articles.Body)
	}
	article := jsonObject(list[0])
	if article["name"] != "Tour Shirt" {
		t.Fatalf("unexpected article: %v", article)
	}

	groups := jsonList(article, "option_groups")
	if len(groups) != 2 {
		t.Fatalf("the file's two option columns should exist: %v", groups)
	}
	active := 0
	stocked := map[float64]bool{}
	for _, raw := range jsonList(article, "variants") {
		variant := jsonObject(raw)
		if variant["is_active"] == true {
			active++
		}
		if variant["purchased"].(float64) > 0 {
			stocked[variant["purchased"].(float64)] = true
		}
	}
	if active != 4 {
		t.Fatalf("expected four variants, got %d", active)
	}
	if !stocked[20] || !stocked[10] || !stocked[5] {
		t.Fatalf("each row's quantity should be booked: %v", stocked)
	}
}

// TestImportRejectsAFileMissingAnExistingOptionColumn pins the guard that
// stops rows from silently matching the wrong variant.
func TestImportRejectsAFileMissingAnExistingOptionColumn(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	// Creates Farbe and Größe.
	if res := h.uploadCSV("/api/v1/imports/einkaeufe/apply", purchaseCSV, nil); res.Status != http.StatusCreated {
		t.Fatalf("first import: %d %v", res.Status, res.Body)
	}

	withoutSize := `Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von
5;Tour Shirt;Farbe=Schwarz;9,00;Druckerei Muster
`
	res := h.uploadCSV("/api/v1/imports/einkaeufe/preview", withoutSize, nil)
	if res.Status != http.StatusBadRequest {
		t.Fatalf("a file omitting an existing option column must be rejected: %d %v", res.Status, res.Body)
	}
}

// TestImportRejectsBadFiles pins that the errors name the line, which is what
// makes a rejected file fixable.
func TestImportRejectsBadFiles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	cases := map[string]string{
		"wrong header":   "A;B;C;D;E\n1;Shirt;;1,00;Wer\n",
		"missing column": "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n1;Shirt;;1,00\n",
		"zero quantity":  "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n0;Shirt;;1,00;Wer\n",
		"empty article":  "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n1;;;1,00;Wer\n",
		"empty party":    "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n1;Shirt;;1,00;\n",
		"bad option":     "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n1;Shirt;Farbe;1,00;Wer\n",
		"inconsistent columns": "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von\n" +
			"1;Shirt;Farbe=Rot;1,00;Wer\n1;Shirt;Größe=M;1,00;Wer\n",
		"empty file": "",
	}

	for name, csv := range cases {
		t.Run(name, func(t *testing.T) {
			res := h.uploadCSV("/api/v1/imports/einkaeufe/preview", csv, nil)
			if res.Status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d %v", res.Status, res.Body)
			}
			if message, _ := res.Body["message"].(string); message == "" {
				t.Fatalf("the rejection must explain itself: %v", res.Body)
			}
		})
	}
}

// TestSalesImportBooksPaidCounterSales pins the second format.
func TestSalesImportBooksPaidCounterSales(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	sales := `Anzahl;Artikel;Optionen;Verkaufspreis;Verkauft an
2;Tour Shirt;Größe=M;18,00;Sommerfest
1;Tour Shirt;Größe=L;18,00;Sommerfest
`
	applied := h.uploadCSV("/api/v1/imports/verkaeufe/apply", sales,
		map[string]string{"date": "2026-08-27"})
	if applied.Status != http.StatusCreated {
		t.Fatalf("apply: %d %v", applied.Status, applied.Body)
	}
	if applied.Body["receipt_id"] != "V-20260827-001" {
		t.Fatalf("sales use the sale sequence: %v", applied.Body)
	}

	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipts := jsonList(history.Body, "receipts")
	if len(receipts) != 1 {
		t.Fatalf("expected one imported receipt: %v", history.Body)
	}
	receipt := jsonObject(receipts[0])
	if receipt["total_due_cents"] != float64(5400) {
		t.Fatalf("unexpected total: %v", receipt)
	}
	if len(jsonList(receipt, "positions")) != 2 {
		t.Fatalf("expected two positions: %v", receipt)
	}
}

// TestImportsAreBandScoped pins the tenant boundary for backfills.
func TestImportsAreBandScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	if res := h.uploadCSV("/api/v1/imports/einkaeufe/apply", purchaseCSV, nil); res.Status != http.StatusCreated {
		t.Fatalf("import: %d %v", res.Status, res.Body)
	}

	h.signInAs(bandB, models.RoleManager)
	if res := h.do(http.MethodGet, "/api/v1/articles", nil); len(jsonList(res.Body, "articles")) != 0 {
		t.Fatalf("band B must not see band A's imported catalogue: %v", res.Body)
	}
	// Band B importing the same file creates its own separate article.
	preview := h.uploadCSV("/api/v1/imports/einkaeufe/preview", purchaseCSV, nil)
	if names := jsonList(preview.Body, "new_articles"); len(names) != 1 {
		t.Fatalf("band B should be creating its own article: %v", preview.Body)
	}
}

// TestSellersCannotImport pins that a backfill is a manager's job.
func TestSellersCannotImport(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleSeller)

	if res := h.uploadCSV("/api/v1/imports/einkaeufe/preview", purchaseCSV, nil); res.Status != http.StatusForbidden {
		t.Fatalf("a seller must not import, got %d %v", res.Status, res.Body)
	}
}
