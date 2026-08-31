// Package catalogue holds the article, option and variant logic.
//
// The central rule, carried over from the Flask original, is that nothing is
// ever physically deleted. Removing an option deactivates it, so a receipt
// from last summer still resolves the option names it was booked with — and
// renaming a value retroactively updates old receipts, which is exactly what
// the band asked for.
package catalogue

import (
	"slices"
	"strconv"
	"strings"
)

// CombinationSeparator joins the option-value IDs of a variant key.
const CombinationSeparator = "|"

// CombinationKey gives a variant a stable, order-independent identity.
//
// The IDs are sorted, so reordering an article's option groups never creates a
// duplicate variant for a combination that already exists. It mirrors
// sorted_combination_key in _old/app.py:5611.
func CombinationKey(optionValueIDs []int64) string {
	if len(optionValueIDs) == 0 {
		return ""
	}
	sorted := slices.Clone(optionValueIDs)
	slices.Sort(sorted)

	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, CombinationSeparator)
}

// ParseCombinationKey reverses CombinationKey. An empty key is an article
// without options, which has exactly one variant.
func ParseCombinationKey(key string) ([]int64, error) {
	if key == "" {
		return []int64{}, nil
	}
	parts := strings.Split(key, CombinationSeparator)
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// OptionGroupValues is one option column and its selectable value IDs, in the
// order the band arranged them.
type OptionGroupValues struct {
	GroupID  int64
	ValueIDs []int64
}

// ExpectedCombinations returns the variant keys an article's option
// configuration implies — the Cartesian product of every group's values.
//
// Two edge cases are deliberate and match the original:
//   - an article with no option groups has exactly one variant, keyed "";
//   - an article with a group that has no values yet is incompletely
//     configured and has no valid variants at all, rather than silently
//     dropping that dimension from the product.
func ExpectedCombinations(groups []OptionGroupValues) []string {
	if len(groups) == 0 {
		return []string{""}
	}
	for _, group := range groups {
		if len(group.ValueIDs) == 0 {
			return nil
		}
	}

	combinations := [][]int64{{}}
	for _, group := range groups {
		next := make([][]int64, 0, len(combinations)*len(group.ValueIDs))
		for _, prefix := range combinations {
			for _, valueID := range group.ValueIDs {
				next = append(next, append(slices.Clone(prefix), valueID))
			}
		}
		combinations = next
	}

	keys := make([]string, 0, len(combinations))
	for _, combination := range combinations {
		keys = append(keys, CombinationKey(combination))
	}
	return keys
}

// IsConfigurationComplete reports whether an article can be sold: every option
// group it defines must offer at least one value.
func IsConfigurationComplete(groups []OptionGroupValues) bool {
	for _, group := range groups {
		if len(group.ValueIDs) == 0 {
			return false
		}
	}
	return true
}
