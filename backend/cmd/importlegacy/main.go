// Command importlegacy migrates one decrypted Flask/SQLite installation into
// the MariaDB application as a new band tenant.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/services/legacyimport"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "importlegacy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var opts legacyimport.Options
	var reportOut string

	flag.StringVar(&opts.OperationalDB, "operational-db", "", "path to a decrypted/plain merch.sqlite3 snapshot")
	flag.StringVar(&opts.UsersDB, "users-db", "", "path to a decrypted/plain users.sqlite3 snapshot")
	flag.StringVar(&opts.LegacyStorage, "legacy-storage", "", "directory containing invoices/ and variant-photos/")
	flag.StringVar(&opts.BandName, "band", "", "name of the new band tenant")
	flag.StringVar(&opts.BandSlug, "slug", "", "slug of the new band tenant (derived from -band when omitted)")
	flag.StringVar(&opts.ContactEmail, "contact-email", "", "optional band contact email")
	flag.BoolVar(&opts.DryRun, "dry-run", true, "validate and report without creating the band or copying files")
	flag.StringVar(&opts.CredentialsOut, "credentials-out", "", "JSON file for one-time setup codes; required when -dry-run=false")
	flag.StringVar(&reportOut, "report-out", "", "optional JSON file for the non-secret migration report")
	flag.Parse()

	for name, value := range map[string]string{
		"-operational-db": opts.OperationalDB,
		"-users-db":       opts.UsersDB,
		"-band":           opts.BandName,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	database, err := db.Open(cfg)
	if err != nil {
		return err
	}
	if sqlDB, err := database.DB(); err == nil {
		defer sqlDB.Close()
	}

	platformService := platform.NewService(database)
	var authService *auth.Service
	var fileStore storage.Store
	if !opts.DryRun {
		// A real import may run against a freshly deployed image. Bringing the
		// MariaDB schema current is safe and keeps the one-shot tool independent
		// of whether the HTTP server happened to start first.
		if err := db.Migrate(database); err != nil {
			return err
		}
		authService, err = auth.NewService(database, cfg)
		if err != nil {
			return err
		}
		localStore, err := storage.NewLocalStore(cfg.StorageRoot)
		if err != nil {
			return err
		}
		fileStore = localStore
	}

	service := legacyimport.New(database, authService, platformService, fileStore)
	report, err := service.Run(context.Background(), opts)
	if err != nil {
		return err
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if _, err := os.Stdout.Write(payload); err != nil {
		return err
	}
	if reportOut != "" {
		if err := writeReport(reportOut, payload); err != nil {
			if opts.DryRun {
				return err
			}
			fmt.Fprintf(os.Stderr, "warning: import committed, but migration report could not be written: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "migration report: %s\n", absoluteOrInput(reportOut))
		}
	}
	if !opts.DryRun {
		fmt.Fprintf(os.Stderr, "one-time setup codes: %s\n", report.CredentialFile)
		fmt.Fprintln(os.Stderr, "keep that file private; imported users must choose new passwords on first sign-in")
	}
	return nil
}

func writeReport(path string, payload []byte) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return err
	}
	return os.WriteFile(absolute, payload, 0o640)
}

func absoluteOrInput(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}
