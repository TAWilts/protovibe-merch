ALTER TABLE band_transactions
    ADD COLUMN account_holder_user_id BIGINT NULL AFTER amount_cents,
    ADD COLUMN account_holder_username VARCHAR(150) NOT NULL DEFAULT '' AFTER account_holder_user_id,
    ADD KEY idx_band_transactions_account_holder
        (band_id, account_holder_user_id, is_cancelled, is_settled, transaction_on);

ALTER TABLE recurring_band_transactions
    ADD COLUMN account_holder_user_id BIGINT NULL AFTER amount_cents,
    ADD COLUMN account_holder_username VARCHAR(150) NOT NULL DEFAULT '' AFTER account_holder_user_id,
    ADD KEY idx_recurring_band_transactions_account_holder
        (band_id, account_holder_user_id);
