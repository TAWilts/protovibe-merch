-- Initial schema for the multi-tenant Merch Manager.
--
-- Ported from the single-band SQLite schema of the Flask version with three
-- systematic changes:
--   1. every operational table carries band_id and its unique keys are scoped
--      to the band, so two bands may both have an article called "Shirt";
--   2. the account and operational audit logs are merged into one table that
--      also records which support-access grant an action ran under;
--   3. the SQLCipher key envelopes are gone — the database is protected by
--      transport security and host-level volume encryption instead.
--
-- Money is always an integer number of cents. Stock is never stored; it is
-- derived from purchase and sale movements.

-- ---------------------------------------------------------------- tenants --

CREATE TABLE bands (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    slug                VARCHAR(64)  NOT NULL,
    name                VARCHAR(200) NOT NULL,
    contact_email       VARCHAR(254) NOT NULL DEFAULT '',
    is_active           TINYINT(1)   NOT NULL DEFAULT 1,
    deactivated_at      DATETIME(3)  NULL,
    -- A soft delete with a grace period, so a band removed by mistake can be
    -- restored without going to a backup.
    deleted_at          DATETIME(3)  NULL,
    maintenance_message VARCHAR(500) NOT NULL DEFAULT '',
    storage_quota_bytes BIGINT       NOT NULL DEFAULT 0,
    user_quota          INT          NOT NULL DEFAULT 0,
    feature_flags       JSON         NOT NULL,
    created_at          DATETIME(3)  NOT NULL,
    updated_at          DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_bands_slug (slug),
    KEY idx_bands_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- --------------------------------------------------------------- accounts --

CREATE TABLE users (
    id                           BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    -- NULL identifies a platform account. Those belong to no band and reach
    -- band data only through a live support_access_grant.
    band_id                      BIGINT       NULL,
    username                     VARCHAR(150) NOT NULL,
    password_hash                VARCHAR(255) NOT NULL,
    role                         VARCHAR(20)  NOT NULL DEFAULT 'seller',
    is_active                    TINYINT(1)   NOT NULL DEFAULT 1,
    must_set_password            TINYINT(1)   NOT NULL DEFAULT 0,
    setup_code_hash              VARCHAR(255) NOT NULL DEFAULT '',
    setup_code_expires_at        DATETIME(3)  NULL,
    -- TOTP secrets are encrypted with a key derived from SECRET_KEY; recovery
    -- codes are one-way hashes because they are only ever compared.
    mfa_secret_encrypted         TEXT         NULL,
    mfa_pending_secret_encrypted TEXT         NULL,
    mfa_recovery_code_hashes     JSON         NOT NULL,
    mfa_enabled                  TINYINT(1)   NOT NULL DEFAULT 0,
    mfa_enrolled_at              DATETIME(3)  NULL,
    -- Bumping this invalidates every existing session for the user.
    session_version              INT          NOT NULL DEFAULT 0,
    last_login_at                DATETIME(3)  NULL,
    ui_theme                     VARCHAR(20)  NOT NULL DEFAULT 'aurora',
    ui_language                  VARCHAR(5)   NOT NULL DEFAULT 'de',
    show_variant_photos          TINYINT(1)   NOT NULL DEFAULT 0,
    created_at                   DATETIME(3)  NOT NULL,
    updated_at                   DATETIME(3)  NOT NULL,
    -- utf8mb4_unicode_ci is case-insensitive, matching the original's
    -- COLLATE NOCASE on usernames.
    UNIQUE KEY uq_users_band_username (band_id, username),
    KEY idx_users_role_active (role, is_active),
    CONSTRAINT fk_users_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_users_role CHECK (role IN
        ('seller','member','manager','band_admin','support_admin','system_admin')),
    -- Platform roles must not belong to a band; band roles must.
    CONSTRAINT ck_users_role_band CHECK (
        (role IN ('support_admin','system_admin') AND band_id IS NULL)
        OR (role IN ('seller','member','manager','band_admin') AND band_id IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE sessions (
    id              VARCHAR(64)  NOT NULL PRIMARY KEY,
    user_id         BIGINT       NOT NULL,
    -- The band this session currently operates on. For platform staff it is
    -- only set while acting_grant_id points at a live grant.
    band_id         BIGINT       NULL,
    acting_grant_id BIGINT       NULL,
    session_version INT          NOT NULL,
    csrf_token_hash VARCHAR(64)  NOT NULL,
    pos_mode        TINYINT(1)   NOT NULL DEFAULT 0,
    reauth_at       DATETIME(3)  NULL,
    user_agent      VARCHAR(255) NOT NULL DEFAULT '',
    ip_address      VARCHAR(45)  NOT NULL DEFAULT '',
    created_at      DATETIME(3)  NOT NULL,
    last_seen_at    DATETIME(3)  NOT NULL,
    expires_at      DATETIME(3)  NOT NULL,
    KEY idx_sessions_user (user_id),
    KEY idx_sessions_expires (expires_at),
    KEY idx_sessions_band (band_id),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Short-lived state between a correct password and the second factor, or
-- between a setup code and the new password. Keeping it server-side means a
-- stolen cookie cannot skip a step.
CREATE TABLE pending_auth (
    id         VARCHAR(64) NOT NULL PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    purpose    VARCHAR(30) NOT NULL,
    created_at DATETIME(3) NOT NULL,
    expires_at DATETIME(3) NOT NULL,
    KEY idx_pending_auth_user (user_id),
    KEY idx_pending_auth_expires (expires_at),
    CONSTRAINT fk_pending_auth_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT ck_pending_auth_purpose CHECK (purpose IN
        ('mfa_login','mfa_enrollment','password_setup'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ------------------------------------------------------- control plane ----

-- The only path from a platform account to band data. There is deliberately
-- no break-glass: without a band admin's approval the status never reaches
-- 'active', and without 'active' the tenant scope is never set.
CREATE TABLE support_access_grants (
    id                        BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id                   BIGINT        NOT NULL,
    requested_by_user_id      BIGINT        NOT NULL,
    requested_by_username     VARCHAR(150)  NOT NULL,
    reason                    VARCHAR(1000) NOT NULL,
    scope                     VARCHAR(20)   NOT NULL,
    requested_duration_seconds INT          NOT NULL,
    status                    VARCHAR(20)   NOT NULL,
    decided_by_user_id        BIGINT        NULL,
    decided_by_username       VARCHAR(150)  NOT NULL DEFAULT '',
    decided_at                DATETIME(3)   NULL,
    decision_note             VARCHAR(1000) NOT NULL DEFAULT '',
    activated_at              DATETIME(3)   NULL,
    expires_at                DATETIME(3)   NULL,
    revoked_at                DATETIME(3)   NULL,
    revoked_by_user_id        BIGINT        NULL,
    created_at                DATETIME(3)   NOT NULL,
    updated_at                DATETIME(3)   NOT NULL,
    KEY idx_grants_band_status (band_id, status),
    KEY idx_grants_requester (requested_by_user_id),
    KEY idx_grants_expires (expires_at),
    CONSTRAINT fk_grants_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_grants_scope CHECK (scope IN ('read_only','read_write')),
    CONSTRAINT ck_grants_status CHECK (status IN
        ('pending','approved','denied','active','expired','revoked')),
    CONSTRAINT ck_grants_duration CHECK (requested_duration_seconds BETWEEN 300 AND 86400)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE sessions
    ADD CONSTRAINT fk_sessions_grant FOREIGN KEY (acting_grant_id)
        REFERENCES support_access_grants (id) ON DELETE SET NULL;

CREATE TABLE platform_settings (
    id                       BIGINT        NOT NULL PRIMARY KEY,
    maintenance_enabled      TINYINT(1)    NOT NULL DEFAULT 0,
    maintenance_message      VARCHAR(500)  NOT NULL DEFAULT '',
    announcement_text        VARCHAR(1000) NOT NULL DEFAULT '',
    announcement_level       VARCHAR(20)   NOT NULL DEFAULT 'info',
    announcement_expires_at  DATETIME(3)   NULL,
    smtp_enabled             TINYINT(1)    NOT NULL DEFAULT 0,
    smtp_host                VARCHAR(255)  NOT NULL DEFAULT '',
    smtp_port                INT           NOT NULL DEFAULT 465,
    smtp_security            VARCHAR(20)   NOT NULL DEFAULT 'ssl',
    smtp_username            VARCHAR(255)  NOT NULL DEFAULT '',
    smtp_password_encrypted  TEXT          NULL,
    smtp_from                VARCHAR(254)  NOT NULL DEFAULT '',
    smtp_timeout_seconds     INT           NOT NULL DEFAULT 8,
    notification_email       VARCHAR(254)  NOT NULL DEFAULT '',
    updated_at               DATETIME(3)   NOT NULL,
    updated_by_user_id       BIGINT        NULL,
    updated_by_username      VARCHAR(150)  NOT NULL DEFAULT '',
    CONSTRAINT ck_platform_settings_single_row CHECK (id = 1),
    CONSTRAINT ck_platform_settings_security CHECK (smtp_security IN ('ssl','starttls','none'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE backup_runs (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    -- NULL means a full-instance dump rather than a single band.
    band_id             BIGINT       NULL,
    status              VARCHAR(20)  NOT NULL,
    trigger_kind        VARCHAR(20)  NOT NULL,
    path                VARCHAR(500) NOT NULL DEFAULT '',
    size_bytes          BIGINT       NOT NULL DEFAULT 0,
    error               TEXT         NULL,
    started_at          DATETIME(3)  NOT NULL,
    finished_at         DATETIME(3)  NULL,
    started_by_user_id  BIGINT       NULL,
    started_by_username VARCHAR(150) NOT NULL DEFAULT '',
    KEY idx_backup_runs_band (band_id),
    KEY idx_backup_runs_started (started_at),
    KEY idx_backup_runs_status (status),
    CONSTRAINT fk_backup_runs_band FOREIGN KEY (band_id) REFERENCES bands (id) ON DELETE SET NULL,
    CONSTRAINT ck_backup_runs_status CHECK (status IN ('running','succeeded','failed')),
    CONSTRAINT ck_backup_runs_trigger CHECK (trigger_kind IN ('scheduled','manual','pre_restore'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- -------------------------------------------------------------- catalogue --

CREATE TABLE articles (
    id                           BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id                      BIGINT       NOT NULL,
    name                         VARCHAR(200) NOT NULL,
    default_sale_price_cents     BIGINT       NOT NULL DEFAULT 0,
    default_purchase_price_cents BIGINT       NOT NULL DEFAULT 0,
    -- is_offered is independent of is_active: an article can leave the sales
    -- assortment while its bookings, stock and future purchases stay intact.
    is_offered                   TINYINT(1)   NOT NULL DEFAULT 1,
    is_active                    TINYINT(1)   NOT NULL DEFAULT 1,
    created_at                   DATETIME(3)  NOT NULL,
    updated_at                   DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_articles_band_name (band_id, name),
    CONSTRAINT fk_articles_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_articles_sale_price CHECK (default_sale_price_cents >= 0),
    CONSTRAINT ck_articles_purchase_price CHECK (default_purchase_price_cents >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE option_groups (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id    BIGINT       NOT NULL,
    article_id BIGINT       NOT NULL,
    name       VARCHAR(120) NOT NULL,
    position   INT          NOT NULL DEFAULT 0,
    -- Removing an option deactivates it. Historic receipts must keep resolving
    -- their option names, including after a rename.
    is_active  TINYINT(1)   NOT NULL DEFAULT 1,
    created_at DATETIME(3)  NOT NULL,
    updated_at DATETIME(3)  NOT NULL,
    KEY idx_option_groups_article (band_id, article_id, position),
    CONSTRAINT fk_option_groups_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_option_groups_article FOREIGN KEY (article_id) REFERENCES articles (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE option_values (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id         BIGINT       NOT NULL,
    option_group_id BIGINT       NOT NULL,
    value           VARCHAR(120) NOT NULL,
    position        INT          NOT NULL DEFAULT 0,
    is_active       TINYINT(1)   NOT NULL DEFAULT 1,
    created_at      DATETIME(3)  NOT NULL,
    updated_at      DATETIME(3)  NOT NULL,
    KEY idx_option_values_group (band_id, option_group_id, position),
    CONSTRAINT fk_option_values_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_option_values_group FOREIGN KEY (option_group_id) REFERENCES option_groups (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE variants (
    id                           BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id                      BIGINT       NOT NULL,
    article_id                   BIGINT       NOT NULL,
    option_value_ids             JSON         NOT NULL,
    -- The sorted option_value_ids joined by "|". This is what makes a variant
    -- unique inside its article.
    combination_key              VARCHAR(255) NOT NULL,
    sale_price_cents             BIGINT       NOT NULL DEFAULT 0,
    default_purchase_price_cents BIGINT       NOT NULL DEFAULT 0,
    -- NULL means no minimum-stock warning is configured, which keeps an
    -- explicit 0 meaningful: warn only once the variant is actually sold out.
    minimum_stock                INT          NULL,
    is_offered                   TINYINT(1)   NOT NULL DEFAULT 1,
    no_reorder                   TINYINT(1)   NOT NULL DEFAULT 0,
    is_active                    TINYINT(1)   NOT NULL DEFAULT 1,
    created_at                   DATETIME(3)  NOT NULL,
    updated_at                   DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_variants_article_combination (band_id, article_id, combination_key),
    KEY idx_variants_article (band_id, article_id, is_active),
    CONSTRAINT fk_variants_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_variants_article FOREIGN KEY (article_id) REFERENCES articles (id),
    CONSTRAINT ck_variants_sale_price CHECK (sale_price_cents >= 0),
    CONSTRAINT ck_variants_purchase_price CHECK (default_purchase_price_cents >= 0),
    CONSTRAINT ck_variants_minimum_stock CHECK (minimum_stock IS NULL OR minimum_stock >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Image bytes live in the file store; only the opaque managed filename is kept
-- here, so database dumps stay small.
CREATE TABLE variant_photos (
    id                   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id              BIGINT       NOT NULL,
    variant_id           BIGINT       NOT NULL,
    file_path            VARCHAR(255) NOT NULL,
    original_filename    VARCHAR(255) NOT NULL,
    position             INT          NOT NULL DEFAULT 0,
    include_in_slideshow TINYINT(1)   NOT NULL DEFAULT 1,
    show_price           TINYINT(1)   NOT NULL DEFAULT 1,
    size_bytes           BIGINT       NOT NULL DEFAULT 0,
    created_at           DATETIME(3)  NOT NULL,
    created_by_user_id   BIGINT       NULL,
    created_by_username  VARCHAR(150) NOT NULL DEFAULT '',
    UNIQUE KEY uq_variant_photos_path (file_path),
    KEY idx_variant_photos_variant (band_id, variant_id, position, id),
    KEY idx_variant_photos_slideshow (band_id, include_in_slideshow),
    CONSTRAINT fk_variant_photos_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_variant_photos_variant FOREIGN KEY (variant_id) REFERENCES variants (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Display pictures without a product relation, for example a price overview.
CREATE TABLE slideshow_extra_photos (
    id                   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id              BIGINT       NOT NULL,
    file_path            VARCHAR(255) NOT NULL,
    original_filename    VARCHAR(255) NOT NULL,
    position             INT          NOT NULL DEFAULT 0,
    include_in_slideshow TINYINT(1)   NOT NULL DEFAULT 1,
    show_price           TINYINT(1)   NOT NULL DEFAULT 1,
    size_bytes           BIGINT       NOT NULL DEFAULT 0,
    created_at           DATETIME(3)  NOT NULL,
    created_by_user_id   BIGINT       NULL,
    created_by_username  VARCHAR(150) NOT NULL DEFAULT '',
    UNIQUE KEY uq_slideshow_extra_photos_path (file_path),
    KEY idx_slideshow_extra_photos_position (band_id, position, id),
    CONSTRAINT fk_slideshow_extra_photos_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE slideshow_settings (
    id                  BIGINT      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id             BIGINT      NOT NULL,
    collage_show_prices TINYINT(1)  NOT NULL DEFAULT 1,
    updated_at          DATETIME(3) NOT NULL,
    UNIQUE KEY uq_slideshow_settings_band (band_id),
    CONSTRAINT fk_slideshow_settings_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ------------------------------------------------------- sales & receipts --

CREATE TABLE sale_events (
    id               BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id          BIGINT       NOT NULL,
    name             VARCHAR(200) NOT NULL,
    created_at       DATETIME(3)  NOT NULL,
    last_selected_at DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_sale_events_band_name (band_id, name),
    KEY idx_sale_events_last_selected (band_id, last_selected_at),
    CONSTRAINT fk_sale_events_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The selected event is shared across the band rather than per user, so
-- several phones at the same stand book against one event.
CREATE TABLE sale_event_state (
    id         BIGINT      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id    BIGINT      NOT NULL,
    event_id   BIGINT      NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE KEY uq_sale_event_state_band (band_id),
    CONSTRAINT fk_sale_event_state_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_sale_event_state_event FOREIGN KEY (event_id) REFERENCES sale_events (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE sales (
    id                 BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id            BIGINT        NOT NULL,
    -- A basket with several positions shares one receipt_id, so history shows
    -- one purchase while stock, payment and delivery stay per item.
    receipt_id         VARCHAR(40)   NOT NULL,
    variant_id         BIGINT        NOT NULL,
    quantity           INT           NOT NULL,
    unit_price_cents   BIGINT        NOT NULL,
    amount_due_cents   BIGINT        NOT NULL,
    -- NULL while the sale is unpaid.
    amount_given_cents BIGINT        NULL,
    -- The overpayment, distributed across the basket to the cent so a single
    -- position can be cancelled without distorting the rest of the receipt.
    donation_cents     BIGINT        NOT NULL DEFAULT 0,
    payment_method     VARCHAR(40)   NOT NULL,
    is_paid            TINYINT(1)    NOT NULL DEFAULT 1,
    payment_follow_up  TINYINT(1)    NOT NULL DEFAULT 0,
    is_received        TINYINT(1)    NOT NULL DEFAULT 1,
    delivery_status    VARCHAR(20)   NOT NULL DEFAULT 'not_applicable',
    -- A cancellation keeps the booking readable while removing its effect from
    -- stock, balances and the work queues.
    is_cancelled       TINYINT(1)    NOT NULL DEFAULT 0,
    cancelled_at       DATETIME(3)   NULL,
    customer_name      VARCHAR(200)  NOT NULL DEFAULT '',
    customer_address   VARCHAR(500)  NOT NULL DEFAULT '',
    -- An immutable snapshot, so exports and offline clients stay readable
    -- independently of later catalogue edits.
    event_name         VARCHAR(200)  NOT NULL DEFAULT '',
    sold_by            VARCHAR(150)  NOT NULL DEFAULT '',
    comment            VARCHAR(1000) NOT NULL DEFAULT '',
    sold_on            DATE          NOT NULL,
    created_at         DATETIME(3)   NOT NULL,
    -- Deliberately not a foreign key: deleting an account must never make a
    -- historic booking unreadable, which is what the username snapshot is for.
    created_by_user_id BIGINT        NULL,
    created_by_username VARCHAR(150) NOT NULL DEFAULT '',
    KEY idx_sales_receipt (band_id, receipt_id),
    KEY idx_sales_variant (band_id, variant_id),
    KEY idx_sales_sold_on (band_id, sold_on),
    KEY idx_sales_delivery (band_id, delivery_status, is_cancelled),
    KEY idx_sales_payment_follow_up (band_id, payment_follow_up, is_cancelled),
    KEY idx_sales_cancelled (band_id, is_cancelled),
    CONSTRAINT fk_sales_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_sales_variant FOREIGN KEY (variant_id) REFERENCES variants (id),
    CONSTRAINT ck_sales_quantity CHECK (quantity > 0),
    CONSTRAINT ck_sales_unit_price CHECK (unit_price_cents >= 0),
    CONSTRAINT ck_sales_amount_due CHECK (amount_due_cents >= 0),
    CONSTRAINT ck_sales_donation CHECK (donation_cents >= 0),
    CONSTRAINT ck_sales_delivery_status CHECK (delivery_status IN
        ('not_applicable','pending','shipped','received'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Offline clients never write ledger rows directly. They replay a durable
-- client-generated event ID once a connection is back; storing the exact
-- response makes a retry idempotent even if the browser lost the first answer.
CREATE TABLE sync_events (
    id                BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id           BIGINT       NOT NULL,
    event_id          VARCHAR(64)  NOT NULL,
    event_type        VARCHAR(20)  NOT NULL,
    actor_user_id     BIGINT       NOT NULL,
    actor_username    VARCHAR(150) NOT NULL DEFAULT '',
    device_id         VARCHAR(64)  NOT NULL,
    -- A reused event ID carrying different data is a conflict (409), not an
    -- accepted duplicate.
    payload_hash      VARCHAR(64)  NOT NULL,
    client_created_at DATETIME(3)  NOT NULL,
    response_json     LONGTEXT     NOT NULL,
    created_at        DATETIME(3)  NOT NULL,
    UNIQUE KEY uq_sync_events_band_event (band_id, event_id),
    KEY idx_sync_events_actor (band_id, actor_user_id, created_at),
    CONSTRAINT fk_sync_events_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_sync_events_type CHECK (event_type IN ('sale'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE payment_qr_settings (
    id                   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id              BIGINT       NOT NULL,
    paypal_me_url        VARCHAR(255) NOT NULL DEFAULT '',
    bank_account_holder  VARCHAR(200) NOT NULL DEFAULT '',
    bank_iban            VARCHAR(34)  NOT NULL DEFAULT '',
    bank_bic             VARCHAR(11)  NOT NULL DEFAULT '',
    bank_remittance_text VARCHAR(140) NOT NULL DEFAULT 'Merch-Kauf',
    updated_at           DATETIME(3)  NOT NULL,
    updated_by_user_id   BIGINT       NULL,
    updated_by_username  VARCHAR(150) NOT NULL DEFAULT '',
    UNIQUE KEY uq_payment_qr_settings_band (band_id),
    CONSTRAINT fk_payment_qr_settings_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Showing a code is deliberately not a sale: the intent reserves the receipt
-- ID and the quoted amount, but no stock or ledger row changes until the
-- seller confirms.
CREATE TABLE payment_qr_intents (
    token               VARCHAR(64) NOT NULL PRIMARY KEY,
    band_id             BIGINT      NOT NULL,
    receipt_id          VARCHAR(40) NOT NULL,
    sale_payload_json   LONGTEXT    NOT NULL,
    created_by_user_id  BIGINT      NOT NULL,
    created_at          DATETIME(3) NOT NULL,
    expires_at          DATETIME(3) NOT NULL,
    cancelled_at        DATETIME(3) NULL,
    consumed_at         DATETIME(3) NULL,
    response_json       LONGTEXT    NULL,
    UNIQUE KEY uq_payment_qr_intents_receipt (band_id, receipt_id),
    KEY idx_payment_qr_intents_creator (band_id, created_by_user_id, expires_at),
    CONSTRAINT fk_payment_qr_intents_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ------------------------------------------------------------- purchases --

CREATE TABLE purchases (
    id                        BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id                   BIGINT        NOT NULL,
    -- Like a sales basket, one goods-receipt receipt can hold several lines.
    receipt_id                VARCHAR(40)   NOT NULL,
    variant_id                BIGINT        NOT NULL,
    quantity                  INT           NOT NULL,
    unit_cost_cents           BIGINT        NOT NULL,
    purchased_on              DATE          NOT NULL,
    supplier                  VARCHAR(200)  NOT NULL DEFAULT '',
    -- A typed invoice number stays useful even when a document is attached.
    invoice_reference         VARCHAR(200)  NOT NULL DEFAULT '',
    invoice_file_path         VARCHAR(255)  NULL,
    invoice_original_filename VARCHAR(255)  NOT NULL DEFAULT '',
    invoice_size_bytes        BIGINT        NOT NULL DEFAULT 0,
    comment                   VARCHAR(1000) NOT NULL DEFAULT '',
    created_at                DATETIME(3)   NOT NULL,
    updated_at                DATETIME(3)   NOT NULL,
    created_by_user_id        BIGINT        NULL,
    created_by_username       VARCHAR(150)  NOT NULL DEFAULT '',
    UNIQUE KEY uq_purchases_invoice_path (invoice_file_path),
    KEY idx_purchases_receipt (band_id, receipt_id),
    KEY idx_purchases_variant (band_id, variant_id, purchased_on),
    CONSTRAINT fk_purchases_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_purchases_variant FOREIGN KEY (variant_id) REFERENCES variants (id),
    CONSTRAINT ck_purchases_quantity CHECK (quantity > 0),
    CONSTRAINT ck_purchases_unit_cost CHECK (unit_cost_cents >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE purchase_receipt_attachments (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id             BIGINT       NOT NULL,
    receipt_id          VARCHAR(40)  NOT NULL,
    file_path           VARCHAR(255) NOT NULL,
    original_filename   VARCHAR(255) NOT NULL,
    size_bytes          BIGINT       NOT NULL DEFAULT 0,
    created_at          DATETIME(3)  NOT NULL,
    created_by_user_id  BIGINT       NULL,
    created_by_username VARCHAR(150) NOT NULL DEFAULT '',
    UNIQUE KEY uq_purchase_receipt_attachments_path (file_path),
    KEY idx_purchase_receipt_attachments_receipt (band_id, receipt_id),
    CONSTRAINT fk_purchase_receipt_attachments_band FOREIGN KEY (band_id) REFERENCES bands (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- --------------------------------------------------------- band finances --

-- A separate ledger on purpose: gigs, royalties and equipment are tracked
-- alongside merch without ever changing a historic merch balance.
CREATE TABLE band_transactions (
    id                    BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id               BIGINT       NOT NULL,
    transaction_type      VARCHAR(20)  NOT NULL,
    transaction_on        DATE         NOT NULL,
    category              VARCHAR(120) NOT NULL,
    description           VARCHAR(500) NOT NULL,
    amount_cents          BIGINT       NOT NULL,
    is_cancelled          TINYINT(1)   NOT NULL DEFAULT 0,
    cancelled_at          DATETIME(3)  NULL,
    cancelled_by_user_id  BIGINT       NULL,
    cancelled_by_username VARCHAR(150) NOT NULL DEFAULT '',
    created_at            DATETIME(3)  NOT NULL,
    updated_at            DATETIME(3)  NOT NULL,
    created_by_user_id    BIGINT       NULL,
    created_by_username   VARCHAR(150) NOT NULL DEFAULT '',
    KEY idx_band_transactions_on (band_id, transaction_on),
    KEY idx_band_transactions_type (band_id, transaction_type, is_cancelled),
    CONSTRAINT fk_band_transactions_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_band_transactions_type CHECK (transaction_type IN ('income','expense')),
    CONSTRAINT ck_band_transactions_amount CHECK (amount_cents > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE band_transaction_attachments (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id             BIGINT       NOT NULL,
    transaction_id      BIGINT       NOT NULL,
    file_path           VARCHAR(255) NOT NULL,
    original_filename   VARCHAR(255) NOT NULL,
    size_bytes          BIGINT       NOT NULL DEFAULT 0,
    created_at          DATETIME(3)  NOT NULL,
    created_by_user_id  BIGINT       NULL,
    created_by_username VARCHAR(150) NOT NULL DEFAULT '',
    UNIQUE KEY uq_band_transaction_attachments_path (file_path),
    KEY idx_band_transaction_attachments_transaction (band_id, transaction_id),
    CONSTRAINT fk_band_transaction_attachments_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_band_transaction_attachments_transaction FOREIGN KEY (transaction_id)
        REFERENCES band_transactions (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ----------------------------------------------------- support & audit ----

CREATE TABLE admin_messages (
    id                   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id              BIGINT       NOT NULL,
    -- The username snapshot keeps the inbox readable after the sender's
    -- account is removed.
    sender_user_id       BIGINT       NULL,
    sender_username      VARCHAR(150) NOT NULL,
    sender_email         VARCHAR(254) NOT NULL DEFAULT '',
    message_type         VARCHAR(20)  NOT NULL,
    subject              VARCHAR(200) NOT NULL,
    body                 TEXT         NOT NULL,
    is_resolved          TINYINT(1)   NOT NULL DEFAULT 0,
    resolved_at          DATETIME(3)  NULL,
    resolved_by_user_id  BIGINT       NULL,
    resolved_by_username VARCHAR(150) NOT NULL DEFAULT '',
    created_at           DATETIME(3)  NOT NULL,
    KEY idx_admin_messages_created (created_at, id),
    KEY idx_admin_messages_band_resolution (band_id, is_resolved, created_at),
    CONSTRAINT fk_admin_messages_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_admin_messages_type CHECK (message_type IN ('issue','question'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Account and operational audit entries share one table. acting_grant_id is
-- what makes "who looked at our data, when, and under which approval"
-- answerable for a band.
CREATE TABLE audit_log (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id         BIGINT       NULL,
    user_id         BIGINT       NULL,
    username        VARCHAR(150) NOT NULL DEFAULT '',
    acting_grant_id BIGINT       NULL,
    action          VARCHAR(80)  NOT NULL,
    entity_type     VARCHAR(60)  NOT NULL,
    entity_id       BIGINT       NULL,
    details         JSON         NOT NULL,
    ip_address      VARCHAR(45)  NOT NULL DEFAULT '',
    created_at      DATETIME(3)  NOT NULL,
    KEY idx_audit_log_band_created (band_id, created_at, id),
    KEY idx_audit_log_action (action, created_at),
    KEY idx_audit_log_user (user_id, created_at),
    KEY idx_audit_log_grant (acting_grant_id),
    CONSTRAINT fk_audit_log_band FOREIGN KEY (band_id) REFERENCES bands (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO platform_settings (id, updated_at) VALUES (1, UTC_TIMESTAMP(3));
