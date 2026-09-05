// Package telemetry stores opt-in anonymous usage aggregates.
//
// The schema deliberately contains no user ID, band ID, username, IP address,
// slug or stable hash. That is stronger than pseudonymisation: once a sample
// has been added to a daily counter there is no lookup key back to its source.
package telemetry

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

type Service struct {
	db *gorm.DB
}

func NewService(database *gorm.DB) *Service { return &Service{db: database} }

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

// RecordEvent adds a non-identifying categorical event such as payment method
// or role. Callers only pass values from closed application enums.
func (s *Service) RecordEvent(ctx context.Context, kind, dimension string) error {
	return s.record(ctx, kind, dimension, 0, 0, 0)
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
	if durationMS < 0 {
		durationMS = 0
	}
	if requestBytes < 0 {
		requestBytes = 0
	}
	if responseBytes < 0 {
		responseBytes = 0
	}

	now := time.Now().UTC()
	day := now.Format(models.DateLayout)
	// Atomic upsert: every consenting sample for a day/dimension merges into
	// the same row immediately. No raw event stream exists.
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
	`, day, kind, dimension, durationMS, requestBytes, responseBytes, now).Error
}

// List exposes already-aggregated rows only.
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
