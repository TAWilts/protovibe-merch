package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/packing"
	"github.com/tawilts/protovibe-merch/backend/internal/services/photos"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

func (s *Server) registerPackingRoutes(g *gin.RouterGroup) {
	users := g.Group("/packing-list", requireAuth(), requireBandRole(models.RoleSeller))
	users.GET("", s.packingSnapshot)
	users.GET("/events", s.packingEvents)
	users.GET("/photos/:id/file", s.packingPhoto)
	users.POST("/operations", s.applyPackingOperation)

	members := g.Group("/packing-list", requireAuth(), requireBandRole(models.RoleMember))
	members.POST("/photos", s.uploadPackingPhoto)
}

func (s *Server) packingSnapshot(c *gin.Context) {
	snapshot, err := s.packing.Snapshot(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (s *Server) applyPackingOperation(c *gin.Context) {
	var operation packing.Operation
	if err := c.ShouldBindJSON(&operation); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state := stateFrom(c)
	if operation.IsManagement() {
		if state.Session.POSMode {
			forbidden(c, "pos_mode_restricted", "packing-list management is disabled while POS mode is active")
			return
		}
		if !state.User.Role.AtLeast(models.RoleMember) && state.Grant == nil {
			forbidden(c, "insufficient_role", "this action requires the member role")
			return
		}
	}

	result, err := s.packing.Apply(c.Request.Context(), operation, packing.Actor{UserID: state.User.ID, Username: state.User.Username})
	if err != nil {
		s.reportPackingError(c, err)
		return
	}
	for _, key := range result.DeletedFileKeys {
		s.removeStoredFile(c.Request.Context(), key)
	}

	if operation.IsManagement() && !result.Replayed {
		action := audit.ActionPackingChanged
		if operation.Type == packing.OpReset {
			action = audit.ActionPackingReset
		}
		if operation.Type == packing.OpDeletePhoto {
			action = audit.ActionPackingPhotoChanged
		}
		s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
			Action: action, EntityType: "packing_list",
			Details: map[string]any{"operation": operation.Type, "bag_id": operation.BagID, "item_id": operation.ItemID, "photo_id": operation.PhotoID, "revision": result.Revision},
		})
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) uploadPackingPhoto(c *gin.Context) {
	state := stateFrom(c)
	if state.Session.POSMode {
		forbidden(c, "pos_mode_restricted", "packing-list management is disabled while POS mode is active")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, photos.MaxUploadBytes+16*1024)
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
	createdAt, err := time.Parse(time.RFC3339Nano, c.PostForm("client_created_at"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "client_created_at must be an RFC3339 timestamp")
		return
	}
	input := packing.PhotoInput{
		EventID: c.PostForm("event_id"), DeviceID: c.PostForm("device_id"), ClientCreatedAt: createdAt,
		PhotoID: c.PostForm("photo_id"), BagID: c.PostForm("bag_id"), ItemID: c.PostForm("item_id"),
		OriginalFilename: sanitizeFilename(header.Filename), Data: normalized.Data,
	}
	if replay, found, err := s.packing.ReplayPhoto(c.Request.Context(), input); err != nil {
		s.reportPackingError(c, err)
		return
	} else if found {
		c.JSON(http.StatusCreated, replay)
		return
	}
	if !s.checkStorageQuota(c, int64(len(normalized.Data)), 0) {
		return
	}
	result, err := s.packing.UploadPhoto(c.Request.Context(), input, packing.Actor{UserID: state.User.ID, Username: state.User.Username}, s.files)
	if err != nil {
		s.reportPackingError(c, err)
		return
	}
	if !result.Replayed {
		s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
			Action: audit.ActionPackingPhotoChanged, EntityType: "packing_photo",
			Details: map[string]any{"photo_id": input.PhotoID, "bag_id": input.BagID, "item_id": input.ItemID, "revision": result.Revision},
		})
	}
	c.JSON(http.StatusCreated, result)
}

func (s *Server) packingPhoto(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var photo models.PackingPhoto
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&photo).Error; err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such packing photo")
		return
	}
	reader, object, err := s.files.Open(c.Request.Context(), photo.FilePath)
	if err != nil {
		fail(c, http.StatusNotFound, "file_missing", "the stored file is no longer available")
		return
	}
	defer reader.Close()
	c.Header("Content-Type", object.MediaType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	http.ServeContent(c.Writer, c.Request, photo.OriginalFilename, time.Time{}, reader)
}

func (s *Server) packingEvents(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		fail(c, http.StatusNotImplemented, "streaming_unavailable", "streaming is unavailable")
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	bandID := tenant.MustBandID(c.Request.Context())
	changes, unsubscribe := s.packing.Hub().Subscribe(bandID)
	defer unsubscribe()
	_, _ = fmt.Fprint(c.Writer, ": connected\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case revision := <-changes:
			payload, _ := json.Marshal(map[string]int64{"revision": revision})
			_, _ = fmt.Fprintf(c.Writer, "event: revision\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(c.Writer, ": heartbeat\n\n")
			flusher.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}

func (s *Server) reportPackingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, packing.ErrGenerationChanged):
		fail(c, http.StatusConflict, "packing_generation_conflict", err.Error())
	case errors.Is(err, packing.ErrSyncConflict):
		fail(c, http.StatusConflict, "sync_conflict", err.Error())
	case errors.Is(err, packing.ErrTargetNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		fail(c, http.StatusNotFound, "packing_target_deleted", err.Error())
	case errors.Is(err, packing.ErrInvalidOperation), errors.Is(err, packing.ErrInvalidID), errors.Is(err, packing.ErrInvalidName), errors.Is(err, packing.ErrInvalidStatus):
		fail(c, http.StatusBadRequest, "invalid_packing_operation", err.Error())
	default:
		serverError(c, err)
	}
}
