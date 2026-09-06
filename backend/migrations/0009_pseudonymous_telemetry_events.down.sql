DROP TABLE IF EXISTS telemetry_events;

ALTER TABLE users
    DROP COLUMN telemetry_consent_version;
