package purchases

import "testing"

func TestGrossFromEntered(t *testing.T) {
	if got := GrossFromEntered(10000, false, 1900); got != 11900 {
		t.Fatalf("100.00 net at 19%% = %d cents, want 11900", got)
	}
	if got := GrossFromEntered(11900, true, 1900); got != 11900 {
		t.Fatalf("gross input changed: %d", got)
	}
}

func TestNetFromGross(t *testing.T) {
	if got := NetFromGross(11900, 1900); got != 10000 {
		t.Fatalf("119.00 gross at 19%% = %d cents net, want 10000", got)
	}
}
