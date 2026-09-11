package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
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
	if jsonObject(identity["sandbox"])["template_version"].(float64) != 2 {
		t.Fatalf("unexpected sandbox template: %v", identity["sandbox"])
	}

	articles := h.do(http.MethodGet, "/api/v1/sandbox/articles?include_inactive=true", nil)
	if articles.Status != http.StatusOK || len(jsonList(articles.Body, "articles")) == 0 {
		t.Fatalf("seed catalogue missing: %d %v", articles.Status, articles.Body)
	}
	var tourShirt map[string]any
	for _, raw := range jsonList(articles.Body, "articles") {
		article := jsonObject(raw)
		if article["name"] == "Tour Shirt" {
			tourShirt = article
			break
		}
	}
	if tourShirt == nil {
		t.Fatalf("Tour Shirt missing from sandbox catalogue: %v", articles.Body)
	}
	groups := jsonList(tourShirt, "option_groups")
	wantedOptions := []struct {
		name   string
		values []string
	}{
		{name: "Größe", values: []string{"S", "M", "L", "XL", "XXL"}},
		{name: "Farbe", values: []string{"Weiß", "Schwarz"}},
	}
	if len(groups) != len(wantedOptions) {
		t.Fatalf("Tour Shirt should have size and colour options: %v", groups)
	}
	for index, wanted := range wantedOptions {
		group := jsonObject(groups[index])
		if group["name"] != wanted.name {
			t.Errorf("option group %d = %v, want %q", index, group["name"], wanted.name)
		}
		values := jsonList(group, "values")
		if len(values) != len(wanted.values) {
			t.Fatalf("option group %q values = %v, want %v", wanted.name, values, wanted.values)
		}
		for valueIndex, wantedValue := range wanted.values {
			if got := jsonObject(values[valueIndex])["value"]; got != wantedValue {
				t.Errorf("option group %q value %d = %v, want %q", wanted.name, valueIndex, got, wantedValue)
			}
		}
	}
	if variants := jsonList(tourShirt, "variants"); len(variants) != 10 {
		t.Fatalf("Tour Shirt should have all ten size/colour combinations, got %d", len(variants))
	}

	photos := h.do(http.MethodGet, "/api/v1/sandbox/photos", nil)
	photoList := jsonList(photos.Body, "photos")
	if photos.Status != http.StatusOK || len(photoList) != 1 {
		t.Fatalf("sandbox shirt photo missing: %d %v", photos.Status, photos.Body)
	}
	photo := jsonObject(photoList[0])
	if photo["article_name"] != "Tour Shirt" || photo["original_filename"] != "shirt.jpg" {
		t.Fatalf("sandbox photo is not attached to the Tour Shirt: %v", photo)
	}
	photoID := int64(photo["id"].(float64))
	photoStatus, photoBody, _ := h.download("/api/v1/sandbox/photos/" + itoa(photoID) + "/file")
	const landingShirtSHA256 = "9ecc09892f0ea7b9ee803a709dc95545667a3c12542d6534cbfcbef44ebacdac"
	if digest := fmt.Sprintf("%x", sha256.Sum256(photoBody)); photoStatus != http.StatusOK || digest != landingShirtSHA256 {
		t.Fatalf("sandbox shirt photo differs from the landing-page demo photo: status=%d sha256=%s", photoStatus, digest)
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
