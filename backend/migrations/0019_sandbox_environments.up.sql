-- Disposable, isolated tenants used by the public product sandbox.
CREATE TABLE sandbox_environments (
    id                BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id           BIGINT       NOT NULL,
    user_id           BIGINT       NOT NULL,
    source_user_id    BIGINT       NULL,
    template_version  INT          NOT NULL DEFAULT 1,
    tutorial_state    JSON         NOT NULL,
    tutorial_visible  TINYINT(1)   NOT NULL DEFAULT 1,
    status            VARCHAR(20)  NOT NULL DEFAULT 'active',
    last_active_at    DATETIME(3)  NOT NULL,
    expires_at        DATETIME(3)  NOT NULL,
    created_at        DATETIME(3)  NOT NULL,
    updated_at        DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_sandbox_band (band_id),
    UNIQUE KEY uq_sandbox_user (user_id),
    KEY idx_sandbox_source_user (source_user_id),
    KEY idx_sandbox_expiry (status, expires_at),
    CONSTRAINT fk_sandbox_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_sandbox_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_sandbox_source_user FOREIGN KEY (source_user_id) REFERENCES users (id),
    CONSTRAINT ck_sandbox_status CHECK (status IN ('active','purging'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE sessions
    ADD COLUMN sandbox_environment_id BIGINT NULL AFTER acting_grant_id,
    ADD KEY idx_sessions_sandbox (sandbox_environment_id),
    ADD CONSTRAINT fk_sessions_sandbox FOREIGN KEY (sandbox_environment_id)
        REFERENCES sandbox_environments (id) ON DELETE CASCADE;

ALTER TABLE users
    ADD COLUMN sandbox_intro_seen_at DATETIME(3) NULL AFTER last_login_at;
