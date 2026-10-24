-- ADR 0006: catalog identifiers + packaging hierarchy, and the optional owner
-- of a listing. Additive: every existing row keeps its meaning (no GTIN, no
-- pack levels, operator-owned listings).

ALTER TABLE catalog_items ADD COLUMN gtin TEXT;
-- A GTIN identifies one item. The partial unique index is the backstop of the
-- use case's pre-check (NULL = no identifier).
CREATE UNIQUE INDEX catalog_items_gtin_key ON catalog_items (gtin) WHERE gtin IS NOT NULL;

-- catalog_item_pack_levels: every pack level an item ever declared, retired
-- ones included (active = false, retired_at set). seq keeps declaration order.
-- One GTIN identifies one pack level of one item, forever; at most one active
-- definition per (item, level).
CREATE TABLE catalog_item_pack_levels (
    catalog_item_id TEXT COLLATE "C" NOT NULL,
    seq             INTEGER     NOT NULL CHECK (seq >= 0),
    level           TEXT        NOT NULL CHECK (level IN ('EACH', 'INNER', 'CASE', 'PALLET')),
    gtin            TEXT        NOT NULL CHECK (gtin ~ '^[0-9]{14}$'),
    child_gtin      TEXT        CHECK (child_gtin ~ '^[0-9]{14}$'),
    units_per_pack  INTEGER     NOT NULL CHECK (units_per_pack BETWEEN 1 AND 1000000),
    length_mm       INTEGER     NOT NULL DEFAULT 0 CHECK (length_mm >= 0),
    width_mm        INTEGER     NOT NULL DEFAULT 0 CHECK (width_mm >= 0),
    height_mm       INTEGER     NOT NULL DEFAULT 0 CHECK (height_mm >= 0),
    gross_weight_g  INTEGER     NOT NULL DEFAULT 0 CHECK (gross_weight_g >= 0),
    active          BOOLEAN     NOT NULL,
    declared_at     TIMESTAMPTZ NOT NULL,
    retired_at      TIMESTAMPTZ,
    PRIMARY KEY (catalog_item_id, seq),
    CONSTRAINT catalog_item_pack_levels_gtin_key UNIQUE (gtin),
    CONSTRAINT catalog_item_pack_levels_retired CHECK (active = (retired_at IS NULL)),
    CONSTRAINT catalog_item_pack_levels_each CHECK ((level = 'EACH') = (child_gtin IS NULL))
);
CREATE UNIQUE INDEX catalog_item_pack_levels_active_key
    ON catalog_item_pack_levels (catalog_item_id, level) WHERE active;

-- listings: one per (item, node, owner). owner_ref '' is operator-owned, so the
-- existing uniqueness holds for every existing row.
ALTER TABLE listings ADD COLUMN owner_ref TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE listings DROP CONSTRAINT listings_item_node_key;
ALTER TABLE listings ADD CONSTRAINT listings_item_node_owner_key UNIQUE (catalog_item_id, node_id, owner_ref);
