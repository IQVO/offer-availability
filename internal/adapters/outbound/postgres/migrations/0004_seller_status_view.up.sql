-- ADR 0013: the SellerStatusView read model and the OWNER_SUSPENDED reason.
-- Additive: every existing listing keeps its reason; no seller is known yet,
-- and an unknown seller is ACTIVE.

-- seller_status_view: the local, event-fed status of each seller, written only
-- by the seller-inbound consumer (SellerSuspended / SellerReinstated).
CREATE TABLE seller_status_view (
    seller_id TEXT COLLATE "C" PRIMARY KEY,
    status    TEXT        NOT NULL CHECK (status IN ('ACTIVE', 'SUSPENDED')),
    as_of     TIMESTAMPTZ NOT NULL,
    version   BIGINT      NOT NULL CHECK (version >= 1)
);

-- The reasons vocabulary gains OWNER_SUSPENDED (precedence DISCONTINUED >
-- NODE_NOT_SELLABLE > OWNER_SUSPENDED).
ALTER TABLE listings DROP CONSTRAINT listings_suppression_reason_check;
ALTER TABLE listings ADD CONSTRAINT listings_suppression_reason_check
    CHECK (suppression_reason IN ('NODE_NOT_SELLABLE', 'DISCONTINUED', 'OWNER_SUSPENDED'));

-- The seller policy reads every listing of one owner.
CREATE INDEX listings_owner_ref_idx ON listings (owner_ref) WHERE owner_ref <> '';
