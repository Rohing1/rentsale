-- A payment-provider transaction/reference must identify one ledger entry
-- across the whole tenant, not merely within a single order.  Stop before
-- adding the unique index if an older database already contains ambiguous
-- references; operators must reconcile those records explicitly.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payments
        GROUP BY reference
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot make payment references globally unique: duplicate references exist in payments';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_payments_order_reference;
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_reference ON payments (reference);
