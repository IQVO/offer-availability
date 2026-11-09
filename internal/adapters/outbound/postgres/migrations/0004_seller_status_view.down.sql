-- Reverts 0004. A listing suppressed for OWNER_SUSPENDED makes the restored
-- constraint fail: reconcile those listings first (ADR 0013).
DROP INDEX listings_owner_ref_idx;
ALTER TABLE listings DROP CONSTRAINT listings_suppression_reason_check;
ALTER TABLE listings ADD CONSTRAINT listings_suppression_reason_check
    CHECK (suppression_reason IN ('NODE_NOT_SELLABLE', 'DISCONTINUED'));
DROP TABLE seller_status_view;
