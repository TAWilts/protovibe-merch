package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func packingUUID() string {
	return fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012x", uniqueCounter.Add(1))
}

func packingOperation(eventID, operationType string, generation int64, fields map[string]any) map[string]any {
	result := map[string]any{
		"event_id": eventID, "device_id": "api-test-device",
		"client_created_at": "2026-09-08T10:00:00Z", "base_generation": generation,
		"type": operationType,
	}
	for key, value := range fields {
		result[key] = value
	}
	return result
}

func (h *harness) uploadPackingPhoto(fields map[string]string, content []byte, filename string) response {
	h.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			h.t.Fatalf("write packing photo field: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("create packing photo part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		h.t.Fatalf("write packing photo: %v", err)
	}
	if err := writer.Close(); err != nil {
		h.t.Fatalf("close packing photo body: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/packing-list/photos", &body)
	if err != nil {
		h.t.Fatalf("build packing photo request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", h.cookie)
	req.Header.Set("X-CSRF-Token", h.csrfToken)
	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("upload packing photo: %v", err)
	}
	defer res.Body.Close()
	out := response{Status: res.StatusCode, Body: map[string]any{}}
	_ = json.NewDecoder(res.Body).Decode(&out.Body)
	return out
}

func TestPackingListRolesStatusResetAndIdempotency(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)
	bagID, itemID, eventID := packingUUID(), packingUUID(), packingUUID()

	createBag := packingOperation(eventID, "create_bag", 1, map[string]any{"bag_id": bagID, "name": "Stagerack"})
	created := h.do(http.MethodPost, "/api/v1/packing-list/operations", createBag)
	if created.Status != http.StatusOK || created.Body["revision"] != float64(1) {
		t.Fatalf("create bag: %d %v", created.Status, created.Body)
	}
	replayed := h.do(http.MethodPost, "/api/v1/packing-list/operations", createBag)
	if replayed.Status != http.StatusOK || replayed.Body["replayed"] != true || replayed.Body["revision"] != float64(1) {
		t.Fatalf("idempotent replay: %d %v", replayed.Status, replayed.Body)
	}
	tampered := packingOperation(eventID, "create_bag", 1, map[string]any{"bag_id": bagID, "name": "Andere Daten"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", tampered); result.Status != http.StatusConflict || result.Body["code"] != "sync_conflict" {
		t.Fatalf("event reuse must conflict: %d %v", result.Status, result.Body)
	}

	createItem := packingOperation(packingUUID(), "create_item", 1, map[string]any{
		"bag_id": bagID, "item_id": itemID, "name": "Laptop",
	})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", createItem); result.Status != http.StatusOK {
		t.Fatalf("create item: %d %v", result.Status, result.Body)
	}

	h.signInAs(band, models.RoleSeller)
	forbiddenCreate := packingOperation(packingUUID(), "create_bag", 1, map[string]any{"bag_id": packingUUID(), "name": "Kiste"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", forbiddenCreate); result.Status != http.StatusForbidden {
		t.Fatalf("seller must not manage structure: %d %v", result.Status, result.Body)
	}
	pack := packingOperation(packingUUID(), "set_item_status", 1, map[string]any{"item_id": itemID, "status": "packed"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", pack); result.Status != http.StatusOK {
		t.Fatalf("seller may pack: %d %v", result.Status, result.Body)
	}
	exclude := packingOperation(packingUUID(), "set_bag_status", 1, map[string]any{"bag_id": bagID, "status": "stays_here"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", exclude); result.Status != http.StatusOK {
		t.Fatalf("seller may exclude bag: %d %v", result.Status, result.Body)
	}
	include := packingOperation(packingUUID(), "set_bag_status", 1, map[string]any{"bag_id": bagID, "status": "open"})
	included := h.do(http.MethodPost, "/api/v1/packing-list/operations", include)
	bag := jsonObject(jsonList(included.Body, "bags")[0])
	item := jsonObject(jsonList(bag, "items")[0])
	if bag["status"] != "packed" || item["status"] != "packed" {
		t.Fatalf("including an excluded bag must preserve children: %v", included.Body)
	}

	h.signInAs(band, models.RoleMember)
	reset := packingOperation(packingUUID(), "reset", 1, nil)
	resetResult := h.do(http.MethodPost, "/api/v1/packing-list/operations", reset)
	if resetResult.Status != http.StatusOK || resetResult.Body["generation"] != float64(2) {
		t.Fatalf("reset: %d %v", resetResult.Status, resetResult.Body)
	}
	h.signInAs(band, models.RoleSeller)
	stale := packingOperation(packingUUID(), "set_item_status", 1, map[string]any{"item_id": itemID, "status": "packed"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", stale); result.Status != http.StatusConflict || result.Body["code"] != "packing_generation_conflict" {
		t.Fatalf("stale status must be discarded: %d %v", result.Status, result.Body)
	}
}

func TestPackingListTenantIsolationFeatureFlagAndPhotos(t *testing.T) {
	h := newHarness(t)
	first := h.makeBand()
	h.signInAs(first, models.RoleMember)
	bagID := packingUUID()
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", packingOperation(packingUUID(), "create_bag", 1, map[string]any{"bag_id": bagID, "name": "Kiste"})); result.Status != http.StatusOK {
		t.Fatalf("create first tenant bag: %d %v", result.Status, result.Body)
	}

	photoID, photoEvent := packingUUID(), packingUUID()
	fields := map[string]string{
		"event_id": photoEvent, "device_id": "api-test-device", "client_created_at": "2026-09-08T10:00:00Z",
		"photo_id": photoID, "bag_id": bagID,
	}
	uploaded := h.uploadPackingPhoto(fields, samplePNG(t, 40, 30), "inhalt.png")
	if uploaded.Status != http.StatusCreated {
		t.Fatalf("upload theme photo: %d %v", uploaded.Status, uploaded.Body)
	}
	replayed := h.uploadPackingPhoto(fields, samplePNG(t, 40, 30), "inhalt.png")
	if replayed.Status != http.StatusCreated || replayed.Body["replayed"] != true {
		t.Fatalf("photo retry must replay: %d %v", replayed.Status, replayed.Body)
	}
	if file := h.do(http.MethodGet, "/api/v1/packing-list/photos/"+photoID+"/file", nil); file.Status != http.StatusOK {
		t.Fatalf("download cached photo source: %d %v", file.Status, file.Body)
	}
	deleted := packingOperation(packingUUID(), "delete_photo", 1, map[string]any{"photo_id": photoID})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", deleted); result.Status != http.StatusOK {
		t.Fatalf("delete packing photo: %d %v", result.Status, result.Body)
	}
	if file := h.do(http.MethodGet, "/api/v1/packing-list/photos/"+photoID+"/file", nil); file.Status != http.StatusNotFound {
		t.Fatalf("deleted photo must disappear: %d %v", file.Status, file.Body)
	}

	second := h.makeBand()
	h.signInAs(second, models.RoleMember)
	foreignStatus := packingOperation(packingUUID(), "set_bag_status", 1, map[string]any{"bag_id": bagID, "status": "packed"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", foreignStatus); result.Status != http.StatusNotFound {
		t.Fatalf("foreign bag must be invisible: %d %v", result.Status, result.Body)
	}

	disabled := false
	second.FeatureFlags.PackingList = &disabled
	if err := h.db.WithContext(h.ctx()).Model(second).Update("feature_flags", second.FeatureFlags).Error; err != nil {
		t.Fatalf("disable packing flag: %v", err)
	}
	if result := h.do(http.MethodGet, "/api/v1/packing-list", nil); result.Status != http.StatusForbidden || result.Body["code"] != "feature_disabled" {
		t.Fatalf("feature flag must guard API: %d %v", result.Status, result.Body)
	}
}

func TestPackingListPOSModeKeepsCheckingButBlocksManagement(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)
	bagID, itemID := packingUUID(), packingUUID()
	createBag := packingOperation(packingUUID(), "create_bag", 1, map[string]any{"bag_id": bagID, "name": "Instrumente"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", createBag); result.Status != http.StatusOK {
		t.Fatalf("create bag: %d %v", result.Status, result.Body)
	}
	createItem := packingOperation(packingUUID(), "create_item", 1, map[string]any{"bag_id": bagID, "item_id": itemID, "name": "Gitarre"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", createItem); result.Status != http.StatusOK {
		t.Fatalf("create item: %d %v", result.Status, result.Body)
	}
	if result := h.do(http.MethodPost, "/api/v1/session/pos-mode", map[string]any{"enabled": true}); result.Status != http.StatusOK {
		t.Fatalf("enter POS mode: %d %v", result.Status, result.Body)
	}
	if result := h.do(http.MethodGet, "/api/v1/packing-list", nil); result.Status != http.StatusOK {
		t.Fatalf("POS mode must read packing list: %d %v", result.Status, result.Body)
	}
	status := packingOperation(packingUUID(), "set_item_status", 1, map[string]any{"item_id": itemID, "status": "packed"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", status); result.Status != http.StatusOK {
		t.Fatalf("POS mode must check items: %d %v", result.Status, result.Body)
	}
	rename := packingOperation(packingUUID(), "rename_item", 1, map[string]any{"item_id": itemID, "name": "Bass"})
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", rename); result.Status != http.StatusForbidden || result.Body["code"] != "pos_mode_restricted" {
		t.Fatalf("POS management must be blocked: %d %v", result.Status, result.Body)
	}
	photo := h.uploadPackingPhoto(map[string]string{
		"event_id": packingUUID(), "device_id": "api-test-device", "client_created_at": "2026-09-08T10:00:00Z",
		"photo_id": packingUUID(), "item_id": itemID,
	}, samplePNG(t, 20, 20), "gitarre.png")
	if photo.Status != http.StatusForbidden || photo.Body["code"] != "pos_mode_restricted" {
		t.Fatalf("POS photo upload must be blocked: %d %v", photo.Status, photo.Body)
	}
}

func TestPackingListCRUDAndOrdering(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)
	firstBag, secondBag := packingUUID(), packingUUID()
	firstItem, secondItem := packingUUID(), packingUUID()

	operations := []map[string]any{
		packingOperation(packingUUID(), "create_bag", 1, map[string]any{"bag_id": firstBag, "name": "Kiste"}),
		packingOperation(packingUUID(), "create_bag", 1, map[string]any{"bag_id": secondBag, "name": "Gepäck"}),
		packingOperation(packingUUID(), "create_item", 1, map[string]any{"bag_id": firstBag, "item_id": firstItem, "name": "Batterien"}),
		packingOperation(packingUUID(), "create_item", 1, map[string]any{"bag_id": firstBag, "item_id": secondItem, "name": "Stifte"}),
		packingOperation(packingUUID(), "rename_bag", 1, map[string]any{"bag_id": secondBag, "name": "Gepäck Thomas"}),
		packingOperation(packingUUID(), "rename_item", 1, map[string]any{"item_id": secondItem, "name": "Edding"}),
		packingOperation(packingUUID(), "reorder_bags", 1, map[string]any{"order_ids": []string{secondBag, firstBag}}),
		packingOperation(packingUUID(), "reorder_items", 1, map[string]any{"bag_id": firstBag, "order_ids": []string{secondItem, firstItem}}),
	}
	for _, operation := range operations {
		if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", operation); result.Status != http.StatusOK {
			t.Fatalf("apply %s: %d %v", operation["type"], result.Status, result.Body)
		}
	}
	snapshot := h.do(http.MethodGet, "/api/v1/packing-list", nil)
	bags := jsonList(snapshot.Body, "bags")
	if jsonObject(bags[0])["name"] != "Gepäck Thomas" || jsonObject(bags[1])["name"] != "Kiste" {
		t.Fatalf("bag order or rename lost: %v", snapshot.Body)
	}
	items := jsonList(jsonObject(bags[1]), "items")
	if jsonObject(items[0])["name"] != "Edding" || jsonObject(items[1])["name"] != "Batterien" {
		t.Fatalf("item order or rename lost: %v", snapshot.Body)
	}

	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", packingOperation(packingUUID(), "delete_item", 1, map[string]any{"item_id": firstItem})); result.Status != http.StatusOK {
		t.Fatalf("delete item: %d %v", result.Status, result.Body)
	}
	if result := h.do(http.MethodPost, "/api/v1/packing-list/operations", packingOperation(packingUUID(), "delete_bag", 1, map[string]any{"bag_id": secondBag})); result.Status != http.StatusOK {
		t.Fatalf("delete bag: %d %v", result.Status, result.Body)
	}
	remaining := h.do(http.MethodGet, "/api/v1/packing-list", nil)
	remainingBags := jsonList(remaining.Body, "bags")
	if len(remainingBags) != 1 || len(jsonList(jsonObject(remainingBags[0]), "items")) != 1 {
		t.Fatalf("delete operations left unexpected structure: %v", remaining.Body)
	}
}
