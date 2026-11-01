---
id: core-domain-chart
title: Core Domain Chart
sidebar_label: Core Domain Chart
description: "Where Offer and Availability sits on the ddd-crew Core Domain Chart, and why it is a Supporting subdomain."
---

# Core Domain Chart

Following the ddd-crew [Core Domain Charts](https://github.com/ddd-crew/core-domain-charts):
business differentiation (y) against model complexity (x).

```mermaid
quadrantChart
    title offer-availability on the Core Domain Chart
    x-axis Low model complexity --> High model complexity
    y-axis Low business differentiation --> High business differentiation
    quadrant-1 Core
    quadrant-2 Decisive - Short-term Core
    quadrant-3 Supporting
    quadrant-4 Generic
    offer-availability context: [0.30, 0.38]
    Listing availability and basis: [0.36, 0.44]
    InventoryFeed application: [0.34, 0.30]
    CatalogItem master: [0.16, 0.20]
```

Source: positions from the aggregator's chart for this context (complexity 0.30, differentiation 0.38,
[Core Domain Chart](https://iqvo.github.io/warehouse-docs/network/strategic-design/core-domain-chart)); the three slice positions are
this page's estimate. The model behind them is `internal/domain/{catalog,listing,feed,nodestatus}`.

Omitted: the other contexts of the network, each of which has its own chart.

## Classification: Supporting

The context point and all three slices sit in **quadrant 3 (Supporting)**,
bottom-left. The model classifies the context `Supporting`, business model
"Revenue enabler (what can be sold, from where)", evolution `Product`.

**Differentiation is moderate-low (y = 0.38).** The network needs to know
what it can sell and from where, or it cannot promise anything. But the
decisions that win customers (a date that is true, a node that keeps it)
are made in `delivery-promise` and `order-sourcing`, which are Core. This
context feeds them facts. The one idea with some edge is keeping the
vendor's **basis** next to the number, so a `THROUGHPUT_CONSTRAINED`
quantity is not read as stock. That is why the Listing slice is plotted
highest. It is still a declaration the context stores, not a decision it
makes, so it stays below the Core line.

**Complexity is low (x = 0.30).** Three aggregates with two to four states each, a node status read model, two policies
(suppress on node unavailability, zero on a full feed) and a packaging hierarchy with GS1 identifiers. The non-trivial rules
are monotonic `asOf`, the node-level `SUPERSEDED` rule, full-feed zeroing and node-driven suppression. The hardest part is
operational, not conceptual: applying a full feed for a node atomically across many listings, serialised per node by a row
lock (`node_feed_marks`).

## Business model

| Aspect | Verdict |
| --- | --- |
| Business model | Revenue enabler: nothing is sold that is not listed, and nothing is promised from a node without a quantity. |
| Who pays if it is wrong | `delivery-promise` quotes from a quantity a node cannot ship, or misses one it can. `node-performance` measures the cost afterwards (feed freshness, rejections). |
| Who it serves | `delivery-promise`, `order-sourcing`, `vendor-connectivity`, `shipping-and-tracking`, `node-performance`, the storefront. |

## Evolution

| Part | Evolution stage | Why |
| --- | --- | --- |
| Catalogue item master | **Product / Commodity** | Item master data with dimensions and flags is a well-understood shape. |
| Listing and availability | **Product** | Per-node offers fed by partner submissions are an established pattern; the model's addition is the kept basis. |
| Feed application (full / partial, monotonic `asOf`) | **Product** | Known semantics; the work is in doing it atomically at volume. |
| Node-driven suppression | **Custom-built** | Specific to this network: it follows `node-topology`'s node lifecycle. |

**Movement to watch.** If the network ever re-derived or corrected the
vendor's number (for example, from `node-performance`'s measured feed
freshness), the Listing slice would move right and up. The model rules
that out today ("kept with the number, never re-derived"); such a change
would need an ADR next to
[ADR 0003 of the aggregator](https://iqvo.github.io/warehouse-docs/network/adr/0003-declared-and-measured-capability).