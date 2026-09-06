-- Account recovery and management added after the initial multi-tenant cut.
ALTER TABLE users
    ADD COLUMN contact_email VARCHAR(254) NOT NULL DEFAULT '' AFTER username,
    -- MariaDB permits repeated NULLs in a composite UNIQUE key, so
    -- (band_id, username) alone did not keep platform usernames unique.
    ADD COLUMN platform_username VARCHAR(150)
        AS (CASE WHEN band_id IS NULL THEN username ELSE NULL END) STORED,
    ADD UNIQUE KEY uq_users_platform_username (platform_username);

CREATE TABLE password_reset_challenges (
    user_id         BIGINT       NOT NULL PRIMARY KEY,
    code_hash       VARCHAR(64)  NOT NULL,
    expires_at      DATETIME(3)  NOT NULL,
    requested_at    DATETIME(3)  NOT NULL,
    failed_attempts INT          NOT NULL DEFAULT 0,
    CONSTRAINT fk_password_reset_user FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
