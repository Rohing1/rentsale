CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE SEQUENCE IF NOT EXISTS order_number_seq;

CREATE TABLE IF NOT EXISTS products (
    id BIGSERIAL PRIMARY KEY,
    sku VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(160) NOT NULL,
    product_type VARCHAR(40) NOT NULL DEFAULT 'general',
    category VARCHAR(80) NOT NULL DEFAULT '',
    location VARCHAR(120) NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    rent_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sell_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    daily_rent_cents BIGINT NOT NULL DEFAULT 0 CHECK (daily_rent_cents >= 0),
    sale_price_cents BIGINT NOT NULL DEFAULT 0 CHECK (sale_price_cents >= 0),
    deposit_cents BIGINT NOT NULL DEFAULT 0 CHECK (deposit_cents >= 0),
    stock_on_hand INTEGER NOT NULL DEFAULT 0 CHECK (stock_on_hand >= 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (rent_enabled OR sell_enabled)
);

CREATE TABLE IF NOT EXISTS customers (
    id BIGSERIAL PRIMARY KEY,
    full_name VARCHAR(120) NOT NULL,
    phone VARCHAR(40) NOT NULL UNIQUE,
    email VARCHAR(180) NOT NULL DEFAULT '',
    identity_ref VARCHAR(120) NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS orders (
    id BIGSERIAL PRIMARY KEY,
    order_no VARCHAR(48) NOT NULL UNIQUE,
    order_type VARCHAR(12) NOT NULL CHECK (order_type IN ('rental', 'sale')),
    customer_id BIGINT NOT NULL REFERENCES customers(id),
    status VARCHAR(20) NOT NULL CHECK (status IN ('confirmed', 'checked_out', 'returned', 'completed', 'cancelled')),
    start_date DATE NULL,
    end_date DATE NULL,
    rental_days INTEGER NOT NULL DEFAULT 0 CHECK (rental_days >= 0),
    rental_total_cents BIGINT NOT NULL DEFAULT 0 CHECK (rental_total_cents >= 0),
    deposit_total_cents BIGINT NOT NULL DEFAULT 0 CHECK (deposit_total_cents >= 0),
    total_cents BIGINT NOT NULL DEFAULT 0 CHECK (total_cents >= 0),
    paid_cents BIGINT NOT NULL DEFAULT 0,
    payment_status VARCHAR(20) NOT NULL DEFAULT 'unpaid' CHECK (payment_status IN ('unpaid', 'partial', 'paid', 'refunded')),
    notes TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    checked_out_at TIMESTAMPTZ NULL,
    returned_at TIMESTAMPTZ NULL,
    cancelled_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (order_type = 'rental' AND start_date IS NOT NULL AND end_date IS NOT NULL AND end_date > start_date AND rental_days > 0)
        OR
        (order_type = 'sale' AND start_date IS NULL AND end_date IS NULL AND rental_days = 0)
    )
);

CREATE TABLE IF NOT EXISTS order_lines (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES orders(id),
    product_id BIGINT NOT NULL REFERENCES products(id),
    sku_snapshot VARCHAR(64) NOT NULL,
    product_name_snapshot VARCHAR(160) NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_sale_price_cents BIGINT NOT NULL DEFAULT 0 CHECK (unit_sale_price_cents >= 0),
    unit_daily_rent_cents BIGINT NOT NULL DEFAULT 0 CHECK (unit_daily_rent_cents >= 0),
    unit_deposit_cents BIGINT NOT NULL DEFAULT 0 CHECK (unit_deposit_cents >= 0),
    rental_subtotal_cents BIGINT NOT NULL DEFAULT 0 CHECK (rental_subtotal_cents >= 0),
    deposit_subtotal_cents BIGINT NOT NULL DEFAULT 0 CHECK (deposit_subtotal_cents >= 0),
    line_total_cents BIGINT NOT NULL DEFAULT 0 CHECK (line_total_cents >= 0)
);

-- A commitment is the audit trail for inventory reservations. The product row is
-- locked in application transactions, because a GiST index alone cannot enforce
-- an aggregate quantity capacity greater than one.
CREATE TABLE IF NOT EXISTS inventory_commitments (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES products(id),
    order_id BIGINT NOT NULL REFERENCES orders(id),
    commitment_type VARCHAR(12) NOT NULL CHECK (commitment_type IN ('rental', 'sale')),
    state VARCHAR(16) NOT NULL CHECK (state IN ('confirmed', 'active', 'released', 'consumed')),
    period DATERANGE NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ NULL,
    CHECK (NOT isempty(period))
);

CREATE TABLE IF NOT EXISTS inventory_movements (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES products(id),
    order_id BIGINT NULL REFERENCES orders(id),
    movement_type VARCHAR(32) NOT NULL CHECK (movement_type IN ('manual_adjustment', 'sale', 'sale_cancellation')),
    quantity_delta INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS payments (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES orders(id),
    direction VARCHAR(12) NOT NULL CHECK (direction IN ('charge', 'refund')),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    reference VARCHAR(120) NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS order_status_events (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES orders(id),
    from_status VARCHAR(20) NOT NULL DEFAULT '',
    to_status VARCHAR(20) NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_products_active_name ON products (active, name);
CREATE INDEX IF NOT EXISTS idx_orders_customer_created ON orders (customer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_type_status ON orders (order_type, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_order_lines_product ON order_lines (product_id);
CREATE INDEX IF NOT EXISTS idx_commitments_active_period
    ON inventory_commitments USING gist (product_id, period)
    WHERE state IN ('confirmed', 'active');
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments (order_id, created_at DESC);
