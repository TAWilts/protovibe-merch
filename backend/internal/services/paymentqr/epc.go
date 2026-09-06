// Package paymentqr renders the codes a customer scans to pay.
//
// Two kinds are supported, matching the original: a plain URL code for a
// PayPal.me link, and an EPC/GiroCode for a SEPA transfer. The EPC payload is
// built by hand rather than pulled from a library because the format is a
// short, fixed list of lines and the validation rules are the interesting part.
package paymentqr

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// EPC limits from the European Payments Council's "Quick Response Code:
// Guidelines to Enable Data Capture for the Initiation of a SCT" specification.
const (
	MaxAccountHolderLength = 70
	MaxRemittanceLength    = 140
	// The whole payload must stay within 331 bytes, which is what makes a long
	// account holder plus a long reference impossible to combine.
	MaxPayloadBytes = 331
)

// Errors returned when a band's payment settings cannot produce a code.
var (
	ErrNoAmount        = errors.New("paymentqr: a payment code needs an amount greater than zero")
	ErrMissingIBAN     = errors.New("paymentqr: an IBAN is required")
	ErrInvalidIBAN     = errors.New("paymentqr: the IBAN is not valid")
	ErrMissingHolder   = errors.New("paymentqr: the account holder is required")
	ErrPayloadTooLarge = errors.New("paymentqr: the code is too long for this bank account; " +
		"shorten the account holder or drop the optional BIC")
	ErrAmountTooLarge = errors.New("paymentqr: the amount exceeds what an EPC code can carry")
	ErrInvalidBIC     = errors.New("paymentqr: the BIC is not valid")
)

// ibanPattern is a structural check. It deliberately does not try to validate
// every national format; the checksum below is what actually catches typos.
var ibanPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{10,30}$`)

var bicPattern = regexp.MustCompile(`^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

// NormalizeIBAN strips the spaces people type and upper-cases the result.
func NormalizeIBAN(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
}

// ValidateIBAN checks the structure and the ISO 7064 mod-97 checksum.
//
// The checksum is what turns a mistyped digit into an error at setup time
// rather than into money landing in a stranger's account.
func ValidateIBAN(value string) error {
	iban := NormalizeIBAN(value)
	if iban == "" {
		return ErrMissingIBAN
	}
	if !ibanPattern.MatchString(iban) {
		return ErrInvalidIBAN
	}

	// Move the first four characters to the end, then reduce mod 97 digit by
	// digit so no big-integer arithmetic is needed.
	rearranged := iban[4:] + iban[:4]
	remainder := 0
	for _, r := range rearranged {
		var digits string
		switch {
		case r >= '0' && r <= '9':
			digits = string(r)
		case r >= 'A' && r <= 'Z':
			digits = fmt.Sprintf("%d", int(r-'A')+10)
		default:
			return ErrInvalidIBAN
		}
		for _, d := range digits {
			remainder = (remainder*10 + int(d-'0')) % 97
		}
	}
	if remainder != 1 {
		return ErrInvalidIBAN
	}
	return nil
}

// ValidateBIC checks the optional BIC.
func ValidateBIC(value string) error {
	bic := strings.ToUpper(strings.TrimSpace(value))
	if bic == "" {
		return nil
	}
	if !bicPattern.MatchString(bic) {
		return ErrInvalidBIC
	}
	return nil
}

// BankAccount is the destination of a SEPA transfer.
type BankAccount struct {
	Holder     string
	IBAN       string
	BIC        string
	Remittance string
}

// EPCPayload builds the text an EPC/GiroCode encodes.
//
// When the payload does not fit, the optional item list in the remittance text
// is shortened one character at a time. The receipt ID in front of the colon is
// never touched: it is the only link between an incoming transfer and its sale,
// so losing it to make room for a long account name would defeat the purpose.
func EPCPayload(account BankAccount, amountCents int64) (string, error) {
	if amountCents <= 0 {
		return "", ErrNoAmount
	}
	// EUR999999999.99 is the format's ceiling.
	if amountCents > 99_999_999_999 {
		return "", ErrAmountTooLarge
	}

	holder := strings.TrimSpace(account.Holder)
	if holder == "" {
		return "", ErrMissingHolder
	}
	if utf8.RuneCountInString(holder) > MaxAccountHolderLength {
		holder = string([]rune(holder)[:MaxAccountHolderLength])
	}

	iban := NormalizeIBAN(account.IBAN)
	if err := ValidateIBAN(iban); err != nil {
		return "", err
	}
	bic := strings.ToUpper(strings.TrimSpace(account.BIC))
	if err := ValidateBIC(bic); err != nil {
		return "", err
	}

	remittance := strings.TrimSpace(account.Remittance)
	if utf8.RuneCountInString(remittance) > MaxRemittanceLength {
		remittance = string([]rune(remittance)[:MaxRemittanceLength])
	}

	for {
		payload := buildEPC(holder, iban, bic, remittance, amountCents)
		if len(payload) <= MaxPayloadBytes {
			return payload, nil
		}
		shortened := shortenRemittance(remittance)
		if shortened == remittance {
			return "", ErrPayloadTooLarge
		}
		remittance = shortened
	}
}

// buildEPC assembles the eleven-line SEPA credit transfer payload.
func buildEPC(holder, iban, bic, remittance string, amountCents int64) string {
	lines := []string{
		"BCD",  // service tag
		"002",  // version
		"1",    // UTF-8
		"SCT",  // SEPA credit transfer
		bic,    // optional in version 002
		holder, // beneficiary name
		iban,   // beneficiary account
		fmt.Sprintf("EUR%d.%02d", amountCents/100, amountCents%100),
		"",         // purpose code, unused
		"",         // structured reference, unused
		remittance, // unstructured reference
	}
	return strings.Join(lines, "\n")
}

// shortenRemittance trims the item list after the colon while preserving the
// receipt reference in front of it. It returns the input unchanged once there
// is nothing left to trim.
func shortenRemittance(value string) string {
	prefix, details, found := strings.Cut(value, ": ")
	if !found || details == "" {
		return value
	}

	trimmed := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(details, " "), "..."), " ")
	if trimmed == "" {
		return prefix
	}

	runes := []rune(trimmed)
	trimmed = strings.TrimRight(string(runes[:len(runes)-1]), " ")
	if trimmed == "" {
		return prefix
	}
	return prefix + ": " + trimmed + "..."
}
