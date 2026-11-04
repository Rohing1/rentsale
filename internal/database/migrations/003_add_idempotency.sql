CREATE TABLE IF NOT EXISTS idempotency_keys (
    scope VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    response_id BIGINT NULL CHECK (response_id IS NULL OR response_id > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (scope, idempotency_key)
);

ALTER TABLE payments
    ADD CONSTRAINT payments_reference_required CHECK (char_length(btrim(reference)) > 0);

CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_order_reference
    ON payments (order_id, reference);
