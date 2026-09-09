ALTER TABLE purchases
    DROP CONSTRAINT ck_purchases_line_total_cost,
    DROP CONSTRAINT ck_purchases_price_mode,
    DROP COLUMN line_total_cost_cents,
    DROP COLUMN price_mode;

ALTER TABLE variants
    DROP CONSTRAINT ck_variants_target_stock,
    DROP COLUMN target_stock;
