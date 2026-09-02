// Package scheduler runs the recurring maintenance the instance needs.
//
// Everything here is idempotent and safe to skip: a missed run costs a little
// disk or leaves a lapsed grant marked active for a few minutes longer, never
// correctness.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/services/backup"
	"github.com/tawilts/protovibe-merch/backend/internal/services/paymentqr"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/services/registration"
)

// Scheduler owns the cron jobs.
type Scheduler struct {
	cron          *cron.Cron
	cfg           *config.Config
	auth          *auth.Service
	backups       *backup.Service
	platform      *platform.Service
	registrations *registration.Service
	paymentQR     *paymentqr.Service
}

// New builds the scheduler.
func New(
	cfg *config.Config,
	authService *auth.Service,
	backups *backup.Service,
	platformService *platform.Service,
	registrationService *registration.Service,
	paymentQR *paymentqr.Service,
) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		cfg:           cfg,
		auth:          authService,
		backups:       backups,
		platform:      platformService,
		registrations: registrationService,
		paymentQR:     paymentQR,
	}
}

// Start registers the jobs and begins running them.
func (s *Scheduler) Start() error {
	// Housekeeping runs often because its jobs are cheap and their delay is
	// user-visible: an expired support grant should close within minutes.
	if _, err := s.cron.AddFunc("*/5 * * * *", s.housekeeping); err != nil {
		return err
	}

	if s.cfg.BackupCronFull != "" {
		if _, err := s.cron.AddFunc(s.cfg.BackupCronFull, s.fullBackup); err != nil {
			return err
		}
	}
	if s.cfg.BackupCronPerBand != "" {
		if _, err := s.cron.AddFunc(s.cfg.BackupCronPerBand, s.perBandBackups); err != nil {
			return err
		}
	}

	s.cron.Start()
	slog.Info("scheduler started",
		"full_backup", s.cfg.BackupCronFull, "per_band_backup", s.cfg.BackupCronPerBand)
	return nil
}

// Stop waits for running jobs to finish.
func (s *Scheduler) Stop() {
	<-s.cron.Stop().Done()
}

// housekeeping closes lapsed grants and clears expired short-lived rows.
func (s *Scheduler) housekeeping() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if expired, err := s.platform.ExpireLapsed(ctx); err != nil {
		slog.Error("could not expire support grants", "error", err)
	} else if expired > 0 {
		slog.Info("support grants expired", "count", expired)
	}

	if err := s.auth.PurgeExpired(ctx); err != nil {
		slog.Error("could not purge expired sessions", "error", err)
	}
	if expired, err := s.registrations.Expire(ctx); err != nil {
		slog.Error("could not expire registration requests", "error", err)
	} else if expired > 0 {
		slog.Info("registration requests expired", "count", expired)
	}
	// A day's grace before removing consumed or abandoned payment codes keeps
	// them available for a support question about a receipt.
	if err := s.paymentQR.PurgeExpired(ctx, 24*time.Hour); err != nil {
		slog.Error("could not purge payment codes", "error", err)
	}
	if removed, err := s.backups.Prune(ctx); err != nil {
		slog.Error("could not prune backups", "error", err)
	} else if removed > 0 {
		slog.Info("backups pruned", "removed", removed)
	}
}

func (s *Scheduler) fullBackup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if _, err := s.backups.Run(ctx, nil, "scheduled", backup.Actor{Username: "scheduler"}); err != nil {
		slog.Error("scheduled full backup failed", "error", err)
		return
	}
	slog.Info("scheduled full backup finished")
}

// perBandBackups dumps each band separately, which is what makes restoring a
// single band possible without touching the others.
func (s *Scheduler) perBandBackups() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	bands, err := s.platform.ListBands(ctx, false)
	if err != nil {
		slog.Error("could not list bands for backup", "error", err)
		return
	}

	for _, band := range bands {
		id := band.ID
		if _, err := s.backups.Run(ctx, &id, "scheduled", backup.Actor{Username: "scheduler"}); err != nil {
			// One band failing must not stop the rest.
			slog.Error("scheduled band backup failed", "error", err, "band_id", id)
		}
	}
	slog.Info("scheduled per-band backups finished", "bands", len(bands))
}
