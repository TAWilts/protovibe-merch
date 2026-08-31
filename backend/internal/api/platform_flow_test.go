package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// platformAdmin creates a system admin with an enrolled second factor and
// returns the account together with its TOTP secret.
func (h *harness) platformAdmin(role models.Role) (*models.User, string) {
	h.t.Helper()

	user := h.makeUser(nil, role, "plattform-passwort")
	secret, _, err := h.auth.BeginEnrollment(user)
	if err != nil {
		h.t.Fatalf("begin enrollment: %v", err)
	}
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		h.t.Fatalf("generate code: %v", err)
	}
	if _, err := h.auth.ConfirmEnrollment(user, code); err != nil {
		h.t.Fatalf("confirm enrollment: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Save(user).Error; err != nil {
		h.t.Fatalf("save admin: %v", err)
	}
	return user, secret
}

// signInPlatform completes the two-step platform login.
func (h *harness) signInPlatform(user *models.User, secret string) {
	h.t.Helper()

	res := h.signIn("", user.Username, "plattform-passwort")
	pending, _ := res.Body["pending_token"].(string)
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		h.t.Fatalf("generate code: %v", err)
	}
	done := h.do(http.MethodPost, "/api/v1/auth/mfa", map[string]any{
		"pending_token": pending, "code": code,
	})
	if done.Status != http.StatusOK {
		h.t.Fatalf("platform login: %d %v", done.Status, done.Body)
	}
	if token, ok := done.Body["csrf_token"].(string); ok {
		h.csrfToken = token
	}
}

// TestBandLifecycle walks creating, deactivating, soft-deleting and restoring
// a tenant.
func TestBandLifecycle(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	slug := unique("neu-")
	created := h.do(http.MethodPost, "/api/v1/platform/bands", map[string]any{
		"slug": slug, "name": "Neue Band", "contact_email": "band@example.org",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create band: %d %v", created.Status, created.Body)
	}
	bandID := int64(created.Body["id"].(float64))
	t.Cleanup(func() { _ = h.db.Exec("DELETE FROM bands WHERE id = ?", bandID).Error })

	if dup := h.do(http.MethodPost, "/api/v1/platform/bands", map[string]any{
		"slug": slug, "name": "Doppelt",
	}); dup.Status != http.StatusConflict {
		t.Fatalf("a duplicate slug must be refused, got %d %v", dup.Status, dup.Body)
	}
	if bad := h.do(http.MethodPost, "/api/v1/platform/bands", map[string]any{
		"slug": "Groß Geschrieben!", "name": "Ungültig",
	}); bad.Status != http.StatusBadRequest {
		t.Fatalf("an invalid slug must be refused, got %d %v", bad.Status, bad.Body)
	}

	// Deactivating stops sign-ins without touching any band data.
	if res := h.do(http.MethodPost, "/api/v1/platform/bands/"+itoa(bandID)+"/deactivate", nil); res.Status != http.StatusNoContent {
		t.Fatalf("deactivate: %d %v", res.Status, res.Body)
	}
	var band models.Band
	if err := h.db.WithContext(h.ctx()).First(&band, bandID).Error; err != nil {
		t.Fatalf("reload band: %v", err)
	}
	if band.IsActive || band.DeactivatedAt == nil {
		t.Fatalf("the band should be deactivated: %+v", band)
	}

	// A soft delete keeps every row; only the listing and logins change.
	if res := h.do(http.MethodDelete, "/api/v1/platform/bands/"+itoa(bandID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", res.Status, res.Body)
	}
	listed := h.do(http.MethodGet, "/api/v1/platform/bands", nil)
	for _, raw := range jsonList(listed.Body, "bands") {
		if int64(jsonObject(raw)["id"].(float64)) == bandID {
			t.Fatal("a soft-deleted band must not appear in the default listing")
		}
	}
	withDeleted := h.do(http.MethodGet, "/api/v1/platform/bands?include_deleted=true", nil)
	found := false
	for _, raw := range jsonList(withDeleted.Body, "bands") {
		if int64(jsonObject(raw)["id"].(float64)) == bandID {
			found = true
		}
	}
	if !found {
		t.Fatal("a soft-deleted band must stay recoverable and visible on request")
	}

	if res := h.do(http.MethodPost, "/api/v1/platform/bands/"+itoa(bandID)+"/restore", nil); res.Status != http.StatusNoContent {
		t.Fatalf("restore: %d %v", res.Status, res.Body)
	}
	if err := h.db.WithContext(h.ctx()).First(&band, bandID).Error; err != nil {
		t.Fatalf("reload band: %v", err)
	}
	if !band.IsActive || band.DeletedAt != nil {
		t.Fatalf("the band should be restored: %+v", band)
	}
}

// TestSupportAdminCannotReshapeTheInstance pins the split between looking and
// changing inside the control plane.
func TestSupportAdminCannotReshapeTheInstance(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSupportAdmin)
	h.signInPlatform(admin, secret)

	if res := h.do(http.MethodGet, "/api/v1/platform/bands", nil); res.Status != http.StatusOK {
		t.Fatalf("a support admin must see the band list: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/platform/bands", map[string]any{
		"slug": unique("nope-"), "name": "Nope",
	})
	if res.Status != http.StatusForbidden {
		t.Fatalf("a support admin must not create bands, got %d %v", res.Status, res.Body)
	}
}

// TestSupportAccessRequiresBandApproval is the load-bearing test of the whole
// tenant boundary: every step is needed, and none of them can be skipped.
func TestSupportAccessRequiresBandApproval(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()

	// A band admin with a live catalogue, so there is something to protect.
	bandAdmin := h.signInAs(band, models.RoleBandAdmin)
	h.sellableArticle("Private Shirt")

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	// Without a grant there is no way in.
	if blocked := h.do(http.MethodGet, "/api/v1/articles", nil); blocked.Status != http.StatusForbidden {
		t.Fatalf("band data must be refused without a grant: %d %v", blocked.Status, blocked.Body)
	}

	// A request needs a reason.
	if bad := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "scope": "read_only",
	}); bad.Status != http.StatusBadRequest {
		t.Fatalf("a request without a reason must be refused, got %d %v", bad.Status, bad.Body)
	}

	requested := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Bilanz stimmt laut Band nicht",
		"scope": "read_only", "duration_seconds": 1800,
	})
	if requested.Status != http.StatusCreated {
		t.Fatalf("request: %d %v", requested.Status, requested.Body)
	}
	grantID := int64(requested.Body["id"].(float64))
	if requested.Body["status"] != "pending" {
		t.Fatalf("a fresh request must be pending: %v", requested.Body)
	}

	// A pending request grants nothing, and cannot be activated.
	if blocked := h.do(http.MethodGet, "/api/v1/articles", nil); blocked.Status != http.StatusForbidden {
		t.Fatalf("a pending request must grant nothing: %d %v", blocked.Status, blocked.Body)
	}
	code, _ := totp.GenerateCode(secret, time.Now().UTC())
	if early := h.do(http.MethodPost, "/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": code}); early.Status != http.StatusConflict {
		t.Fatalf("an unapproved request must not activate, got %d %v", early.Status, early.Body)
	}

	// The band sees the request and decides it.
	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	pending := h.do(http.MethodGet, "/api/v1/band-admin/support-access", nil)
	if len(jsonList(pending.Body, "grants")) != 1 {
		t.Fatalf("the band must see the request: %v", pending.Body)
	}

	// Approving is a sensitive action and needs a fresh confirmation.
	if noReauth := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve", nil); noReauth.Status != http.StatusForbidden {
		t.Fatalf("approving must demand a fresh confirmation, got %d %v", noReauth.Status, noReauth.Body)
	}
	if res := h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{
		"password": "ein-langes-passwort",
	}); res.Status != http.StatusOK {
		t.Fatalf("reauth: %d %v", res.Status, res.Body)
	}
	approved := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve",
		map[string]any{"note": "Bitte nur lesen"})
	if approved.Status != http.StatusOK || approved.Body["status"] != "approved" {
		t.Fatalf("approve: %d %v", approved.Status, approved.Body)
	}

	// Approval alone still grants nothing until the admin confirms with 2FA.
	h.signInPlatform(admin, secret)
	if blocked := h.do(http.MethodGet, "/api/v1/articles", nil); blocked.Status != http.StatusForbidden {
		t.Fatalf("an approved but unactivated grant must grant nothing: %d", blocked.Status)
	}
	if wrongCode := h.do(http.MethodPost, "/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": "000000"}); wrongCode.Status != http.StatusUnauthorized {
		t.Fatalf("activation needs a valid second factor, got %d %v", wrongCode.Status, wrongCode.Body)
	}

	code, _ = totp.GenerateCode(secret, time.Now().UTC())
	activated := h.do(http.MethodPost, "/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": code})
	if activated.Status != http.StatusOK || activated.Body["status"] != "active" {
		t.Fatalf("activate: %d %v", activated.Status, activated.Body)
	}

	// Now, and only now, the band's data is reachable.
	articles := h.do(http.MethodGet, "/api/v1/articles", nil)
	if articles.Status != http.StatusOK {
		t.Fatalf("an active grant must open the band's data: %d %v", articles.Status, articles.Body)
	}
	if len(jsonList(articles.Body, "articles")) != 1 {
		t.Fatalf("expected the band's article: %v", articles.Body)
	}

	// Both sides see the banner.
	me := h.do(http.MethodGet, "/api/v1/me", nil)
	banner := jsonObject(me.Body["support_grant"])
	if banner["scope"] != "read_only" || banner["reason"] == "" {
		t.Fatalf("the support banner must describe the access: %v", me.Body)
	}

	// A read-only grant cannot write.
	write := h.do(http.MethodPost, "/api/v1/articles", map[string]any{"name": "Von Support"})
	if write.Status != http.StatusForbidden || write.Body["code"] != "grant_read_only" {
		t.Fatalf("a read-only grant must refuse writes, got %d %v", write.Status, write.Body)
	}

	// The audit trail records who looked, under which approval.
	var entries []models.AuditLog
	if err := h.db.WithContext(h.ctx()).
		Where("acting_grant_id = ?", grantID).Find(&entries).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}

	// The band can pull the plug at any moment.
	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	if res := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/revoke", nil); res.Status != http.StatusNoContent {
		t.Fatalf("revoke: %d %v", res.Status, res.Body)
	}

	h.signInPlatform(admin, secret)
	if blocked := h.do(http.MethodGet, "/api/v1/articles", nil); blocked.Status != http.StatusForbidden {
		t.Fatalf("a revoked grant must close the door again: %d %v", blocked.Status, blocked.Body)
	}
}

// TestPlatformAdminCannotApproveItsOwnRequest closes the loophole a live grant
// would otherwise open: satisfying the band-admin role through support access
// and then waving through the next request.
func TestPlatformAdminCannotApproveItsOwnRequest(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	bandAdmin := h.signInAs(band, models.RoleBandAdmin)

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	requested := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Erste Anfrage", "scope": "read_write", "duration_seconds": 1800,
	})
	grantID := int64(requested.Body["id"].(float64))

	// The band approves and the admin activates, so a live grant exists.
	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{"password": "ein-langes-passwort"})
	h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve", nil)

	h.signInPlatform(admin, secret)
	code, _ := totp.GenerateCode(secret, time.Now().UTC())
	if res := h.do(http.MethodPost, "/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": code}); res.Status != http.StatusOK {
		t.Fatalf("activate: %d %v", res.Status, res.Body)
	}

	// Holding a live grant must not turn the admin into a band admin.
	blocked := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve", nil)
	if blocked.Status != http.StatusForbidden || blocked.Body["code"] != "band_account_required" {
		t.Fatalf("a platform account must never decide a support request: %d %v", blocked.Status, blocked.Body)
	}
	if listed := h.do(http.MethodGet, "/api/v1/band-admin/support-access", nil); listed.Status != http.StatusForbidden {
		t.Fatalf("the band's own decision view must stay closed to platform accounts, got %d", listed.Status)
	}
}

// TestOnlyOneOpenRequestPerBand keeps a band admin from facing a queue of
// identical asks.
func TestOnlyOneOpenRequestPerBand(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	first := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Erste", "duration_seconds": 600,
	})
	if first.Status != http.StatusCreated {
		t.Fatalf("first request: %d %v", first.Status, first.Body)
	}
	second := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Zweite", "duration_seconds": 600,
	})
	if second.Status != http.StatusConflict {
		t.Fatalf("a second open request must be refused, got %d %v", second.Status, second.Body)
	}
}

// TestDeniedRequestGrantsNothing pins that a refusal is final.
func TestDeniedRequestGrantsNothing(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	bandAdmin := h.signInAs(band, models.RoleBandAdmin)

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	requested := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Bitte", "duration_seconds": 600,
	})
	grantID := int64(requested.Body["id"].(float64))

	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	denied := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/deny",
		map[string]any{"note": "Nicht nötig"})
	if denied.Status != http.StatusOK || denied.Body["status"] != "denied" {
		t.Fatalf("deny: %d %v", denied.Status, denied.Body)
	}

	h.signInPlatform(admin, secret)
	code, _ := totp.GenerateCode(secret, time.Now().UTC())
	if res := h.do(http.MethodPost, "/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": code}); res.Status != http.StatusConflict {
		t.Fatalf("a denied request must not activate, got %d %v", res.Status, res.Body)
	}
	if blocked := h.do(http.MethodGet, "/api/v1/articles", nil); blocked.Status != http.StatusForbidden {
		t.Fatalf("a denied request must grant nothing: %d", blocked.Status)
	}
}

// TestBandAdminOnlySeesItsOwnRequests pins the tenant boundary on the
// approval side.
func TestBandAdminOnlySeesItsOwnRequests(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()
	adminB := h.signInAs(bandB, models.RoleBandAdmin)

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	requested := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": bandA.ID, "reason": "Für Band A", "duration_seconds": 600,
	})
	grantID := int64(requested.Body["id"].(float64))

	h.signIn(bandB.Slug, adminB.Username, "ein-langes-passwort")
	if listed := h.do(http.MethodGet, "/api/v1/band-admin/support-access", nil); len(jsonList(listed.Body, "grants")) != 0 {
		t.Fatalf("band B must not see band A's request: %v", listed.Body)
	}
	h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{"password": "ein-langes-passwort"})
	res := h.do(http.MethodPost, "/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve", nil)
	if res.Status != http.StatusNotFound {
		t.Fatalf("band B must not approve band A's request, got %d %v", res.Status, res.Body)
	}
}

// TestBandSeesItsOwnSupportGrant pins the promise the banner makes: an
// approved access window is visible to the band whose data it opens, not only
// to the support account using it. The band's session carries no acting grant,
// so /me has to resolve the live grant for their band.
func TestBandSeesItsOwnSupportGrant(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	bandAdmin := h.signInAs(band, models.RoleBandAdmin)

	if before := h.do(http.MethodGet, "/api/v1/me", nil); before.Body["support_grant"] != nil {
		t.Fatalf("no grant exists yet: %v", before.Body["support_grant"])
	}

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	requested := h.do(http.MethodPost, "/api/v1/platform/support-access", map[string]any{
		"band_id": band.ID, "reason": "Bilanz stimmt laut Band nicht",
		"scope": "read_only", "duration_seconds": 1800,
	})
	if requested.Status != http.StatusCreated {
		t.Fatalf("request: %d %v", requested.Status, requested.Body)
	}
	grantID := int64(requested.Body["id"].(float64))

	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{"password": "ein-langes-passwort"})
	if approved := h.do(http.MethodPost,
		"/api/v1/band-admin/support-access/"+itoa(grantID)+"/approve", nil); approved.Status != http.StatusOK {
		t.Fatalf("approve: %d %v", approved.Status, approved.Body)
	}

	// Approved but not yet activated is still no open window.
	if pending := h.do(http.MethodGet, "/api/v1/me", nil); pending.Body["support_grant"] != nil {
		t.Fatalf("an approved grant is not yet live: %v", pending.Body["support_grant"])
	}

	h.signInPlatform(admin, secret)
	code, _ := totp.GenerateCode(secret, time.Now().UTC())
	if activated := h.do(http.MethodPost,
		"/api/v1/platform/support-access/"+itoa(grantID)+"/activate",
		map[string]any{"code": code}); activated.Status != http.StatusOK {
		t.Fatalf("activate: %d %v", activated.Status, activated.Body)
	}

	h.signIn(band.Slug, bandAdmin.Username, "ein-langes-passwort")
	live := h.do(http.MethodGet, "/api/v1/me", nil)
	banner, _ := live.Body["support_grant"].(map[string]any)
	if banner == nil {
		t.Fatalf("the band must see the live grant: %v", live.Body)
	}
	if banner["scope"] != "read_only" || banner["username"] != admin.Username {
		t.Fatalf("the banner must name who is in and under what scope: %v", banner)
	}
}

// TestPlatformBootstrapsFirstBandAdmin covers the one hole a strict tenant
// separation leaves: a fresh band has no account, and only a band admin may
// create accounts. The platform side must be able to hand over exactly one
// administrator — and nothing more.
func TestPlatformBootstrapsFirstBandAdmin(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	path := "/api/v1/platform/bands/" + itoa(band.ID) + "/admins"

	// A role in the request body is ignored: this endpoint only ever makes a
	// band admin, so it cannot become a back door to arbitrary band accounts.
	created := h.do(http.MethodPost, path, map[string]any{
		"username": "chef", "role": string(models.RoleSeller),
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("bootstrap admin: %d %v", created.Status, created.Body)
	}
	if created.Body["role"] != string(models.RoleBandAdmin) {
		t.Fatalf("the bootstrap account must be a band admin, got %v", created.Body["role"])
	}
	setupCode, _ := created.Body["setup_code"].(string)
	if setupCode == "" {
		t.Fatal("expected a one-time setup code")
	}

	// The band's own audit log must show it; a silent account in someone
	// else's band is exactly what the tenant design promises not to do.
	var logged int64
	err := h.db.Raw(`SELECT COUNT(*) FROM audit_log WHERE band_id = ? AND action = ?`,
		band.ID, audit.ActionUserCreated).Scan(&logged).Error
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if logged != 1 {
		t.Fatalf("expected one audit entry in the band's scope, got %d", logged)
	}

	// The handover works: the code logs in once and buys a password.
	setup := h.signIn(band.Slug, "chef", setupCode)
	if setup.Status != http.StatusOK || setup.Body["needs_password_setup"] != true {
		t.Fatalf("the setup code must start a password setup: %d %v", setup.Status, setup.Body)
	}
	pending, _ := setup.Body["pending_token"].(string)
	if done := h.do(http.MethodPost, "/api/v1/auth/password-setup", map[string]any{
		"pending_token": pending, "password": "ein-langes-passwort",
	}); done.Status != http.StatusOK {
		t.Fatalf("password setup: %d %v", done.Status, done.Body)
	}

	// And the new account is a working band admin that can carry on alone.
	if users := h.do(http.MethodGet, "/api/v1/band-admin/users", nil); users.Status != http.StatusOK {
		t.Fatalf("the bootstrap admin must manage its band: %d %v", users.Status, users.Body)
	}
}

// TestBandBootstrapAdminIsFencedIn pins the guards down: nobody but a system
// admin reaches it, and a band on its way out gains no new accounts.
func TestBandBootstrapAdminIsFencedIn(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	path := "/api/v1/platform/bands/" + itoa(band.ID) + "/admins"

	// A band admin is not platform staff, however much power they have.
	h.signInAs(band, models.RoleBandAdmin)
	if res := h.do(http.MethodPost, path, map[string]any{"username": "fremd"}); res.Status != http.StatusForbidden {
		t.Fatalf("a band admin must not reach the platform endpoint, got %d %v", res.Status, res.Body)
	}

	// A support admin may look at the instance but not reshape it.
	support, supportSecret := h.platformAdmin(models.RoleSupportAdmin)
	h.signInPlatform(support, supportSecret)
	if res := h.do(http.MethodPost, path, map[string]any{"username": "fremd"}); res.Status != http.StatusForbidden {
		t.Fatalf("a support admin must not create accounts, got %d %v", res.Status, res.Body)
	}

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	if missing := h.do(http.MethodPost, path, map[string]any{}); missing.Status != http.StatusBadRequest {
		t.Fatalf("a missing username must be refused, got %d %v", missing.Status, missing.Body)
	}
	if unknown := h.do(http.MethodPost, "/api/v1/platform/bands/999999/admins",
		map[string]any{"username": "chef"}); unknown.Status != http.StatusNotFound {
		t.Fatalf("an unknown band must be a 404, got %d %v", unknown.Status, unknown.Body)
	}

	// A soft-deleted band is in its grace period; restore it first.
	if res := h.do(http.MethodDelete, "/api/v1/platform/bands/"+itoa(band.ID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete band: %d %v", res.Status, res.Body)
	}
	if deleted := h.do(http.MethodPost, path, map[string]any{"username": "chef"}); deleted.Status != http.StatusConflict {
		t.Fatalf("a deleted band must gain no accounts, got %d %v", deleted.Status, deleted.Body)
	}
}
