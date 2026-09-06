package api_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	registrationservice "github.com/tawilts/protovibe-merch/backend/internal/services/registration"
)

func registrationToken(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse status URL: %v", err)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("parse status fragment: %v", err)
	}
	return fragment.Get("registration")
}

func cleanupRegistration(t *testing.T, h *harness, requestedSlug string) {
	t.Helper()
	var requests []models.BandRegistrationRequest
	_ = h.db.WithContext(h.ctx()).Where("requested_band_slug = ?", requestedSlug).Find(&requests).Error
	for _, request := range requests {
		_ = h.db.Exec("DELETE FROM audit_log WHERE entity_type = ? AND entity_id = ?", "band_registration_request", request.ID).Error
		_ = h.db.Exec("DELETE FROM band_registration_requests WHERE id = ?", request.ID).Error
		if request.BandID != nil {
			_ = h.db.Exec("DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE band_id = ?)", *request.BandID).Error
			_ = h.db.Exec("DELETE FROM audit_log WHERE band_id = ?", *request.BandID).Error
			_ = h.db.Exec("DELETE FROM audit_log WHERE entity_type = ? AND entity_id = ?", "band", *request.BandID).Error
			if request.AdminUserID != nil {
				_ = h.db.Exec("DELETE FROM audit_log WHERE entity_type = ? AND entity_id = ?", "user", *request.AdminUserID).Error
			}
			_ = h.db.Exec("DELETE FROM users WHERE band_id = ?", *request.BandID).Error
			_ = h.db.Exec("DELETE FROM bands WHERE id = ?", *request.BandID).Error
		}
	}
}

func TestPublicRegistrationFlagAndRateLimit(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("PUBLIC_REGISTRATION_ENABLED", "false")
		h := newHarness(t)
		config := h.do(http.MethodGet, "/api/v1/public/registrations/config", nil)
		if config.Status != http.StatusOK || config.Body["registration_enabled"] != false {
			t.Fatalf("disabled config: %d %v", config.Status, config.Body)
		}
		created := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
			"band_name": "Disabled Band", "band_slug": "disabled-band",
			"admin_username": "disabled-admin", "contact_email": "band@example.org",
			"privacy_accepted": true,
		})
		if created.Status != http.StatusForbidden {
			t.Fatalf("disabled registration should be refused: %d %v", created.Status, created.Body)
		}
	})

	t.Run("three creates per hour and IP", func(t *testing.T) {
		t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
		h := newHarness(t)
		slug := unique("limited-")
		defer cleanupRegistration(t, h, slug)
		payload := map[string]any{
			"band_name": "Limited Band", "band_slug": slug,
			"admin_username": "limited-admin", "contact_email": "band@example.org",
			"privacy_accepted": true,
		}
		for attempt := 1; attempt <= 3; attempt++ {
			if created := h.do(http.MethodPost, "/api/v1/public/registrations", payload); created.Status != http.StatusCreated {
				t.Fatalf("allowed registration %d: %d %v", attempt, created.Status, created.Body)
			}
		}
		if limited := h.do(http.MethodPost, "/api/v1/public/registrations", payload); limited.Status != http.StatusTooManyRequests {
			t.Fatalf("fourth registration should be limited: %d %v", limited.Status, limited.Body)
		}
	})
}

func TestPublicRegistrationApprovalAndOneTimeClaim(t *testing.T) {
	t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
	h := newHarness(t)
	slug := unique("apply-")
	defer cleanupRegistration(t, h, slug)

	created := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
		"band_name": "The Example Band", "band_slug": slug,
		"admin_username": "tour-admin", "contact_email": "band@example.org",
		"privacy_accepted": true, "website": "",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create registration: %d %v", created.Status, created.Body)
	}
	token := registrationToken(t, created.Body["status_url"].(string))
	if token == "" {
		t.Fatal("status URL did not contain a registration token")
	}

	var stored models.BandRegistrationRequest
	if err := h.db.WithContext(h.ctx()).Where("public_id = ?", created.Body["reference"]).First(&stored).Error; err != nil {
		t.Fatalf("load registration: %v", err)
	}
	if stored.TokenHash != auth.HashToken(token) || strings.Contains(stored.TokenHash, token) {
		t.Fatal("only the status-token hash may be persisted")
	}
	if stored.SetupCodeEncrypted != "" || stored.BandID != nil || stored.AdminUserID != nil {
		t.Fatalf("a pending request must not create credentials or tenant data: %+v", stored)
	}

	pending := h.do(http.MethodPost, "/api/v1/public/registrations/status", map[string]any{"token": token})
	if pending.Status != http.StatusOK || pending.Body["status"] != "pending" {
		t.Fatalf("pending status: %d %v", pending.Status, pending.Body)
	}
	if _, leaked := pending.Body["setup_code"]; leaked {
		t.Fatal("status response must never contain the setup code")
	}

	support, supportSecret := h.platformAdmin(models.RoleSupportAdmin)
	h.signInPlatform(support, supportSecret)
	if res := h.do(http.MethodGet, "/api/v1/platform/registration-requests", nil); res.Status != http.StatusForbidden {
		t.Fatalf("support admin must not read registrations: %d %v", res.Status, res.Body)
	}

	admin, adminSecret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, adminSecret)
	listed := h.do(http.MethodGet, "/api/v1/platform/registration-requests?status=pending", nil)
	if listed.Status != http.StatusOK || len(jsonList(listed.Body, "requests")) == 0 {
		t.Fatalf("system-admin inbox: %d %v", listed.Status, listed.Body)
	}
	finalSlug := slug + "-final"
	approved := h.do(http.MethodPost, "/api/v1/platform/registration-requests/"+itoa(stored.ID)+"/approve", map[string]any{
		"band_name": "The Example Band e.V.", "band_slug": finalSlug,
		"admin_username": "merch-admin", "contact_email": "office@example.org",
	})
	if approved.Status != http.StatusOK || approved.Body["status"] != "approved" {
		t.Fatalf("approve registration: %d %v", approved.Status, approved.Body)
	}
	if err := h.db.WithContext(h.ctx()).First(&stored, stored.ID).Error; err != nil {
		t.Fatalf("reload approved registration: %v", err)
	}
	encryptedSetupCode := stored.SetupCodeEncrypted
	if encryptedSetupCode == "" {
		t.Fatal("approved registration must hold an encrypted setup code until claim")
	}

	approvedStatus := h.do(http.MethodPost, "/api/v1/public/registrations/status", map[string]any{"token": token})
	if approvedStatus.Status != http.StatusOK || approvedStatus.Body["credentials_available"] != true {
		t.Fatalf("approved public status: %d %v", approvedStatus.Status, approvedStatus.Body)
	}
	if approvedStatus.Body["band_slug"] != finalSlug || approvedStatus.Body["admin_username"] != "merch-admin" {
		t.Fatalf("public status must expose final edited values: %v", approvedStatus.Body)
	}

	claimed := h.do(http.MethodPost, "/api/v1/public/registrations/claim", map[string]any{"token": token})
	if claimed.Status != http.StatusOK || claimed.Body["setup_code"] == "" {
		t.Fatalf("claim credentials: %d %v", claimed.Status, claimed.Body)
	}
	setupCode := claimed.Body["setup_code"].(string)
	if strings.Contains(encryptedSetupCode, setupCode) {
		t.Fatal("the persisted handover must not contain the plaintext setup code")
	}
	if again := h.do(http.MethodPost, "/api/v1/public/registrations/claim", map[string]any{"token": token}); again.Status != http.StatusGone {
		t.Fatalf("credentials must be one-time, got %d %v", again.Status, again.Body)
	}

	if err := h.db.WithContext(h.ctx()).First(&stored, stored.ID).Error; err != nil {
		t.Fatalf("reload registration: %v", err)
	}
	if stored.SetupCodeEncrypted != "" || stored.ClaimedAt == nil {
		t.Fatalf("claimed plaintext handover must be cleared: %+v", stored)
	}
	login := h.signIn(finalSlug, "merch-admin", setupCode)
	if login.Status != http.StatusOK || login.Body["needs_password_setup"] != true {
		t.Fatalf("claimed setup code must start password setup: %d %v", login.Status, login.Body)
	}
}

func TestRegistrationRejectionAndBandAdminPermissions(t *testing.T) {
	t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
	h := newHarness(t)
	slug := unique("reject-")
	defer cleanupRegistration(t, h, slug)

	created := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
		"band_name": "Rejected Band", "band_slug": slug,
		"admin_username": "request-admin", "contact_email": "band@example.org",
		"privacy_accepted": true,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create registration: %d %v", created.Status, created.Body)
	}
	token := registrationToken(t, created.Body["status_url"].(string))
	var request models.BandRegistrationRequest
	if err := h.db.WithContext(h.ctx()).Where("requested_band_slug = ?", slug).First(&request).Error; err != nil {
		t.Fatalf("load request: %v", err)
	}

	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	if result := h.do(http.MethodGet, "/api/v1/platform/registration-requests", nil); result.Status != http.StatusForbidden {
		t.Fatalf("band admin must not read registrations: %d %v", result.Status, result.Body)
	}

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	rejected := h.do(http.MethodPost, "/api/v1/platform/registration-requests/"+itoa(request.ID)+"/reject", map[string]any{
		"note": "Die Kennung ist bereits für einen Partner reserviert.",
	})
	if rejected.Status != http.StatusOK || rejected.Body["status"] != "rejected" {
		t.Fatalf("reject registration: %d %v", rejected.Status, rejected.Body)
	}

	publicStatus := h.do(http.MethodPost, "/api/v1/public/registrations/status", map[string]any{"token": token})
	if publicStatus.Status != http.StatusOK || publicStatus.Body["status"] != "rejected" {
		t.Fatalf("public rejection status: %d %v", publicStatus.Status, publicStatus.Body)
	}
	if publicStatus.Body["decision_note"] != "Die Kennung ist bereits für einen Partner reserviert." {
		t.Fatalf("public rejection note missing: %v", publicStatus.Body)
	}
	if claim := h.do(http.MethodPost, "/api/v1/public/registrations/claim", map[string]any{"token": token}); claim.Status != http.StatusConflict {
		t.Fatalf("rejected credentials must not be claimable: %d %v", claim.Status, claim.Body)
	}
}

func TestConcurrentRegistrationApprovalCreatesOneTenant(t *testing.T) {
	t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
	h := newHarness(t)
	slug := unique("parallel-")
	defer cleanupRegistration(t, h, slug)

	service := registrationservice.NewService(h.db, h.auth, platform.NewService(h.db), "https://merch.example.org")
	created, err := service.Create(h.ctx(), registrationservice.CreateInput{
		BandName: "Parallel Band", BandSlug: slug,
		AdminUsername: "parallel-admin", ContactEmail: "band@example.org",
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	admin, _ := h.platformAdmin(models.RoleSystemAdmin)
	input := registrationservice.ApproveInput{
		BandName: "Parallel Band", BandSlug: slug, AdminUsername: "parallel-admin",
		ContactEmail: "band@example.org", DecidedByID: admin.ID, DecidedByName: admin.Username,
	}

	results := make(chan error, 2)
	for attempt := 0; attempt < 2; attempt++ {
		go func() {
			_, approveErr := service.Approve(h.ctx(), created.Request.ID, input)
			results <- approveErr
		}()
	}
	succeeded, alreadyDecided := 0, 0
	for attempt := 0; attempt < 2; attempt++ {
		switch approveErr := <-results; {
		case approveErr == nil:
			succeeded++
		case errors.Is(approveErr, registrationservice.ErrNotPending):
			alreadyDecided++
		default:
			t.Fatalf("unexpected parallel approval result: %v", approveErr)
		}
	}
	if succeeded != 1 || alreadyDecided != 1 {
		t.Fatalf("parallel approval results: succeeded=%d already_decided=%d", succeeded, alreadyDecided)
	}

	var bands, users int64
	if err := h.db.WithContext(h.ctx()).Model(&models.Band{}).Where("slug = ?", slug).Count(&bands).Error; err != nil {
		t.Fatalf("count bands: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Model(&models.User{}).
		Where("band_id = (SELECT id FROM bands WHERE slug = ?)", slug).Count(&users).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if bands != 1 || users != 1 {
		t.Fatalf("parallel approval created bands=%d users=%d", bands, users)
	}
}

func TestRegistrationApprovalRollsBackBandWhenUserCreationFails(t *testing.T) {
	t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
	h := newHarness(t)
	slug := unique("rollback-")
	defer cleanupRegistration(t, h, slug)

	created := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
		"band_name": "Rollback Band", "band_slug": slug,
		"admin_username": "rollback-admin", "contact_email": "band@example.org",
		"privacy_accepted": true,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create registration: %d %v", created.Status, created.Body)
	}
	var request models.BandRegistrationRequest
	if err := h.db.WithContext(h.ctx()).Where("requested_band_slug = ?", slug).First(&request).Error; err != nil {
		t.Fatalf("load request: %v", err)
	}
	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)

	const callbackName = "test:fail-registration-admin-create"
	if err := h.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "users" {
			return
		}
		user, ok := tx.Statement.Dest.(*models.User)
		if ok && user.Username == "rollback-admin" {
			tx.AddError(errors.New("forced registration user failure"))
		}
	}); err != nil {
		t.Fatalf("register failure callback: %v", err)
	}
	defer func() { _ = h.db.Callback().Create().Remove(callbackName) }()

	failed := h.do(http.MethodPost, "/api/v1/platform/registration-requests/"+itoa(request.ID)+"/approve", map[string]any{
		"band_name": "Rollback Band", "band_slug": slug,
		"admin_username": "rollback-admin", "contact_email": "band@example.org",
	})
	if failed.Status != http.StatusInternalServerError {
		t.Fatalf("forced user failure should fail approval: %d %v", failed.Status, failed.Body)
	}

	var bandCount int64
	if err := h.db.WithContext(h.ctx()).Model(&models.Band{}).Where("slug = ?", slug).Count(&bandCount).Error; err != nil {
		t.Fatalf("count rolled back bands: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).First(&request, request.ID).Error; err != nil {
		t.Fatalf("reload request: %v", err)
	}
	if bandCount != 0 || request.Status != models.BandRegistrationPending {
		t.Fatalf("failed approval must roll back completely: bands=%d status=%s", bandCount, request.Status)
	}
}

func TestRegistrationValidationConflictAndExpiry(t *testing.T) {
	t.Setenv("PUBLIC_REGISTRATION_ENABLED", "true")
	h := newHarness(t)
	slug := unique("review-")
	defer cleanupRegistration(t, h, slug)

	invalid := h.do(http.MethodPost, "/api/v1/public/registrations/status", map[string]any{
		"token": "not-a-valid-status-token",
	})
	if invalid.Status != http.StatusNotFound {
		t.Fatalf("invalid status token must be rejected: %d %v", invalid.Status, invalid.Body)
	}

	withoutConsent := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
		"band_name": "No Consent", "band_slug": slug,
		"admin_username": "admin-user", "contact_email": "band@example.org",
	})
	if withoutConsent.Status != http.StatusBadRequest {
		t.Fatalf("privacy consent must be required: %d %v", withoutConsent.Status, withoutConsent.Body)
	}

	created := h.do(http.MethodPost, "/api/v1/public/registrations", map[string]any{
		"band_name": "Review Band", "band_slug": slug,
		"admin_username": "admin-user", "contact_email": "band@example.org",
		"privacy_accepted": true,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create registration: %d %v", created.Status, created.Body)
	}
	token := registrationToken(t, created.Body["status_url"].(string))
	var request models.BandRegistrationRequest
	if err := h.db.WithContext(h.ctx()).Where("requested_band_slug = ?", slug).First(&request).Error; err != nil {
		t.Fatalf("load request: %v", err)
	}

	admin, secret := h.platformAdmin(models.RoleSystemAdmin)
	h.signInPlatform(admin, secret)
	taken := h.makeBand()
	conflict := h.do(http.MethodPost, "/api/v1/platform/registration-requests/"+itoa(request.ID)+"/approve", map[string]any{
		"band_name": "Conflict", "band_slug": taken.Slug,
		"admin_username": "admin-user", "contact_email": "band@example.org",
	})
	if conflict.Status != http.StatusConflict {
		t.Fatalf("approval must recheck the slug: %d %v", conflict.Status, conflict.Body)
	}
	if err := h.db.WithContext(h.ctx()).First(&request, request.ID).Error; err != nil || request.Status != models.BandRegistrationPending {
		t.Fatalf("failed approval must leave the request pending: %v %+v", err, request)
	}

	past := time.Now().UTC().Add(-time.Minute)
	if err := h.db.WithContext(h.ctx()).Model(&request).Update("expires_at", past).Error; err != nil {
		t.Fatalf("expire fixture: %v", err)
	}
	expired := h.do(http.MethodPost, "/api/v1/public/registrations/status", map[string]any{"token": token})
	if expired.Status != http.StatusOK || expired.Body["status"] != "expired" {
		t.Fatalf("status must lazily expire old requests: %d %v", expired.Status, expired.Body)
	}
	if claim := h.do(http.MethodPost, "/api/v1/public/registrations/claim", map[string]any{"token": token}); claim.Status != http.StatusGone {
		t.Fatalf("expired credentials must not be claimable: %d %v", claim.Status, claim.Body)
	}
}
