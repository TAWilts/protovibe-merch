ALTER TABLE purchases
    ADD COLUMN prices_include_vat TINYINT(1) NOT NULL DEFAULT 1 AFTER unit_cost_cents,
    ADD COLUMN vat_rate_basis_points INT NOT NULL DEFAULT 1900 AFTER prices_include_vat,
    ADD COLUMN shipping_cost_cents BIGINT NOT NULL DEFAULT 0 AFTER vat_rate_basis_points;
