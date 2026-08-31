package money_test

import (
	"slices"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
)

// TestDistributeIsExact is the property that matters: however a donation is
// split across a basket, the parts must add back up to the whole. Anything
// else would let a cancelled position distort the remaining balance.
func TestDistributeIsExact(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		weights []int64
		want    []int64
	}{
		{"even split", 300, []int64{1000, 1000, 1000}, []int64{100, 100, 100}},
		{"odd cent goes left", 100, []int64{1000, 1000, 1000}, []int64{34, 33, 33}},
		{"two odd cents", 101, []int64{1000, 1000, 1000}, []int64{34, 34, 33}},
		{"proportional", 100, []int64{3000, 1000}, []int64{75, 25}},
		{"single position", 250, []int64{1800}, []int64{250}},
		{"no donation", 0, []int64{1800, 1200}, []int64{0, 0}},
		{"negative is treated as none", -50, []int64{1800}, []int64{0}},
		// A fully free basket has no meaningful denominator, so the whole
		// amount goes to the first position rather than being lost.
		{"free basket", 500, []int64{0, 0}, []int64{500, 0}},
		{"zero-priced position is skipped", 100, []int64{0, 1000}, []int64{0, 100}},
		{"empty basket", 100, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := money.Distribute(tc.total, tc.weights)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDistributeAlwaysSumsToTotal sweeps a wide range of awkward inputs,
// because a rounding bug here silently corrupts the balance sheet rather than
// failing loudly.
func TestDistributeAlwaysSumsToTotal(t *testing.T) {
	weightSets := [][]int64{
		{1800, 1200, 999},
		{1, 1, 1, 1, 1, 1, 1},
		{2500},
		{333, 333, 334},
		{10000, 1},
	}
	for _, weights := range weightSets {
		for total := int64(0); total <= 250; total++ {
			shares := money.Distribute(total, weights)
			var sum int64
			for _, share := range shares {
				if share < 0 {
					t.Fatalf("negative share for total %d, weights %v: %v", total, weights, shares)
				}
				sum += share
			}
			if sum != total {
				t.Fatalf("total %d with weights %v distributed to %v (sum %d)", total, weights, shares, sum)
			}
		}
	}
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"18,00", 1800},
		{"18.00", 1800},
		{"18", 1800},
		{"18,5", 1850},
		{"0,99", 99},
		{",99", 99},
		{"  18,00 € ", 1800},
		{"-4,20", -420},
		{"1.234,56", 123456},
		{"1,234.56", 123456},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := money.ParseAmount(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d cents, want %d", got, tc.want)
			}
		})
	}
}

// TestParseAmountRejectsAmbiguousInput pins the deliberate choice to refuse a
// single separator with three following digits rather than guess whether it
// groups thousands or is an over-precise decimal. Guessing would misbook money
// silently; refusing asks the person to be explicit.
func TestParseAmountRejectsAmbiguousInput(t *testing.T) {
	for _, input := range []string{"1.234", "18,005", "1,000"} {
		if got, err := money.ParseAmount(input); err == nil {
			t.Errorf("%q must be rejected as ambiguous, got %d cents", input, got)
		}
	}
	// The same numbers written unambiguously still work.
	for input, want := range map[string]int64{"1234,00": 123400, "1.234,00": 123400, "1000,00": 100000} {
		got, err := money.ParseAmount(input)
		if err != nil {
			t.Errorf("%q must be accepted: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseAmount(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseAmountRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "   ", "abc", "1.2.3", "1,2,3"} {
		if _, err := money.ParseAmount(input); err == nil {
			t.Errorf("%q must be rejected", input)
		}
	}
}

func TestFormatCSV(t *testing.T) {
	cases := map[int64]string{
		0:     "0,00",
		5:     "0,05",
		1800:  "18,00",
		1805:  "18,05",
		-420:  "-4,20",
		12345: "123,45",
	}
	for cents, want := range cases {
		if got := money.FormatCSV(cents); got != want {
			t.Errorf("FormatCSV(%d) = %q, want %q", cents, got, want)
		}
	}
}
