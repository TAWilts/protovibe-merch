ALTER TABLE sales
    DROP CONSTRAINT ck_sales_shipping_cost,
    DROP COLUMN shipping_cost_cents;

ALTER TABLE variants
    ADD COLUMN default_purchase_price_cents BIGINT NOT NULL DEFAULT 0 AFTER sale_price_cents,
    ADD CONSTRAINT ck_variants_purchase_price CHECK (default_purchase_price_cents >= 0);

ALTER TABLE articles
    ADD COLUMN default_purchase_price_cents BIGINT NOT NULL DEFAULT 0 AFTER default_sale_price_cents,
    ADD CONSTRAINT ck_articles_purchase_price CHECK (default_purchase_price_cents >= 0);
