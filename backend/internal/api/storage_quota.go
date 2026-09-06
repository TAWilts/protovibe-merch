package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// checkStorageQuota rejects an upload before it reaches the file store. The
// replacement size is subtracted for updates such as exchanging an invoice,
// so a band at its limit can still replace a document with a smaller one.
func (s *Server) checkStorageQuota(c *gin.Context, incomingBytes, replacingBytes int64) bool {
	ctx := c.Request.Context()
	bandID := tenant.MustBandID(ctx)

	var band models.Band
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).
		Select("id", "storage_quota_bytes").First(&band, bandID).Error; err != nil {
		serverError(c, err)
		return false
	}
	quota := band.StorageQuotaBytes
	if quota <= 0 {
		settings, err := s.platformSettings(ctx)
		if err != nil {
			serverError(c, err)
			return false
		}
		quota = settings.DefaultStorageQuotaBytes
	}
	if quota <= 0 {
		return true
	}

	used, err := s.files.UsageBytes(ctx, bandID)
	if err != nil {
		serverError(c, err)
		return false
	}
	projected := used - replacingBytes + incomingBytes
	if projected < 0 {
		projected = incomingBytes
	}
	if projected > quota {
		fail(c, http.StatusInsufficientStorage, "storage_quota_exceeded",
			"the storage quota for this band would be exceeded")
		return false
	}
	return true
}
