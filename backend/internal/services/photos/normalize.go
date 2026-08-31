// Package photos stores product pictures.
//
// Uploads are re-encoded rather than stored as received. That does three
// things at once: it caps the size a band's phone photo takes on disk, it
// strips metadata a customer-facing slideshow has no business carrying (GPS
// coordinates from the photo of a shirt on a kitchen table, for instance), and
// it guarantees the stored bytes really are an image.
package photos

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/disintegration/imaging"

	// Registered for their decoders; the encoder is always JPEG.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/webp"
)

// Limits, matching the original.
const (
	MaxUploadBytes = 10 << 20
	MaxDimension   = 1600
	MaxPixels      = 30_000_000
	JPEGQuality    = 84
)

// Errors returned by the normaliser.
var (
	ErrTooLarge      = errors.New("photos: the file exceeds 10 MB")
	ErrNotAnImage    = errors.New("photos: the file is not a readable image")
	ErrTooManyPixels = errors.New("photos: the image has more than 30 megapixels")
)

// Normalized is a re-encoded picture ready for the store.
type Normalized struct {
	Data   []byte
	Width  int
	Height int
}

// Normalize decodes, orients and re-encodes an upload as JPEG.
//
// The pixel budget is checked before decoding finishes, so a small file
// claiming enormous dimensions cannot make the server allocate gigabytes.
func Normalize(r io.Reader) (*Normalized, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxUploadBytes {
		return nil, ErrTooLarge
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		// WebP is not in the standard decoder set, so it is tried explicitly.
		if decoded, webpErr := webp.Decode(bytes.NewReader(raw)); webpErr == nil {
			return encode(decoded)
		}
		return nil, ErrNotAnImage
	}
	if int64(config.Width)*int64(config.Height) > MaxPixels {
		return nil, ErrTooManyPixels
	}
	_ = format

	// imaging.Decode applies the EXIF orientation, so a photo taken sideways
	// on a phone is stored the way the person saw it.
	decoded, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return nil, ErrNotAnImage
	}
	return encode(decoded)
}

func encode(source image.Image) (*Normalized, error) {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	if width > MaxDimension || height > MaxDimension {
		source = imaging.Fit(source, MaxDimension, MaxDimension, imaging.Lanczos)
		bounds = source.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
	}

	var buffer bytes.Buffer
	if err := imaging.Encode(&buffer, source, imaging.JPEG, imaging.JPEGQuality(JPEGQuality)); err != nil {
		return nil, fmt.Errorf("photos: encode: %w", err)
	}
	return &Normalized{Data: buffer.Bytes(), Width: width, Height: height}, nil
}
