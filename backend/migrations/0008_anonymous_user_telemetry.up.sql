ALTER TABLE users
    ADD COLUMN telemetry_enabled TINYINT(1) NOT NULL DEFAULT 0 AFTER show_variant_photos,
    ADD COLUMN telemetry_decided_at DATETIME(3) NULL AFTER telemetry_enabled;

-- Deliberately no user_id, band_id, username, slug, IP address or stable hash.
-- Every row is already a daily aggregate and therefore has no lookup key back
-- to an individual account or band.
CREATE TABLE telemetry_daily (
    id                   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    day                  DATE         NOT NULL,
    event_kind           VARCHAR(40)  NOT NULL,
    dimension_value      VARCHAR(255) NOT NULL DEFAULT '',
    sample_count         BIGINT       NOT NULL DEFAULT 0,
    total_duration_ms    BIGINT       NOT NULL DEFAULT 0,
    total_request_bytes  BIGINT       NOT NULL DEFAULT 0,
    total_response_bytes BIGINT       NOT NULL DEFAULT 0,
    updated_at           DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_telemetry_daily_dimension (day, event_kind, dimension_value),
    KEY idx_telemetry_daily_kind_day (event_kind, day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
