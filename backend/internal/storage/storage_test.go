package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/storage"
)

func newStore(t *testing.T) (*storage.LocalStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.NewLocalStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, root
}

func TestPutAndOpenRoundTrip(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	content := []byte("%PDF-1.4 invoice")

	object, err := store.Put(ctx, 7, storage.CategoryInvoice, "application/pdf", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if object.SizeBytes != int64(len(content)) {
		t.Fatalf("unexpected size %d", object.SizeBytes)
	}

	reader, info, err := store.Open(ctx, object.Key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: %q", got)
	}
	if info.MediaType != "application/pdf" {
		t.Fatalf("unexpected media type %q", info.MediaType)
	}
}

// TestKeysAreBandPrefixed is what makes a per-band backup or deletion a
// directory operation instead of a query.
func TestKeysAreBandPrefixed(t *testing.T) {
	store, _ := newStore(t)

	object, err := store.Put(context.Background(), 42, storage.CategoryVariantPhoto, "image/jpeg", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasPrefix(object.Key, "band-42/variant-photos/") {
		t.Fatalf("unexpected key %q", object.Key)
	}
	if !strings.HasSuffix(object.Key, ".jpg") {
		t.Fatalf("the key must carry the media type's extension: %q", object.Key)
	}
}

// TestStoredNamesCarryNoUserInput pins that a crafted upload filename can
// never reach the filesystem.
func TestStoredNamesCarryNoUserInput(t *testing.T) {
	store, _ := newStore(t)

	first, err := store.Put(context.Background(), 1, storage.CategoryInvoice, "application/pdf", strings.NewReader("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	second, err := store.Put(context.Background(), 1, storage.CategoryInvoice, "application/pdf", strings.NewReader("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if first.Key == second.Key {
		t.Fatal("two uploads must never collide on a name")
	}
}

// TestResolveRefusesTraversal is the guard against a crafted key from the
// database or a request parameter reaching outside the store.
func TestResolveRefusesTraversal(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	secret := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	for _, key := range []string{
		"../secret.txt",
		"band-1/../../secret.txt",
		"/etc/passwd",
	} {
		t.Run(key, func(t *testing.T) {
			reader, _, err := store.Open(ctx, key)
			if err == nil {
				reader.Close()
				t.Fatalf("key %q must not resolve", key)
			}
			// It either escapes and is refused, or it is confined and simply
			// does not exist. Both are safe; reading the secret is not.
			if !errors.Is(err, storage.ErrNotFound) && !strings.Contains(err.Error(), "escapes") {
				t.Fatalf("unexpected error for %q: %v", key, err)
			}
		})
	}

	if content, err := os.ReadFile(secret); err != nil || string(content) != "private" {
		t.Fatal("the file outside the store must be untouched")
	}
}

func TestUnsupportedMediaTypeIsRejected(t *testing.T) {
	store, _ := newStore(t)
	if _, err := store.Put(context.Background(), 1, storage.CategoryInvoice, "text/html", strings.NewReader("<script>")); err == nil {
		t.Fatal("an unsupported media type must be rejected before it is stored")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	object, err := store.Put(ctx, 1, storage.CategoryInvoice, "application/pdf", strings.NewReader("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete(ctx, object.Key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Deleting again must stay silent, so cleanup after a partial failure is
	// safe to retry.
	if err := store.Delete(ctx, object.Key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := store.Delete(ctx, ""); err != nil {
		t.Fatalf("deleting nothing must be a no-op: %v", err)
	}
	if _, _, err := store.Open(ctx, object.Key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestUsageBytesCountsOnlyOneBand backs the per-band quota display.
func TestUsageBytesCountsOnlyOneBand(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	if _, err := store.Put(ctx, 1, storage.CategoryInvoice, "application/pdf", strings.NewReader("12345")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Put(ctx, 2, storage.CategoryInvoice, "application/pdf", strings.NewReader("1234567890")); err != nil {
		t.Fatalf("put: %v", err)
	}

	first, err := store.UsageBytes(ctx, 1)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if first != 5 {
		t.Fatalf("expected 5 bytes for band 1, got %d", first)
	}

	// A band that has stored nothing reports zero rather than failing.
	empty, err := store.UsageBytes(ctx, 99)
	if err != nil {
		t.Fatalf("usage for an empty band: %v", err)
	}
	if empty != 0 {
		t.Fatalf("expected 0 bytes, got %d", empty)
	}
}
