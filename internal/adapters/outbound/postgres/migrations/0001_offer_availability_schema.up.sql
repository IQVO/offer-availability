-- offer-availability: the service's own database (hard rule: no shared tables).
-- Ids are compared byte-wise (COLLATE "C") so keyset pages and the in-memory
-- doubles order identically. Instants are timestamptz (microsecond precision,
-- which is what shared.UTC truncates to).

-- catalog_items: the CatalogItem aggregate. version counts saves (optimistic
-- concurrency: a save expects the stored version to be version-1).
CREATE TABLE catalog_items (
    catalog_item_id TEXT COLLATE "C" PRIMARY KEY,
    title           TEXT        NOT NULL,
    length_mm       INTEGER     NOT NULL CHECK (length_mm > 0),
    width_mm        INTEGER     NOT NULL CHECK (width_mm > 0),
    height_mm       INTEGER     NOT NULL CHECK (height_mm > 0),
    weight_g        INTEGER     NOT NULL CHECK (weight_g > 0),
    hazmat          BOOLEAN     NOT NULL,
    fragile         BOOLEAN     NOT NULL,
    state           TEXT        NOT NULL CHECK (state IN ('ACTIVE', 'DISCONTINUED')),
    version         BIGINT      NOT NULL CHECK (version >= 1),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- listings: the Listing aggregate, one per (catalog item, node). The unique
-- constraint listings_item_node_key is ErrDuplicateListing. There is no foreign
-- key to catalog_items: aggregates reference each other by id and CreateListing
-- checks the item in its own transaction (ADR 0002).
CREATE TABLE listings (
    listing_id         TEXT COLLATE "C" PRIMARY KEY,
    catalog_item_id    TEXT COLLATE "C" NOT NULL,
    node_id            TEXT COLLATE "C" NOT NULL,
    state              TEXT        NOT NULL CHECK (state IN ('LIVE', 'SUPPRESSED')),
    suppression_reason TEXT        CHECK (suppression_reason IN ('NODE_NOT_SELLABLE', 'DISCONTINUED')),
    available_qty      INTEGER     NOT NULL CHECK (available_qty >= 0),
    basis              TEXT        NOT NULL CHECK (basis IN ('PHYSICAL', 'THROUGHPUT_CONSTRAINED', 'UNKNOWN')),
    as_of              TIMESTAMPTZ,
    feed_id            TEXT,
    version            BIGINT      NOT NULL CHECK (version >= 1),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT listings_item_node_key UNIQUE (catalog_item_id, node_id),
    CONSTRAINT listings_state_reason CHECK ((state = 'SUPPRESSED') = (suppression_reason IS NOT NULL)),
    CONSTRAINT listings_as_of_feed CHECK ((as_of IS NULL) = (feed_id IS NULL))
);
-- ListByNode (full feeds, node policy) and GET /listings?nodeId=.
CREATE INDEX idx_listings_node ON listings (node_id, listing_id);

-- inventory_feeds: the InventoryFeed aggregate. Counters, never lines. Retained
-- 30 days (the janitor deletes older rows).
CREATE TABLE inventory_feeds (
    feed_id       TEXT COLLATE "C" PRIMARY KEY,
    node_id       TEXT COLLATE "C" NOT NULL,
    full_update   BOOLEAN     NOT NULL,
    as_of         TIMESTAMPTZ NOT NULL,
    line_count    INTEGER     NOT NULL CHECK (line_count BETWEEN 0 AND 10000),
    state         TEXT        NOT NULL CHECK (state IN ('RECEIVED', 'APPLIED', 'FAILED', 'SUPERSEDED')),
    applied_count INTEGER     NOT NULL CHECK (applied_count >= 0),
    stale_count   INTEGER     NOT NULL CHECK (stale_count >= 0),
    zeroed_count  INTEGER     NOT NULL CHECK (zeroed_count >= 0),
    failure_code  TEXT,
    received_at   TIMESTAMPTZ NOT NULL,
    applied_at    TIMESTAMPTZ,
    version       BIGINT      NOT NULL CHECK (version >= 1)
);
CREATE INDEX idx_inventory_feeds_received ON inventory_feeds (received_at);

-- node_feed_marks: per node, the asOf of the last APPLIED feed (the node-level
-- staleness rule). The row is also the lock that serialises the feeds of a node.
CREATE TABLE node_feed_marks (
    node_id            TEXT COLLATE "C" PRIMARY KEY,
    last_applied_as_of TIMESTAMPTZ
);

-- node_status_view: the local copy of node-topology's node status, fed by the
-- consumer (read model, ADR 0003).
CREATE TABLE node_status_view (
    node_id   TEXT COLLATE "C" PRIMARY KEY,
    status    TEXT        NOT NULL CHECK (status IN ('PROPOSED', 'ACTIVE', 'SUSPENDED', 'RETIRED')),
    status_at TIMESTAMPTZ NOT NULL,
    version   BIGINT      NOT NULL CHECK (version >= 1)
);

-- processed_events: the consumer inbox, one row per CloudEvents id, written in
-- the same transaction as the event's effects.
CREATE TABLE processed_events (
    event_id     TEXT        PRIMARY KEY,
    event_type   TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_processed_events_at ON processed_events (processed_at);

-- outbox_events: the transactional outbox. One row per already-encoded Kafka
-- message, inserted in the SAME transaction as the aggregates; the relay drains
-- unpublished rows to Kafka.
--   event_id    the CloudEvents `id`, minted once and persisted: a relay retry
--               republishes the same id.
--   event_type  the FULL CloudEvents `type`.
--   subject     the CloudEvents `subject` (the aggregate root id).
--   key         the Kafka message key (the same id).
--   dataschema  the CloudEvents `dataschema` URN.
--   value       the structured-mode CloudEvents JSON bytes.
--   headers     [{"key":..,"value":..}] Kafka headers (content-type).
CREATE TABLE outbox_events (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    key          BYTEA,
    dataschema   TEXT        NOT NULL,
    value        BYTEA       NOT NULL,
    headers      JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT,
    CONSTRAINT outbox_events_event_id_topic_key UNIQUE (event_id, topic)
);
-- The relay only ever scans the unpublished tail; keep that scan tiny.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events (id) WHERE published_at IS NULL;

-- idempotency_keys: the Idempotency-Key middleware, keyed by (route, key), kept
-- 24 hours. A COMMITTED row always carries its outcome: the middleware inserts
-- it and sets the outcome in the same transaction as the use case.
CREATE TABLE idempotency_keys (
    route            TEXT        NOT NULL,
    key              TEXT        NOT NULL,
    request_hash     TEXT        NOT NULL,
    response_status  INTEGER,
    response_body    BYTEA,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (route, key)
);
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
