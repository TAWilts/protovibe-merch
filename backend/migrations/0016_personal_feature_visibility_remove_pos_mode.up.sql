ALTER TABLE users
    ADD COLUMN hide_packing_list TINYINT(1) NOT NULL DEFAULT 0 AFTER show_variant_photos,
    ADD COLUMN hide_product_palette TINYINT(1) NOT NULL DEFAULT 0 AFTER hide_packing_list;

ALTER TABLE sessions
    DROP COLUMN pos_mode;
