CREATE TABLE recurring_band_transactions (
    id                  BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id             BIGINT        NOT NULL,
    transaction_type    VARCHAR(20)   NOT NULL,
    start_on            DATE          NOT NULL,
    next_run_on         DATE          NOT NULL,
    category            VARCHAR(120)  NOT NULL,
    description         VARCHAR(500)  NOT NULL,
    amount_cents        BIGINT        NOT NULL,
    interval_value      INT           NOT NULL,
    interval_unit       VARCHAR(10)   NOT NULL,
    is_active           TINYINT(1)    NOT NULL DEFAULT 1,
    created_by_user_id  BIGINT        NULL,
    created_by_username VARCHAR(150)  NOT NULL DEFAULT '',
    created_at          DATETIME(3)   NOT NULL,
    updated_at          DATETIME(3)   NOT NULL,
    KEY idx_recurring_band_due (band_id, is_active, next_run_on),
    CONSTRAINT fk_recurring_band_transactions_band
        FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_recurring_band_transactions_type
        CHECK (transaction_type IN ('income','expense')),
    CONSTRAINT ck_recurring_band_transactions_amount
        CHECK (amount_cents > 0),
    CONSTRAINT ck_recurring_band_transactions_interval_value
        CHECK (interval_value > 0),
    CONSTRAINT ck_recurring_band_transactions_interval_unit
        CHECK (interval_unit IN ('day','week','month','year'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE recurring_band_transaction_runs (
    id                       BIGINT      NOT NULL AUTO_INCREMENT PRIMARY KEY,
    band_id                  BIGINT      NOT NULL,
    recurring_transaction_id BIGINT      NOT NULL,
    occurrence_on            DATE        NOT NULL,
    transaction_id           BIGINT      NOT NULL,
    created_at               DATETIME(3) NOT NULL,
    UNIQUE KEY uq_recurring_band_occurrence
        (band_id, recurring_transaction_id, occurrence_on),
    KEY idx_recurring_band_run_transaction (band_id, transaction_id),
    CONSTRAINT fk_recurring_band_runs_band
        FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_recurring_band_runs_rule
        FOREIGN KEY (recurring_transaction_id)
        REFERENCES recurring_band_transactions (id) ON DELETE CASCADE,
    CONSTRAINT fk_recurring_band_runs_transaction
        FOREIGN KEY (transaction_id)
        REFERENCES band_transactions (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
