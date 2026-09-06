DROP TABLE IF EXISTS telemetry_daily;

ALTER TABLE users
    DROP COLUMN telemetry_decided_at,
    DROP COLUMN telemetry_enabled;
