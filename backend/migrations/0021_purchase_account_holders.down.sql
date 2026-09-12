ALTER TABLE purchases
    DROP KEY idx_purchases_account_holder,
    DROP COLUMN account_holder_username,
    DROP COLUMN account_holder_user_id;
