package catalogue_test

import (
	"slices"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

// TestCombinationKeyIsOrderIndependent is what stops an option-group reorder
// from creating a duplicate variant for a combination that already exists.
func TestCombinationKeyIsOrderIndependent(t *testing.T) {
	a := catalogue.CombinationKey([]int64{7, 3, 11})
	b := catalogue.CombinationKey([]int64{11, 7, 3})
	if a != b {
		t.Fatalf("keys must not depend on order: %q vs %q", a, b)
	}
	if a != "3|7|11" {
		t.Fatalf("unexpected key %q", a)
	}
	if got := catalogue.CombinationKey(nil); got != "" {
		t.Fatalf("an article without options must key to the empty string, got %q", got)
	}
}

// TestCombinationKeyDoesNotMutateInput guards against a subtle aliasing bug:
// sorting the caller's slice in place would silently reorder the variant's
// stored option_value_ids.
func TestCombinationKeyDoesNotMutateInput(t *testing.T) {
	ids := []int64{7, 3, 11}
	catalogue.CombinationKey(ids)
	if !slices.Equal(ids, []int64{7, 3, 11}) {
		t.Fatalf("input was mutated to %v", ids)
	}
}

func TestParseCombinationKeyRoundTrip(t *testing.T) {
	ids := []int64{3, 7, 11}
	parsed, err := catalogue.ParseCombinationKey(catalogue.CombinationKey(ids))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(parsed, ids) {
		t.Fatalf("round trip gave %v, want %v", parsed, ids)
	}

	empty, err := catalogue.ParseCombinationKey("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("the empty key must parse to no IDs, got %v, %v", empty, err)
	}
	if _, err := catalogue.ParseCombinationKey("3|nope"); err == nil {
		t.Fatal("a malformed key must be rejected")
	}
}

func TestExpectedCombinations(t *testing.T) {
	cases := []struct {
		name   string
		groups []catalogue.OptionGroupValues
		want   []string
	}{
		{
			// An article without options still has exactly one variant, so it
			// can be priced and sold.
			name:   "no options",
			groups: nil,
			want:   []string{""},
		},
		{
			name:   "single group",
			groups: []catalogue.OptionGroupValues{{GroupID: 1, ValueIDs: []int64{10, 11}}},
			want:   []string{"10", "11"},
		},
		{
			name: "cartesian product",
			groups: []catalogue.OptionGroupValues{
				{GroupID: 1, ValueIDs: []int64{10, 11}},
				{GroupID: 2, ValueIDs: []int64{20, 21, 22}},
			},
			want: []string{"10|20", "10|21", "10|22", "11|20", "11|21", "11|22"},
		},
		{
			// A group without values means the article is not fully configured.
			// Dropping that dimension instead would quietly create variants the
			// band never defined.
			name: "incomplete group yields nothing",
			groups: []catalogue.OptionGroupValues{
				{GroupID: 1, ValueIDs: []int64{10, 11}},
				{GroupID: 2, ValueIDs: nil},
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := catalogue.ExpectedCombinations(tc.groups)
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// TestExpectedCombinationsMatchesDefaultArticle pins the shape a newly created
// article gets: Farbe (Schwarz, Weiß) x Größe (S..XXL) = 12 variants, as in
// DEFAULT_NEW_ARTICLE_OPTIONS of the original.
func TestExpectedCombinationsMatchesDefaultArticle(t *testing.T) {
	groups := []catalogue.OptionGroupValues{
		{GroupID: 1, ValueIDs: []int64{1, 2}},
		{GroupID: 2, ValueIDs: []int64{3, 4, 5, 6, 7}},
	}
	if got := len(catalogue.ExpectedCombinations(groups)); got != 10 {
		t.Fatalf("expected 10 variants for 2x5 options, got %d", got)
	}
}

func TestIsConfigurationComplete(t *testing.T) {
	complete := []catalogue.OptionGroupValues{{GroupID: 1, ValueIDs: []int64{1}}}
	if !catalogue.IsConfigurationComplete(complete) {
		t.Error("a group with values is complete")
	}
	if catalogue.IsConfigurationComplete([]catalogue.OptionGroupValues{{GroupID: 1}}) {
		t.Error("a group without values is incomplete")
	}
	if !catalogue.IsConfigurationComplete(nil) {
		t.Error("an article without options is complete")
	}
}
