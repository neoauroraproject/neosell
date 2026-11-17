-- The selection a service was bought with (used to price renewals) and
-- pay-as-you-go billing state.
ALTER TABLE services ADD COLUMN selection TEXT NOT NULL DEFAULT '{}';
ALTER TABLE services ADD COLUMN payg_per_gb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN payg_per_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN payg_billed_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN payg_billed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN refunded_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_orders_discount ON orders(discount_id, user_id);
