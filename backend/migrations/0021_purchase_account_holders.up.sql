ALTER TABLE purchases
    -- No foreign key: deleted users must not invalidate historical receipts.
    ADD COLUMN account_holder_user_id BIGINT NULL AFTER shipping_cost_cents,
    ADD COLUMN account_holder_username VARCHAR(150) NOT NULL DEFAULT '' AFTER account_holder_user_id,
    ADD KEY idx_purchases_account_holder
        (band_id, account_holder_user_id, is_cancelled, purchased_on);
