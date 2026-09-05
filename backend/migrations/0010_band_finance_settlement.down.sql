ALTER TABLE recurring_band_transactions
    DROP COLUMN is_settled;

ALTER TABLE band_transactions
    DROP KEY idx_band_transactions_settlement,
    DROP COLUMN settled_by_username,
    DROP COLUMN settled_by_user_id,
    DROP COLUMN settled_at,
    DROP COLUMN is_settled;
