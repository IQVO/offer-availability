---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 2
---

# Subdomain classification

**Offer & Availability is a Supporting subdomain** of the retail network (area Network, CloudEvents tier `commerce`),
with business model *revenue enabler* and evolution stage *Product*. Verdicts for all contexts are the aggregator's
([Subdomain Classification](https://iqvo.github.io/warehouse-docs/network/strategic-design/subdomain-classification)); this
page gives the reasoning for this one and its neighbours.

## Why Supporting

- **Necessary and specific, but well understood.** The network cannot promise or source anything without knowing what it
  can sell and from which node, and the vendor's *basis* next to the number matters to the promise. But listings per ship
  node with a declared quantity are a shape marketplaces publish too. The aggregator's verdict: "necessary and specific
  (the basis matters to the promise) but well understood".
- **Facts, not decisions.** The decisions that win customers (a delivery date that is true, a node that keeps it) are made
  in `delivery-promise` and `order-sourcing`, which are Core and read this context's events into their own views. This
  context stores declarations and keeps the vendor's basis; it never re-derives or corrects a number.
- **Low model complexity.** Three aggregates (`CatalogItem`, `Listing`, `InventoryFeed`) with two to four states each, a
  local read model (`NodeStatusView`) and two policies (suppress on node unavailability, zero on a full feed). The hard
  part is operational, applying a full feed atomically across many listings, not conceptual.
- **Where it could move.** Re-deriving or correcting the vendor's number (for example from `node-performance`'s measured
  freshness) would raise differentiation; today the context rules that out and a change would need an ADR.

See the [Core Domain Chart](/docs/ddd/core-domain-chart) for the placement.

## Evolution by part

| Part | Stage | Why |
| --- | --- | --- |
| Catalogue item master (incl. GTIN and packaging hierarchy) | Product / Commodity | Item master data with dimensions, flags and GS1 identifiers is a well-understood shape |
| Listing and availability | Product | Per-node offers fed by partner submissions are established; the addition is the kept basis |
| Feed application (full/partial, ordered by `asOf`) | Product | Known semantics; the work is doing it atomically at volume |
| Node-driven suppression | Custom-built | Specific to this network: it follows `node-topology`'s node lifecycle |

## The neighbours (copied from the aggregator)

From `network/strategic-design/subdomain-classification.md` on `origin/main` of `warehouse-docs`:

| Bounded context | Classification | Tier | Business model | Evolution | Relationship to this context |
| --- | --- | --- | --- | --- | --- |
| `delivery-promise` | Core | `commerce` | Revenue (conversion) and cost | Custom-built | Downstream: builds its `AvailabilityView` from catalogue and listing events |
| `order-sourcing` | Core | `commerce` | Cost-to-serve reduction, speed protection | Custom-built | Downstream: re-optimises from its own `AvailabilityView` |
| `node-topology` | Supporting | `logistics` | Enabler | Custom-built | Upstream: node lifecycle events, Conformist |
| `vendor-connectivity` | Supporting | `partner` | Revenue-channel enabler | Product | Calls `POST /inventory-feeds`; consumes `ListingCreated` and pack-level events |
| `node-performance` | Supporting | `partner` | Risk reducer | Custom-built | Downstream: measures feed freshness from `InventoryFeedApplied` |
| `seller-inbound` | Supporting | `partner` | Supply enabler | Custom-built | Downstream: keeps a packaging view from pack-level events |
| `shipping-and-tracking` | Generic | `logistics` | Compliance and experience | Commodity | Downstream: keeps catalogue dimensions from `CatalogItemCreated` |
| `linehaul-transportation`, `last-mile-delivery`, `customer-ordering` | Supporting, Supporting, Generic | `logistics`, `logistics`, `commerce` | | | No relationship (Separate Ways) |

The aggregator counts two Core, seven Supporting and two Generic contexts across the network. Problem-space subdomain:
*Selection & Availability*, "knowing what can be sold from which node right now", verdict Supporting.
