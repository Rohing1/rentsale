-- Billing reports filter and order the immutable payment ledger by posting
-- time. This avoids scanning the full payments table as the ledger grows.
CREATE INDEX IF NOT EXISTS idx_payments_created_at_id
    ON payments (created_at DESC, id DESC);
