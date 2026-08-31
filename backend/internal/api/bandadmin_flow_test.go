package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// reauth opens the step-up window every account change needs.
func (h *harness) reauth(password string) {
	h.t.Helper()
	if res := h.do(http.MethodPost, "/api/v1/profile/reauth",
		map[string]any{"password": password}); res.Status != http.StatusOK {
		h.t.Fatalf("reauth: %d %v", res.Status, res.Body)
	}
}

// TestBandUserLifecycle walks creating an account, handing over its setup code,
// changing its role and removing it.
func TestBandUserLifecycle(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)

	// Every account change needs a fresh confirmation, as in the original.
	if res := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "neuer-seller", "role": "seller"}); res.Status != http.StatusForbidden {
		t.Fatalf("creating an account must demand a fresh confirmation, got %d %v", res.Status, res.Body)
	}
	h.reauth("ein-langes-passwort")

	created := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "neuer-seller", "role": "seller"})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", created.Status, created.Body)
	}
	code, _ := created.Body["setup_code"].(string)
	if code == "" {
		t.Fatalf("the one-time setup code must be returned once: %v", created.Body)
	}
	userID := int64(created.Body["id"].(float64))

	// The new account can sign in with it and must choose a password.
	adminCookie, adminCSRF := h.cookie, h.csrfToken
	res := h.signIn(band.Slug, "neuer-seller", code)
	if res.Status != http.StatusOK || res.Body["needs_password_setup"] != true {
		t.Fatalf("the setup code must work: %d %v", res.Status, res.Body)
	}

	h.cookie, h.csrfToken = adminCookie, adminCSRF
	h.reauth("ein-langes-passwort")

	// Promotion takes effect at once, which is why sessions are revoked.
	if res := h.do(http.MethodPatch, "/api/v1/band-admin/users/"+itoa(userID)+"/role",
		map[string]any{"role": "manager"}); res.Status != http.StatusNoContent {
		t.Fatalf("change role: %d %v", res.Status, res.Body)
	}
	listed := h.do(http.MethodGet, "/api/v1/band-admin/users", nil)
	for _, raw := range jsonList(listed.Body, "users") {
		user := jsonObject(raw)
		if int64(user["id"].(float64)) == userID && user["role"] != "manager" {
			t.Fatalf("the role should have changed: %v", user)
		}
	}

	if res := h.do(http.MethodDelete, "/api/v1/band-admin/users/"+itoa(userID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", res.Status, res.Body)
	}
}

// TestABandCannotLockItselfOut pins the guards that keep a band from ending up
// with nobody able to manage it.
func TestABandCannotLockItselfOut(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	admin := h.signInAs(band, models.RoleBandAdmin)
	h.reauth("ein-langes-passwort")

	self := "/api/v1/band-admin/users/" + itoa(admin.ID)

	if res := h.do(http.MethodPatch, self+"/role", map[string]any{"role": "seller"}); res.Status != http.StatusForbidden {
		t.Fatalf("an admin must not demote themselves, got %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodPatch, self+"/active", map[string]any{"active": false}); res.Status != http.StatusForbidden {
		t.Fatalf("an admin must not deactivate themselves, got %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodDelete, self, nil); res.Status != http.StatusForbidden {
		t.Fatalf("an admin must not delete themselves, got %d %v", res.Status, res.Body)
	}

	// A second admin makes the first one removable again.
	second := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "zweiter-admin", "role": "band_admin"})
	if second.Status != http.StatusCreated {
		t.Fatalf("create second admin: %d %v", second.Status, second.Body)
	}
	secondID := int64(second.Body["id"].(float64))

	// Now demoting the *other* admin is allowed, because one remains.
	if res := h.do(http.MethodPatch, "/api/v1/band-admin/users/"+itoa(secondID)+"/role",
		map[string]any{"role": "manager"}); res.Status != http.StatusNoContent {
		t.Fatalf("demoting the second admin should work: %d %v", res.Status, res.Body)
	}
}

// TestBandAdminsCannotReachOtherBandsAccounts pins the tenant boundary on the
// most sensitive surface there is.
func TestBandAdminsCannotReachOtherBandsAccounts(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	victim := h.makeUser(&bandA.ID, models.RoleMember, "ein-langes-passwort")

	h.signInAs(bandB, models.RoleBandAdmin)
	h.reauth("ein-langes-passwort")

	if listed := h.do(http.MethodGet, "/api/v1/band-admin/users", nil); len(jsonList(listed.Body, "users")) != 1 {
		t.Fatalf("band B must only see its own account: %v", listed.Body)
	}
	for _, path := range []string{
		"/api/v1/band-admin/users/" + itoa(victim.ID) + "/reset-password",
		"/api/v1/band-admin/users/" + itoa(victim.ID) + "/reset-mfa",
	} {
		if res := h.do(http.MethodPost, path, nil); res.Status != http.StatusNotFound {
			t.Fatalf("%s must be refused, got %d %v", path, res.Status, res.Body)
		}
	}
	if res := h.do(http.MethodDelete, "/api/v1/band-admin/users/"+itoa(victim.ID), nil); res.Status != http.StatusNotFound {
		t.Fatalf("band B must not delete band A's account, got %d", res.Status)
	}
}

// TestUserQuotaIsEnforced pins the per-band limit the admin center sets.
func TestUserQuotaIsEnforced(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	h.reauth("ein-langes-passwort")

	// One account exists already; a limit of two allows exactly one more.
	if err := h.db.WithContext(h.ctx()).Model(&models.Band{}).
		Where("id = ?", band.ID).Update("user_quota", 2).Error; err != nil {
		t.Fatalf("set quota: %v", err)
	}

	if res := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "zweiter", "role": "seller"}); res.Status != http.StatusCreated {
		t.Fatalf("the second account must fit: %d %v", res.Status, res.Body)
	}
	res := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "dritter", "role": "seller"})
	if res.Status != http.StatusConflict || res.Body["code"] != "user_quota_reached" {
		t.Fatalf("the third must be refused, got %d %v", res.Status, res.Body)
	}
}

// TestOnlyBandRolesCanBeAssigned keeps a band admin from minting a platform
// account for themselves.
func TestOnlyBandRolesCanBeAssigned(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	h.reauth("ein-langes-passwort")

	for _, role := range []string{"system_admin", "support_admin", "nonsense"} {
		res := h.do(http.MethodPost, "/api/v1/band-admin/users",
			map[string]any{"username": "eskaliert-" + role, "role": role})
		if res.Status != http.StatusBadRequest {
			t.Fatalf("role %q must be refused, got %d %v", role, res.Status, res.Body)
		}
	}
}

// TestDeletingAnAccountKeepsItsBookings pins the reason bookings store a
// username snapshot rather than only a foreign key.
func TestDeletingAnAccountKeepsItsBookings(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	admin := h.signInAs(band, models.RoleBandAdmin)
	_, variants := h.sellableArticle("Legacy Shirt")

	h.reauth("ein-langes-passwort")
	created := h.do(http.MethodPost, "/api/v1/band-admin/users",
		map[string]any{"username": "verkaeufer", "role": "manager"})
	sellerID := int64(created.Body["id"].(float64))
	code, _ := created.Body["setup_code"].(string)

	adminCookie, adminCSRF := h.cookie, h.csrfToken

	// The seller sets a password and books a sale.
	res := h.signIn(band.Slug, "verkaeufer", code)
	pending, _ := res.Body["pending_token"].(string)
	setup := h.do(http.MethodPost, "/api/v1/auth/password-setup",
		map[string]any{"pending_token": pending, "password": "verkaeufer-passwort"})
	if token, ok := setup.Body["csrf_token"].(string); ok {
		h.csrfToken = token
	}
	booked := h.do(http.MethodPost, "/api/v1/sales", map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "Bar", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	})
	if booked.Status != http.StatusCreated {
		t.Fatalf("book: %d %v", booked.Status, booked.Body)
	}

	// The admin removes the account.
	h.cookie, h.csrfToken = adminCookie, adminCSRF
	h.reauth("ein-langes-passwort")
	if res := h.do(http.MethodDelete, "/api/v1/band-admin/users/"+itoa(sellerID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", res.Status, res.Body)
	}

	// The sale is still there, still attributed.
	history := h.do(http.MethodGet, "/api/v1/history", nil)
	receipts := jsonList(history.Body, "receipts")
	if len(receipts) != 1 {
		t.Fatalf("the booking must survive the account: %v", history.Body)
	}
	if jsonObject(receipts[0])["sold_by"] != "verkaeufer" {
		t.Fatalf("the username snapshot must remain: %v", receipts[0])
	}
	_ = admin
}
