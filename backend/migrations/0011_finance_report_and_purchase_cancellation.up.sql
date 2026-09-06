ALTER TABLE purchases
    ADD COLUMN is_cancelled TINYINT(1) NOT NULL DEFAULT 0 AFTER comment,
    ADD COLUMN cancelled_at DATETIME(3) NULL AFTER is_cancelled,
    ADD COLUMN cancelled_by_user_id BIGINT NULL AFTER cancelled_at,
    ADD COLUMN cancelled_by_username VARCHAR(150) NOT NULL DEFAULT '' AFTER cancelled_by_user_id,
    ADD KEY idx_purchases_cancelled (band_id, is_cancelled, purchased_on);

ALTER TABLE band_transactions
    ADD COLUMN is_asset TINYINT(1) NOT NULL DEFAULT 0 AFTER is_settled,
    ADD KEY idx_band_transactions_asset (band_id, is_cancelled, is_asset);

ALTER TABLE recurring_band_transactions
    ADD COLUMN is_asset TINYINT(1) NOT NULL DEFAULT 0 AFTER is_settled;
