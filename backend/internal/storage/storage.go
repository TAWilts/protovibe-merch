// Package storage keeps uploaded files out of the database.
//
// Invoices and product photos are written to a content store rather than into
// MariaDB, so dumps stay small and a restore does not have to move image bytes
// through SQL. Stored names are opaque and carry no user input, which keeps a
// crafted filename from escaping the store or being served back as something
// it is not.
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when a stored object no longer exists.
var ErrNotFound = errors.New("storage: object not found")

// Object describes a stored file.
type Object struct {
	// Key is the opaque path used to retrieve the file again. It is what the
	// database stores.
	Key       string
	SizeBytes int64
	MediaType string
}

// Store is the abstraction the handlers use. The local implementation writes
// to a mounted volume; an S3-backed one can be added without touching callers.
type Store interface {
	// Put writes a new object under a band's prefix and returns its key.
	Put(ctx context.Context, bandID int64, category string, mediaType string, r io.Reader) (*Object, error)
	// Open returns a reader for a stored object.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, *Object, error)
	// Delete removes an object. A missing object is not an error, so cleanup
	// after a partially failed upload stays idempotent.
	Delete(ctx context.Context, key string) error
	// UsageBytes reports how much a band currently stores, which is what the
	// admin center's quota display reads.
	UsageBytes(ctx context.Context, bandID int64) (int64, error)
}

// Categories used by the application.
const (
	CategoryInvoice      = "invoices"
	CategoryVariantPhoto = "variant-photos"
	CategorySlideshow    = "slideshow-photos"
	CategoryBandDocument = "band-documents"
)

// LocalStore writes to a directory on a mounted volume.
type LocalStore struct {
	root string
}

// NewLocalStore prepares the store directory.
func NewLocalStore(root string) (*LocalStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &LocalStore{root: absolute}, nil
}

// extensions maps the media types the application accepts onto a file suffix.
// Anything not listed is rejected before it reaches the store.
var extensions = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
}

// ExtensionFor returns the suffix used for a media type, and whether the type
// is accepted at all.
func ExtensionFor(mediaType string) (string, bool) {
	ext, ok := extensions[strings.ToLower(strings.TrimSpace(mediaType))]
	return ext, ok
}

func (s *LocalStore) Put(ctx context.Context, bandID int64, category, mediaType string, r io.Reader) (*Object, error) {
	ext, ok := ExtensionFor(mediaType)
	if !ok {
		return nil, fmt.Errorf("storage: unsupported media type %q", mediaType)
	}

	name, err := randomName()
	if err != nil {
		return nil, err
	}
	// The key is band-prefixed, which makes a per-band backup or deletion a
	// directory operation rather than a query.
	key := path.Join(fmt.Sprintf("band-%d", bandID), category, name+ext)

	target, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return nil, err
	}

	// Write to a temporary file first and rename into place, so a failed or
	// interrupted upload never leaves a half-written invoice behind.
	temp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return nil, err
	}
	tempName := temp.Name()
	defer func() {
		temp.Close()
		os.Remove(tempName)
	}()

	written, err := io.Copy(temp, r)
	if err != nil {
		return nil, err
	}
	if err := temp.Sync(); err != nil {
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tempName, 0o640); err != nil {
		return nil, err
	}
	if err := os.Rename(tempName, target); err != nil {
		return nil, err
	}

	return &Object{Key: key, SizeBytes: written, MediaType: mediaType}, nil
}

func (s *LocalStore) Open(ctx context.Context, key string) (io.ReadSeekCloser, *Object, error) {
	target, err := s.resolve(key)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, &Object{Key: key, SizeBytes: info.Size(), MediaType: mediaTypeOf(key)}, nil
}

func (s *LocalStore) Delete(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *LocalStore) UsageBytes(ctx context.Context, bandID int64) (int64, error) {
	base := filepath.Join(s.root, fmt.Sprintf("band-%d", bandID))

	var total int64
	err := filepath.Walk(base, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return total, nil
}

// resolve turns a key into an absolute path and refuses anything that would
// escape the store, which is the guard against a crafted key from the database
// or a request parameter.
func (s *LocalStore) resolve(key string) (string, error) {
	cleaned := path.Clean("/" + strings.TrimSpace(key))
	target := filepath.Join(s.root, filepath.FromSlash(cleaned))

	if target != s.root && !strings.HasPrefix(target, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("storage: key %q escapes the store", key)
	}
	return target, nil
}

func randomName() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func mediaTypeOf(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
