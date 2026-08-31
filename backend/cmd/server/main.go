// Command server runs the Protovibe Merch Manager API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/api"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/scheduler"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg)

	database, err := db.Open(cfg)
	if err != nil {
		return err
	}
	if err := db.Migrate(database); err != nil {
		return err
	}

	apiServer, err := api.NewServer(cfg, database)
	if err != nil {
		return err
	}
	if err := apiServer.Bootstrap(context.Background()); err != nil {
		return err
	}

	jobs := scheduler.New(cfg, apiServer.Auth(), apiServer.Backups(),
		apiServer.Platform(), apiServer.PaymentQR())
	if err := jobs.Start(); err != nil {
		return err
	}
	defer jobs.Stop()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(apiServer),
		ReadHeaderTimeout: 10 * time.Second,
		// Uploads of invoices and product photos are allowed to be slow on a
		// phone at a gig, so the write timeout is generous rather than tight.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "environment", cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		slog.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	if sqlDB, err := database.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return nil
}

func setupLogging(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.IsDevelopment() {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler).With("service", "merch-api", "version", cfg.AppVersion))
}
