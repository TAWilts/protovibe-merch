ALTER TABLE sales
    DROP CONSTRAINT IF EXISTS ck_sales_discount,
    DROP COLUMN IF EXISTS discount_cents;
