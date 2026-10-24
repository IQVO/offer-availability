ALTER TABLE listings DROP CONSTRAINT listings_item_node_owner_key;
ALTER TABLE listings DROP COLUMN owner_ref;
ALTER TABLE listings ADD CONSTRAINT listings_item_node_key UNIQUE (catalog_item_id, node_id);
DROP TABLE catalog_item_pack_levels;
DROP INDEX catalog_items_gtin_key;
ALTER TABLE catalog_items DROP COLUMN gtin;
