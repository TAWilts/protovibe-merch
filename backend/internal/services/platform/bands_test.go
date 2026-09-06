package platform

import "testing"

func TestValidateBandIdentity(t *testing.T) {
	tests := []struct {
		slug string
		name string
		ok   bool
	}{
		{slug: "ab", name: "Band", ok: true},
		{slug: "tour-band-2026", name: "  Tour Band  ", ok: true},
		{slug: "a", name: "Band", ok: false},
		{slug: "-band", name: "Band", ok: false},
		{slug: "Band Name", name: "Band", ok: false},
		{slug: "band", name: "", ok: false},
	}
	for _, test := range tests {
		_, _, err := ValidateBandIdentity(test.slug, test.name)
		if (err == nil) != test.ok {
			t.Errorf("ValidateBandIdentity(%q, %q) error=%v, want ok=%v", test.slug, test.name, err, test.ok)
		}
	}
}
