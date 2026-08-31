package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// MaxUploadBytes caps a single upload. It matches the original's 10 MB limit,
// which comfortably fits a scanned invoice or a phone photo.
const MaxUploadBytes = 10 << 20

func (s *Server) registerUploadRoutes(g *gin.RouterGroup) {
	managers := g.Group("", requireAuth(), requireBandRole(models.RoleManager))
	managers.POST("/purchases/:id/invoice", s.uploadPurchaseInvoice)
	managers.DELETE("/purchases/:id/invoice", s.deletePurchaseInvoice)
	managers.POST("/purchase-receipts/:receiptID/attachments", s.uploadReceiptAttachment)
	managers.DELETE("/purchase-receipts/:receiptID/attachments/:attachmentID", s.deleteReceiptAttachment)

	// Reading an attachment is a member's right; it is part of the history.
	members := g.Group("", requireAuth(), requireBandRole(models.RoleMember))
	members.GET("/purchases/:id/invoice", s.downloadPurchaseInvoice)
	members.GET("/purchase-receipts/:receiptID/attachments", s.listReceiptAttachments)
	members.GET("/purchase-receipts/:receiptID/attachments/:attachmentID", s.downloadReceiptAttachment)
}

// uploadedFile is one validated upload, ready to be written to the store.
type uploadedFile struct {
	Reader    io.Reader
	MediaType string
	Filename  string
	Size      int64
}

// readUpload validates a multipart upload before a single byte reaches the
// store: the size cap, and a media type from the accepted list.
//
// The declared content type is not trusted on its own — the extension has to
// agree with it, so a script cannot arrive labelled as a PDF.
func (s *Server) readUpload(c *gin.Context, field string) (*uploadedFile, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadBytes)

	header, err := c.FormFile(field)
	if err != nil {
		fail(c, http.StatusBadRequest, "missing_file", "no file was uploaded")
		return nil, false
	}
	if header.Size > MaxUploadBytes {
		fail(c, http.StatusRequestEntityTooLarge, "file_too_large", "the file exceeds 10 MB")
		return nil, false
	}

	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(header.Header.Get("Content-Type"), ";")[0]))
	extension, accepted := storage.ExtensionFor(mediaType)
	if !accepted {
		fail(c, http.StatusUnsupportedMediaType, "unsupported_type",
			"only PDF, JPEG, PNG and WebP files are accepted")
		return nil, false
	}
	if name := strings.ToLower(header.Filename); name != "" {
		if !strings.HasSuffix(name, extension) && !(extension == ".jpg" && strings.HasSuffix(name, ".jpeg")) {
			fail(c, http.StatusUnsupportedMediaType, "type_mismatch",
				"the file extension does not match its content type")
			return nil, false
		}
	}

	file, err := header.Open()
	if err != nil {
		serverError(c, err)
		return nil, false
	}
	c.Set("upload_closer", file)

	return &uploadedFile{
		Reader:    file,
		MediaType: mediaType,
		Filename:  sanitizeFilename(header.Filename),
		Size:      header.Size,
	}, true
}

// sanitizeFilename keeps the original name for display only. It never reaches
// the filesystem; the store generates its own opaque name.
func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, strings.TrimSpace(name))

	if len(cleaned) > 255 {
		cleaned = cleaned[:255]
	}
	if cleaned == "" {
		return "upload"
	}
	return cleaned
}

func (s *Server) uploadPurchaseInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var purchase models.Purchase
	if err := s.db.WithContext(ctx).First(&purchase, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "not_found", "no such purchase")
			return
		}
		serverError(c, err)
		return
	}

	upload, ok := s.readUpload(c, "file")
	if !ok {
		return
	}
	if closer, exists := c.Get("upload_closer"); exists {
		defer closer.(io.Closer).Close()
	}

	bandID := tenant.MustBandID(ctx)
	object, err := s.files.Put(ctx, bandID, storage.CategoryInvoice, upload.MediaType, upload.Reader)
	if err != nil {
		serverError(c, err)
		return
	}

	previous := purchase.InvoiceFilePath
	err = s.db.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", id).Updates(map[string]any{
		"invoice_file_path":         object.Key,
		"invoice_original_filename": upload.Filename,
		"invoice_size_bytes":        object.SizeBytes,
	}).Error
	if err != nil {
		// The row still points at the old file, so the new one is orphaned
		// and has to go rather than linger unreferenced.
		s.removeStoredFile(ctx, object.Key)
		serverError(c, err)
		return
	}
	// Replacing an invoice removes the one it superseded.
	s.removeStoredFile(ctx, previous)

	c.JSON(http.StatusCreated, gin.H{
		"original_filename": upload.Filename,
		"size_bytes":        object.SizeBytes,
	})
}

func (s *Server) downloadPurchaseInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var purchase models.Purchase
	if err := s.db.WithContext(ctx).First(&purchase, id).Error; err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such purchase")
		return
	}
	if purchase.InvoiceFilePath == "" {
		fail(c, http.StatusNotFound, "no_invoice", "this position has no invoice")
		return
	}
	s.serveStoredFile(c, purchase.InvoiceFilePath, purchase.InvoiceOriginalFilename)
}

func (s *Server) deletePurchaseInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var purchase models.Purchase
	if err := s.db.WithContext(ctx).First(&purchase, id).Error; err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such purchase")
		return
	}

	err := s.db.WithContext(ctx).Model(&models.Purchase{}).Where("id = ?", id).Updates(map[string]any{
		"invoice_file_path":         nil,
		"invoice_original_filename": "",
		"invoice_size_bytes":        0,
	}).Error
	if err != nil {
		serverError(c, err)
		return
	}
	s.removeStoredFile(ctx, purchase.InvoiceFilePath)
	c.Status(http.StatusNoContent)
}

type attachmentPayload struct {
	ID               int64  `json:"id"`
	OriginalFilename string `json:"original_filename"`
	SizeBytes        int64  `json:"size_bytes"`
}

func (s *Server) listReceiptAttachments(c *gin.Context) {
	receiptID := c.Param("receiptID")

	var rows []models.PurchaseReceiptAttachment
	err := s.db.WithContext(c.Request.Context()).
		Where("receipt_id = ?", receiptID).Order("id").Find(&rows).Error
	if err != nil {
		serverError(c, err)
		return
	}

	payload := make([]attachmentPayload, 0, len(rows))
	for _, row := range rows {
		payload = append(payload, attachmentPayload{
			ID: row.ID, OriginalFilename: row.OriginalFilename, SizeBytes: row.SizeBytes,
		})
	}
	c.JSON(http.StatusOK, gin.H{"attachments": payload})
}

func (s *Server) uploadReceiptAttachment(c *gin.Context) {
	receiptID := c.Param("receiptID")
	ctx := c.Request.Context()

	var exists int64
	if err := s.db.WithContext(ctx).Model(&models.Purchase{}).
		Where("receipt_id = ?", receiptID).Count(&exists).Error; err != nil {
		serverError(c, err)
		return
	}
	if exists == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such goods receipt")
		return
	}

	upload, ok := s.readUpload(c, "file")
	if !ok {
		return
	}
	if closer, exists := c.Get("upload_closer"); exists {
		defer closer.(io.Closer).Close()
	}

	object, err := s.files.Put(ctx, tenant.MustBandID(ctx), storage.CategoryInvoice, upload.MediaType, upload.Reader)
	if err != nil {
		serverError(c, err)
		return
	}

	state := stateFrom(c)
	attachment := &models.PurchaseReceiptAttachment{
		ReceiptID:        receiptID,
		FilePath:         object.Key,
		OriginalFilename: upload.Filename,
		SizeBytes:        object.SizeBytes,
	}
	attachment.CreatedByUserID = &state.User.ID
	attachment.CreatedByUsername = state.User.Username

	if err := s.db.WithContext(ctx).Create(attachment).Error; err != nil {
		s.removeStoredFile(ctx, object.Key)
		serverError(c, err)
		return
	}

	c.JSON(http.StatusCreated, attachmentPayload{
		ID: attachment.ID, OriginalFilename: attachment.OriginalFilename, SizeBytes: attachment.SizeBytes,
	})
}

func (s *Server) downloadReceiptAttachment(c *gin.Context) {
	attachment, ok := s.loadAttachment(c)
	if !ok {
		return
	}
	s.serveStoredFile(c, attachment.FilePath, attachment.OriginalFilename)
}

func (s *Server) deleteReceiptAttachment(c *gin.Context) {
	attachment, ok := s.loadAttachment(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	if err := s.db.WithContext(ctx).Delete(&models.PurchaseReceiptAttachment{}, attachment.ID).Error; err != nil {
		serverError(c, err)
		return
	}
	s.removeStoredFile(ctx, attachment.FilePath)
	c.Status(http.StatusNoContent)
}

// loadAttachment resolves the attachment and verifies it belongs to the
// receipt in the path, so a valid ID from another receipt cannot be read.
func (s *Server) loadAttachment(c *gin.Context) (*models.PurchaseReceiptAttachment, bool) {
	attachmentID, ok := parsePathID(c, "attachmentID")
	if !ok {
		return nil, false
	}
	receiptID := c.Param("receiptID")

	var attachment models.PurchaseReceiptAttachment
	err := s.db.WithContext(c.Request.Context()).
		Where("id = ? AND receipt_id = ?", attachmentID, receiptID).
		First(&attachment).Error
	if err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such attachment")
		return nil, false
	}
	return &attachment, true
}

// serveStoredFile streams a stored file back.
//
// It is always sent as an attachment with a fixed content type, so an uploaded
// file can never be rendered inline as active content in the app's origin.
func (s *Server) serveStoredFile(c *gin.Context, key, filename string) {
	reader, object, err := s.files.Open(c.Request.Context(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			fail(c, http.StatusNotFound, "file_missing", "the stored file is no longer available")
			return
		}
		serverError(c, err)
		return
	}
	defer reader.Close()

	if filename == "" {
		filename = "download"
	}
	c.Header("Content-Type", object.MediaType)
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(filename))
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, filename, time.Time{}, reader)
}

func urlEscape(value string) string {
	return url.PathEscape(value)
}
