// Package backup produces the dumps the admin center offers.
//
// A backup is only useful if it can be restored, so each run writes a
// self-contained directory: the SQL dump plus the band's uploaded files. A
// per-band dump is what makes "restore this one band" possible without
// rolling back everybody else.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// Errors from the backup service.
var (
	ErrRunNotFound = errors.New("backup: no such backup run")
	ErrDumpFailed  = errors.New("backup: the database dump failed")
	ErrUnsafePath  = errors.New("backup: run path is outside the backup root")
)

// bandScopedTables are dumped with a band_id filter for a per-band backup.
// The control-plane tables are deliberately absent: restoring one band must
// never rewrite another band's accounts or the instance settings.
var bandScopedTables = []string{
	"articles", "option_groups", "option_values", "variants",
	"variant_photos", "slideshow_extra_photos", "slideshow_settings",
	"sales", "sale_events", "sale_event_state", "sync_events",
	"payment_qr_settings", "payment_qr_intents",
	"purchases", "purchase_receipt_attachments",
	"band_transactions", "band_transaction_attachments",
	"recurring_band_transactions", "recurring_band_transaction_runs",
	"admin_messages",
}

// Config is what the service needs from the environment.
type Config struct {
	DatabaseDSN   string
	Root          string
	StorageRoot   string
	MysqldumpPath string
	RetentionDays int
}

// Service runs and tracks backups.
type Service struct {
	db  *gorm.DB
	cfg Config
}

// NewService builds the backup service.
func NewService(database *gorm.DB, cfg Config) *Service {
	return &Service{db: database, cfg: cfg}
}

func (s *Service) crossBand(ctx context.Context) *gorm.DB {
	return s.db.WithContext(tenant.WithCrossBandAccess(ctx))
}

// Actor is who triggered a manual run.
type Actor struct {
	UserID   int64
	Username string
}

// Run performs one backup and records it.
//
// A nil bandID means the whole instance. The run row is written before the
// dump starts, so a crash mid-dump leaves a visible "running" entry rather
// than silence.
func (s *Service) Run(ctx context.Context, bandID *int64, trigger string, actor Actor) (*models.BackupRun, error) {
	started := time.Now().UTC()
	run := &models.BackupRun{
		BandID:            bandID,
		Status:            models.BackupStatusRunning,
		Trigger:           trigger,
		StartedAt:         started,
		StartedByUsername: actor.Username,
	}
	if actor.UserID != 0 {
		run.StartedByUserID = &actor.UserID
	}
	if err := s.crossBand(ctx).Create(run).Error; err != nil {
		return nil, err
	}

	directory := filepath.Join(s.cfg.Root, s.directoryName(bandID, started))
	size, err := s.perform(ctx, bandID, directory)

	finished := time.Now().UTC()
	updates := map[string]any{"finished_at": finished, "path": directory, "size_bytes": size}
	if err != nil {
		updates["status"] = models.BackupStatusFailed
		updates["error"] = err.Error()
		// The failed directory is removed so a half-written dump can never be
		// mistaken for a restorable one.
		_ = os.RemoveAll(directory)
	} else {
		updates["status"] = models.BackupStatusSucceeded
	}
	if updateErr := s.crossBand(ctx).Model(&models.BackupRun{}).
		Where("id = ?", run.ID).Updates(updates).Error; updateErr != nil {
		return nil, updateErr
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, run.ID)
}

func (s *Service) directoryName(bandID *int64, at time.Time) string {
	stamp := at.Format("2006-01-02_15-04-05")
	if bandID == nil {
		return filepath.Join("full", stamp)
	}
	return filepath.Join(fmt.Sprintf("band-%d", *bandID), stamp)
}

// perform writes the dump and the file store copy.
func (s *Service) perform(ctx context.Context, bandID *int64, directory string) (int64, error) {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return 0, err
	}

	dumpPath := filepath.Join(directory, "dump.sql")
	if err := s.dump(ctx, bandID, dumpPath); err != nil {
		return 0, err
	}

	if bandID != nil && s.cfg.StorageRoot != "" {
		source := filepath.Join(s.cfg.StorageRoot, fmt.Sprintf("band-%d", *bandID))
		if err := copyTree(source, filepath.Join(directory, "files")); err != nil {
			return 0, err
		}
	}
	return directorySize(directory)
}

// dump shells out to mariadb-dump.
//
// The credentials go in via the environment rather than the command line, so
// they never appear in the process list of a shared host.
func (s *Service) dump(ctx context.Context, bandID *int64, target string) error {
	settings, err := parseDSN(s.cfg.DatabaseDSN)
	if err != nil {
		return err
	}

	args := []string{
		"--host=" + settings.host,
		"--port=" + settings.port,
		"--user=" + settings.user,
		"--single-transaction",
		"--quick",
		"--default-character-set=utf8mb4",
	}
	if bandID == nil {
		args = append(args, "--databases", settings.database)
	} else {
		// A per-band dump is data only: the schema belongs to the migrations,
		// and a restore must not recreate tables from an older shape.
		args = append(args, "--no-create-info", "--skip-add-drop-table",
			"--where=band_id="+fmt.Sprint(*bandID), settings.database)
		args = append(args, bandScopedTables...)
	}

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	command := exec.CommandContext(ctx, s.cfg.MysqldumpPath, args...)
	command.Env = append(os.Environ(), "MYSQL_PWD="+settings.password)
	command.Stdout = file

	var stderr strings.Builder
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: %v: %s", ErrDumpFailed, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Get loads one run.
func (s *Service) Get(ctx context.Context, id int64) (*models.BackupRun, error) {
	var run models.BackupRun
	if err := s.crossBand(ctx).First(&run, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return &run, nil
}

// List returns the run history, newest first.
func (s *Service) List(ctx context.Context, bandID *int64) ([]models.BackupRun, error) {
	query := s.crossBand(ctx).Model(&models.BackupRun{}).Order("started_at DESC, id DESC").Limit(200)
	if bandID != nil {
		query = query.Where("band_id = ?", *bandID)
	}

	var runs []models.BackupRun
	if err := query.Find(&runs).Error; err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []models.BackupRun{}
	}
	return runs, nil
}

// Prune deletes runs older than the retention window, on disk and in the
// record. It reports how many it removed.
func (s *Service) Prune(ctx context.Context) (int, error) {
	if s.cfg.RetentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.cfg.RetentionDays)

	var runs []models.BackupRun
	err := s.crossBand(ctx).Where("started_at < ?", cutoff).Find(&runs).Error
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, run := range runs {
		if run.Path != "" {
			path, err := s.safeRunPath(run.Path)
			if err != nil {
				return removed, err
			}
			if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
				return removed, err
			}
		}
		if err := s.crossBand(ctx).Delete(&models.BackupRun{}, run.ID).Error; err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// Open returns a reader for a finished dump so the admin center can offer it
// as a download.
func (s *Service) Open(ctx context.Context, id int64) (io.ReadSeekCloser, string, error) {
	run, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if run.Status != models.BackupStatusSucceeded || run.Path == "" {
		return nil, "", ErrRunNotFound
	}

	runPath, err := s.safeRunPath(run.Path)
	if err != nil {
		return nil, "", ErrRunNotFound
	}
	path := filepath.Join(runPath, "dump.sql")
	file, err := os.Open(path)
	if err != nil {
		return nil, "", ErrRunNotFound
	}
	name := filepath.Base(filepath.Dir(runPath)) + "_" + filepath.Base(runPath) + ".sql"
	return file, name, nil
}

// safeRunPath rejects database paths that do not point below the configured
// backup root. BackupRun rows are persistent state and must never be able to
// turn a download, restore or retention cleanup into arbitrary file access.
func (s *Service) safeRunPath(path string) (string, error) {
	root, err := filepath.Abs(s.cfg.Root)
	if err != nil {
		return "", ErrUnsafePath
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return "", ErrUnsafePath
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", ErrUnsafePath
	}
	return candidate, nil
}

// dsnSettings is the subset of the DSN the dump command needs.
type dsnSettings struct {
	user, password, host, port, database string
}

// parseDSN reads a go-sql-driver DSN such as
// "user:pass@tcp(host:3306)/db?params".
func parseDSN(dsn string) (*dsnSettings, error) {
	credentials, rest, found := strings.Cut(dsn, "@")
	if !found {
		return nil, fmt.Errorf("backup: cannot parse the database DSN")
	}
	user, password, _ := strings.Cut(credentials, ":")

	protocolAndAddress, database, found := strings.Cut(rest, "/")
	if !found {
		return nil, fmt.Errorf("backup: cannot parse the database DSN")
	}
	database, _, _ = strings.Cut(database, "?")

	open := strings.Index(protocolAndAddress, "(")
	close := strings.LastIndex(protocolAndAddress, ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("backup: cannot parse the database DSN")
	}
	host, port, hasPort := strings.Cut(protocolAndAddress[open+1:close], ":")
	if !hasPort {
		port = "3306"
	}

	decodedPassword, err := url.QueryUnescape(password)
	if err != nil {
		decodedPassword = password
	}
	return &dsnSettings{
		user: user, password: decodedPassword,
		host: host, port: port, database: database,
	}, nil
}

// copyTree copies a directory recursively. A missing source is not an error:
// a band that never uploaded anything simply has no files to back up.
func copyTree(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return copyFile(source, target)
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// SortRuns orders runs newest first, used where the caller assembled them from
// several queries.
func SortRuns(runs []models.BackupRun) {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
}
