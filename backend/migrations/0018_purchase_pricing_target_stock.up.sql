ALTER TABLE variants
    ADD COLUMN target_stock INT NULL AFTER minimum_stock,
    ADD CONSTRAINT ck_variants_target_stock CHECK (target_stock IS NULL OR target_stock >= 0);

ALTER TABLE purchases
    ADD COLUMN price_mode VARCHAR(20) NOT NULL DEFAULT 'unit' AFTER unit_cost_cents,
    ADD COLUMN line_total_cost_cents BIGINT NOT NULL DEFAULT 0 AFTER price_mode,
    ADD CONSTRAINT ck_purchases_price_mode CHECK (price_mode IN ('unit','basket')),
    ADD CONSTRAINT ck_purchases_line_total_cost CHECK (line_total_cost_cents >= 0);

UPDATE purchases
SET line_total_cost_cents = quantity * unit_cost_cents;

UPDATE variants v
LEFT JOIN (
    SELECT variant_id, SUM(quantity) AS quantity
    FROM purchases
    WHERE is_cancelled = 0
    GROUP BY variant_id
) purchased ON purchased.variant_id = v.id
LEFT JOIN (
    SELECT variant_id, SUM(quantity) AS quantity
    FROM sales
    WHERE is_cancelled = 0 AND line_type = 'merchandise' AND variant_id IS NOT NULL
    GROUP BY variant_id
) sold ON sold.variant_id = v.id
SET v.is_offered = 0
WHERE v.no_reorder = 1
  AND COALESCE(purchased.quantity, 0) - COALESCE(sold.quantity, 0) <= 0;

UPDATE articles a
SET a.is_offered = 0
WHERE EXISTS (
    SELECT 1 FROM variants active_variant
    WHERE active_variant.article_id = a.id AND active_variant.is_active = 1
)
AND NOT EXISTS (
    SELECT 1
    FROM variants v
    LEFT JOIN (
        SELECT variant_id, SUM(quantity) AS quantity
        FROM purchases
        WHERE is_cancelled = 0
        GROUP BY variant_id
    ) purchased ON purchased.variant_id = v.id
    LEFT JOIN (
        SELECT variant_id, SUM(quantity) AS quantity
        FROM sales
        WHERE is_cancelled = 0 AND line_type = 'merchandise' AND variant_id IS NOT NULL
        GROUP BY variant_id
    ) sold ON sold.variant_id = v.id
    WHERE v.article_id = a.id
      AND v.is_active = 1
      AND (
          v.no_reorder = 0
          OR COALESCE(purchased.quantity, 0) - COALESCE(sold.quantity, 0) > 0
      )
);
