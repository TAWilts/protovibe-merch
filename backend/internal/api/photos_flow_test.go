package api_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// uploadPhoto posts an image with optional form fields.
func (h *harness) uploadPhoto(fields map[string]string, content []byte, filename string) response {
	h.t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		_ = writer.WriteField(name, value)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		h.t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		h.t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/photos", &body)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if h.cookie != "" {
		req.Header.Set("Cookie", h.cookie)
	}
	if h.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", h.csrfToken)
	}

	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("perform request: %v", err)
	}
	defer res.Body.Close()

	out := response{Status: res.StatusCode, Body: map[string]any{}}
	_ = json.NewDecoder(res.Body).Decode(&out.Body)
	return out
}

// samplePNG builds a small test image.
func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 60, B: 220, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buffer.Bytes()
}

// TestPhotoGalleryLifecycle walks uploading a product picture and a
// free-standing display picture, and curating the slideshow selection.
func TestPhotoGalleryLifecycle(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	_, variants := h.sellableArticle("Photo Shirt")

	product := h.uploadPhoto(map[string]string{"variant_id": itoa(variants[0])},
		samplePNG(t, 300, 200), "shirt.png")
	if product.Status != http.StatusCreated {
		t.Fatalf("upload product photo: %d %v", product.Status, product.Body)
	}
	productID := int64(product.Body["id"].(float64))
	if productID <= 0 {
		t.Fatalf("a product photo gets a positive identifier: %v", product.Body)
	}

	extra := h.uploadPhoto(nil, samplePNG(t, 200, 200), "preise.png")
	if extra.Status != http.StatusCreated {
		t.Fatalf("upload display photo: %d %v", extra.Status, extra.Body)
	}
	extraID := int64(extra.Body["id"].(float64))
	if extraID >= 0 {
		t.Fatalf("a free-standing photo gets a negative identifier: %v", extra.Body)
	}

	gallery := h.do(http.MethodGet, "/api/v1/photos", nil)
	if len(jsonList(gallery.Body, "photos")) != 2 {
		t.Fatalf("both pictures belong to one gallery: %v", gallery.Body)
	}
	for _, raw := range jsonList(gallery.Body, "photos") {
		photo := jsonObject(raw)
		if int64(photo["id"].(float64)) == productID {
			if photo["article_name"] != "Photo Shirt" || photo["variant_label"] == "" {
				t.Fatalf("a product photo must carry its labels: %v", photo)
			}
			if photo["sale_price_cents"] != float64(1800) {
				t.Fatalf("the price is needed for the overlay: %v", photo)
			}
		}
	}

	// Both start selected for the shop display, as in the original.
	slideshow := h.do(http.MethodGet, "/api/v1/slideshow", nil)
	if len(jsonList(slideshow.Body, "photos")) != 2 {
		t.Fatalf("new pictures start selected: %v", slideshow.Body)
	}
	if slideshow.Body["collage_show_prices"] != true {
		t.Fatalf("a missing settings row must default to showing prices: %v", slideshow.Body)
	}

	// Opting one out removes it from the display but not from the gallery.
	if res := h.do(http.MethodPatch, "/api/v1/photos/"+itoa(extraID),
		map[string]any{"include_in_slideshow": false}); res.Status != http.StatusNoContent {
		t.Fatalf("update: %d %v", res.Status, res.Body)
	}
	slideshow = h.do(http.MethodGet, "/api/v1/slideshow", nil)
	if len(jsonList(slideshow.Body, "photos")) != 1 {
		t.Fatalf("the opted-out picture must leave the display: %v", slideshow.Body)
	}
	if gallery := h.do(http.MethodGet, "/api/v1/photos", nil); len(jsonList(gallery.Body, "photos")) != 2 {
		t.Fatalf("it must stay in the gallery: %v", gallery.Body)
	}

	if res := h.do(http.MethodPatch, "/api/v1/slideshow/settings",
		map[string]any{"collage_show_prices": false}); res.Status != http.StatusNoContent {
		t.Fatalf("settings: %d %v", res.Status, res.Body)
	}
	slideshow = h.do(http.MethodGet, "/api/v1/slideshow", nil)
	if slideshow.Body["collage_show_prices"] != false {
		t.Fatalf("the preference must stick: %v", slideshow.Body)
	}

	if res := h.do(http.MethodDelete, "/api/v1/photos/"+itoa(productID), nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", res.Status, res.Body)
	}
	if gallery := h.do(http.MethodGet, "/api/v1/photos", nil); len(jsonList(gallery.Body, "photos")) != 1 {
		t.Fatalf("the deleted picture must be gone: %v", gallery.Body)
	}
}

// TestUploadedPhotosAreReencoded pins that whatever a phone sends is stored as
// a plain JPEG — that caps the size, strips camera metadata and guarantees the
// stored bytes really are an image.
func TestUploadedPhotosAreReencoded(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	uploaded := h.uploadPhoto(nil, samplePNG(t, 2400, 1800), "gross.png")
	if uploaded.Status != http.StatusCreated {
		t.Fatalf("upload: %d %v", uploaded.Status, uploaded.Body)
	}
	id := int64(uploaded.Body["id"].(float64))

	status, body, _ := h.download("/api/v1/photos/" + itoa(id) + "/file")
	if status != http.StatusOK {
		t.Fatalf("download: %d", status)
	}
	if !bytes.HasPrefix(body, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("the stored picture must be a JPEG, got %x", body[:4])
	}

	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > 1600 || bounds.Dy() > 1600 {
		t.Fatalf("the picture should be scaled down, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

// TestNonImageUploadsAreRefused keeps a disguised file out of the store.
func TestNonImageUploadsAreRefused(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)

	res := h.uploadPhoto(nil, []byte("<script>alert(1)</script>"), "evil.png")
	if res.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("a non-image must be refused, got %d %v", res.Status, res.Body)
	}
}

// TestSellersSeeButCannotCuratePhotos pins the role split.
func TestSellersSeeButCannotCuratePhotos(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleSeller)

	if res := h.do(http.MethodGet, "/api/v1/slideshow", nil); res.Status != http.StatusOK {
		t.Fatalf("a seller must be able to run the display: %d %v", res.Status, res.Body)
	}
	if res := h.uploadPhoto(nil, samplePNG(t, 100, 100), "x.png"); res.Status != http.StatusForbidden {
		t.Fatalf("a seller must not upload, got %d %v", res.Status, res.Body)
	}
}

// TestPhotosAreBandScoped pins the tenant boundary on the gallery.
func TestPhotosAreBandScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()

	h.signInAs(bandA, models.RoleManager)
	uploaded := h.uploadPhoto(nil, samplePNG(t, 120, 120), "a.png")
	id := int64(uploaded.Body["id"].(float64))

	h.signInAs(bandB, models.RoleManager)
	if res := h.do(http.MethodGet, "/api/v1/photos", nil); len(jsonList(res.Body, "photos")) != 0 {
		t.Fatalf("band B must not see band A's gallery: %v", res.Body)
	}
	if status, _, _ := h.download("/api/v1/photos/" + itoa(id) + "/file"); status != http.StatusNotFound {
		t.Fatalf("band B must not fetch band A's picture, got %d", status)
	}
}
