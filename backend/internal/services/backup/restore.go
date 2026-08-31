package backup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Errors from the restore workflow.
var (
	ErrNotPerBand     = errors.New("backup: only a per-band backup can be restored")
	ErrRestoreFailed  = errors.New("backup: the restore failed and was rolled back")
	ErrBandMismatch   = errors.New("backup: this backup belongs to a different band")
	ErrRestoreMissing = errors.New("backup: the dump file of this run is gone")
)

// Trigger values recorded on a run.
const (
	TriggerManual     = "manual"
	TriggerScheduled  = "scheduled"
	TriggerPreRestore = "pre_restore"
)

// RestoreBand puts one band back to the state a per-band backup captured.
//
// Three rules make this safe enough to offer in a web UI. A safety point is
// taken first, so the state being replaced is never the only copy. The
// database work runs in a single transaction, so a half-restored band cannot
// exist. And only the band's own tables are touched — the control plane, the
// other bands and the account data are out of reach by construction, because
// the dump carries nothing else.
//
// The file store is copied after the commit, since a filesystem takes part in
// no transaction; the previous directory is kept aside until that succeeded.
func (s *Service) RestoreBand(ctx context.Context, runID int64, actor Actor) (*models.BackupRun, error) {
	run, err := s.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.BandID == nil {
		return nil, ErrNotPerBand
	}
	if run.Status != models.BackupStatusSucceeded || run.Path == "" {
		return nil, ErrRunNotFound
	}
	dumpPath := filepath.Join(run.Path, "dump.sql")
	statements, err := readStatements(dumpPath)
	if err != nil {
		return nil, err
	}

	bandID := *run.BandID

	// The safety point first: whatever is about to be overwritten stays
	// recoverable even if this restore turns out to be the wrong choice.
	safety, err := s.Run(ctx, &bandID, TriggerPreRestore, actor)
	if err != nil {
		return nil, err
	}

	err = s.crossBand(ctx).Transaction(func(tx *gorm.DB) error {
		// Foreign keys are switched off for this connection only. A dump lists
		// tables in dependency order, but the deletions below run in reverse
		// and a restore must not fail on an order the dump never promised.
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			return err
		}
		defer func() { _ = tx.Exec("SET FOREIGN_KEY_CHECKS = 1").Error }()

		for i := len(bandScopedTables) - 1; i >= 0; i-- {
			if err := tx.Exec(
				"DELETE FROM "+bandScopedTables[i]+" WHERE band_id = ?", bandID).Error; err != nil {
				return err
			}
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("%w: %v", ErrRestoreFailed, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := s.restoreFiles(run, bandID); err != nil {
		return nil, err
	}
	return safety, nil
}

// restoreFiles swaps the band's uploads for the copy inside the backup.
//
// The previous directory is only removed once the new one is in place, so a
// failure halfway leaves the band with its old files rather than none.
func (s *Service) restoreFiles(run *models.BackupRun, bandID int64) error {
	if s.cfg.StorageRoot == "" {
		return nil
	}
	source := filepath.Join(run.Path, "files")
	if _, err := os.Stat(source); err != nil {
		// A backup without a file copy restores the database only.
		return nil
	}

	target := filepath.Join(s.cfg.StorageRoot, fmt.Sprintf("band-%d", bandID))
	aside := target + ".replaced-" + time.Now().UTC().Format("20060102150405")
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, aside); err != nil {
			return err
		}
	}
	if err := copyTree(source, target); err != nil {
		// Put the old files back rather than leaving the band with nothing.
		_ = os.RemoveAll(target)
		_ = os.Rename(aside, target)
		return err
	}
	return os.RemoveAll(aside)
}

// readStatements turns a mariadb-dump file into executable statements.
//
// Comments and the /*! ... */ session directives a dump wraps itself in are
// dropped: they set server variables that have no business leaking out of a
// restore and would fail inside a transaction.
func readStatements(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrRestoreMissing
		}
		return nil, err
	}
	defer file.Close()

	var statements []string
	var current strings.Builder

	scanner := bufio.NewScanner(file)
	// A dump writes one extended INSERT per table, which easily exceeds the
	// default 64 KiB line limit.
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "/*!") {
			continue
		}
		current.WriteString(line)
		if strings.HasSuffix(line, ";") {
			statements = append(statements, strings.TrimSuffix(current.String(), ";"))
			current.Reset()
			continue
		}
		current.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return statements, nil
}
