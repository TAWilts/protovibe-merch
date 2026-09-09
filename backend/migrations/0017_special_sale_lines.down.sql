ALTER TABLE sales
    DROP FOREIGN KEY fk_sales_variant;

ALTER TABLE sales
    DROP CONSTRAINT ck_sales_line_type;

DELETE FROM sales WHERE line_type <> 'merchandise';

ALTER TABLE sales
    MODIFY COLUMN variant_id BIGINT NOT NULL,
    DROP COLUMN line_description,
    DROP COLUMN line_type;

ALTER TABLE sales
    ADD CONSTRAINT fk_sales_variant FOREIGN KEY (variant_id) REFERENCES variants (id);
