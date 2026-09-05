ALTER TABLE users
    ADD COLUMN telemetry_consent_version INT NOT NULL DEFAULT 0 AFTER telemetry_decided_at;

-- Version 2 expands the scope from unlinkable daily aggregates to detailed
-- pseudonymised events. Existing choices must not silently authorise that
-- broader scope, so every account is asked again after this migration.
UPDATE users
SET telemetry_enabled = 0,
    telemetry_decided_at = NULL,
    telemetry_consent_version = 0;

-- Detailed telemetry deliberately lives outside tenant tables. It contains no
-- internal band/user/article IDs, usernames, IP addresses, customer data or
-- contact data. Stable aliases are HMAC-derived before the row is written.
CREATE TABLE telemetry_events (
    id                    BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    occurred_at           DATETIME(3)  NOT NULL,
    event_type            VARCHAR(40)  NOT NULL,
    band_alias            VARCHAR(32)  NOT NULL DEFAULT '',
    operation_alias       VARCHAR(32)  NOT NULL DEFAULT '',
    subject_alias         VARCHAR(32)  NOT NULL DEFAULT '',
    feature_key           VARCHAR(80)  NOT NULL DEFAULT '',
    role                  VARCHAR(40)  NOT NULL DEFAULT '',
    quantity              INT          NULL,
    unit_price_cents      BIGINT       NULL,
    amount_cents          BIGINT       NULL,
    payment_method        VARCHAR(40)  NOT NULL DEFAULT '',
    is_paid               TINYINT(1)   NULL,
    is_received           TINYINT(1)   NULL,
    is_open               TINYINT(1)   NULL,
    status                VARCHAR(40)  NOT NULL DEFAULT '',
    delivery_status       VARCHAR(40)  NOT NULL DEFAULT '',
    payment_follow_up     TINYINT(1)   NULL,
    location              VARCHAR(200) NOT NULL DEFAULT '',
    storage_bytes         BIGINT       NULL,
    http_status           INT          NOT NULL DEFAULT 0,
    request_bytes         BIGINT       NOT NULL DEFAULT 0,
    response_bytes        BIGINT       NOT NULL DEFAULT 0,
    duration_ms           BIGINT       NOT NULL DEFAULT 0,
    KEY idx_telemetry_events_time (occurred_at),
    KEY idx_telemetry_events_type_time (event_type, occurred_at),
    KEY idx_telemetry_events_band_time (band_alias, occurred_at),
    KEY idx_telemetry_events_feature_time (feature_key, occurred_at),
    KEY idx_telemetry_events_location_time (location, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
