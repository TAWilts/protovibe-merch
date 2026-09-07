ALTER TABLE articles
    DROP CONSTRAINT IF EXISTS ck_articles_purchase_price,
    DROP COLUMN IF EXISTS default_purchase_price_cents;

ALTER TABLE variants
    DROP CONSTRAINT IF EXISTS ck_variants_purchase_price,
    DROP COLUMN IF EXISTS default_purchase_price_cents;

ALTER TABLE sales
    ADD COLUMN shipping_cost_cents BIGINT NOT NULL DEFAULT 0 AFTER amount_due_cents,
    ADD CONSTRAINT ck_sales_shipping_cost CHECK (shipping_cost_cents >= 0);
