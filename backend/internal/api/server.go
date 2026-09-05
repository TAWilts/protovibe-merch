package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/backup"
	"github.com/tawilts/protovibe-merch/backend/internal/services/balances"
	"github.com/tawilts/protovibe-merch/backend/internal/services/bandfinance"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/export"
	"github.com/tawilts/protovibe-merch/backend/internal/services/importer"
	"github.com/tawilts/protovibe-merch/backend/internal/services/paymentqr"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/services/purchases"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
	"github.com/tawilts/protovibe-merch/backend/internal/services/registration"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sales"
	"github.com/tawilts/protovibe-merch/backend/internal/services/updates"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Server holds everything the handlers need.
type Server struct {
	cfg   *config.Config
	db    *gorm.DB
	auth  *auth.Service
	audit *audit.Logger

	catalogue *catalogue.Service
	receipts  *receipt.Service
	sales     *sales.Service
	purchases *purchases.Service
	// balancesService is named for clarity: `balances` alone would shadow the
	// package inside the handlers.
	balancesService *balances.Service
	bandFinance     *bandfinance.Service
	exports         *export.Service
	paymentQR       *paymentqr.Service
	importer        *importer.Service
	updates         *updates.Service
	platform        *platform.Service
	registrations   *registration.Service
	backups         *backup.Service
	metrics         *metrics
	files           storage.Store

	// settingsCache avoids reading the single platform_settings row on every
	// request. It is refreshed on write and after a short TTL, so a change made
	// on another instance still lands quickly.
	settingsMu                sync.RWMutex
	settings                  *models.PlatformSettings
	settingsFetched           time.Time
	authLimiter               *requestLimiter
	registrationCreateLimiter *requestLimiter
	registrationAccessLimiter *requestLimiter
}

// settingsTTL is how long a cached copy of platform_settings is trusted.
const settingsTTL = 15 * time.Second

// NewServer builds the API server.
func NewServer(cfg *config.Config, database *gorm.DB) (*Server, error) {
	authService, err := auth.NewService(database, cfg)
	if err != nil {
		return nil, err
	}
	files, err := storage.NewLocalStore(cfg.StorageRoot)
	if err != nil {
		return nil, err
	}

	platformService := platform.NewService(database)
	return &Server{
		cfg:             cfg,
		db:              database,
		auth:            authService,
		audit:           audit.New(database),
		catalogue:       catalogue.NewService(database),
		receipts:        receipt.NewService(database),
		sales:           sales.NewService(database),
		purchases:       purchases.NewService(database),
		balancesService: balances.NewService(database),
		bandFinance:     bandfinance.NewService(database),
		exports:         export.NewService(database),
		paymentQR:       paymentqr.NewService(database),
		importer:        importer.NewService(database),
		updates: updates.NewService(updates.Config{
			Repository: cfg.UpdateCheckRepository,
			Token:      cfg.UpdateCheckToken,
			Timeout:    cfg.UpdateCheckTimeout,
			CacheTTL:   cfg.UpdateCheckCacheTTL,
			Version:    cfg.AppVersion,
		}),
		platform:      platformService,
		registrations: registration.NewService(database, authService, platformService, cfg.PublicBaseURL),
		backups: backup.NewService(database, backup.Config{
			DatabaseDSN:   cfg.DatabaseDSN,
			Root:          cfg.BackupRoot,
			StorageRoot:   cfg.StorageRoot,
			MysqldumpPath: cfg.MysqldumpPath,
			RetentionDays: cfg.BackupRetentionDays,
		}),
		metrics:                   newMetrics(),
		files:                     files,
		authLimiter:               newRequestLimiter(20, time.Minute),
		registrationCreateLimiter: newRequestLimiter(3, time.Hour),
		registrationAccessLimiter: newRequestLimiter(60, time.Hour),
	}, nil
}

// platformSettings returns the instance configuration, cached briefly.
func (s *Server) platformSettings(ctx context.Context) (*models.PlatformSettings, error) {
	s.settingsMu.RLock()
	cached, fetched := s.settings, s.settingsFetched
	s.settingsMu.RUnlock()

	if cached != nil && time.Since(fetched) < settingsTTL {
		return cached, nil
	}

	var settings models.PlatformSettings
	err := s.db.WithContext(tenant.WithCrossBandAccess(ctx)).First(&settings, 1).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The row is seeded by the migration; treat a missing one as
			// "nothing configured" rather than failing every request.
			settings = models.PlatformSettings{ID: 1}
		} else {
			return nil, err
		}
	}

	s.settingsMu.Lock()
	s.settings, s.settingsFetched = &settings, time.Now()
	s.settingsMu.Unlock()
	return &settings, nil
}

// invalidateSettings forces the next read to hit the database. Called after
// the admin center changes anything in platform_settings.
func (s *Server) invalidateSettings() {
	s.settingsMu.Lock()
	s.settings, s.settingsFetched = nil, time.Time{}
	s.settingsMu.Unlock()
}

// setSessionCookie writes the session cookie.
//
// It is HttpOnly so no script can read it, SameSite=Lax so a cross-site form
// post cannot ride it, and Secure in every deployment that is not explicitly
// local development.
func (s *Server) setSessionCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		Secure:   s.cfg.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// setCSRFCookie stores the CSRF token in a cookie the frontend can read.
//
// It is deliberately not HttpOnly: the double-submit scheme needs JavaScript to
// echo the value back in a header, and a cross-origin attacker can do neither —
// the same-origin policy hides the cookie from them, and a browser will not let
// them set a custom header on a cross-site request. Keeping it only in memory
// broke every write after a page reload.
func (s *Server) setCSRFCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		Secure:   s.cfg.CookieSecure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCSRFCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   s.cfg.CookieSecure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(c *gin.Context) {
	s.clearCSRFCookie(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   s.cfg.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Bootstrap prepares an empty instance: the single platform settings row and,
// when configured, the very first system administrator.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := auth.EnsurePlatformSettings(ctx, s.db); err != nil {
		return err
	}
	return s.auth.EnsureBootstrapAdmin(ctx, s.cfg.BootstrapAdminUsername, s.cfg.BootstrapAdminPassword)
}

// removeStoredFile deletes an uploaded file after its database row is gone.
//
// A failure here leaves an orphaned file rather than failing the request the
// user asked for: the booking is already correct, and an unreferenced file
// costs disk space rather than correctness.
func (s *Server) removeStoredFile(ctx context.Context, key string) {
	if key == "" {
		return
	}
	if err := s.files.Delete(ctx, key); err != nil {
		slog.Error("could not remove stored file", "error", err, "key", key)
	}
}

// Auth exposes the auth service so the scheduler can reuse it.
func (s *Server) Auth() *auth.Service { return s.auth }

// Backups exposes the backup service to the scheduler.
func (s *Server) Backups() *backup.Service { return s.backups }

// Platform exposes the control-plane service to the scheduler.
func (s *Server) Platform() *platform.Service { return s.platform }

// Registrations exposes the public onboarding service to housekeeping.
func (s *Server) Registrations() *registration.Service { return s.registrations }

// PaymentQR exposes the payment-code service to the scheduler.
func (s *Server) PaymentQR() *paymentqr.Service { return s.paymentQR }

// BandFinance exposes recurring ledger materialisation to the scheduler.
func (s *Server) BandFinance() *bandfinance.Service { return s.bandFinance }
