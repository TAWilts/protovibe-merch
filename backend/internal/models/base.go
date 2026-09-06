// Package models holds the GORM entities.
//
// The schema itself is owned by the versioned SQL migrations in
// backend/migrations; these structs only map onto it. Two invariants carry
// over from the Flask original and must never be relaxed:
//
//   - Money is always an integer number of cents. There is no float anywhere.
//   - Stock is derived from purchase and sale movements, never stored.
package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DateLayout is the wire and storage format for calendar dates such as
// sold_on or purchased_on, which carry no time of day.
const DateLayout = "2006-01-02"

// Date is a calendar date without a time component. It maps to a MariaDB DATE
// column and serialises as "2006-01-02", so a sale booked at a gig keeps the
// date the seller entered regardless of the server's timezone.
type Date struct {
	time.Time
}

// NewDate builds a Date from year, month and day.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// ParseDate reads the canonical "2006-01-02" representation.
func ParseDate(value string) (Date, error) {
	t, err := time.ParseInLocation(DateLayout, strings.TrimSpace(value), time.UTC)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q, expected YYYY-MM-DD", value)
	}
	return Date{t}, nil
}

func (d Date) String() string { return d.Format(DateLayout) }

func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format(DateLayout) + `"`), nil
}

func (d *Date) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(string(data), `"`)
	if raw == "" || raw == "null" {
		*d = Date{}
		return nil
	}
	parsed, err := ParseDate(raw)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func (d *Date) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*d = Date{}
	case time.Time:
		*d = Date{time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)}
	case []byte:
		parsed, err := ParseDate(string(v))
		if err != nil {
			return err
		}
		*d = parsed
	case string:
		parsed, err := ParseDate(v)
		if err != nil {
			return err
		}
		*d = parsed
	default:
		return fmt.Errorf("cannot scan %T into Date", value)
	}
	return nil
}

func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, nil
	}
	return d.Format(DateLayout), nil
}

// GormDataType keeps AutoMigrate and the query builder aware that this is a
// DATE rather than a DATETIME.
func (Date) GormDataType() string { return "date" }

// Tenant is embedded by every band-scoped entity. Its presence is what the
// GORM tenant callback detects: a query touching a struct that embeds Tenant
// without a band in the context is rejected rather than silently run across
// all bands.
type Tenant struct {
	BandID int64 `gorm:"column:band_id;not null;index" json:"band_id"`
}

// TenantScoped marks band-scoped models. Tenant satisfies it for every entity
// that embeds it, so no model can opt out by accident.
type TenantScoped interface {
	TenantBandID() int64
	SetTenantBandID(id int64)
}

func (t Tenant) TenantBandID() int64       { return t.BandID }
func (t *Tenant) SetTenantBandID(id int64) { t.BandID = id }

// Timestamps is the common created/updated pair. GORM maintains both.
type Timestamps struct {
	CreatedAt time.Time `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

// Actor records who performed a booking. The user ID is deliberately not a
// foreign key and is paired with an immutable username snapshot, so deleting
// an account never makes historic bookings unreadable — the same decision the
// Flask version made when it split accounts into a second database.
type Actor struct {
	CreatedByUserID   *int64 `gorm:"column:created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedByUsername string `gorm:"column:created_by_username;size:150" json:"created_by_username"`
}

// JSONSlice is a string slice stored in a MariaDB JSON column. It is used for
// MFA recovery-code hashes and for a variant's option-value ID list.
type JSONSlice []string

func (s JSONSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal([]string(s))
}

func (s *JSONSlice) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*s = JSONSlice{}
		return nil
	case []byte:
		return json.Unmarshal(v, (*[]string)(s))
	case string:
		return json.Unmarshal([]byte(v), (*[]string)(s))
	default:
		return fmt.Errorf("cannot scan %T into JSONSlice", value)
	}
}

// JSONMap is a free-form object stored in a MariaDB JSON column, used for
// audit-log details.
type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return json.Marshal(map[string]any(m))
}

func (m *JSONMap) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*m = JSONMap{}
		return nil
	case []byte:
		return json.Unmarshal(v, (*map[string]any)(m))
	case string:
		return json.Unmarshal([]byte(v), (*map[string]any)(m))
	default:
		return fmt.Errorf("cannot scan %T into JSONMap", value)
	}
}

// JSONInt64Slice stores an ordered list of IDs, used for a variant's
// option_value_ids.
type JSONInt64Slice []int64

func (s JSONInt64Slice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal([]int64(s))
}

func (s *JSONInt64Slice) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*s = JSONInt64Slice{}
		return nil
	case []byte:
		return json.Unmarshal(v, (*[]int64)(s))
	case string:
		return json.Unmarshal([]byte(v), (*[]int64)(s))
	default:
		return fmt.Errorf("cannot scan %T into JSONInt64Slice", value)
	}
}
