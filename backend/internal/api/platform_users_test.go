package api_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func (h *harness) reauthPlatform(secret string) {
	h.t.Helper()
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		h.t.Fatalf("generate reauth code: %v", err)
	}
	res := h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{
		"password": "plattform-passwort", "code": code,
	})
	if res.Status != http.StatusOK {
		h.t.Fatalf("reauthenticate: %d %v", res.Status, res.Body)
	}
}

func TestSystemAdminManagesPlatformAccounts(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	h.reauthPlatform(secret)

	created := h.do(http.MethodPost, "/api/v1/platform/users", map[string]any{
		"username": unique("support-"), "contact_email": "support@example.org", "role": "support_admin",
	})
	if created.Status != http.StatusCreated || created.Body["setup_code"] == "" {
		t.Fatalf("create platform account: %d %v", created.Status, created.Body)
	}
	userID := int64(created.Body["id"].(float64))
	t.Cleanup(func() { _ = h.db.Exec("DELETE FROM users WHERE id = ?", userID).Error })

	listed := h.do(http.MethodGet, "/api/v1/platform/users", nil)
	if listed.Status != http.StatusOK || len(jsonList(listed.Body, "users")) < 2 {
		t.Fatalf("list platform accounts: %d %v", listed.Status, listed.Body)
	}
	if reset := h.do(http.MethodPost, "/api/v1/platform/users/"+itoa(userID)+"/reset-password", nil); reset.Status != http.StatusOK || reset.Body["setup_code"] == "" {
		t.Fatalf("reset platform password: %d %v", reset.Status, reset.Body)
	}
	if deactivate := h.do(http.MethodPatch, "/api/v1/platform/users/"+itoa(userID)+"/active",
		map[string]any{"active": false}); deactivate.Status != http.StatusNoContent {
		t.Fatalf("deactivate platform account: %d %v", deactivate.Status, deactivate.Body)
	}
	if self := h.do(http.MethodPatch, "/api/v1/platform/users/"+itoa(admin.ID)+"/active",
		map[string]any{"active": false}); self.Status != http.StatusForbidden {
		t.Fatalf("self deactivation must be refused: %d %v", self.Status, self.Body)
	}
}

func TestSupportAdminCannotManagePlatformAccounts(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSupportAdmin)
	h.signInPlatform(admin, secret)
	if res := h.do(http.MethodGet, "/api/v1/platform/users", nil); res.Status != http.StatusForbidden {
		t.Fatalf("support admin must not list platform accounts: %d %v", res.Status, res.Body)
	}
}

func TestPlatformUsernameIsUnique(t *testing.T) {
	h := newHarness(t)
	name := unique("platform-unique-")
	first, _, err := h.auth.CreateUser(h.ctx(), nil, name, models.RoleSupportAdmin)
	if err != nil {
		t.Fatalf("create first platform user: %v", err)
	}
	t.Cleanup(func() { _ = h.db.Exec("DELETE FROM users WHERE id = ?", first.ID).Error })
	if _, _, err := h.auth.CreateUser(h.ctx(), nil, name, models.RoleSupportAdmin); !errors.Is(err, auth.ErrUsernameTaken) {
		t.Fatalf("duplicate platform username must be rejected, got %v", err)
	}
}

func TestSystemAdminPasswordResetConsumesChallenge(t *testing.T) {
	h := newHarness(t)
	user := h.makeUser(nil, models.RoleSystemAdmin, "altes-passwort")
	const resetCode = "RESET-CODE-123"
	challenge := &models.PasswordResetChallenge{
		UserID: user.ID, CodeHash: auth.HashCode(resetCode),
		RequestedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := h.db.WithContext(h.ctx()).Create(challenge).Error; err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	bad := h.do(http.MethodPost, "/api/v1/auth/password-reset/confirm", map[string]any{
		"username": user.Username, "code": "wrong", "new_password": "neues-langes-passwort",
	})
	if bad.Status != http.StatusUnauthorized {
		t.Fatalf("wrong reset code must fail: %d %v", bad.Status, bad.Body)
	}
	good := h.do(http.MethodPost, "/api/v1/auth/password-reset/confirm", map[string]any{
		"username": user.Username, "code": resetCode, "new_password": "neues-langes-passwort",
	})
	if good.Status != http.StatusNoContent {
		t.Fatalf("valid reset code: %d %v", good.Status, good.Body)
	}
	if !auth.VerifyPassword("neues-langes-passwort", h.reload(user).PasswordHash) {
		t.Fatal("new password was not stored")
	}
	var count int64
	if err := h.db.WithContext(h.ctx()).Model(&models.PasswordResetChallenge{}).
		Where("user_id = ?", user.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("challenge must be consumed, count=%d err=%v", count, err)
	}
}

func TestPlatformAccountCanStoreRecoveryEmail(t *testing.T) {
	h := newHarness(t)
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	h.reauthPlatform(secret)
	res := h.do(http.MethodPut, "/api/v1/profile/contact-email", map[string]any{
		"contact_email": "admin@example.org",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("store contact email: %d %v", res.Status, res.Body)
	}
	if h.reload(admin).ContactEmail != "admin@example.org" {
		t.Fatal("contact email was not persisted")
	}
}
