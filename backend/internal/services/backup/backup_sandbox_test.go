package backup

import (
	"strings"
	"testing"
)

func TestProductionBackupFilterExcludesSandboxData(t *testing.T) {
	if _, include := productionBackupFilter("merch", "sandbox_environments", true); include {
		t.Fatal("sandbox environment rows must never be written to a production backup")
	}

	for _, test := range []struct {
		table       string
		hasBandID   bool
		mustContain string
	}{
		{table: "bands", mustContain: "SELECT band_id"},
		{table: "users", hasBandID: true, mustContain: "band_id NOT IN"},
		{table: "sessions", hasBandID: true, mustContain: "band_id NOT IN"},
		{table: "articles", hasBandID: true, mustContain: "band_id NOT IN"},
		{table: "pending_auth", mustContain: "SELECT user_id"},
	} {
		where, include := productionBackupFilter("merch", test.table, test.hasBandID)
		if !include || !strings.Contains(where, test.mustContain) {
			t.Fatalf("%s is not filtered from sandbox data: include=%v where=%q", test.table, include, where)
		}
	}

	if where, include := productionBackupFilter("merch", "platform_settings", false); !include || where != "" {
		t.Fatalf("global production data must stay in the backup, include=%v where=%q", include, where)
	}
}
