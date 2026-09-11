package sandbox

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedTourShirtImageMatchesLandingPage(t *testing.T) {
	landingImage, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "..", "frontend", "public", "demo-products", "shirt.jpg",
	))
	if err != nil {
		t.Fatalf("read landing-page Tour Shirt image: %v", err)
	}
	if !bytes.Equal(tourShirtDemoImage, landingImage) {
		t.Fatal("embedded sandbox Tour Shirt image differs from the landing-page demo image")
	}
}
