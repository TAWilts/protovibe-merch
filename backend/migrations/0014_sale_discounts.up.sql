ALTER TABLE sales
    ADD COLUMN discount_cents BIGINT NOT NULL DEFAULT 0 AFTER shipping_cost_cents,
    ADD CONSTRAINT ck_sales_discount CHECK (discount_cents >= 0);
