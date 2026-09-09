package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func (h *harness) uploadFinanceAttachment(transactionID int64, filename, mediaType string, content []byte) response {
	h.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		h.t.Fatalf("create upload part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		h.t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		h.t.Fatalf("close upload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost,
		h.server.URL+"/api/v1/band-finances/"+itoa(transactionID)+"/attachments", &body)
	if err != nil {
		h.t.Fatalf("build upload request: %v", err)
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
		h.t.Fatalf("perform upload: %v", err)
	}
	defer res.Body.Close()
	out := response{Status: res.StatusCode, Body: map[string]any{}}
	_ = json.NewDecoder(res.Body).Decode(&out.Body)
	return out
}

func TestBandFinanceAttachmentsLifecycleAndRoles(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleMember)
	created := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "expense", "transaction_on": "2026-09-09",
		"category": "Equipment", "description": "Kabel", "amount_cents": 1299,
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("create transaction: %d %v", created.Status, created.Body)
	}
	transactionID := int64(created.Body["id"].(float64))

	first := h.uploadFinanceAttachment(transactionID, "rechnung.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF"))
	second := h.uploadFinanceAttachment(transactionID, "foto.png", "image/png", samplePNG(t, 16, 16))
	if first.Status != http.StatusCreated || second.Status != http.StatusCreated {
		t.Fatalf("members must upload multiple attachments: first=%d %v second=%d %v",
			first.Status, first.Body, second.Status, second.Body)
	}
	firstID := int64(first.Body["id"].(float64))
	list := h.do(http.MethodGet, "/api/v1/band-finances/"+itoa(transactionID)+"/attachments", nil)
	if list.Status != http.StatusOK || len(jsonList(list.Body, "attachments")) != 2 {
		t.Fatalf("attachments missing: %d %v", list.Status, list.Body)
	}
	ledger := h.do(http.MethodGet, "/api/v1/band-finances", nil)
	entry := jsonObject(jsonList(ledger.Body, "entries")[0])
	if got := len(jsonList(entry, "attachments")); got != 2 {
		t.Fatalf("ledger must include attachment metadata, got %d: %v", got, entry)
	}
	h.signInAs(band, models.RoleManager)
	if cancelled := h.do(http.MethodPost, "/api/v1/band-finances/"+itoa(transactionID)+"/cancel", nil); cancelled.Status != http.StatusNoContent {
		t.Fatalf("cancel transaction: %d %v", cancelled.Status, cancelled.Body)
	}
	h.signInAs(band, models.RoleMember)
	if afterCancel := h.do(http.MethodGet, "/api/v1/band-finances/"+itoa(transactionID)+"/attachments", nil); afterCancel.Status != http.StatusOK || len(jsonList(afterCancel.Body, "attachments")) != 2 {
		t.Fatalf("attachments must survive cancellation: %d %v", afterCancel.Status, afterCancel.Body)
	}

	status, body, disposition := h.download("/api/v1/band-finances/" + itoa(transactionID) + "/attachments/" + itoa(firstID))
	if status != http.StatusOK || !bytes.HasPrefix(body, []byte("%PDF")) || disposition == "" {
		t.Fatalf("download mismatch: status=%d disposition=%q body=%q", status, disposition, body)
	}
	if removed := h.do(http.MethodDelete, "/api/v1/band-finances/"+itoa(transactionID)+"/attachments/"+itoa(firstID), nil); removed.Status != http.StatusNoContent {
		t.Fatalf("delete attachment: %d %v", removed.Status, removed.Body)
	}

	var auditCount int64
	if err := h.db.Raw("SELECT COUNT(*) FROM audit_log WHERE band_id = ? AND action IN (?, ?)",
		band.ID, "band_transaction.attachment_added", "band_transaction.attachment_removed").Scan(&auditCount).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if auditCount != 3 {
		t.Fatalf("expected two upload and one deletion audit entries, got %d", auditCount)
	}

	h.signInAs(band, models.RoleSeller)
	if res := h.do(http.MethodGet, "/api/v1/band-finances/"+itoa(transactionID)+"/attachments", nil); res.Status != http.StatusForbidden {
		t.Fatalf("seller must not access finance attachments: %d %v", res.Status, res.Body)
	}
}

func TestBandFinanceAttachmentsAreTenantAndParentScoped(t *testing.T) {
	h := newHarness(t)
	bandA := h.makeBand()
	bandB := h.makeBand()
	h.signInAs(bandA, models.RoleMember)
	created := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-09",
		"category": "Gage", "description": "Gig", "amount_cents": 10000,
	})
	transactionID := int64(created.Body["id"].(float64))
	uploaded := h.uploadFinanceAttachment(transactionID, "beleg.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF"))
	attachmentID := int64(uploaded.Body["id"].(float64))

	other := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-09",
		"category": "Gage", "description": "Other", "amount_cents": 20000,
	})
	otherID := int64(other.Body["id"].(float64))
	if res := h.do(http.MethodGet, "/api/v1/band-finances/"+itoa(otherID)+"/attachments/"+itoa(attachmentID), nil); res.Status != http.StatusNotFound {
		t.Fatalf("attachment must be resolved with its parent: %d %v", res.Status, res.Body)
	}

	h.signInAs(bandB, models.RoleMember)
	if res := h.do(http.MethodGet, "/api/v1/band-finances/"+itoa(transactionID)+"/attachments", nil); res.Status != http.StatusNotFound {
		t.Fatalf("another band must only see not_found: %d %v", res.Status, res.Body)
	}
}
