package paymentqr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/services/paymentqr"
)

// A real, well-formed test IBAN.
const testIBAN = "DE89370400440532013000"

func account() paymentqr.BankAccount {
	return paymentqr.BankAccount{
		Holder:     "Protovibe",
		IBAN:       testIBAN,
		BIC:        "COBADEFFXXX",
		Remittance: "V-20260827-001",
	}
}

// TestValidateIBANChecksum is what turns a mistyped digit into an error at
// setup time instead of money landing in a stranger's account.
func TestValidateIBANChecksum(t *testing.T) {
	if err := paymentqr.ValidateIBAN(testIBAN); err != nil {
		t.Fatalf("a valid IBAN must be accepted: %v", err)
	}
	if err := paymentqr.ValidateIBAN("DE89 3704 0044 0532 0130 00"); err != nil {
		t.Fatalf("spaces as typed must be tolerated: %v", err)
	}
	if err := paymentqr.ValidateIBAN("de89370400440532013000"); err != nil {
		t.Fatalf("lower case must be tolerated: %v", err)
	}

	// One digit changed: structurally fine, checksum wrong.
	if err := paymentqr.ValidateIBAN("DE89370400440532013001"); !errors.Is(err, paymentqr.ErrInvalidIBAN) {
		t.Fatalf("a mistyped digit must be caught, got %v", err)
	}
	for _, bad := range []string{"", "DE", "1234567890", "DEXX370400440532013000"} {
		if err := paymentqr.ValidateIBAN(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestValidateBIC(t *testing.T) {
	for _, good := range []string{"", "COBADEFF", "COBADEFFXXX"} {
		if err := paymentqr.ValidateBIC(good); err != nil {
			t.Errorf("%q must be accepted: %v", good, err)
		}
	}
	for _, bad := range []string{"COBA", "COBADEFFXXXX", "1234DEFF"} {
		if err := paymentqr.ValidateBIC(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestNormalizePayPalMeURL(t *testing.T) {
	got, err := paymentqr.NormalizePayPalMeURL("https://paypal.me/protovibe")
	if err != nil || got != "https://paypal.me/protovibe" {
		t.Fatalf("canonical PayPal.Me URL = %q, %v", got, err)
	}
	for _, bad := range []string{
		"http://paypal.me/protovibe",
		"https://example.com/protovibe",
		"https://paypal.me/protovibe/extra",
		"https://paypal.me/protovibe?country=DE",
	} {
		if _, err := paymentqr.NormalizePayPalMeURL(bad); !errors.Is(err, paymentqr.ErrInvalidPayPalURL) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
}

func TestGeneratedRemittanceMatchesOriginalFormat(t *testing.T) {
	got := paymentqr.RemittanceText("V-20260827-001", []string{
		"2x Geometry Shirt Schwarz/M", "1x Cap",
	})
	want := "Protovibe Merch V-20260827-001: 2x Geometry Shirt Schwarz/M, 1x Cap"
	if got != want {
		t.Fatalf("remittance = %q, want %q", got, want)
	}

	long := paymentqr.RemittanceText("V-20260827-001", []string{strings.Repeat("Langer Artikel ", 20)})
	if !strings.HasPrefix(long, "Protovibe Merch V-20260827-001: ") || len([]rune(long)) > paymentqr.MaxRemittanceLength {
		t.Fatalf("receipt must survive a long description within 140 characters: %q", long)
	}
}

// TestEPCPayloadShape pins the eleven-line SEPA credit transfer format.
func TestEPCPayloadShape(t *testing.T) {
	payload, err := paymentqr.EPCPayload(account(), 5400)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}

	lines := strings.Split(payload, "\n")
	if len(lines) != 11 {
		t.Fatalf("expected 11 lines, got %d: %q", len(lines), payload)
	}
	want := map[int]string{
		0: "BCD", 1: "002", 2: "1", 3: "SCT",
		4: "COBADEFFXXX", 5: "Protovibe", 6: testIBAN,
		7: "EUR54.00", 8: "", 9: "", 10: "V-20260827-001",
	}
	for index, expected := range want {
		if lines[index] != expected {
			t.Errorf("line %d = %q, want %q", index, lines[index], expected)
		}
	}
}

// TestEPCAmountFormatting pins the cent handling, where an off-by-one would
// charge the customer the wrong amount.
func TestEPCAmountFormatting(t *testing.T) {
	cases := map[int64]string{
		1:     "EUR0.01",
		99:    "EUR0.99",
		100:   "EUR1.00",
		1805:  "EUR18.05",
		12345: "EUR123.45",
	}
	for cents, want := range cases {
		payload, err := paymentqr.EPCPayload(account(), cents)
		if err != nil {
			t.Fatalf("payload for %d: %v", cents, err)
		}
		if got := strings.Split(payload, "\n")[7]; got != want {
			t.Errorf("%d cents rendered as %q, want %q", cents, got, want)
		}
	}
}

func TestEPCRejectsUnusableInput(t *testing.T) {
	if _, err := paymentqr.EPCPayload(account(), 0); !errors.Is(err, paymentqr.ErrNoAmount) {
		t.Errorf("a zero amount must be rejected, got %v", err)
	}
	if _, err := paymentqr.EPCPayload(account(), -100); !errors.Is(err, paymentqr.ErrNoAmount) {
		t.Errorf("a negative amount must be rejected, got %v", err)
	}

	missingHolder := account()
	missingHolder.Holder = "  "
	if _, err := paymentqr.EPCPayload(missingHolder, 100); !errors.Is(err, paymentqr.ErrMissingHolder) {
		t.Errorf("a missing account holder must be rejected, got %v", err)
	}

	badIBAN := account()
	badIBAN.IBAN = "DE89370400440532013001"
	if _, err := paymentqr.EPCPayload(badIBAN, 100); !errors.Is(err, paymentqr.ErrInvalidIBAN) {
		t.Errorf("a bad IBAN must be rejected, got %v", err)
	}
}

// TestRemittanceShorteningKeepsTheReceiptID is the load-bearing rule: when the
// payload does not fit, the item list is trimmed but the receipt reference in
// front of the colon survives — it is the only link between an incoming
// transfer and the sale it belongs to.
func TestRemittanceShorteningKeepsTheReceiptID(t *testing.T) {
	long := account()
	long.Holder = strings.Repeat("Protovibe Merchandising GmbH ", 2)
	long.Remittance = "V-20260827-001: " + strings.Repeat("Geometry Shirt Schwarz M, ", 12)

	payload, err := paymentqr.EPCPayload(long, 5400)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload) > paymentqr.MaxPayloadBytes {
		t.Fatalf("payload is %d bytes, limit is %d", len(payload), paymentqr.MaxPayloadBytes)
	}

	remittance := strings.Split(payload, "\n")[10]
	if !strings.HasPrefix(remittance, "V-20260827-001") {
		t.Fatalf("the receipt reference must survive shortening, got %q", remittance)
	}
	if !strings.Contains(remittance, "Geometry Shirt") {
		t.Fatalf("some of the item list should remain when it fits: %q", remittance)
	}
}

// TestPayloadTooLargeIsReported pins that an account which simply cannot fit
// produces a clear error rather than a truncated, unusable code.
func TestPayloadTooLargeIsReported(t *testing.T) {
	impossible := account()
	// The holder alone is capped at 70 characters, so an over-long one is
	// truncated rather than fatal; the failure has to come from the total.
	impossible.Holder = strings.Repeat("A", 70)
	impossible.Remittance = strings.Repeat("B", 140)

	// 70 + 140 + the fixed lines stays inside 331 bytes, so this must succeed:
	// the guard is about combinations that genuinely overflow.
	if _, err := paymentqr.EPCPayload(impossible, 100); err != nil {
		t.Fatalf("a maximal but legal account must still work: %v", err)
	}

	// Multi-byte characters are what actually pushes it over, since the limit
	// counts bytes rather than runes.
	overflowing := account()
	overflowing.Holder = strings.Repeat("Ä", 70)
	overflowing.Remittance = strings.Repeat("Ö", 140)
	if _, err := paymentqr.EPCPayload(overflowing, 100); !errors.Is(err, paymentqr.ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}
