package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/photos"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerPhotoRoutes(g *gin.RouterGroup) {
	// Everyone in the band may look at the pictures; only managers curate them.
	viewers := g.Group("", requireAuth(), requireBandRole(models.RoleSeller))
	viewers.GET("/photos", s.listPhotos)
	viewers.GET("/photos/:id/file", s.servePhoto)
	viewers.GET("/slideshow", s.slideshowPayload)

	managers := g.Group("", requireAuth(), requireBandRole(models.RoleManager))
	managers.POST("/photos", s.uploadPhoto)
	managers.PATCH("/photos/:id", s.updatePhoto)
	managers.DELETE("/photos/:id", s.deletePhoto)
	managers.PATCH("/slideshow/settings", s.updateSlideshowSettings)
}

// photoPayload is one picture. Variant-bound and free-standing pictures share
// one shape so the gallery can show them together.
type photoPayload struct {
	ID                 int64  `json:"id"`
	VariantID          *int64 `json:"variant_id"`
	ArticleName        string `json:"article_name"`
	VariantLabel       string `json:"variant_label"`
	OriginalFilename   string `json:"original_filename"`
	Position           int    `json:"position"`
	IncludeInSlideshow bool   `json:"include_in_slideshow"`
	ShowPrice          bool   `json:"show_price"`
	SalePriceCents     int64  `json:"sale_price_cents"`
	SizeBytes          int64  `json:"size_bytes"`
	CreatedByUsername  string `json:"created_by_username"`
}

// buildGallery assembles the band's whole gallery: product pictures and the
// free-standing display pictures together, as the original's slideshow page
// showed them.
func (s *Server) buildGallery(ctx context.Context) ([]photoPayload, error) {
	var variantPhotos []models.VariantPhoto
	if err := s.db.WithContext(ctx).Order("position, id").Find(&variantPhotos).Error; err != nil {
		return nil, err
	}
	var extraPhotos []models.SlideshowExtraPhoto
	if err := s.db.WithContext(ctx).Order("position, id").Find(&extraPhotos).Error; err != nil {
		return nil, err
	}

	labels, err := s.catalogue.VariantLabels(ctx)
	if err != nil {
		return nil, err
	}
	prices, err := s.variantPrices(ctx)
	if err != nil {
		return nil, err
	}

	payload := make([]photoPayload, 0, len(variantPhotos)+len(extraPhotos))
	for _, photo := range variantPhotos {
		variantID := photo.VariantID
		payload = append(payload, photoPayload{
			ID: photo.ID, VariantID: &variantID,
			ArticleName:        labels[variantID].ArticleName,
			VariantLabel:       labels[variantID].VariantLabel,
			OriginalFilename:   photo.OriginalFilename,
			Position:           photo.Position,
			IncludeInSlideshow: photo.IncludeInSlideshow,
			ShowPrice:          photo.ShowPrice,
			SalePriceCents:     prices[variantID],
			SizeBytes:          photo.SizeBytes,
			CreatedByUsername:  photo.CreatedByUsername,
		})
	}
	for _, photo := range extraPhotos {
		payload = append(payload, photoPayload{
			// A negative ID keeps the two kinds distinguishable in one list
			// without inventing a second identifier scheme in the client.
			ID:                 -photo.ID,
			OriginalFilename:   photo.OriginalFilename,
			Position:           photo.Position,
			IncludeInSlideshow: photo.IncludeInSlideshow,
			ShowPrice:          photo.ShowPrice,
			SizeBytes:          photo.SizeBytes,
			CreatedByUsername:  photo.CreatedByUsername,
		})
	}
	return payload, nil
}

func (s *Server) listPhotos(c *gin.Context) {
	gallery, err := s.buildGallery(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"photos": gallery})
}

func (s *Server) variantPrices(ctx context.Context) (map[int64]int64, error) {
	type row struct {
		ID             int64
		SalePriceCents int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select("id, sale_price_cents").Scan(&rows).Error; err != nil {
		return nil, err
	}
	prices := make(map[int64]int64, len(rows))
	for _, entry := range rows {
		prices[entry.ID] = entry.SalePriceCents
	}
	return prices, nil
}

// uploadPhoto stores a picture, optionally bound to a variant.
//
// Every upload is re-encoded, which caps its size, strips camera metadata and
// guarantees the stored bytes really are an image.
func (s *Server) uploadPhoto(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, photos.MaxUploadBytes+1024)

	header, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "missing_file", "no file was uploaded")
		return
	}
	file, err := header.Open()
	if err != nil {
		serverError(c, err)
		return
	}
	defer file.Close()

	normalized, err := photos.Normalize(file)
	if err != nil {
		s.reportPhotoError(c, err)
		return
	}

	ctx := c.Request.Context()
	bandID := tenant.MustBandID(ctx)
	category := storage.CategorySlideshow

	var variantID *int64
	if raw := c.PostForm("variant_id"); raw != "" && raw != "0" {
		parsed, err := parseInt64(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid variant identifier")
			return
		}
		var variant models.Variant
		if err := s.db.WithContext(ctx).First(&variant, parsed).Error; err != nil {
			fail(c, http.StatusBadRequest, "unknown_variant", "no such variant")
			return
		}
		variantID = &parsed
		category = storage.CategoryVariantPhoto
	}

	object, err := s.files.Put(ctx, bandID, category, "image/jpeg", bytes.NewReader(normalized.Data))
	if err != nil {
		serverError(c, err)
		return
	}

	state := stateFrom(c)
	filename := sanitizeFilename(header.Filename)

	if variantID != nil {
		photo := &models.VariantPhoto{
			VariantID: *variantID, FilePath: object.Key, OriginalFilename: filename,
			IncludeInSlideshow: true, ShowPrice: true,
			SizeBytes: object.SizeBytes, CreatedAt: time.Now().UTC(),
		}
		photo.CreatedByUserID = &state.User.ID
		photo.CreatedByUsername = state.User.Username
		if err := s.db.WithContext(ctx).Create(photo).Error; err != nil {
			s.removeStoredFile(ctx, object.Key)
			serverError(c, err)
			return
		}
		s.audit.Log(ctx, actorFrom(c), audit.Entry{
			Action: "photo.uploaded", EntityType: "variant_photo", EntityID: &photo.ID,
		})
		c.JSON(http.StatusCreated, gin.H{"id": photo.ID, "variant_id": *variantID})
		return
	}

	photo := &models.SlideshowExtraPhoto{
		FilePath: object.Key, OriginalFilename: filename,
		IncludeInSlideshow: true, ShowPrice: true,
		SizeBytes: object.SizeBytes, CreatedAt: time.Now().UTC(),
	}
	photo.CreatedByUserID = &state.User.ID
	photo.CreatedByUsername = state.User.Username
	if err := s.db.WithContext(ctx).Create(photo).Error; err != nil {
		s.removeStoredFile(ctx, object.Key)
		serverError(c, err)
		return
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "photo.uploaded", EntityType: "slideshow_photo", EntityID: &photo.ID,
	})
	c.JSON(http.StatusCreated, gin.H{"id": -photo.ID})
}

type updatePhotoRequest struct {
	IncludeInSlideshow *bool `json:"include_in_slideshow"`
	ShowPrice          *bool `json:"show_price"`
	Position           *int  `json:"position"`
}

func (s *Server) updatePhoto(c *gin.Context) {
	id, ok := signedPathID(c)
	if !ok {
		return
	}
	var req updatePhotoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	updates := map[string]any{}
	if req.IncludeInSlideshow != nil {
		updates["include_in_slideshow"] = *req.IncludeInSlideshow
	}
	if req.ShowPrice != nil {
		updates["show_price"] = *req.ShowPrice
	}
	if req.Position != nil {
		updates["position"] = *req.Position
	}
	if len(updates) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	ctx := c.Request.Context()
	var result *gorm.DB
	if id > 0 {
		result = s.db.WithContext(ctx).Model(&models.VariantPhoto{}).Where("id = ?", id).Updates(updates)
	} else {
		result = s.db.WithContext(ctx).Model(&models.SlideshowExtraPhoto{}).Where("id = ?", -id).Updates(updates)
	}
	if result.Error != nil {
		serverError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such photo")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) deletePhoto(c *gin.Context) {
	id, ok := signedPathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var path string
	if id > 0 {
		var photo models.VariantPhoto
		if err := s.db.WithContext(ctx).First(&photo, id).Error; err != nil {
			fail(c, http.StatusNotFound, "not_found", "no such photo")
			return
		}
		path = photo.FilePath
		if err := s.db.WithContext(ctx).Delete(&models.VariantPhoto{}, id).Error; err != nil {
			serverError(c, err)
			return
		}
	} else {
		var photo models.SlideshowExtraPhoto
		if err := s.db.WithContext(ctx).First(&photo, -id).Error; err != nil {
			fail(c, http.StatusNotFound, "not_found", "no such photo")
			return
		}
		path = photo.FilePath
		if err := s.db.WithContext(ctx).Delete(&models.SlideshowExtraPhoto{}, -id).Error; err != nil {
			serverError(c, err)
			return
		}
	}
	// The file goes only after the row, so a failed delete never leaves a
	// gallery entry pointing at nothing.
	s.removeStoredFile(ctx, path)

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "photo.deleted", EntityType: "photo", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) servePhoto(c *gin.Context) {
	id, ok := signedPathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var path, filename string
	if id > 0 {
		var photo models.VariantPhoto
		if err := s.db.WithContext(ctx).First(&photo, id).Error; err != nil {
			fail(c, http.StatusNotFound, "not_found", "no such photo")
			return
		}
		path, filename = photo.FilePath, photo.OriginalFilename
	} else {
		var photo models.SlideshowExtraPhoto
		if err := s.db.WithContext(ctx).First(&photo, -id).Error; err != nil {
			fail(c, http.StatusNotFound, "not_found", "no such photo")
			return
		}
		path, filename = photo.FilePath, photo.OriginalFilename
	}

	reader, object, err := s.files.Open(ctx, path)
	if err != nil {
		fail(c, http.StatusNotFound, "file_missing", "the stored file is no longer available")
		return
	}
	defer reader.Close()

	// Pictures render inline — that is the point of a slideshow — but the
	// content type is fixed and sniffing is off, so an upload can never be
	// served as something executable.
	c.Header("Content-Type", object.MediaType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	http.ServeContent(c.Writer, c.Request, filename, time.Time{}, reader)
}

// slideshowPayload is the shop display: the selected pictures plus the band's
// display preference.
func (s *Server) slideshowPayload(c *gin.Context) {
	ctx := c.Request.Context()

	// A missing settings row resolves to the safe default of showing prices,
	// rather than being an error.
	collagePrices := true
	var settings models.SlideshowSettings
	err := s.db.WithContext(ctx).First(&settings).Error
	if err == nil {
		collagePrices = settings.CollageShowPrices
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		serverError(c, err)
		return
	}

	gallery, err := s.buildGallery(ctx)
	if err != nil {
		serverError(c, err)
		return
	}

	selected := make([]photoPayload, 0, len(gallery))
	for _, photo := range gallery {
		if photo.IncludeInSlideshow {
			selected = append(selected, photo)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"photos":              selected,
		"collage_show_prices": collagePrices,
	})
}

type slideshowSettingsRequest struct {
	CollageShowPrices *bool `json:"collage_show_prices"`
}

func (s *Server) updateSlideshowSettings(c *gin.Context) {
	var req slideshowSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.CollageShowPrices == nil {
		c.Status(http.StatusNoContent)
		return
	}

	ctx := c.Request.Context()
	var settings models.SlideshowSettings
	err := s.db.WithContext(ctx).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings = models.SlideshowSettings{
			CollageShowPrices: *req.CollageShowPrices, UpdatedAt: time.Now().UTC(),
		}
		if err := s.db.WithContext(ctx).Create(&settings).Error; err != nil {
			serverError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}

	err = s.db.WithContext(ctx).Model(&models.SlideshowSettings{}).Where("id = ?", settings.ID).
		Updates(map[string]any{
			"collage_show_prices": *req.CollageShowPrices,
			"updated_at":          time.Now().UTC(),
		}).Error
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) reportPhotoError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, photos.ErrTooLarge):
		fail(c, http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
	case errors.Is(err, photos.ErrTooManyPixels):
		fail(c, http.StatusBadRequest, "too_many_pixels", err.Error())
	case errors.Is(err, photos.ErrNotAnImage):
		fail(c, http.StatusUnsupportedMediaType, "not_an_image", err.Error())
	default:
		serverError(c, err)
	}
}

// signedPathID reads a photo identifier. A negative value addresses a
// free-standing display picture, a positive one a product picture.
func signedPathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		fail(c, http.StatusBadRequest, "invalid_id", "invalid identifier")
		return 0, false
	}
	return id, true
}

func parseInt64(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}
