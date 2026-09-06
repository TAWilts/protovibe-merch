ALTER TABLE recurring_band_transactions
    DROP COLUMN is_asset;

ALTER TABLE band_transactions
    DROP KEY idx_band_transactions_asset,
    DROP COLUMN is_asset;

ALTER TABLE purchases
    DROP KEY idx_purchases_cancelled,
    DROP COLUMN cancelled_by_username,
    DROP COLUMN cancelled_by_user_id,
    DROP COLUMN cancelled_at,
    DROP COLUMN is_cancelled;
