// Package money holds the cent arithmetic shared by sales, purchases and
// reporting.
//
// Every amount in this application is an integer number of cents. There is no
// float anywhere in the money path, which is why a basket's positions always
// sum back to exactly what the customer handed over.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// Distribute splits a positive amount across a basket's positions in
// proportion to their weights, exactly.
//
// A basket has one "given" amount but several ledger rows. The invoice amount
// itself belongs unambiguously to each row; only the donation has to be split.
// The unavoidable rounding cents are handed out deterministically from left to
// right, so the positions always add back up to the total and cancelling one
// position never distorts the rest of the receipt.
//
// A fully free basket (every weight zero) gives the whole amount to the first
// position, because proportional allocation has no meaningful denominator.
// This mirrors distribute_cents in _old/app.py:5851.
func Distribute(totalCents int64, weights []int64) []int64 {
	if len(weights) == 0 {
		return nil
	}

	shares := make([]int64, len(weights))
	if totalCents <= 0 {
		return shares
	}

	var weightSum int64
	positive := make([]int, 0, len(weights))
	for i, weight := range weights {
		if weight > 0 {
			weightSum += weight
			positive = append(positive, i)
		}
	}
	if weightSum <= 0 {
		shares[0] = totalCents
		return shares
	}

	var assigned int64
	for i, weight := range weights {
		if weight > 0 {
			shares[i] = totalCents * weight / weightSum
			assigned += shares[i]
		}
	}

	for i := int64(0); i < totalCents-assigned; i++ {
		shares[positive[int(i)%len(positive)]]++
	}
	return shares
}

// ParseAmount reads a user-entered amount into cents.
//
// It accepts both the German "18,50" and the English "18.50", with or without
// a currency sign and with thousands separators, because the same field is
// filled in on a phone at a gig and pasted from a spreadsheet.
func ParseAmount(input string) (int64, error) {
	cleaned := strings.TrimSpace(input)
	cleaned = strings.NewReplacer("€", "", " ", "", " ", "", " ", "").Replace(cleaned)
	if cleaned == "" {
		return 0, fmt.Errorf("money: empty amount")
	}

	negative := strings.HasPrefix(cleaned, "-")
	cleaned = strings.TrimPrefix(cleaned, "-")
	cleaned = strings.TrimPrefix(cleaned, "+")

	lastComma := strings.LastIndex(cleaned, ",")
	lastDot := strings.LastIndex(cleaned, ".")
	switch {
	case lastComma >= 0 && lastDot >= 0:
		// Whichever separator comes last is the decimal one; the other groups
		// thousands.
		if lastComma > lastDot {
			cleaned = strings.ReplaceAll(cleaned, ".", "")
			cleaned = strings.Replace(cleaned, ",", ".", 1)
		} else {
			cleaned = strings.ReplaceAll(cleaned, ",", "")
		}
	case lastComma >= 0:
		cleaned = strings.Replace(cleaned, ",", ".", 1)
	}

	// A single separator followed by exactly three digits is genuinely
	// ambiguous: "1.234" is 1234 to a German typist and 1.23 to a spreadsheet,
	// and "18,005" is simply a typo. Guessing would silently misbook money, so
	// it is rejected and the person is asked to be explicit.
	if dot := strings.LastIndex(cleaned, "."); dot >= 0 && len(cleaned)-dot-1 == 3 {
		return 0, fmt.Errorf("money: %q is ambiguous; write it as 1234,00 or 1,23", input)
	}

	if strings.Count(cleaned, ".") > 1 {
		return 0, fmt.Errorf("money: %q is not a valid amount", input)
	}

	whole, frac, _ := strings.Cut(cleaned, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 2 {
		return 0, fmt.Errorf("money: %q has more than two decimal places", input)
	}
	frac = frac + strings.Repeat("0", 2-len(frac))

	wholeCents, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: %q is not a valid amount", input)
	}
	fracCents, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: %q is not a valid amount", input)
	}

	cents := wholeCents*100 + fracCents
	if negative {
		cents = -cents
	}
	return cents, nil
}

// FormatCSV renders cents for the CSV export, which uses a decimal comma to
// stay readable in a German spreadsheet — matching csv_rows in the original.
func FormatCSV(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d,%02d", sign, cents/100, cents%100)
}
