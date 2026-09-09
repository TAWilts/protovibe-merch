ALTER TABLE sessions
    ADD COLUMN pos_mode TINYINT(1) NOT NULL DEFAULT 0 AFTER csrf_token_hash;

ALTER TABLE users
    DROP COLUMN hide_product_palette,
    DROP COLUMN hide_packing_list;
