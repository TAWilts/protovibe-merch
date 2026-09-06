package telemetry

import (
	"strings"
	"testing"
)

func TestAliasIsStableAndDoesNotExposeDatabaseID(t *testing.T) {
	service := &Service{aliasKey: []byte("0123456789abcdef0123456789abcdef")}

	first := service.alias("band", 42)
	second := service.alias("band", 42)
	other := service.alias("band", 43)

	if first != second {
		t.Fatalf("alias changed: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("different IDs produced the same alias: %q", first)
	}
	if strings.Contains(first, "42") {
		t.Fatalf("alias leaks the source ID: %q", first)
	}
	if !strings.HasPrefix(first, "band_") {
		t.Fatalf("alias has unexpected prefix: %q", first)
	}
}
