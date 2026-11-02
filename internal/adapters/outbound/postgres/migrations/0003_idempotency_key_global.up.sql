-- The Idempotency-Key is global (REST guideline 2.3): looked up by key alone and
-- compared by fingerprint (method + path + body hash). The route column stays as
-- information. Rows live 24 hours, so the duplicates dropped here are transient.
DELETE FROM idempotency_keys a USING idempotency_keys b
 WHERE a.key = b.key AND (a.created_at, a.route) > (b.created_at, b.route);
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (key);
