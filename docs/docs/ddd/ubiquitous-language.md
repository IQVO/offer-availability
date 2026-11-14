---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
description: "The terms Offer & Availability speaks, each mapped to its code identifier, plus the neighbour terms it uses."
---

# Ubiquitous language

The terms this context speaks, each with the identifier it has in the code on `develop`. The first table is the context's
own vocabulary; the second holds the neighbour terms it uses by value (it imports nothing from a neighbour). The
network-wide glossary is the aggregator's
[Ubiquitous Language](https://iqvo.github.io/warehouse-docs/network/strategic-design/ubiquitous-language).

## This context's terms

| Term | Definition | Code identifier | Not to be confused with |
| --- | --- | --- | --- |
| **Catalog Item** | The network's own entry for a product, independent of who ships it: title, dimensions in mm, weight in grams, hazmat and fragile flags, optional GTIN and packaging hierarchy. | `catalog.Item` | A **Listing** (the item offered from one node); a vendor's own SKU, which never appears here |
| **Listing** | A catalogue item offered from one node, optionally for one seller's stock. One per (`catalogItemId`, `nodeId`, `ownerRef`). | `listing.Listing` | A **Catalog Item**; a **Delivery Offer** (`delivery-promise`), which is a quote with a delivery window |
| **Available Quantity** | What a node claims it can ship now for one listing, in eaches. Never negative, at most 2147483647. | `listing.Availability.Qty` (`availableQty` on the wire) | Capacity held by `delivery-promise`; the vendor's physical stock, which this context only sees as a declared number |
| **Availability Basis** | Why the node gave that number: `PHYSICAL`, `THROUGHPUT_CONSTRAINED` or `UNKNOWN`. Kept as declared, never re-derived. | `listing.Basis` | **Promise Basis** (`delivery-promise`) |
| **Availability** | The value object on a listing: quantity, basis, `asOf`, `feedId`. A listing that never had a feed has quantity 0 and basis `UNKNOWN`. | `listing.Availability` | The `AvailabilityView` read models of `delivery-promise` and `order-sourcing`, which are their copies |
| **asOf** | The instant the vendor's number is true for. Strictly increasing per listing: an update whose `asOf` is not after the current one is stale and skipped. | `listing.Availability.AsOf` | The time the feed was applied (`appliedAt`) |
| **Inventory Feed** | One vendor submission of availability for one node, full or partial, applied atomically. Only counters are kept, never the lines. | `feed.InventoryFeed` | A **Submission / Transaction** (`vendor-connectivity`), the asynchronous Vendor API write that carries it |
| **Full Feed** | A feed that is the complete truth for its node; omitted listings become zero. | `feed.Submission.Full`, `feed.Snapshot.FullUpdate` | A partial feed (`fullUpdate` false), which changes only the listings it names |
| **Feed state** | `RECEIVED`, then `APPLIED`, `FAILED` or `SUPERSEDED`. | `feed.State` | **Submission** states in `vendor-connectivity` |
| **Superseded** | A feed whose `asOf` is before the last applied feed of its node: stored, changes nothing. | `feed.Superseded`, `feed.StateSuperseded` | A stale *line* inside an applied feed (`staleCount`) |
| **Listing state** | `LIVE` (sellable) or `SUPPRESSED` (not sellable; reason `NODE_NOT_SELLABLE`, `DISCONTINUED` or `OWNER_SUSPENDED`). | `listing.State`, `listing.Reason` | A zero available quantity: a `LIVE` listing can have zero |
| **Catalog item state** | `ACTIVE` or `DISCONTINUED` (final). | `catalog.State` | **Node** status `ACTIVE` (`node-topology`) |
| **Suppression / Restoration** | Taking a listing out of sale, and putting it back; each raises an event. A `DISCONTINUED` suppression is never restored. | `listing.Listing.Suppress`, `listing.Listing.Restore` | Discontinuing a catalogue item |
| **Suppress on node unavailable** | Policy: on `NodeSuspended` or `NodeRetired` suppress every `LIVE` listing of the node; on `NodeActivated` restore the ones suppressed for `NODE_NOT_SELLABLE`. | `usecases.ApplyNodeEvent` | `node-topology` suspending a node (the cause, not this policy) |
| **Full feed zeroing** | Policy: a full feed sets quantity 0 on every other listing of the node, keeping its basis. | `listing.Listing.ZeroOut` (called from `feed.Apply`) | Suppression: a zeroed listing stays `LIVE` |
| **Node status view** | Local, event-fed copy of each node's status. | `nodestatus.View` | The **Node** aggregate itself, owned by `node-topology` |
| **Packaging Hierarchy** | The pack levels of one item (each, inner pack, case, pallet), each with its own GTIN and a fixed content quantity in eaches. Retired levels stay resolvable. | `catalog.Item.Packaging`, `catalog.PackLevelDef` | A listing's available quantity |
| **Pack Level** | One of `EACH`, `INNER`, `CASE`, `PALLET`. A level above `EACH` knows its child level's GTIN and how many of it it holds. | `catalog.PackLevel` | A node subtype |
| **GTIN** | A GS1 identifier (8, 12, 13 or 14 digits, mod-10 check digit), stored as 14. One GTIN identifies one item or pack level forever. | `gs1.GTIN`, `catalog.ParseGTIN` | A vendor SKU |
| **Owner Reference** | The opaque `ownerRef` on a listing: the seller of record of the stock. Empty means operator-owned. | `listing.OwnerRef` | The vendor operating the node |

## Neighbour terms this context uses

| Term | Definition | Owner | Not to be confused with |
| --- | --- | --- | --- |
| **Node** | One physical building of the network. This context sees only its `nodeId` and, through `NodeStatusView`, its status (`PROPOSED`, `ACTIVE`, `SUSPENDED`, `RETIRED`). | `node-topology` | A **Listing**'s `nodeId`, which is a reference by value |
| **Vendor** | An organisation operating fulfillment nodes for the network. Unknown to this context. | `vendor-connectivity` | A **Seller**, an owner of stock named by `ownerRef` |
| **Submission / Transaction** | An asynchronous vendor write and its outcome. | `vendor-connectivity` | An **Inventory Feed** (what this context applies) |
| **Vendor API** | The Open Host Service of `vendor-connectivity`. | `vendor-connectivity` | This context's in-organization REST API, which vendors never call |
| **Delivery Offer** | A quoted delivery window for an item, quantity and destination. | `delivery-promise` | A **Listing** |
| **Scorecard** | A node's measured performance; one metric is feed freshness, measured from `InventoryFeedApplied`. | `node-performance` | The declared **Availability Basis** |

## Fleet translation

Not applicable: this context never talks to the warehouse-systems fleet. For vendor V-WS the basis originates in the
fleet's `network-fulfillment` and reaches this context only as a feed applied through `vendor-connectivity`. The canonical
translation table is on
[WMS/WES integration](https://iqvo.github.io/warehouse-docs/network/integration/wms-wes-integration).

Source: `internal/domain/{catalog,listing,feed,nodestatus}`, `internal/domain/shared/gs1`, `internal/application/usecases`.
