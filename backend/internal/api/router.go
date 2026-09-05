// Package api wires the HTTP surface together.
//
// Route paths are English (/api/v1/sales) while the user-facing German text
// lives entirely in the Vue frontend's i18n catalogue.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// New builds the router.
//
// The middleware order deliberately mirrors the four before_request guards of
// the Flask original: maintenance, session and CSRF, the platform-staff
// boundary, and the POS-mode restrictions.
func New(s *Server) *gin.Engine {
	if !s.cfg.IsDevelopment() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(), s.telemetryMiddleware(), s.metricsMiddleware())

	if len(s.cfg.TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(s.cfg.TrustedProxies)
	} else {
		_ = r.SetTrustedProxies(nil)
	}

	// Operational endpoints stay outside the guarded chain so an orchestrator
	// can probe the process even during maintenance.
	r.GET("/healthz", s.health)
	r.GET("/readyz", s.ready)
	// Scraped by the monitoring stack, not by the app itself.
	r.GET("/metrics", s.metricsHandler())

	// The guard chain is registered on the engine rather than on the route
	// group, so it also covers unmatched paths. A route added outside the
	// group by mistake can therefore never slip past the tenant boundary.
	for _, guard := range []gin.HandlerFunc{
		noStore(),
		s.authRateLimit(),
		s.resolveSession(),
		s.maintenanceGuard(),
		s.featureGuard(),
		s.csrfGuard(),
		s.platformBoundary(),
		posModeGuard(),
	} {
		r.Use(underPrefix(apiPrefix, guard))
	}

	api := r.Group(apiPrefix)

	api.GET("/version", s.version)
	s.registerPublicRegistrationRoutes(api)
	s.registerAuthRoutes(api)
	s.registerProfileRoutes(api)
	s.registerBandRoutes(api)
	s.registerCatalogueRoutes(api)
	s.registerSalesRoutes(api)
	s.registerPurchaseRoutes(api)
	s.registerUploadRoutes(api)
	s.registerReportRoutes(api)
	s.registerExportRoutes(api)
	s.registerPaymentQRRoutes(api)
	s.registerImportRoutes(api)
	s.registerPlatformRoutes(api)
	s.registerPlatformRegistrationRoutes(api)
	s.registerPlatformUserRoutes(api)
	s.registerPlatformOpsRoutes(api)
	s.registerSupportInboxRoutes(api)
	s.registerBackupRoutes(api)
	s.registerPhotoRoutes(api)
	s.registerBandAdminRoutes(api)

	// Unmatched paths must not slip past the guard chain with a bare 404. The
	// group's middleware only runs for registered routes, so an explicit
	// handler keeps the response shape and the tenant boundary consistent.
	r.NoRoute(func(c *gin.Context) {
		fail(c, http.StatusNotFound, "not_found", "no such endpoint")
	})

	return r
}

// health answers as soon as the process is up. It never touches the database,
// so a database outage does not make the container look dead to the
// orchestrator — that is what /readyz is for.
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": s.cfg.AppVersion})
}

// ready additionally verifies the database, which is what a load balancer
// should use before sending traffic.
func (s *Server) ready(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err == nil {
		err = sqlDB.PingContext(c.Request.Context())
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "detail": "database unreachable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": s.cfg.AppVersion})
}
