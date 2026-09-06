-- Public requests to create a band. These rows are control-plane data: a
-- request is not a tenant and creates no band-scoped data until a system
-- administrator approves it.
CREATE TABLE band_registration_requests (
    id                           BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    public_id                    VARCHAR(24)   NOT NULL,
    token_hash                   CHAR(64)      NOT NULL,
    requested_band_name          VARCHAR(200)  NOT NULL,
    requested_band_slug          VARCHAR(64)   NOT NULL,
    requested_admin_username     VARCHAR(150)  NOT NULL,
    requested_contact_email      VARCHAR(254)  NOT NULL,
    final_band_name              VARCHAR(200)  NOT NULL,
    final_band_slug              VARCHAR(64)   NOT NULL,
    final_admin_username         VARCHAR(150)  NOT NULL,
    final_contact_email          VARCHAR(254)  NOT NULL,
    status                       VARCHAR(20)   NOT NULL DEFAULT 'pending',
    privacy_accepted_at          DATETIME(3)   NOT NULL,
    decision_note                VARCHAR(1000) NOT NULL DEFAULT '',
    decided_by_user_id           BIGINT        NULL,
    decided_by_username          VARCHAR(150)  NOT NULL DEFAULT '',
    decided_at                   DATETIME(3)   NULL,
    band_id                      BIGINT        NULL,
    admin_user_id                BIGINT        NULL,
    setup_code_encrypted         TEXT          NULL,
    credentials_available_until  DATETIME(3)   NULL,
    claimed_at                   DATETIME(3)   NULL,
    expires_at                   DATETIME(3)   NOT NULL,
    created_at                   DATETIME(3)   NOT NULL,
    updated_at                   DATETIME(3)   NOT NULL,
    UNIQUE KEY uq_band_registration_public_id (public_id),
    UNIQUE KEY uq_band_registration_token_hash (token_hash),
    KEY idx_band_registration_status_created (status, created_at),
    KEY idx_band_registration_expires (expires_at),
    CONSTRAINT fk_band_registration_decider FOREIGN KEY (decided_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT fk_band_registration_band FOREIGN KEY (band_id)
        REFERENCES bands (id) ON DELETE SET NULL,
    CONSTRAINT fk_band_registration_admin FOREIGN KEY (admin_user_id)
        REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT ck_band_registration_status CHECK (status IN
        ('pending','approved','rejected','expired'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
