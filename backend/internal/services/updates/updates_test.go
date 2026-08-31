package updates

import "testing"

// TestIsNewerComparesNumerically pins the trap a string comparison falls into:
// v0.10.0 sorts before v0.9.0 lexically, which would hide every release after
// the ninth minor version.
func TestIsNewerComparesNumerically(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v0.9.0", false},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4", true},
		{"v2.0.0", "v1.9.9", false},
		// A pre-release suffix does not make a version newer on its own.
		{"v1.0.0", "v1.0.0-rc1", false},
		// Nothing published yet is never an update.
		{"v1.0.0", "", false},
		// Unparsable tags fall back to "different means newer", so an operator
		// is told rather than left in the dark.
		{"nightly", "2026-08-30", true},
	}

	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
