package api_test

import (
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestDisabledBandFeatureIsEnforcedAndAdvertised(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	disabled := false
	band.FeatureFlags.PaymentQR = &disabled
	if err := h.db.WithContext(h.ctx()).Save(band).Error; err != nil {
		t.Fatalf("disable payment QR: %v", err)
	}
	h.signInAs(band, models.RoleSeller)

	me := h.do(http.MethodGet, "/api/v1/me", nil)
	bandPayload := jsonObject(me.Body["band"])
	flags := jsonObject(bandPayload["feature_flags"])
	if flags["payment_qr"] != false {
		t.Fatalf("identity must advertise the disabled feature: %v", me.Body)
	}

	res := h.do(http.MethodGet, "/api/v1/payment-qr/availability", nil)
	if res.Status != http.StatusForbidden || res.Body["code"] != "feature_disabled" {
		t.Fatalf("disabled feature must be refused server-side: %d %v", res.Status, res.Body)
	}
	if ordinary := h.do(http.MethodGet, "/api/v1/articles", nil); ordinary.Status != http.StatusOK {
		t.Fatalf("an unrelated feature must stay available: %d %v", ordinary.Status, ordinary.Body)
	}
}

func TestBandMaintenanceBlocksOnlyBandTraffic(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	band.MaintenanceMessage = "Wartungsfenster der Band"
	if err := h.db.WithContext(h.ctx()).Save(band).Error; err != nil {
		t.Fatalf("enable maintenance: %v", err)
	}

	// Authentication remains open so the user can still sign in and out.
	h.signInAs(band, models.RoleSeller)
	blocked := h.do(http.MethodGet, "/api/v1/me", nil)
	if blocked.Status != http.StatusServiceUnavailable || blocked.Body["code"] != "maintenance" {
		t.Fatalf("band maintenance must block band traffic: %d %v", blocked.Status, blocked.Body)
	}
	if version := h.do(http.MethodGet, "/api/v1/version", nil); version.Status != http.StatusOK {
		t.Fatalf("operational endpoints must stay open: %d %v", version.Status, version.Body)
	}
}

func TestAuthenticationEndpointsAreRateLimited(t *testing.T) {
	h := newHarness(t)
	payload := map[string]any{
		"band": "does-not-exist", "username": "nobody", "secret": "wrong-password",
	}
	for attempt := 1; attempt <= 20; attempt++ {
		res := h.do(http.MethodPost, "/api/v1/auth/login", payload)
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d should still reach authentication, got %d %v", attempt, res.Status, res.Body)
		}
	}
	limited := h.do(http.MethodPost, "/api/v1/auth/login", payload)
	if limited.Status != http.StatusTooManyRequests || limited.Body["code"] != "rate_limited" {
		t.Fatalf("the next attempt must be rate limited: %d %v", limited.Status, limited.Body)
	}
}

func TestStorageQuotaRejectsUpload(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	band.StorageQuotaBytes = 1
	if err := h.db.WithContext(h.ctx()).Save(band).Error; err != nil {
		t.Fatalf("set storage quota: %v", err)
	}
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Quota Shirt")

	created := h.do(http.MethodPost, "/api/v1/purchases", map[string]any{
		"items":        []any{map[string]any{"variant_id": variants[0], "quantity": 1, "unit_cost_cents": 100}},
		"purchased_on": "2026-08-31",
	})
	positionID := int64(jsonList(created.Body, "purchase_ids")[0].(float64))
	res := h.upload("/api/v1/purchases/"+itoa(positionID)+"/invoice",
		"rechnung.pdf", "application/pdf", []byte("%PDF-1.4 quota"))
	if res.Status != http.StatusInsufficientStorage || res.Body["code"] != "storage_quota_exceeded" {
		t.Fatalf("quota must reject the upload: %d %v", res.Status, res.Body)
	}
}
