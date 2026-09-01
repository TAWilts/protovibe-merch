package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// TestMaintenanceModeBlocksBandsButNotStaff pins the lever an operator reaches
// for during an upgrade: bands are held off, the people fixing it are not.
func TestMaintenanceModeBlocksBandsButNotStaff(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	bandUser := h.signInAs(band, models.RoleMember)
	bandCookie, bandCSRF := h.cookie, h.csrfToken

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	enabled := h.do(http.MethodPut, "/api/v1/platform/settings", map[string]any{
		"maintenance_enabled": true,
		"maintenance_message": "Wir aktualisieren gerade.",
	})
	if enabled.Status != http.StatusOK {
		t.Fatalf("enable maintenance: %d %v", enabled.Status, enabled.Body)
	}
	// Reset directly: an instance-wide setting must not stay on if this test
	// fails, and the reset itself must not depend on a working login.
	t.Cleanup(func() {
		_ = h.db.Exec("UPDATE platform_settings SET maintenance_enabled = 0 WHERE id = 1").Error
	})

	// Platform staff keep working — they are the ones fixing whatever caused it.
	if res := h.do(http.MethodGet, "/api/v1/platform/bands", nil); res.Status != http.StatusOK {
		t.Fatalf("platform staff must work during maintenance: %d %v", res.Status, res.Body)
	}

	// The band is held off, with the operator's message.
	h.cookie, h.csrfToken = bandCookie, bandCSRF
	blocked := h.do(http.MethodGet, "/api/v1/me", nil)
	if blocked.Status != http.StatusServiceUnavailable || blocked.Body["code"] != "maintenance" {
		t.Fatalf("the band must see the maintenance page: %d %v", blocked.Status, blocked.Body)
	}
	// Signing out must keep working, otherwise a stuck session cannot be cleared.
	if res := h.do(http.MethodPost, "/api/v1/auth/logout", nil); res.Status != http.StatusNoContent {
		t.Fatalf("logout must survive maintenance, got %d %v", res.Status, res.Body)
	}
	_ = bandUser
}

// TestAnnouncementReachesTheBands pins the broadcast used before a planned
// window.
func TestAnnouncementReachesTheBands(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	if res := h.do(http.MethodPut, "/api/v1/platform/settings", map[string]any{
		"announcement_text":  "Am Sonntag ab 22 Uhr kurze Wartung.",
		"announcement_level": "warning",
	}); res.Status != http.StatusOK {
		t.Fatalf("set announcement: %d %v", res.Status, res.Body)
	}
	t.Cleanup(func() {
		_ = h.db.Exec("UPDATE platform_settings SET announcement_text = '' WHERE id = 1").Error
	})

	h.signInAs(band, models.RoleSeller)
	res := h.do(http.MethodGet, "/api/v1/announcement", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("announcement: %d %v", res.Status, res.Body)
	}
	banner := jsonObject(res.Body["announcement"])
	if banner["level"] != "warning" || banner["text"] == "" {
		t.Fatalf("the band should see the banner: %v", res.Body)
	}
}

// TestSMTPPasswordIsNeverReturned pins that a stored mail password cannot be
// read back out of the admin center.
func TestSMTPPasswordIsNeverReturned(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	saved := h.do(http.MethodPut, "/api/v1/platform/settings", map[string]any{
		"smtp_enabled": true, "smtp_host": "smtp.example.org",
		"smtp_username": "merch@example.org", "smtp_password": "geheim-app-passwort",
	})
	if saved.Status != http.StatusOK {
		t.Fatalf("save settings: %d %v", saved.Status, saved.Body)
	}
	t.Cleanup(func() {
		_ = h.db.Exec("UPDATE platform_settings SET smtp_enabled = 0 WHERE id = 1").Error
	})

	if saved.Body["smtp_password_set"] != true {
		t.Fatalf("the settings should report a stored password: %v", saved.Body)
	}
	for key, value := range saved.Body {
		if text, ok := value.(string); ok && text == "geheim-app-passwort" {
			t.Fatalf("the password must never be returned, found in %q", key)
		}
	}

	// It is also encrypted at rest rather than stored in the clear.
	var settings models.PlatformSettings
	if err := h.db.WithContext(h.ctx()).First(&settings, 1).Error; err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.SMTPPasswordEncrypted == "" || settings.SMTPPasswordEncrypted == "geheim-app-passwort" {
		t.Fatalf("the stored password must be encrypted, got %q", settings.SMTPPasswordEncrypted)
	}
}

// TestAuditViewerFiltersAcrossBands pins the trail a band relies on to see who
// touched their data.
func TestAuditViewerFiltersAcrossBands(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	h.sellableArticle("Audit Shirt A")
	h.signInAs(bandB, models.RoleManager)
	h.sellableArticle("Audit Shirt B")

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	all := h.do(http.MethodGet, "/api/v1/platform/audit?action=article", nil)
	if all.Status != http.StatusOK {
		t.Fatalf("audit: %d %v", all.Status, all.Body)
	}
	if len(jsonList(all.Body, "entries")) < 2 {
		t.Fatalf("both bands' article creations should appear: %v", all.Body)
	}

	scoped := h.do(http.MethodGet, "/api/v1/platform/audit?action=article&band_id="+itoa(bandA.ID), nil)
	entries := jsonList(scoped.Body, "entries")
	if len(entries) != 1 {
		t.Fatalf("expected exactly band A's entry: %v", scoped.Body)
	}
	entry := jsonObject(entries[0])
	if int64(entry["band_id"].(float64)) != bandA.ID {
		t.Fatalf("the filter must hold: %v", entry)
	}
	if entry["band_name"] == "" {
		t.Fatalf("the viewer should resolve the band name: %v", entry)
	}
}

// TestSessionKillTakesEffectImmediately pins the lever for a suspected leak.
func TestSessionKillTakesEffectImmediately(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)
	bandCookie, bandCSRF := h.cookie, h.csrfToken

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	if res := h.do(http.MethodPost, "/api/v1/platform/bands/"+itoa(band.ID)+"/revoke-sessions", nil); res.Status != http.StatusNoContent {
		t.Fatalf("revoke: %d %v", res.Status, res.Body)
	}

	h.cookie, h.csrfToken = bandCookie, bandCSRF
	if res := h.do(http.MethodGet, "/api/v1/me", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("the revoked session must be rejected at once, got %d", res.Status)
	}
}

// TestSupportInboxCrossesBands pins that a band's report reaches the operator
// and stays attributable.
func TestSupportInboxCrossesBands(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	seller := h.signInAs(band, models.RoleSeller)

	sent := h.do(http.MethodPost, "/api/v1/support-messages", map[string]any{
		"message_type": "question", "subject": "Wie storniere ich?",
		"body": "Wir haben aus Versehen doppelt gebucht.", "sender_email": "band@example.org",
	})
	if sent.Status != http.StatusCreated {
		t.Fatalf("send: %d %v", sent.Status, sent.Body)
	}
	messageID := int64(sent.Body["id"].(float64))

	admin, secret := h.platformAdmin(models.RoleSupportAdmin)
	h.signInPlatform(admin, secret)

	assignees := h.do(http.MethodGet, "/api/v1/platform/message-assignees", nil)
	if assignees.Status != http.StatusOK {
		t.Fatalf("assignees: %d %v", assignees.Status, assignees.Body)
	}
	foundAssignee := false
	for _, raw := range jsonList(assignees.Body, "users") {
		if int64(jsonObject(raw)["id"].(float64)) == admin.ID {
			foundAssignee = true
		}
	}
	if !foundAssignee {
		t.Fatalf("support admin must be assignable: %v", assignees.Body)
	}
	if res := h.do(http.MethodPatch, "/api/v1/platform/messages/"+itoa(messageID)+"/assignment",
		map[string]any{"assignee_user_id": admin.ID}); res.Status != http.StatusNoContent {
		t.Fatalf("assign: %d %v", res.Status, res.Body)
	}

	inbox := h.do(http.MethodGet, "/api/v1/platform/messages?open=true", nil)
	found := false
	for _, raw := range jsonList(inbox.Body, "messages") {
		message := jsonObject(raw)
		if int64(message["id"].(float64)) == messageID {
			found = true
			if message["sender_username"] != seller.Username || message["band_name"] == "" {
				t.Fatalf("the message must stay attributable: %v", message)
			}
			if int64(message["assigned_to_user_id"].(float64)) != admin.ID ||
				message["assigned_to_username"] != admin.Username {
				t.Fatalf("the assignment must appear in the shared inbox: %v", message)
			}
		}
	}
	if !found {
		t.Fatalf("the message should reach the platform inbox: %v", inbox.Body)
	}

	if res := h.do(http.MethodPost, "/api/v1/platform/messages/"+itoa(messageID)+"/resolve",
		map[string]any{"resolved": true}); res.Status != http.StatusNoContent {
		t.Fatalf("resolve: %d %v", res.Status, res.Body)
	}
	open := h.do(http.MethodGet, "/api/v1/platform/messages?open=true", nil)
	for _, raw := range jsonList(open.Body, "messages") {
		if int64(jsonObject(raw)["id"].(float64)) == messageID {
			t.Fatal("a resolved message must leave the open list")
		}
	}
}

// TestBandsOnlySeeTheirOwnSupportMessages pins the tenant boundary on the
// inbox.
func TestBandsOnlySeeTheirOwnSupportMessages(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleBandAdmin)
	created := h.do(http.MethodPost, "/api/v1/support-messages", map[string]any{
		"message_type": "issue", "subject": "Band A", "body": "Etwas ist kaputt.",
		"sender_email": "banda@example.org",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("send band A message: %d %v", created.Status, created.Body)
	}

	h.signInAs(bandB, models.RoleBandAdmin)
	listed := h.do(http.MethodGet, "/api/v1/support-messages", nil)
	if len(jsonList(listed.Body, "messages")) != 0 {
		t.Fatalf("band B must not see band A's report: %v", listed.Body)
	}
}

// TestMetricsAreExposed pins that the monitoring endpoint answers without a
// session, since a scraper has none.
func TestMetricsAreExposed(t *testing.T) {
	h := newHarness(t)
	h.cookie, h.csrfToken = "", ""

	status, body, _ := h.download("/metrics")
	if status != http.StatusOK {
		t.Fatalf("metrics: %d", status)
	}
	if len(body) == 0 {
		t.Fatal("the metrics endpoint returned nothing")
	}
}
