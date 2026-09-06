package legacyimport

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestSlugify(t *testing.T) {
	for input, want := range map[string]string{
		"Protovibe":        "protovibe",
		"Die Äxte & Öl!":   "die-aexte-oel",
		"  Merch   Test  ": "merch-test",
		"x":                "imported-band",
	} {
		if got := Slugify(input); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildUserPlansGuaranteesActiveBandAdmin(t *testing.T) {
	plans, warnings, err := buildUserPlans([]row{
		{"id": int64(1), "username": "old-admin", "role": "band_admin", "is_active": int64(0)},
		{"id": int64(2), "username": "manager", "role": "manager", "is_active": int64(1)},
		{"id": int64(3), "username": "seller", "role": "seller", "is_active": int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plans[1].role != models.RoleBandAdmin {
		t.Fatalf("active manager was not promoted: %#v", plans)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[len(warnings)-1], "no active band_admin") {
		t.Fatalf("promotion warning missing: %v", warnings)
	}
}

func TestBuildUserPlansRejectsNoActiveUsers(t *testing.T) {
	_, _, err := buildUserPlans([]row{
		{"id": int64(1), "username": "admin", "role": "band_admin", "is_active": int64(0)},
	})
	if err == nil || !strings.Contains(err.Error(), "no active users") {
		t.Fatalf("expected no-active-users error, got %v", err)
	}
}

func TestResolveLegacyFileConfinesPaths(t *testing.T) {
	root := t.TempDir()
	invoiceDir := filepath.Join(root, "invoices")
	if err := os.MkdirAll(invoiceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(invoiceDir, "receipt.pdf")
	if err := os.WriteFile(want, []byte("%PDF-1.4\nlegacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveLegacyFile(root, "invoices", `invoices\\receipt.pdf`)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
	if _, err := resolveLegacyFile(root, "invoices", "../secret.pdf"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := resolveLegacyFile(root, "invoices", "receipt.pdf.enc"); err == nil {
		t.Fatal("encrypted attachment must be rejected")
	}
}

func TestInspectLegacyFileComputesSHA256(t *testing.T) {
	root := t.TempDir()
	invoiceDir := filepath.Join(root, "invoices")
	if err := os.MkdirAll(invoiceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invoiceDir, "receipt.pdf"), []byte("%PDF-1.4\nlegacy invoice\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := inspectLegacyFile(root, "invoices", "receipt.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if report.MediaType != "application/pdf" || report.SizeBytes == 0 || len(report.SHA256) != 64 {
		t.Fatalf("unexpected file report: %#v", report)
	}
}

func TestOpenSourceIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	writable, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec(`CREATE TABLE sample (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO sample(value) VALUES ('ok')`); err != nil {
		_ = writable.Close()
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	source, err := openSource(path, "test database")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	rows, err := source.rows(context.Background(), "sample", true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read source: rows=%v err=%v", rows, err)
	}
	if _, err := source.db.Exec("INSERT INTO sample(value) VALUES ('must fail')"); err == nil {
		t.Fatal("legacy source unexpectedly allowed a write")
	}
}
