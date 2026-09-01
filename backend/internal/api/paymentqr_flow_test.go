package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

const testIBAN = "DE89370400440532013000"

// TestPaymentQRSettingsValidation pins that a mistyped IBAN is caught before
// any customer ever scans a code pointing at the wrong account.
func TestPaymentQRSettingsValidation(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)

	bad := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"bank_account_holder": "Protovibe",
		"bank_iban":           "DE89370400440532013001",
	})
	if bad.Status != http.StatusBadRequest || bad.Body["code"] != "invalid_iban" {
		t.Fatalf("a bad IBAN checksum must be rejected: %d %v", bad.Status, bad.Body)
	}

	missingHolder := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"bank_iban": testIBAN,
	})
	if missingHolder.Status != http.StatusBadRequest {
		t.Fatalf("an IBAN without a holder must be rejected: %d %v", missingHolder.Status, missingHolder.Body)
	}

	insecure := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"paypal_me_url": "http://paypal.me/protovibe",
	})
	if insecure.Status != http.StatusBadRequest {
		t.Fatalf("a plain-http PayPal link must be rejected: %d %v", insecure.Status, insecure.Body)
	}
	wrongHost := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"paypal_me_url": "https://example.com/protovibe",
	})
	if wrongHost.Status != http.StatusBadRequest || wrongHost.Body["code"] != "invalid_paypal_url" {
		t.Fatalf("only PayPal.Me may be stored: %d %v", wrongHost.Status, wrongHost.Body)
	}

	good := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"paypal_me_url":        "https://paypal.me/protovibe",
		"bank_account_holder":  "Protovibe",
		"bank_iban":            "DE89 3704 0044 0532 0130 00",
		"bank_bic":             "COBADEFFXXX",
		"bank_remittance_text": "Vom Admin bestimmter Text",
	})
	if good.Status != http.StatusOK {
		t.Fatalf("valid settings must be accepted: %d %v", good.Status, good.Body)
	}
	// Spaces as typed are normalised away.
	if good.Body["bank_iban"] != testIBAN {
		t.Fatalf("the IBAN should be stored without spaces: %v", good.Body)
	}
	if _, exposed := good.Body["bank_remittance_text"]; exposed {
		t.Fatalf("the obsolete admin remittance must no longer be exposed: %v", good.Body)
	}
	var stored models.PaymentQRSettings
	if err := h.db.WithContext(h.ctx()).First(&stored).Error; err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if stored.BankRemittanceText != "Merch-Kauf" {
		t.Fatalf("an admin-supplied remittance must be ignored, got %q", stored.BankRemittanceText)
	}
}

// TestOnlyBandAdminsChangeWhereMoneyGoes pins the role split on the most
// security-relevant setting in the app.
func TestOnlyBandAdminsChangeWhereMoneyGoes(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	res := h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"bank_account_holder": "Woanders", "bank_iban": testIBAN,
	})
	if res.Status != http.StatusForbidden {
		t.Fatalf("a manager must not change the payment destination: %d %v", res.Status, res.Body)
	}
	if res := h.do(http.MethodGet, "/api/v1/payment-qr/availability", nil); res.Status != http.StatusOK {
		t.Fatalf("a manager must still see what is available: %d %v", res.Status, res.Body)
	}
}

// TestShowingACodeBooksNothing is the load-bearing rule of the QR flow: a
// customer can always walk away mid-scan, so no stock may move until the
// seller confirms.
func TestShowingACodeBooksNothing(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	_, variants := h.sellableArticle("QR Shirt")

	h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"bank_account_holder": "Protovibe", "bank_iban": testIBAN,
		"bank_remittance_text": "Merch-Kauf",
	})

	basket := map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 2}},
		"payment_method": "Überweisung", "is_paid": true, "is_received": true,
		"sold_on": "2026-08-27",
	}

	intent := h.do(http.MethodPost, "/api/v1/payment-qr/intents", map[string]any{
		"method": "Überweisung", "sale": basket, "description": "Vom Browser bestimmter Text",
	})
	if intent.Status != http.StatusCreated {
		t.Fatalf("create intent: %d %v", intent.Status, intent.Body)
	}
	if intent.Body["amount_cents"] != float64(3600) {
		t.Fatalf("the amount must come from the catalogue: %v", intent.Body)
	}
	image, _ := intent.Body["image_data_uri"].(string)
	if !strings.HasPrefix(image, "data:image/png;base64,") || len(image) < 200 {
		t.Fatalf("expected a rendered PNG, got %q", image[:min(60, len(image))])
	}
	hint, _ := intent.Body["payload_hint"].(string)
	receiptID, _ := intent.Body["receipt_id"].(string)
	if !strings.HasPrefix(hint, "Protovibe Merch "+receiptID+": 2x QR Shirt") {
		t.Fatalf("the server must generate the reference from receipt and basket: %q", hint)
	}
	if strings.Contains(hint, "Vom Browser") || strings.Contains(hint, "Merch-Kauf") {
		t.Fatalf("neither browser nor admin text may influence the reference: %q", hint)
	}

	// Nothing was booked.
	if h.onHand(variants[0]) != 0 {
		t.Fatalf("showing a code must not move stock, got %d", h.onHand(variants[0]))
	}
	if history := h.do(http.MethodGet, "/api/v1/history", nil); len(jsonList(history.Body, "receipts")) != 0 {
		t.Fatalf("showing a code must not create a receipt: %v", history.Body)
	}

	// The reserved number is held against everyone else...
	preview := h.do(http.MethodGet, "/api/v1/receipt-preview?date=2026-08-27", nil)
	if preview.Body["receipt_id"] == receiptID {
		t.Fatalf("the reserved number must not be offered again: %v", preview.Body)
	}

	// ...but the seller who reserved it keeps it when confirming.
	basket["payment_qr_intent_token"] = intent.Body["token"]
	booked := h.do(http.MethodPost, "/api/v1/sales", basket)
	if booked.Status != http.StatusCreated {
		t.Fatalf("confirm: %d %v", booked.Status, booked.Body)
	}
	if booked.Body["receipt_id"] != receiptID {
		t.Fatalf("the scanned receipt ID must be the booked one: %v vs %v",
			booked.Body["receipt_id"], receiptID)
	}
	if h.onHand(variants[0]) != -2 {
		t.Fatalf("confirming must book the sale, got %d", h.onHand(variants[0]))
	}

	// A code can only be redeemed once.
	again := h.do(http.MethodPost, "/api/v1/sales", basket)
	if again.Status != http.StatusConflict || again.Body["code"] != "payment_code_unusable" {
		t.Fatalf("a redeemed code must be refused: %d %v", again.Status, again.Body)
	}
}

// TestCancellingACodeFreesTheNumber keeps an abandoned scan from permanently
// burning a receipt number.
func TestCancellingACodeFreesTheNumber(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	_, variants := h.sellableArticle("Cancel QR Shirt")

	h.do(http.MethodPut, "/api/v1/payment-qr/settings", map[string]any{
		"paypal_me_url": "https://paypal.me/protovibe",
	})

	sale := map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "PayPal", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	}
	intent := h.do(http.MethodPost, "/api/v1/payment-qr/intents", map[string]any{
		"method": "PayPal",
		"sale":   sale,
	})
	if intent.Status != http.StatusCreated {
		t.Fatalf("create intent: %d %v", intent.Status, intent.Body)
	}
	token, _ := intent.Body["token"].(string)
	reserved, _ := intent.Body["receipt_id"].(string)

	if res := h.do(http.MethodPost, "/api/v1/payment-qr/intents/"+token+"/cancel", nil); res.Status != http.StatusNoContent {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}

	preview := h.do(http.MethodGet, "/api/v1/receipt-preview?date=2026-08-27", nil)
	if preview.Body["receipt_id"] != reserved {
		t.Fatalf("a cancelled reservation must free its number: %v vs %v",
			preview.Body["receipt_id"], reserved)
	}
	if res := h.do(http.MethodPost, "/api/v1/payment-qr/intents/"+token+"/cancel", nil); res.Status != http.StatusNotFound {
		t.Fatalf("cancelling twice must be a 404, got %d", res.Status)
	}

	// The database's unique receipt key must not turn the freed number into an
	// intermittent internal server error on the next PayPal/transfer attempt.
	recreated := h.do(http.MethodPost, "/api/v1/payment-qr/intents", map[string]any{
		"method": "PayPal", "sale": sale,
	})
	if recreated.Status != http.StatusCreated || recreated.Body["receipt_id"] != reserved {
		t.Fatalf("the freed number must be reusable immediately: %d %v", recreated.Status, recreated.Body)
	}
}

// TestUnconfiguredMethodIsRefused keeps a seller from showing a code that
// points nowhere while preserving an ordinary sale with the same method.
func TestUnconfiguredMethodIsRefused(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleBandAdmin)
	_, variants := h.sellableArticle("Unconfigured QR Shirt")

	sale := map[string]any{
		"items":          []any{map[string]any{"variant_id": variants[0], "quantity": 1}},
		"payment_method": "PayPal", "is_paid": true, "is_received": true, "sold_on": "2026-08-27",
	}
	res := h.do(http.MethodPost, "/api/v1/payment-qr/intents", map[string]any{
		"method": "PayPal", "sale": sale,
	})
	if res.Status != http.StatusConflict || res.Body["code"] != "payment_not_configured" {
		t.Fatalf("expected payment_not_configured, got %d %v", res.Status, res.Body)
	}
	if booked := h.do(http.MethodPost, "/api/v1/sales", sale); booked.Status != http.StatusCreated {
		t.Fatalf("missing QR settings must not block the sale: %d %v", booked.Status, booked.Body)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
