# ADR 0005: Runtime: outbox relay cap, consumer dead letters, retention janitor and the FAILED feed

## Status

Accepted (2026-10-23).

## Context

`cmd/api` runs three background loops next to the HTTP server: the transactional
outbox relay, the node-topology consumer and a retention janitor. Four questions
the model does not answer had to be decided, each with a simpler option that was
weighed.

1. **A row the broker keeps refusing.** An unbounded retry of a poison outbox row
   would hold back every later event of the same aggregate for ever; dropping it
   would lose an integration event.
2. **A message the consumer can never apply.** Retrying it blocks the partition
   (the consumer commits in order); skipping it silently loses a node status
   change; there is no human in the loop to look at a log line.
3. **Rows nobody reads again.** Idempotency keys, inbox rows and feed rows grow
   with traffic (A19).
4. **A FAILED feed behind the Idempotency-Key middleware.** The middleware runs a
   keyed handler inside a savepoint and rolls it back on a 4xx, so the feed row
   that records the failure (A11) would vanish with the 422.

## Decision

1. **Attempt cap.** The relay drains `outbox_events` in id order, one row at a
   time, and stops at the first send error, so a later event never overtakes a
   failed one. `OUTBOX_MAX_ATTEMPTS` (default 20, `0` = unlimited, a negative or
   unparsable value falls back to the default with a warning) excludes a row whose
   failures reached the cap from further claims; the row stays in the table with
   `attempts` and `last_error`, the failure that reaches the cap is logged once at
   ERROR with the row id, topic, type and key, and later rows overtake it. An
   operator re-arms it with `UPDATE outbox_events SET attempts = 0 WHERE id = ...`
   after fixing the cause. The CloudEvents id is minted once, at encode time, and
   stored, so a retry republishes the same id.
2. **Dead letters.** The consumer fetches one message, handles it, and commits the
   offset only afterwards. A transient failure is retried with a capped exponential
   backoff, five attempts in all, then the message is published to
   `<topic>.dlq` with `x-dlq-source-topic/-partition/-offset`, `x-dlq-error` and
   `x-dlq-failed-at`; the publish itself is retried until it works. A message of a
   consumed type that can never be applied is dead-lettered without a retry. A
   cancelled context is never dead-lettered: the loop returns instead
   (`TestDeadLetterCallsGuardCancellation`). The consumer group id is
   `NODE_TOPOLOGY_CONSUMER_GROUP`, mandatory, never defaulted.
3. **Janitor.** `RETENTION_INTERVAL` (default 1 h) drives one pass that deletes
   idempotency keys older than 24 hours and inventory feeds and processed events
   older than 30 days. A failed pass is logged and tried again at the next tick;
   the keyed write path also purges expired keys on its own, so an expired key is
   a new key even if the janitor is late.
4. **FAILED feed.** The handler of `POST /inventory-feeds` calls `keepWrites` on
   the request context when the feed is FAILED; the middleware then commits the
   savepoint instead of rolling it back, stores the 422 with the key and replays it
   for the same key. The flag is set by no other handler, and outside the
   middleware it does nothing.

Shutdown order: `/readyz` flips to 503, the drain delay passes, HTTP drains, the
three loops are cancelled and awaited (bounded), the pool closes last. A loop that
returns on its own makes `run` return an error, so the process exits non-zero and
the orchestrator restarts it.

## Consequences

- A stuck outbox row never stops other aggregates, and never disappears; it is a
  visible, re-armable record. Within one aggregate the order is kept until the cap.
- A poison node event costs one DLQ message, not a wedged partition. The DLQ topic
  has no consumer here; draining it is an operator task.
- The janitor is one more loop that must not die silently: it is part of the
  "a worker stopped" exit.
- Every keyed write purges expired keys inside its own transaction, which costs a
  range delete on `idempotency_keys(created_at)`.
- `keepWrites` is a small hole in the "a 4xx leaves nothing behind" rule of the
  middleware, documented here and in A22, covered by
  `internal/adapters/inbound/http/idempotency_integration_test.go`.
