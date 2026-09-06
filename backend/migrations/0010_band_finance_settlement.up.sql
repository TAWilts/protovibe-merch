ALTER TABLE band_transactions
    ADD COLUMN is_settled TINYINT(1) NOT NULL DEFAULT 1 AFTER amount_cents,
    ADD COLUMN settled_at DATETIME(3) NULL AFTER is_settled,
    ADD COLUMN settled_by_user_id BIGINT NULL AFTER settled_at,
    ADD COLUMN settled_by_username VARCHAR(150) NOT NULL DEFAULT '' AFTER settled_by_user_id,
    ADD KEY idx_band_transactions_settlement (band_id, is_cancelled, is_settled);

UPDATE band_transactions
SET settled_at = created_at
WHERE is_settled = 1 AND settled_at IS NULL;

ALTER TABLE recurring_band_transactions
    ADD COLUMN is_settled TINYINT(1) NOT NULL DEFAULT 1 AFTER amount_cents;
