ALTER TABLE sales
    DROP FOREIGN KEY fk_sales_variant;

ALTER TABLE sales
    MODIFY COLUMN variant_id BIGINT NULL,
    ADD COLUMN line_type VARCHAR(20) NOT NULL DEFAULT 'merchandise' AFTER receipt_id,
    ADD COLUMN line_description VARCHAR(200) NOT NULL DEFAULT '' AFTER variant_id,
    ADD CONSTRAINT ck_sales_line_type CHECK (line_type IN ('merchandise','donation','misc_income'));

ALTER TABLE sales
    ADD CONSTRAINT fk_sales_variant FOREIGN KEY (variant_id) REFERENCES variants (id);
