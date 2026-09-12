ALTER TABLE recurring_band_transactions
    DROP KEY idx_recurring_band_transactions_account_holder,
    DROP COLUMN account_holder_username,
    DROP COLUMN account_holder_user_id;

ALTER TABLE band_transactions
    DROP KEY idx_band_transactions_account_holder,
    DROP COLUMN account_holder_username,
    DROP COLUMN account_holder_user_id;
