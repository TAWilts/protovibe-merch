// Package importer reads the five-column transaction CSVs a band can upload to
// backfill a season of sales or goods receipts.
//
// The whole file is validated before anything is written. A half-applied
// import would leave the stock ledger in a state nobody can reason about, so
// the parse and the preflight run first and the commit is one transaction.
package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
)

// Limits mirror the original's.
const (
	MaxBytes              = 2 << 20
	MaxRows               = 5000
	MaxVariantsPerArticle = 10000
	MaxArticleNameLength  = 200
	MaxPartyLength        = 300
	MaxOptionNameLength   = 120
	MaxOptionValueLength  = 120
)

// Kind selects which of the two import formats applies.
type Kind string

const (
	KindPurchases Kind = "einkaeufe"
	KindSales     Kind = "verkaeufe"
)

// Headers are the exact five columns each format expects.
var Headers = map[Kind][]string{
	KindPurchases: {"Anzahl", "Artikel", "Optionen", "Einkaufspreis", "Gekauft von"},
	KindSales:     {"Anzahl", "Artikel", "Optionen", "Verkaufspreis", "Verkauft an"},
}

// Valid reports whether the requested import format exists.
func (k Kind) Valid() bool {
	_, ok := Headers[k]
	return ok
}

// ErrEmptyFile is returned for a file with no data at all.
var ErrEmptyFile = errors.New("importer: the file is empty")

// Option is one "Name=Wert" pair from the options column.
type Option struct {
	GroupName string
	Value     string
	// GroupKey and ValueKey are the case-folded forms used for matching, so
	// "Größe" and "größe" are the same column.
	GroupKey string
	ValueKey string
}

// Row is one validated data line.
type Row struct {
	LineNumber  int
	Quantity    int
	ArticleName string
	Options     []Option
	// PriceCents is nil when the column was left blank, which means "use the
	// catalogue price".
	PriceCents *int64
	Party      string
	// GroupKeys is the set of option columns this row declares, used by the
	// preflight to detect a file that omits an existing option dimension.
	GroupKeys []string
}

// Parse validates the whole file without touching the database.
func Parse(kind Kind, content []byte) ([]Row, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("importer: unknown import kind %q", kind)
	}
	if len(content) == 0 {
		return nil, ErrEmptyFile
	}
	if len(content) > MaxBytes {
		return nil, fmt.Errorf("importer: the file may be at most %d MB", MaxBytes>>20)
	}

	text, err := decode(content)
	if err != nil {
		return nil, err
	}
	if strings.ContainsRune(text, 0) {
		return nil, errors.New("importer: the file contains null bytes")
	}

	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = ';'
	// The row length is checked per line so the error can name the line.
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err == io.EOF {
		return nil, ErrEmptyFile
	}
	if err != nil {
		return nil, fmt.Errorf("importer: the header row is not valid CSV: %w", err)
	}
	if err := checkHeader(kind, header); err != nil {
		return nil, err
	}

	rows := make([]Row, 0, 64)
	// Line 1 is the header; data starts at 2.
	lineNumber := 1
	for {
		values, err := reader.Read()
		if err == io.EOF {
			break
		}
		lineNumber++
		if err != nil {
			return nil, fmt.Errorf("importer: line %d is not valid CSV: %w", lineNumber, err)
		}
		if isBlank(values) {
			continue
		}
		if len(values) != 5 {
			return nil, fmt.Errorf("importer: line %d has %d columns, expected five", lineNumber, len(values))
		}

		row, err := parseRow(lineNumber, values)
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
		if len(rows) > MaxRows {
			return nil, fmt.Errorf("importer: the file may contain at most %d data rows", MaxRows)
		}
	}

	if len(rows) == 0 {
		return nil, ErrEmptyFile
	}
	if err := checkConsistentOptionColumns(rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// decode accepts UTF-8 with or without a BOM, falling back to Windows-1252 —
// which is what a German Excel writes unless told otherwise.
func decode(content []byte) (string, error) {
	trimmed := bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(trimmed) {
		return string(trimmed), nil
	}

	decoded, err := charmap.Windows1252.NewDecoder().Bytes(trimmed)
	if err != nil {
		return "", errors.New("importer: the file must be saved as UTF-8 or Windows-1252")
	}
	return string(decoded), nil
}

func checkHeader(kind Kind, header []string) error {
	expected := Headers[kind]
	if len(header) != len(expected) {
		return headerError(expected)
	}
	for i, column := range header {
		if normalizeHeader(column) != normalizeHeader(expected[i]) {
			return headerError(expected)
		}
	}
	return nil
}

func headerError(expected []string) error {
	return fmt.Errorf("importer: the header must contain exactly these five columns: %s",
		strings.Join(expected, ";"))
}

func normalizeHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "\ufeff")))
}

func isBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func parseRow(lineNumber int, values []string) (*Row, error) {
	quantity, err := strconv.Atoi(strings.TrimSpace(values[0]))
	if err != nil || quantity <= 0 {
		return nil, fmt.Errorf("importer: line %d: the quantity must be a positive whole number", lineNumber)
	}

	articleName := strings.TrimSpace(values[1])
	if articleName == "" {
		return nil, fmt.Errorf("importer: line %d: the article name must not be empty", lineNumber)
	}
	if utf8.RuneCountInString(articleName) > MaxArticleNameLength {
		return nil, fmt.Errorf("importer: line %d: the article name is too long", lineNumber)
	}

	options, err := parseOptions(lineNumber, values[2])
	if err != nil {
		return nil, err
	}

	var priceCents *int64
	if raw := strings.TrimSpace(values[3]); raw != "" {
		parsed, err := money.ParseAmount(raw)
		if err != nil {
			return nil, fmt.Errorf("importer: line %d: %w", lineNumber, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("importer: line %d: the price must not be negative", lineNumber)
		}
		priceCents = &parsed
	}

	party := strings.TrimSpace(values[4])
	if party == "" {
		return nil, fmt.Errorf("importer: line %d: the fifth column must not be empty", lineNumber)
	}
	if utf8.RuneCountInString(party) > MaxPartyLength {
		return nil, fmt.Errorf("importer: line %d: the name in the fifth column is too long", lineNumber)
	}

	groupKeys := make([]string, 0, len(options))
	for _, option := range options {
		groupKeys = append(groupKeys, option.GroupKey)
	}

	return &Row{
		LineNumber:  lineNumber,
		Quantity:    quantity,
		ArticleName: articleName,
		Options:     options,
		PriceCents:  priceCents,
		Party:       party,
		GroupKeys:   groupKeys,
	}, nil
}

// parseOptions reads "Farbe=Schwarz, Größe=M" into its pairs. An empty column
// is valid: it means an article without options.
func parseOptions(lineNumber int, raw string) ([]Option, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	// Both a comma and a middle dot are accepted, because the exports render
	// options with " · " and a band will paste one straight back in.
	separated := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == '·' || r == ';'
	})

	options := make([]Option, 0, len(separated))
	seen := map[string]bool{}

	for _, part := range separated {
		name, value, found := strings.Cut(part, "=")
		if !found {
			// The export writes "Farbe: Schwarz"; accept that shape too.
			name, value, found = strings.Cut(part, ":")
		}
		if !found {
			return nil, fmt.Errorf(
				"importer: line %d: option %q must look like Name=Wert", lineNumber, strings.TrimSpace(part))
		}

		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, fmt.Errorf("importer: line %d: an option name and value must not be empty", lineNumber)
		}
		if utf8.RuneCountInString(name) > MaxOptionNameLength ||
			utf8.RuneCountInString(value) > MaxOptionValueLength {
			return nil, fmt.Errorf("importer: line %d: an option name or value is too long", lineNumber)
		}

		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("importer: line %d: option %q appears twice", lineNumber, name)
		}
		seen[key] = true

		options = append(options, Option{
			GroupName: name,
			Value:     value,
			GroupKey:  key,
			ValueKey:  strings.ToLower(value),
		})
	}
	return options, nil
}

// checkConsistentOptionColumns rejects a file where the same article declares
// different option columns on different lines.
//
// Accepting it would create variants that differ in which dimensions they even
// have, which the catalogue cannot represent.
func checkConsistentOptionColumns(rows []Row) error {
	byArticle := map[string][]string{}
	firstLine := map[string]int{}

	for _, row := range rows {
		key := strings.ToLower(row.ArticleName)
		signature := optionSignature(row.GroupKeys)

		if existing, seen := byArticle[key]; seen {
			if strings.Join(existing, "|") != signature {
				return fmt.Errorf(
					"importer: line %d: %q uses different option columns than line %d",
					row.LineNumber, row.ArticleName, firstLine[key])
			}
			continue
		}
		byArticle[key] = strings.Split(signature, "|")
		firstLine[key] = row.LineNumber
	}
	return nil
}

func optionSignature(groupKeys []string) string {
	sorted := append([]string(nil), groupKeys...)
	sortStrings(sorted)
	return strings.Join(sorted, "|")
}

// sortStrings is a tiny insertion sort; option counts are single digits.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
