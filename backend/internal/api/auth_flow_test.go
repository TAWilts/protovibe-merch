package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestLoginWithPassword(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleSeller, "ein-langes-passwort")

	res := h.signIn(band.Slug, user.Username, "ein-langes-passwort")
	if res.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", res.Status, res.Body)
	}
	if h.csrfToken == "" {
		t.Fatal("login must hand out a CSRF token")
	}

	me := h.do(http.MethodGet, "/api/v1/me", nil)
	if me.Status != http.StatusOK {
		t.Fatalf("me: expected 200, got %d: %v", me.Status, me.Body)
	}
	caps, _ := me.Body["capabilities"].(map[string]any)
	if caps["can_access_band_workflows"] != true {
		t.Fatalf("a seller must have band workflow access: %v", caps)
	}
	if caps["can_manage_articles"] != false {
		t.Fatalf("a seller must not manage articles: %v", caps)
	}
}

// TestWrongCredentialsAreIndistinguishable pins that the endpoint does not
// reveal which usernames exist on an instance.
func TestWrongCredentialsAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleSeller, "ein-langes-passwort")

	wrongPassword := h.signIn(band.Slug, user.Username, "falsches-passwort")
	unknownUser := h.signIn(band.Slug, "does-not-exist", "falsches-passwort")

	if wrongPassword.Status != http.StatusUnauthorized || unknownUser.Status != http.StatusUnauthorized {
		t.Fatalf("both must be 401, got %d and %d", wrongPassword.Status, unknownUser.Status)
	}
	if wrongPassword.Body["code"] != unknownUser.Body["code"] {
		t.Fatalf("error codes must match: %v vs %v", wrongPassword.Body, unknownUser.Body)
	}
}

// TestUsernamesAreUniquePerBand is the multi-tenant consequence of the schema:
// two bands may each have a user with the same name, and each password only
// works within its own band.
func TestUsernamesAreUniquePerBand(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	name := unique("shared-")
	for _, spec := range []struct {
		band     *models.Band
		password string
	}{{bandA, "passwort-band-aaa"}, {bandB, "passwort-band-bbb"}} {
		hash, err := auth.HashPassword(spec.password)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		user := &models.User{
			BandID:                &spec.band.ID,
			Username:              name,
			PasswordHash:          hash,
			Role:                  models.RoleMember,
			IsActive:              true,
			MFARecoveryCodeHashes: models.JSONSlice{},
		}
		if err := h.db.WithContext(h.ctx()).Create(user).Error; err != nil {
			t.Fatalf("create user in %s: %v", spec.band.Slug, err)
		}
	}

	if res := h.signIn(bandA.Slug, name, "passwort-band-aaa"); res.Status != http.StatusOK {
		t.Fatalf("band A login failed: %d %v", res.Status, res.Body)
	}
	if res := h.signIn(bandB.Slug, name, "passwort-band-aaa"); res.Status != http.StatusUnauthorized {
		t.Fatalf("band A's password must not work in band B, got %d", res.Status)
	}
}

// TestSetupCodeFlow walks the account lifecycle an admin creates: one-time
// code, forced password choice, then a normal session.
func TestSetupCodeFlow(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()

	user, code, err := h.auth.CreateUser(h.ctx(), &band.ID, unique("neu-"), models.RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _ = h.db.Exec("DELETE FROM users WHERE id = ?", user.ID).Error })

	res := h.signIn(band.Slug, user.Username, code)
	if res.Status != http.StatusOK || res.Body["needs_password_setup"] != true {
		t.Fatalf("the setup code must lead to password setup: %d %v", res.Status, res.Body)
	}
	pending, _ := res.Body["pending_token"].(string)
	if pending == "" {
		t.Fatal("expected a pending token")
	}

	// The code alone must not yield a session.
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusUnauthorized {
		t.Fatalf("no session may exist before the password is set, got %d", me.Status)
	}

	setup := h.do(http.MethodPost, "/api/v1/auth/password-setup", map[string]any{
		"pending_token": pending,
		"password":      "mein-neues-passwort",
	})
	if setup.Status != http.StatusOK {
		t.Fatalf("password setup failed: %d %v", setup.Status, setup.Body)
	}
	if token, ok := setup.Body["csrf_token"].(string); ok {
		h.csrfToken = token
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusOK {
		t.Fatalf("a session must exist after setup: %d %v", me.Status, me.Body)
	}

	// The one-time code must not work a second time.
	if again := h.signIn(band.Slug, user.Username, code); again.Status != http.StatusUnauthorized {
		t.Fatalf("a consumed setup code must be rejected, got %d %v", again.Status, again.Body)
	}
	if final := h.signIn(band.Slug, user.Username, "mein-neues-passwort"); final.Status != http.StatusOK {
		t.Fatalf("the chosen password must work: %d %v", final.Status, final.Body)
	}
}

// TestPlatformAccountMustEnrolMFA pins the rule that platform staff cannot
// obtain a session without a second factor.
func TestPlatformAccountMustEnrolMFA(t *testing.T) {
	h := newHarness(t)
	admin := h.makeUser(nil, models.RoleSystemAdmin, "plattform-passwort")

	res := h.signIn("", admin.Username, "plattform-passwort")
	if res.Status != http.StatusOK || res.Body["needs_mfa_enrollment"] != true {
		t.Fatalf("expected enrolment to be demanded: %d %v", res.Status, res.Body)
	}
	pending, _ := res.Body["pending_token"].(string)

	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusUnauthorized {
		t.Fatalf("no session may exist before enrolment, got %d", me.Status)
	}

	start := h.do(http.MethodPost, "/api/v1/mfa/enrollment/start", map[string]any{"pending_token": pending})
	if start.Status != http.StatusOK {
		t.Fatalf("enrolment start failed: %d %v", start.Status, start.Body)
	}
	secret, _ := start.Body["secret"].(string)
	if secret == "" {
		t.Fatal("expected a TOTP secret")
	}
	// The scannable code matters as much as the secret: without it enrolment
	// means typing 32 characters into a phone by hand.
	if qr, _ := start.Body["otpauth_qr"].(string); !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("expected a QR code data URI, got %q", qr)
	}

	// A wrong code must leave the account unenrolled.
	bad := h.do(http.MethodPost, "/api/v1/mfa/enrollment/confirm", map[string]any{
		"pending_token": pending, "code": "000000",
	})
	if bad.Status != http.StatusUnauthorized {
		t.Fatalf("a wrong code must be rejected, got %d %v", bad.Status, bad.Body)
	}
	if h.reload(admin).MFAEnabled {
		t.Fatal("a failed confirmation must not enable the second factor")
	}

	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	confirm := h.do(http.MethodPost, "/api/v1/mfa/enrollment/confirm", map[string]any{
		"pending_token": pending, "code": code,
	})
	if confirm.Status != http.StatusOK {
		t.Fatalf("enrolment confirm failed: %d %v", confirm.Status, confirm.Body)
	}
	if token, ok := confirm.Body["csrf_token"].(string); ok {
		h.csrfToken = token
	}

	codes, _ := confirm.Body["recovery_codes"].([]any)
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("expected %d recovery codes, got %d", auth.RecoveryCodeCount, len(codes))
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusOK {
		t.Fatalf("a session must exist after enrolment: %d %v", me.Status, me.Body)
	}
	if !h.reload(admin).MFAEnabled {
		t.Fatal("the second factor must be enabled after confirmation")
	}
}

// TestMFALoginAndRecoveryCode covers both second-factor paths, including that
// a recovery code is single use.
func TestMFALoginAndRecoveryCode(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleBandAdmin, "ein-langes-passwort")

	secret, _, err := h.auth.BeginEnrollment(user)
	if err != nil {
		t.Fatalf("begin enrollment: %v", err)
	}
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	recoveryCodes, err := h.auth.ConfirmEnrollment(user, code)
	if err != nil {
		t.Fatalf("confirm enrollment: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Save(user).Error; err != nil {
		t.Fatalf("save user: %v", err)
	}

	res := h.signIn(band.Slug, user.Username, "ein-langes-passwort")
	if res.Status != http.StatusOK || res.Body["needs_mfa"] != true {
		t.Fatalf("expected a second factor to be demanded: %d %v", res.Status, res.Body)
	}
	pending, _ := res.Body["pending_token"].(string)

	live, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	done := h.do(http.MethodPost, "/api/v1/auth/mfa", map[string]any{
		"pending_token": pending, "code": live,
	})
	if done.Status != http.StatusOK {
		t.Fatalf("second factor failed: %d %v", done.Status, done.Body)
	}

	// Now sign in again and use a recovery code instead.
	res = h.signIn(band.Slug, user.Username, "ein-langes-passwort")
	pending, _ = res.Body["pending_token"].(string)
	recovery := recoveryCodes[0]

	if used := h.do(http.MethodPost, "/api/v1/auth/mfa", map[string]any{
		"pending_token": pending, "code": recovery,
	}); used.Status != http.StatusOK {
		t.Fatalf("recovery code must work: %d %v", used.Status, used.Body)
	}
	if left := len(h.reload(user).MFARecoveryCodeHashes); left != auth.RecoveryCodeCount-1 {
		t.Fatalf("a used recovery code must be consumed, %d remain", left)
	}

	res = h.signIn(band.Slug, user.Username, "ein-langes-passwort")
	pending, _ = res.Body["pending_token"].(string)
	if reused := h.do(http.MethodPost, "/api/v1/auth/mfa", map[string]any{
		"pending_token": pending, "code": recovery,
	}); reused.Status != http.StatusUnauthorized {
		t.Fatalf("a recovery code must be single use, got %d", reused.Status)
	}
}

// TestDeactivatedAccountsAndBandsCannotSignIn pins the two lifecycle switches
// the admin center relies on.
func TestDeactivatedAccountsAndBandsCannotSignIn(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	if err := h.db.WithContext(h.ctx()).Model(user).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusForbidden {
		t.Fatalf("a deactivated account must not sign in, got %d %v", res.Status, res.Body)
	}

	if err := h.db.WithContext(h.ctx()).Model(user).Update("is_active", true).Error; err != nil {
		t.Fatalf("reactivate user: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Model(band).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate band: %v", err)
	}
	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusForbidden {
		t.Fatalf("a deactivated band must not sign in, got %d %v", res.Status, res.Body)
	}
}

// TestSessionVersionBumpInvalidatesSessions is what makes a password reset or
// an admin-triggered session kill take effect immediately, everywhere.
func TestSessionVersionBumpInvalidatesSessions(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusOK {
		t.Fatalf("login failed: %d %v", res.Status, res.Body)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusOK {
		t.Fatalf("session should be live: %d", me.Status)
	}

	if err := h.auth.RevokeBandSessions(h.ctx(), band.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusUnauthorized {
		t.Fatalf("the revoked session must be rejected, got %d", me.Status)
	}
}

// TestCSRFIsEnforced pins that an authenticated unsafe request without the
// token is refused, which is what stops a cross-site form post.
func TestCSRFIsEnforced(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusOK {
		t.Fatalf("login failed: %d %v", res.Status, res.Body)
	}

	valid := h.csrfToken
	h.csrfToken = ""
	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusForbidden {
		t.Fatalf("a request without a CSRF token must be refused, got %d %v", res.Status, res.Body)
	}

	h.csrfToken = "wrong-token"
	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusForbidden {
		t.Fatalf("a wrong CSRF token must be refused, got %d", res.Status)
	}

	h.csrfToken = valid
	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("the valid token must be accepted: %d %v", res.Status, res.Body)
	}
}

// TestProfileRequiresFreshReauth pins the step-up window that guards the
// profile and every destructive administrative action.
func TestProfileRequiresFreshReauth(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusOK {
		t.Fatalf("login failed: %d %v", res.Status, res.Body)
	}

	if res := h.do(http.MethodGet, "/api/v1/profile", nil); res.Status != http.StatusForbidden {
		t.Fatalf("the profile must demand a fresh confirmation, got %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{
		"password": "falsches-passwort",
	}); res.Status != http.StatusUnauthorized {
		t.Fatalf("a wrong password must not open the window, got %d", res.Status)
	}
	if res := h.do(http.MethodPost, "/api/v1/profile/reauth", map[string]any{
		"password": "ein-langes-passwort",
	}); res.Status != http.StatusOK {
		t.Fatalf("reauth failed: %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/profile", nil); res.Status != http.StatusOK {
		t.Fatalf("the profile must be reachable after reauth: %d %v", res.Status, res.Body)
	}
}

// TestPOSModeBlocksRestrictedAreas pins that the restriction is enforced by
// the server, not merely hidden in the client.
func TestPOSModeBlocksRestrictedAreas(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleBandAdmin, "ein-langes-passwort")

	if res := h.signIn(band.Slug, user.Username, "ein-langes-passwort"); res.Status != http.StatusOK {
		t.Fatalf("login failed: %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("enabling POS mode failed: %d %v", res.Status, res.Body)
	}

	// The path need not exist yet; the guard runs before routing does not
	// matter — what matters is that it is never a 404-shaped success.
	if res := h.do(http.MethodGet, "/api/v1/purchases/anything", nil); res.Status != http.StatusForbidden {
		t.Fatalf("POS mode must block purchases, got %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/me", nil); res.Status != http.StatusOK {
		t.Fatalf("POS mode must keep the sales workflow usable: %d", res.Status)
	}
}

// TestPlatformStaffCannotReachBandData is the outer wall of the tenant
// boundary: without a support grant, a system admin gets nowhere near a band.
func TestPlatformStaffCannotReachBandData(t *testing.T) {
	h := newHarness(t)
	admin := h.makeUser(nil, models.RoleSystemAdmin, "plattform-passwort")

	// Give the account a second factor so it can obtain a session at all.
	secret, _, err := h.auth.BeginEnrollment(admin)
	if err != nil {
		t.Fatalf("begin enrollment: %v", err)
	}
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	if _, err := h.auth.ConfirmEnrollment(admin, code); err != nil {
		t.Fatalf("confirm enrollment: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Save(admin).Error; err != nil {
		t.Fatalf("save admin: %v", err)
	}

	res := h.signIn("", admin.Username, "plattform-passwort")
	pending, _ := res.Body["pending_token"].(string)
	live, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	done := h.do(http.MethodPost, "/api/v1/auth/mfa", map[string]any{"pending_token": pending, "code": live})
	if done.Status != http.StatusOK {
		t.Fatalf("platform login failed: %d %v", done.Status, done.Body)
	}

	blocked := h.do(http.MethodGet, "/api/v1/band", nil)
	if blocked.Status != http.StatusForbidden {
		t.Fatalf("band data must be refused without a grant, got %d %v", blocked.Status, blocked.Body)
	}
	if blocked.Body["code"] != "no_support_grant" {
		t.Fatalf("expected the no_support_grant code, got %v", blocked.Body)
	}

	// Its own areas stay reachable.
	if me := h.do(http.MethodGet, "/api/v1/me", nil); me.Status != http.StatusOK {
		t.Fatalf("the platform account must reach /me: %d", me.Status)
	}
}

// TestCSRFTokenSurvivesAPageReload pins the fix for a bug that broke every
// write after a refresh: the token lived only in the frontend's memory, while
// the session cookie survived. It now also travels in a readable cookie.
func TestCSRFTokenSurvivesAPageReload(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	user := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	res := h.signIn(band.Slug, user.Username, "ein-langes-passwort")
	if res.Status != http.StatusOK {
		t.Fatalf("login: %d %v", res.Status, res.Body)
	}
	if h.csrfCookie == "" {
		t.Fatal("login must plant a readable CSRF cookie")
	}
	if h.csrfCookie != h.csrfToken {
		t.Fatalf("the cookie and the response token must match: %q vs %q", h.csrfCookie, h.csrfToken)
	}

	// Simulate a reload: the in-memory token is gone, the cookies remain.
	h.csrfToken = h.csrfCookie
	if res := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); res.Status != http.StatusOK {
		t.Fatalf("a write after a reload must still work: %d %v", res.Status, res.Body)
	}

	// Signing out must clear it, so a stale token cannot linger.
	if res := h.do(http.MethodPost, "/api/v1/auth/logout", nil); res.Status != http.StatusNoContent {
		t.Fatalf("logout: %d %v", res.Status, res.Body)
	}
	if h.csrfCookie != "" {
		t.Fatalf("logout must clear the CSRF cookie, still %q", h.csrfCookie)
	}
}
