package bandfinance

import "testing"

func TestSettledOrDefault(t *testing.T) {
	if !settledOrDefault(nil) {
		t.Fatal("omitted settlement field must preserve the historical settled default")
	}

	open := false
	if settledOrDefault(&open) {
		t.Fatal("explicit false must create an open entry")
	}

	settled := true
	if !settledOrDefault(&settled) {
		t.Fatal("explicit true must create a settled entry")
	}
}
