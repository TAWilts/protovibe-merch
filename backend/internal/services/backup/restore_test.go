package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadStatementsSkipsDirectives pins what a restore must ignore.
//
// mariadb-dump wraps its output in /*! ... */ session directives that set
// server variables. Replaying those inside a restore would change settings the
// instance never agreed to, and some of them fail inside a transaction — so
// only the actual data statements may survive the read.
func TestReadStatementsSkipsDirectives(t *testing.T) {
	dump := "" +
		"-- MariaDB dump 10.19\n" +
		"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
		"\n" +
		"--\n" +
		"-- Dumping data for table `articles`\n" +
		"--\n" +
		"INSERT INTO `articles` VALUES (1,7,'Cap',500,0);\n" +
		"INSERT INTO `variants` VALUES\n" +
		"(1,7,'1|3',500),\n" +
		"(2,7,'1|4',500);\n" +
		"/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;\n"

	path := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(path, []byte(dump), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	statements, err := readStatements(path)
	if err != nil {
		t.Fatalf("read statements: %v", err)
	}
	if len(statements) != 2 {
		t.Fatalf("expected the two INSERTs, got %d: %q", len(statements), statements)
	}
	if statements[0] != "INSERT INTO `articles` VALUES (1,7,'Cap',500,0)" {
		t.Fatalf("first statement is wrong: %q", statements[0])
	}
	// A statement spanning several lines must arrive in one piece.
	want := "INSERT INTO `variants` VALUES\n(1,7,'1|3',500),\n(2,7,'1|4',500)"
	if statements[1] != want {
		t.Fatalf("multi-line statement is wrong:\ngot  %q\nwant %q", statements[1], want)
	}
}

// TestReadStatementsReportsAMissingDump keeps a deleted backup directory from
// looking like an empty, silently successful restore.
func TestReadStatementsReportsAMissingDump(t *testing.T) {
	if _, err := readStatements(filepath.Join(t.TempDir(), "gone.sql")); err != ErrRestoreMissing {
		t.Fatalf("expected ErrRestoreMissing, got %v", err)
	}
}

func TestSafeRunPathStaysBelowBackupRoot(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, Config{Root: root})

	inside := filepath.Join(root, "band-7", "2026-09-01_12-00-00")
	resolved, err := service.safeRunPath(inside)
	if err != nil {
		t.Fatalf("valid backup path was rejected: %v", err)
	}
	want, _ := filepath.Abs(inside)
	if resolved != want {
		t.Fatalf("resolved path %q, want %q", resolved, want)
	}

	outside := filepath.Join(root, "..", "not-a-backup")
	if _, err := service.safeRunPath(outside); err != ErrUnsafePath {
		t.Fatalf("outside path must return ErrUnsafePath, got %v", err)
	}
	if _, err := service.safeRunPath(root); err != ErrUnsafePath {
		t.Fatalf("backup root itself must return ErrUnsafePath, got %v", err)
	}
}
