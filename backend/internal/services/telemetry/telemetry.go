// Package telemetry stores opt-in usage statistics.
//
// There are two layers:
//   - telemetry_daily: unlinkable daily aggregates
//   - telemetry_events: detailed pseudonymised snapshots with HMAC aliases
//
// Neither layer stores usernames, internal user/band/article IDs, IP addresses,
// customer/contact data or comments. Stable aliases exist only so operations
// from the same band/article can be compared over time.
package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

type Service struct {
	db       *gorm.DB
	aliasKey []byte
}

func NewService(database *gorm.DB, aliasSecret string) *Service {
	return &Service{db: database, aliasKey: []byte(aliasSecret)}
}

// SaleSnapshot contains only fields explicitly permitted in detailed
// telemetry. Customer/contact/comment fields cannot be passed through this API.
type SaleSnapshot struct {
	BandID          int64
	SaleID          int64
	ArticleID       int64
	Quantity        int
	UnitPriceCents  int64
	AmountCents     int64
	PaymentMethod   string
	IsPaid          bool
	IsReceived      bool
	IsCancelled     bool
	DeliveryStatus  string
	PaymentFollowUp bool
	Location        string
}

// RecordRoute increments a coarse API-route counter. path must be Gin's route
// template (for example /api/v1/sales/:id), never a concrete URL or query.
func (s *Service) RecordRoute(
	ctx context.Context,
	method, path string,
	duration time.Duration,
	requestBytes, responseBytes int64,
) error {
	dimension := strings.TrimSpace(method) + " " + strings.TrimSpace(path)
	return s.record(ctx, "api_route", dimension, duration.Milliseconds(), requestBytes, responseBytes)
}

// RecordEvent adds a non-identifying categorical aggregate such as payment
// method or role.
func (s *Service) RecordEvent(ctx context.Context, kind, dimension string) error {
	return s.record(ctx, kind, dimension, 0, 0, 0)
}

// RecordFeature stores one detailed feature-use snapshot. Location is only the
// currently selected event/location text entered by the band; no IP lookup is
// performed.
func (s *Service) RecordFeature(
	ctx context.Context,
	bandID int64,
	feature, location string,
	httpStatus int,
	duration time.Duration,
	requestBytes, responseBytes int64,
) error {
	if bandID <= 0 {
		return nil
	}
	event := &models.TelemetryEvent{
		OccurredAt:    time.Now().UTC(),
		EventType:     "feature_used",
		BandAlias:     s.alias("band", bandID),
		FeatureKey:    clean(feature, 80),
		Location:      clean(location, 200),
		HTTPStatus:    httpStatus,
		RequestBytes:  nonNegative(requestBytes),
		ResponseBytes: nonNegative(responseBytes),
		DurationMS:    nonNegative(duration.Milliseconds()),
	}
	if event.FeatureKey == "" {
		return nil
	}
	return s.db.WithContext(ctx).Create(event).Error
}

// RecordSession records an application/session observation for role and active
// band statistics. A platform support account never creates a band event.
func (s *Service) RecordSession(ctx context.Context, bandID int64, role string) error {
	if bandID <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&models.TelemetryEvent{
		OccurredAt: time.Now().UTC(),
		EventType:  "session_seen",
		BandAlias:  s.alias("band", bandID),
		Role:       clean(role, 40),
	}).Error
}

// StorageSnapshotNeeded avoids walking a band's storage tree on every /me
// request. One consenting band account per day is enough for the storage trend.
func (s *Service) StorageSnapshotNeeded(ctx context.Context, bandID int64) (bool, error) {
	if bandID <= 0 {
		return false, nil
	}
	alias := s.alias("band", bandID)
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	var count int64
	err := s.db.WithContext(ctx).Model(&models.TelemetryEvent{}).
		Where(
			"event_type = ? AND band_alias = ? AND occurred_at >= ? AND occurred_at < ?",
			"storage_snapshot", alias, start, end,
		).
		Count(&count).Error
	return count == 0, err
}

func (s *Service) RecordStorageSnapshot(ctx context.Context, bandID, bytes int64) error {
	if bandID <= 0 {
		return nil
	}
	value := nonNegative(bytes)
	return s.db.WithContext(ctx).Create(&models.TelemetryEvent{
		OccurredAt:   time.Now().UTC(),
		EventType:    "storage_snapshot",
		BandAlias:    s.alias("band", bandID),
		StorageBytes: &value,
	}).Error
}

// RecordSale stores either a newly-created sale line or a later status
// snapshot. Stable aliases correlate snapshots without exposing source IDs.
func (s *Service) RecordSale(ctx context.Context, eventType string, sale SaleSnapshot) error {
	switch eventType {
	case "sale_created", "sale_status":
	default:
		return nil
	}
	if sale.BandID <= 0 || sale.SaleID <= 0 {
		return nil
	}

	isOpen := !sale.IsCancelled && (!sale.IsPaid || !sale.IsReceived)
	status := "complete"
	switch {
	case sale.IsCancelled:
		status = "cancelled"
	case isOpen:
		status = "open"
	}

	quantity := sale.Quantity
	unitPrice := sale.UnitPriceCents
	amount := sale.AmountCents
	isPaid := sale.IsPaid
	isReceived := sale.IsReceived
	paymentFollowUp := sale.PaymentFollowUp

	event := &models.TelemetryEvent{
		OccurredAt:      time.Now().UTC(),
		EventType:       eventType,
		BandAlias:       s.alias("band", sale.BandID),
		OperationAlias:  s.alias("sale", sale.SaleID),
		FeatureKey:      "sales",
		Quantity:        &quantity,
		UnitPriceCents:  &unitPrice,
		AmountCents:     &amount,
		PaymentMethod:   clean(sale.PaymentMethod, 40),
		IsPaid:          &isPaid,
		IsReceived:      &isReceived,
		IsOpen:          &isOpen,
		Status:          status,
		DeliveryStatus:  clean(sale.DeliveryStatus, 40),
		PaymentFollowUp: &paymentFollowUp,
		Location:        clean(sale.Location, 200),
	}
	if sale.ArticleID > 0 {
		event.SubjectAlias = s.alias("article", sale.ArticleID)
	}
	return s.db.WithContext(ctx).Create(event).Error
}

func (s *Service) record(
	ctx context.Context,
	kind, dimension string,
	durationMS, requestBytes, responseBytes int64,
) error {
	kind = strings.TrimSpace(kind)
	dimension = strings.TrimSpace(dimension)
	if kind == "" || len(kind) > 40 || len(dimension) > 255 {
		return nil
	}

	now := time.Now().UTC()
	day := now.Format(models.DateLayout)
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO telemetry_daily (
			day, event_kind, dimension_value, sample_count,
			total_duration_ms, total_request_bytes, total_response_bytes, updated_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			sample_count = sample_count + 1,
			total_duration_ms = total_duration_ms + VALUES(total_duration_ms),
			total_request_bytes = total_request_bytes + VALUES(total_request_bytes),
			total_response_bytes = total_response_bytes + VALUES(total_response_bytes),
			updated_at = VALUES(updated_at)
	`, day, kind, dimension,
		nonNegative(durationMS),
		nonNegative(requestBytes),
		nonNegative(responseBytes),
		now,
	).Error
}

// List exposes daily aggregates from the selected range.
func (s *Service) List(ctx context.Context, since models.Date) ([]models.TelemetryDaily, error) {
	var rows []models.TelemetryDaily
	err := s.db.WithContext(ctx).
		Where("day >= ?", since).
		Order("day DESC, event_kind, sample_count DESC, dimension_value").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []models.TelemetryDaily{}
	}
	return rows, nil
}

// ListEvents returns detailed pseudonymised snapshots without joining aliases
// back to bands, users or articles.
func (s *Service) ListEvents(ctx context.Context, since models.Date) ([]models.TelemetryEvent, error) {
	var rows []models.TelemetryEvent
	err := s.db.WithContext(ctx).
		Where("occurred_at >= ?", since.Time).
		Order("occurred_at DESC, id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []models.TelemetryEvent{}
	}
	return rows, nil
}

func (s *Service) AllDaily(ctx context.Context) ([]models.TelemetryDaily, error) {
	var rows []models.TelemetryDaily
	err := s.db.WithContext(ctx).
		Order("day, event_kind, dimension_value").
		Find(&rows).Error
	if rows == nil {
		rows = []models.TelemetryDaily{}
	}
	return rows, err
}

func (s *Service) AllEvents(ctx context.Context) ([]models.TelemetryEvent, error) {
	var rows []models.TelemetryEvent
	err := s.db.WithContext(ctx).
		Order("occurred_at, id").
		Find(&rows).Error
	if rows == nil {
		rows = []models.TelemetryEvent{}
	}
	return rows, err
}

// alias deliberately has no reverse lookup table. The server secret prevents
// exported aliases from revealing low integer database IDs directly.
func (s *Service) alias(kind string, id int64) string {
	if id <= 0 || len(s.aliasKey) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, s.aliasKey)
	_, _ = mac.Write([]byte(kind))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(strconv.FormatInt(id, 10)))
	sum := mac.Sum(nil)
	return kind + "_" + strings.ToUpper(hex.EncodeToString(sum[:8]))
}

func clean(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
