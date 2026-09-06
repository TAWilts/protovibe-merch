package legacyimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	_ "modernc.org/sqlite"
)

type row map[string]any

type sourceDB struct {
	db    *sql.DB
	label string
}

func openSource(path, label string) (*sourceDB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s path is empty", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%s path: %w", label, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s %q is a directory", label, absolute)
	}

	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := &url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var schemaCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master").Scan(&schemaCount); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf(
			"read %s: %w (the legacy database must be a decrypted/plain SQLite database)",
			label, err,
		)
	}
	return &sourceDB{db: db, label: label}, nil
}

func (s *sourceDB) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *sourceDB) hasTable(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		name,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("%s: inspect table %s: %w", s.label, name, err)
	}
	return count > 0, nil
}

func (s *sourceDB) rows(ctx context.Context, name string, required bool) ([]row, error) {
	exists, err := s.hasTable(ctx, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		if required {
			return nil, fmt.Errorf("%s: required table %q is missing", s.label, name)
		}
		return []row{}, nil
	}

	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	result, err := s.db.QueryContext(ctx, "SELECT * FROM "+quoted)
	if err != nil {
		return nil, fmt.Errorf("%s: read table %s: %w", s.label, name, err)
	}
	defer result.Close()

	columns, err := result.Columns()
	if err != nil {
		return nil, err
	}
	rows := []row{}
	for result.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := result.Scan(targets...); err != nil {
			return nil, fmt.Errorf("%s: scan table %s: %w", s.label, name, err)
		}
		entry := row{}
		for i, column := range columns {
			entry[column] = normalizeSQLiteValue(values[i])
		}
		rows = append(rows, entry)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("%s: iterate table %s: %w", s.label, name, err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return int64Value(rows[i], "id") < int64Value(rows[j], "id")
	})
	return rows, nil
}

func normalizeSQLiteValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func stringValue(r row, key string) string {
	value, ok := r[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func int64Value(r row, key string) int64 {
	value, ok := r[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		return n
	default:
		n, _ := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		return n
	}
}

func intValue(r row, key string) int {
	return int(int64Value(r, key))
}

func boolValue(r row, key string, fallback bool) bool {
	value, ok := r[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case int:
		return v != 0
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "ja", "on":
			return true
		case "0", "false", "no", "nein", "off":
			return false
		}
	}
	return fallback
}

func nullableInt(r row, key string) *int {
	value, ok := r[key]
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return nil
	}
	v := intValue(r, key)
	return &v
}

func parseLegacyTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported legacy timestamp %q", raw)
}

func timeValue(r row, key string) (time.Time, error) {
	return parseLegacyTime(stringValue(r, key))
}

func timePtrValue(r row, key string) (*time.Time, error) {
	raw := stringValue(r, key)
	if raw == "" {
		return nil, nil
	}
	parsed, err := parseLegacyTime(raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func dateValue(r row, key string) (models.Date, error) {
	raw := stringValue(r, key)
	if len(raw) >= 10 {
		raw = raw[:10]
	}
	return models.ParseDate(raw)
}

func jsonInt64Slice(r row, key string) ([]int64, error) {
	raw := stringValue(r, key)
	if raw == "" {
		return []int64{}, nil
	}
	var result []int64
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func jsonMapValue(r row, key string) models.JSONMap {
	raw := stringValue(r, key)
	if raw == "" {
		return models.JSONMap{}
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return models.JSONMap{"legacy_raw": raw}
	}
	return models.JSONMap(result)
}

type FileReport struct {
	StoredPath string `json:"stored_path"`
	SourcePath string `json:"source_path"`
	Category   string `json:"category"`
	MediaType  string `json:"media_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}

func inspectLegacyFile(root, category, storedPath string) (*FileReport, error) {
	sourcePath, err := resolveLegacyFile(root, category, storedPath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	hasher := sha256.New()
	prefix := make([]byte, 512)
	n, readErr := file.Read(prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	written, err := io.Copy(hasher, file)
	if err != nil {
		return nil, err
	}

	mediaType := http.DetectContentType(prefix[:n])
	if mediaType == "application/octet-stream" {
		switch strings.ToLower(filepath.Ext(sourcePath)) {
		case ".pdf":
			mediaType = "application/pdf"
		case ".jpg", ".jpeg":
			mediaType = "image/jpeg"
		case ".png":
			mediaType = "image/png"
		case ".webp":
			mediaType = "image/webp"
		}
	}
	switch mediaType {
	case "application/pdf", "image/jpeg", "image/png", "image/webp":
	default:
		return nil, fmt.Errorf("unsupported legacy attachment type %q for %s", mediaType, sourcePath)
	}

	return &FileReport{
		StoredPath: storedPath,
		SourcePath: sourcePath,
		Category:   category,
		MediaType:  mediaType,
		SizeBytes:  written,
		SHA256:     hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func resolveLegacyFile(root, category, storedPath string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("legacy storage path is required because the database references attachments")
	}
	if strings.TrimSpace(storedPath) == "" {
		return "", errors.New("legacy file path is empty")
	}
	if strings.HasSuffix(strings.ToLower(storedPath), ".enc") {
		return "", fmt.Errorf("%q is still encrypted; decrypt legacy attachments before importing", storedPath)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rawPath := strings.ReplaceAll(strings.TrimSpace(storedPath), "\\", "/")
	normalized := filepath.Clean(filepath.FromSlash(rawPath))
	if filepath.IsAbs(normalized) || normalized == ".." || strings.HasPrefix(normalized, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("legacy file path %q escapes the legacy storage directory", storedPath)
	}

	candidates := []string{}
	categoryPrefix := category + string(os.PathSeparator)
	if strings.HasPrefix(normalized, categoryPrefix) {
		candidates = append(candidates, filepath.Join(rootAbs, normalized))
	}
	candidates = append(candidates,
		filepath.Join(rootAbs, category, normalized),
		filepath.Join(rootAbs, normalized),
	)

	for _, candidate := range candidates {
		rel, err := filepath.Rel(rootAbs, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("legacy file %q was not found below %s", storedPath, rootAbs)
}
