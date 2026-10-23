# 1. Offer & Availability is a bounded context of the retail network

Status: Accepted (bootstrap). Design source: retail-network-docs `model/model.yaml`, context `offer-availability`, and its ADRs RN-0001 and RN-0007.

## Context

The retail network is modelled as ten bounded contexts, each its own service and database. Knows what the network can sell and from which node: the catalogue item, one listing per (catalogue item, node), and the quantity each node currently claims it can ship, with the basis the vendor gave for that number (physical stock or throughput-constrained).

## Decision

- This repo owns the aggregates CatalogItem, Listing, InventoryFeed; the invariants and error names are those of the model.
- Business decisions of the context:
- A listing is sellable only while its node is ACTIVE.
- A feed older than the last applied feed for that node is ignored.
- A full feed zeroes every listing of that node it omits.
- The basis the vendor declares (PHYSICAL or THROUGHPUT_CONSTRAINED) is kept with the number, never re-derived.
- Events use the network CloudEvents namespace `com.retailnetwork.commerce.offer-availability.<entity>.<Event>` on `retailnetwork.offer-availability.events`
  (analytics-only events on `.analytics`). Published: CatalogItemCreated, CatalogItemDiscontinued, ListingCreated, ListingAvailabilityChanged, ListingSuppressed, ListingRestored, InventoryFeedApplied. No `warehouse.*` topic is consumed or produced.
- No live REST lookup into sibling contexts except the calls the model lists under `sync_calls`.
- No authentication on REST, consistent with the fleet. PII exists only in customer-ordering and shipping-and-tracking.

## Consequences

Relationships and the full consumed-event list are in the model and in the AsyncAPI contract added with the first feature PR.
