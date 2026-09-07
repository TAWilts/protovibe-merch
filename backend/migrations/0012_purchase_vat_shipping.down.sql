ALTER TABLE purchases
    DROP COLUMN shipping_cost_cents,
    DROP COLUMN vat_rate_basis_points,
    DROP COLUMN prices_include_vat;
