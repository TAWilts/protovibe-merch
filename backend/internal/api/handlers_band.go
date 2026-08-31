package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// registerBandRoutes mounts the band-scoped surface. Everything under it needs
// a band in the tenant scope, which a platform account only ever gets through
// an approved support-access grant.
func (s *Server) registerBandRoutes(g *gin.RouterGroup) {
	b := g.Group("", requireAuth(), requireBandRole(models.RoleSeller))
	b.GET("/band", s.currentBand)
}

// bandContext is what the frontend needs to render the band shell: identity,
// which optional areas are switched on, and any maintenance notice aimed at
// this band specifically.
type bandContext struct {
	ID                 int64               `json:"id"`
	Slug               string              `json:"slug"`
	Name               string              `json:"name"`
	ContactEmail       string              `json:"contact_email"`
	FeatureFlags       featureFlagsPayload `json:"feature_flags"`
	MaintenanceMessage string              `json:"maintenance_message,omitempty"`
}

// featureFlagsPayload resolves the tri-state flags into plain booleans, so the
// client never has to know that "absent means enabled".
type featureFlagsPayload struct {
	Slideshow    bool `json:"slideshow"`
	BandFinances bool `json:"band_finances"`
	PaymentQR    bool `json:"payment_qr"`
	OfflineSales bool `json:"offline_sales"`
	CSVImport    bool `json:"csv_import"`
}

func (s *Server) currentBand(c *gin.Context) {
	ctx := c.Request.Context()
	bandID, err := tenant.BandID(ctx)
	if err != nil {
		forbidden(c, "no_band_scope", "this request has no band context")
		return
	}

	var band models.Band
	if err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).First(&band, bandID).Error; err != nil {
		serverError(c, err)
		return
	}

	c.JSON(http.StatusOK, bandContext{
		ID:           band.ID,
		Slug:         band.Slug,
		Name:         band.Name,
		ContactEmail: band.ContactEmail,
		FeatureFlags: featureFlagsPayload{
			Slideshow:    band.FeatureFlags.SlideshowEnabled(),
			BandFinances: band.FeatureFlags.BandFinancesEnabled(),
			PaymentQR:    band.FeatureFlags.PaymentQREnabled(),
			OfflineSales: band.FeatureFlags.OfflineSalesEnabled(),
			CSVImport:    band.FeatureFlags.CSVImportEnabled(),
		},
		MaintenanceMessage: band.MaintenanceMessage,
	})
}
