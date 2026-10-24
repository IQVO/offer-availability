# Architecture decision records

The decisions of `offer-availability`, in the order they were taken. The ADR bodies
are immutable; a changed decision gets a new ADR that supersedes the old one.
The *Status* column is copied from each ADR's own `## Status` section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-offer-availability-bounded-context.md) | Offer & Availability is a bounded context of the retail network | Accepted (bootstrap) |
| [0002](./0002-aggregates-and-invariants.md) | Aggregates, invariants and assumptions | Accepted (2026-10-23) |
| [0003](./0003-consumed-contracts-and-local-node-status.md) | Consumed contracts and the local node status view | Accepted (2026-10-23) |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-23) |
| [0005](./0005-runtime-relay-consumer-and-idempotency.md) | Runtime: outbox relay cap, consumer dead letters, retention janitor and the FAILED feed | Accepted (2026-10-23) |
| [0006](./0006-catalog-gtin-packaging-and-listing-owner.md) | Catalogue GTIN and packaging hierarchy, and the optional owner of a listing | Accepted (2026-10-24) |
