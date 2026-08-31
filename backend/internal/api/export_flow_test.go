package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// download fetches a binary response with the session cookie attached.
func (h *harness) download(path string) (int, []byte, string) {
	h.t.Helper()

	req, err := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if h.cookie != "" {
		req.Header.Set("Cookie", h.cookie)
	}

	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("perform request: %v", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, body, res.Header.Get("Content-Disposition")
}

// parseExport strips the BOM and parses the semicolon-separated sheet.
func parseExport(t *testing.T, body []byte) ([]string, [][]string) {
	t.Helper()

	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("the export must start with a UTF-8 BOM so Excel reads it correctly")
	}
	reader := csv.NewReader(bytes.NewReader(body[3:]))
	reader.Comma = ';'
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("the export is empty")
	}
	return records[0], records[1:]
}

// TestExportHeadersMatchTheOriginal pins the exact column names, because a
// band may already have spreadsheets and filters built on them.
func TestExportHeadersMatchTheOriginal(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	expected := map[string][]string{
		"artikel": {
			"Artikel-ID", "Artikel", "Varianten-ID", "Optionen", "Bestand", "Mindestbestand",
			"Mindestbestandswarnung", "Verkaufspreis", "Standard-Einkaufspreis",
			"Nachbestellen", "Angeboten", "Status",
		},
		"verkaeufe": {
			"Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Betrag",
			"Gegeben", "Spende", "Bezahlart", "Bezahlt", "Artikel erhalten", "Versandstatus",
			"Storniert", "Kundenname", "Adresse", "Veranstaltung", "Verkauft von", "Kommentar",
		},
		"einkaeufe": {
			"Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Gesamt",
			"Lieferant", "Rechnung", "Kommentar",
		},
		"bestand": {
			"Artikel", "Optionen", "Gekauft", "Verkauft", "Aktueller Bestand", "Mindestbestand",
			"Mindestbestandswarnung", "Nachbestellen", "Angeboten",
		},
	}

	for kind, want := range expected {
		t.Run(kind, func(t *testing.T) {
			status, body, disposition := h.download("/api/v1/exports/" + kind + ".csv")
			if status != http.StatusOK {
				t.Fatalf("download: %d", status)
			}
			if !strings.Contains(disposition, "attachment") {
				t.Fatalf("exports must download rather than render: %q", disposition)
			}
			header, _ := parseExport(t, body)
			if strings.Join(header, ";") != strings.Join(want, ";") {
				t.Fatalf("header mismatch\n got: %v\nwant: %v", header, want)
			}
		})
	}
}

// TestExportContentUsesGermanConventions pins the decimal comma and the
// ja/nein flags a German spreadsheet expects.
func TestExportContentUsesGermanConventions(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Export Shirt")

	h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 10, "unit_cost_cents": 950}},
		"purchased_on": "2026-08-27", "supplier": "Druckerei Muster",
	})
	h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method": "Bar", "is_paid": true, "is_received": true,
		"amount_given_cents": 4000, "sold_on": "2026-08-27", "sold_by": "Jamie",
	})

	_, body, _ := h.download("/api/v1/exports/verkaeufe.csv")
	header, rows := parseExport(t, body)
	if len(rows) != 1 {
		t.Fatalf("expected one sale row, got %d", len(rows))
	}

	byColumn := map[string]string{}
	for i, name := range header {
		byColumn[name] = rows[0][i]
	}
	if byColumn["Preis/Stück"] != "18,00" {
		t.Errorf("amounts use a decimal comma, got %q", byColumn["Preis/Stück"])
	}
	if byColumn["Betrag"] != "36,00" || byColumn["Gegeben"] != "40,00" || byColumn["Spende"] != "4,00" {
		t.Errorf("unexpected amounts: %v", rows[0])
	}
	if byColumn["Bezahlt"] != "ja" || byColumn["Storniert"] != "nein" {
		t.Errorf("booleans render as ja/nein: %v", rows[0])
	}
	if byColumn["Versandstatus"] != "Nicht relevant" {
		t.Errorf("a counter sale has no delivery status: %q", byColumn["Versandstatus"])
	}
	if byColumn["Optionen"] == "" || byColumn["Verkauft von"] != "Jamie" {
		t.Errorf("labels are missing: %v", rows[0])
	}

	// The inventory sheet must show the movements behind the stock figure.
	_, body, _ = h.download("/api/v1/exports/bestand.csv")
	header, rows = parseExport(t, body)
	for i, name := range header {
		byColumn[name] = rows[0][i]
	}
	if byColumn["Gekauft"] != "10" || byColumn["Verkauft"] != "2" || byColumn["Aktueller Bestand"] != "8" {
		t.Errorf("unexpected inventory row: %v", rows[0])
	}
}

// TestZipBundlesEverySheet pins the one-click full export.
func TestZipBundlesEverySheet(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	status, body, disposition := h.download("/api/v1/exports/all.zip")
	if status != http.StatusOK {
		t.Fatalf("download: %d", status)
	}
	if !strings.Contains(disposition, ".zip") {
		t.Fatalf("unexpected disposition %q", disposition)
	}

	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	names := map[string]bool{}
	for _, file := range archive.File {
		names[file.Name] = true
	}
	for _, want := range []string{"artikel.csv", "verkaeufe.csv", "einkaeufe.csv", "bestand.csv"} {
		if !names[want] {
			t.Errorf("the archive is missing %s: %v", want, names)
		}
	}
}

// TestExportsAreBandScoped pins that a download can never reach across bands.
func TestExportsAreBandScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	_, variants := h.sellableArticle("Band A Export")
	h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})

	h.signInAs(bandB, models.RoleManager)
	_, body, _ := h.download("/api/v1/exports/verkaeufe.csv")
	_, rows := parseExport(t, body)
	if len(rows) != 0 {
		t.Fatalf("band B's export must be empty, got %d rows", len(rows))
	}
}

// TestUnknownExportIsRejected keeps a typo from producing an empty file that
// looks like a valid export.
func TestUnknownExportIsRejected(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	if status, _, _ := h.download("/api/v1/exports/unbekannt.csv"); status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
}
