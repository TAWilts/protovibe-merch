package photos_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/services/photos"
)

// makeImage builds a solid test image of the given size.
func makeImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

// TestNormalizeReencodesAsJPEG pins that whatever arrives leaves as JPEG,
// which is what guarantees the stored bytes really are an image.
func TestNormalizeReencodesAsJPEG(t *testing.T) {
	source := encodePNG(t, makeImage(400, 300))

	result, err := photos.Normalize(bytes.NewReader(source))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(result.Data)); err != nil {
		t.Fatalf("the output must be a valid JPEG: %v", err)
	}
	if result.Width != 400 || result.Height != 300 {
		t.Fatalf("a small image must keep its size, got %dx%d", result.Width, result.Height)
	}
}

// TestOversizedImagesAreScaledDown pins the 1600 px cap, which is what keeps a
// band's phone photos from filling the disk.
func TestOversizedImagesAreScaledDown(t *testing.T) {
	source := encodePNG(t, makeImage(3000, 2000))

	result, err := photos.Normalize(bytes.NewReader(source))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Width > photos.MaxDimension || result.Height > photos.MaxDimension {
		t.Fatalf("expected a scaled image, got %dx%d", result.Width, result.Height)
	}
	// The aspect ratio must survive, or a shirt ends up stretched.
	ratio := float64(result.Width) / float64(result.Height)
	if ratio < 1.49 || ratio > 1.51 {
		t.Fatalf("aspect ratio drifted to %.3f", ratio)
	}
}

// TestMetadataIsDropped pins that re-encoding strips whatever the camera wrote
// into the file — a slideshow of shirts has no business carrying GPS data.
func TestMetadataIsDropped(t *testing.T) {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, makeImage(200, 200), nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// A comment segment stands in for any metadata a camera might add.
	withComment := append([]byte{}, buffer.Bytes()[:2]...)
	comment := []byte("GPS 52.5200 13.4050")
	withComment = append(withComment, 0xFF, 0xFE, 0, byte(len(comment)+2))
	withComment = append(withComment, comment...)
	withComment = append(withComment, buffer.Bytes()[2:]...)

	result, err := photos.Normalize(bytes.NewReader(withComment))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if bytes.Contains(result.Data, comment) {
		t.Fatal("metadata from the original must not survive re-encoding")
	}
}

func TestNormalizeRejectsBadInput(t *testing.T) {
	if _, err := photos.Normalize(strings.NewReader("nicht wirklich ein bild")); !errors.Is(err, photos.ErrNotAnImage) {
		t.Errorf("a non-image must be rejected, got %v", err)
	}
	// A file just over the cap.
	oversized := bytes.Repeat([]byte{0}, photos.MaxUploadBytes+1)
	if _, err := photos.Normalize(bytes.NewReader(oversized)); !errors.Is(err, photos.ErrTooLarge) {
		t.Errorf("an over-large file must be rejected, got %v", err)
	}
}

// TestPixelBudgetIsCheckedBeforeDecoding pins the guard against a small file
// that claims enormous dimensions — the classic decompression bomb.
func TestPixelBudgetIsCheckedBeforeDecoding(t *testing.T) {
	// A PNG header describing 40000x40000 pixels compresses to almost nothing.
	source := encodePNG(t, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	// Rewrite the IHDR width and height to an absurd value.
	bomb := append([]byte{}, source...)
	copy(bomb[16:24], []byte{0x00, 0x00, 0x9C, 0x40, 0x00, 0x00, 0x9C, 0x40})

	_, err := photos.Normalize(bytes.NewReader(bomb))
	if err == nil {
		t.Fatal("an image claiming 40000x40000 pixels must be refused")
	}
	if !errors.Is(err, photos.ErrTooManyPixels) && !errors.Is(err, photos.ErrNotAnImage) {
		t.Fatalf("unexpected error: %v", err)
	}
}
