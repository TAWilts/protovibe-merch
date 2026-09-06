ALTER TABLE platform_settings
    ADD COLUMN default_storage_quota_bytes BIGINT NOT NULL DEFAULT 5368709120
    AFTER notification_email;

-- Existing bands with storage_quota_bytes = 0 now inherit this five-GiB
-- platform default. No uploaded file is changed or removed.
UPDATE platform_settings
SET default_storage_quota_bytes = 5368709120
WHERE id = 1;
