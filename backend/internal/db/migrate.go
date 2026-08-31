package db

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/migrations"
)

// Migrate applies every pending schema migration. It runs at startup so a
// deployment never serves traffic against an outdated schema.
func Migrate(gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("sql handle: %w", err)
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	driver, err := migratemysql.WithInstance(sqlDB, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "mariadb", driver)
	if err != nil {
		return fmt.Errorf("migrator: %w", err)
	}

	before, dirty, _ := m.Version()
	if dirty {
		return fmt.Errorf("schema version %d is dirty; resolve it manually before starting", before)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	after, _, _ := m.Version()
	if after != before {
		slog.Info("schema migrated", "from", before, "to", after)
	} else {
		slog.Info("schema up to date", "version", after)
	}
	return nil
}
