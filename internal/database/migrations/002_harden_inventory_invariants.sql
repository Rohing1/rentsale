ALTER TABLE orders
    ADD CONSTRAINT orders_type_status_match CHECK (
        (order_type = 'rental' AND status IN ('confirmed', 'checked_out', 'returned', 'cancelled'))
        OR
        (order_type = 'sale' AND status IN ('confirmed', 'completed', 'cancelled'))
    ),
    ADD CONSTRAINT orders_paid_within_total CHECK (paid_cents >= 0 AND paid_cents <= total_cents);

ALTER TABLE order_lines
    ADD CONSTRAINT order_lines_one_product_per_order UNIQUE (order_id, product_id);

ALTER TABLE inventory_commitments
    ADD CONSTRAINT inventory_commitments_one_per_product_order UNIQUE (order_id, product_id, commitment_type),
    ADD CONSTRAINT inventory_commitments_state_matches_type CHECK (
        (commitment_type = 'rental' AND state IN ('confirmed', 'active', 'released'))
        OR
        (commitment_type = 'sale' AND state IN ('consumed', 'released'))
    ),
    ADD CONSTRAINT inventory_commitments_release_timestamp CHECK (
        (state = 'released' AND released_at IS NOT NULL)
        OR
        (state <> 'released' AND released_at IS NULL)
    );
