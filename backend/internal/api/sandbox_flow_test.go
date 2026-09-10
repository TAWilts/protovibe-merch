package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func sandboxIdentity(body map[string]any) map[string]any {
	return jsonObject(body["session"])
}

func sandboxID(body map[string]any) int64 {
	return int64(jsonObject(sandboxIdentity(body)["sandbox"])["id"].(float64))
}

func cleanupSandboxes(t *testing.T, h *harness) {
	t.Helper()
	var environments []models.SandboxEnvironment
	if err := h.db.WithContext(h.ctx()).Find(&environments).Error; err != nil {
		t.Fatalf("list sandbox cleanup targets: %v", err)
	}
	for i := range environments {
		if err := h.api.Sandboxes().Purge(context.Background(), &environments[i]); err != nil {
			t.Fatalf("purge sandbox: %v", err)
		}
	}
}

func TestAnonymousSandboxUsesSeparateSessionAndSeedData(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { cleanupSandboxes(t, h) })

	started := h.startSandbox()
	if started.Status != http.StatusOK {
		t.Fatalf("start sandbox: %d %v", started.Status, started.Body)
	}
	if h.cookie != "" || h.sandboxCookie == "" || h.sandboxCSRFToken == "" {
		t.Fatalf("sandbox must use only its separate cookies: normal=%q sandbox=%q", h.cookie, h.sandboxCookie)
	}
	identity := sandboxIdentity(started.Body)
	if jsonObject(identity["sandbox"])["storage_quota_bytes"].(float64) != 25*1024*1024 {
		t.Fatalf("unexpected sandbox quota: %v", identity["sandbox"])
	}

	articles := h.do(http.MethodGet, "/api/v1/sandbox/articles?include_inactive=true", nil)
	if articles.Status != http.StatusOK || len(jsonList(articles.Body, "articles")) == 0 {
		t.Fatalf("seed catalogue missing: %d %v", articles.Status, articles.Body)
	}
	balances := h.do(http.MethodGet, "/api/v1/sandbox/balances", nil)
	if balances.Status != http.StatusOK {
		t.Fatalf("seed balances unavailable: %d %v", balances.Status, balances.Body)
	}
	modes := map[string]bool{}
	for _, key := range []string{"reorder_rows", "obsolete_rows"} {
		for _, raw := range jsonList(balances.Body, key) {
			modes[jsonObject(raw)["stock_mode"].(string)] = true
		}
	}
	for _, mode := range []string{"stocked", "on_demand", "clearance", "paused", "discontinued"} {
		if !modes[mode] {
			t.Errorf("seed balance is missing stock mode %q: %v", mode, modes)
		}
	}
}

func TestSignedInSandboxPreservesRealSessionAndCanSwitchRole(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { cleanupSandboxes(t, h) })
	band := h.makeBand()
	user := h.signInAs(band, models.RoleManager)
	realCookie := h.cookie

	started := h.startSandbox()
	if started.Status != http.StatusOK || h.cookie != realCookie {
		t.Fatalf("starting sandbox replaced real session: %d normal=%q", started.Status, h.cookie)
	}
	changed := h.do(http.MethodPatch, "/api/v1/sandbox/role", map[string]any{"role": "seller"})
	if changed.Status != http.StatusOK || jsonObject(sandboxIdentity(changed.Body)["user"])["role"] != "seller" {
		t.Fatalf("switch role: %d %v", changed.Status, changed.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/sandbox/balances", nil); res.Status != http.StatusForbidden {
		t.Fatalf("seller should not receive member reports, got %d", res.Status)
	}
	real := h.do(http.MethodGet, "/api/v1/me", nil)
	if real.Status != http.StatusOK || int64(jsonObject(real.Body["user"])["id"].(float64)) != user.ID {
		t.Fatalf("real identity was not preserved: %d %v", real.Status, real.Body)
	}
}

func TestSigningInReplacesAnonymousSandboxWithUserBoundEnvironment(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { cleanupSandboxes(t, h) })
	anonymous := h.startSandbox()
	anonymousID := sandboxID(anonymous.Body)

	band := h.makeBand()
	user := h.signInAs(band, models.RoleMember)
	linked := h.startSandbox()
	if linked.Status != http.StatusOK || sandboxID(linked.Body) == anonymousID {
		t.Fatalf("signed-in browser retained its anonymous sandbox: %d %v", linked.Status, linked.Body)
	}
	var environment models.SandboxEnvironment
	if err := h.db.WithContext(h.ctx()).First(&environment, sandboxID(linked.Body)).Error; err != nil || environment.SourceUserID == nil || *environment.SourceUserID != user.ID {
		t.Fatalf("sandbox was not linked to the real user: %v %+v", err, environment)
	}
	var retired models.SandboxEnvironment
	if err := h.db.WithContext(h.ctx()).First(&retired, anonymousID).Error; err != nil || retired.Status != models.SandboxStatusPurging {
		t.Fatalf("anonymous predecessor was not retired: %v %+v", err, retired)
	}

	// Without the corresponding real login cookie, the linked demo cookie is
	// not enough to cross an account boundary on a shared browser.
	h.cookie = ""
	if denied := h.do(http.MethodGet, "/api/v1/sandbox/me", nil); denied.Status != http.StatusUnauthorized {
		t.Fatalf("linked sandbox survived without its real session: %d %v", denied.Status, denied.Body)
	}
}

func TestSandboxResetAndExternalActionBlock(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { cleanupSandboxes(t, h) })
	started := h.startSandbox()
	oldID := sandboxID(started.Body)

	if blocked := h.do(http.MethodPost, "/api/v1/sandbox/support-messages", map[string]any{}); blocked.Status != http.StatusForbidden || blocked.Body["code"] != "sandbox_external_action_blocked" {
		t.Fatalf("external action was not explicitly blocked: %d %v", blocked.Status, blocked.Body)
	}
	reset := h.do(http.MethodPost, "/api/v1/sandbox/reset", nil)
	if reset.Status != http.StatusOK || sandboxID(reset.Body) == oldID {
		t.Fatalf("reset did not switch atomically to a new sandbox: %d %v", reset.Status, reset.Body)
	}
	var old models.SandboxEnvironment
	if err := h.db.WithContext(h.ctx()).First(&old, oldID).Error; err != nil || old.Status != models.SandboxStatusPurging {
		t.Fatalf("old sandbox was not scheduled for cleanup: %v %+v", err, old)
	}
	if secondReset := h.do(http.MethodPost, "/api/v1/sandbox/reset", nil); secondReset.Status != http.StatusOK {
		t.Fatalf("second replacement should still be inside the hourly creation limit: %d %v", secondReset.Status, secondReset.Body)
	}
	if limited := h.do(http.MethodPost, "/api/v1/sandbox/reset", nil); limited.Status != http.StatusTooManyRequests || limited.Body["code"] != "sandbox_rate_limited" {
		t.Fatalf("sandbox replacements must count towards the hourly IP limit: %d %v", limited.Status, limited.Body)
	}

	_, body, disposition := h.download("/api/v1/sandbox/exports/artikel.csv")
	if !strings.Contains(disposition, "DEMO_") || !bytes.Contains(body, []byte("SANDBOX")) {
		t.Fatalf("sandbox export is not visibly marked: %q", disposition)
	}
}
