// Package db owns the GORM connection and the tenant-scoping callbacks that
// make the shared-database multi-tenancy safe.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tawilts/protovibe-merch/backend/internal/config"
)

// Open connects to MariaDB and configures the connection pool.
func Open(cfg *config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		// Money is integer cents and every timestamp is stored in UTC, so the
		// driver must never reinterpret values in a local zone.
		NowFunc:                func() time.Time { return time.Now().UTC() },
		TranslateError:         true,
		SkipDefaultTransaction: false,
		Logger:                 gormLogger(cfg),
	}

	gdb, err := gorm.Open(mysql.Open(cfg.DatabaseDSN), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("connect mariadb: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("sql handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLife)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping mariadb: %w", err)
	}

	// Registered here rather than at the call site so no code path can obtain
	// an unguarded handle to a band-scoped table.
	if err := RegisterTenantCallbacks(gdb); err != nil {
		return nil, fmt.Errorf("register tenant callbacks: %w", err)
	}

	return gdb, nil
}

func gormLogger(cfg *config.Config) logger.Interface {
	level := logger.Warn
	if cfg.IsDevelopment() {
		level = logger.Info
	}
	return logger.New(slogWriter{}, logger.Config{
		SlowThreshold:             400 * time.Millisecond,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      !cfg.IsDevelopment(),
		Colorful:                  false,
	})
}

// slogWriter funnels GORM's printf-style output into the structured logger so
// there is exactly one log format in production.
type slogWriter struct{}

func (slogWriter) Printf(format string, args ...any) {
	slog.Debug(fmt.Sprintf(format, args...), "source", "gorm")
}
