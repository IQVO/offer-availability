---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Offer & Availability

`offer-availability` (code `OA`, CloudEvents tier `commerce`) is a bounded context of the retail network (the **Network**
area of the warehouse-systems fleet). It knows **what the network can sell and from which node**. The network sells items
it does not store: every node is run by a vendor. Before anything downstream can show a product, promise a delivery date
or choose a node for an order, someone has to hold two facts, and this service holds them:

1. **What can be sold.** The network's own entry for a product, the `CatalogItem`: title, dimensions in millimetres,
   weight in grams, hazmat and fragile flags, an optional GS1 GTIN and an optional packaging hierarchy
   (`EACH`, `INNER`, `CASE`, `PALLET`).
2. **From where, and how much.** One `Listing` per catalogue item, node and optional owner, with the quantity the node
   claims it can ship and the **basis** the vendor gave for that number.

Its subdomain class is **Supporting** (revenue enabler, evolution Product): the decisions that win customers are made in
`delivery-promise` and `order-sourcing`, which this context only feeds with facts. See
[Subdomain classification](/docs/ddd/subdomain-classification).

## What it does

| Capability | How |
| --- | --- |
| Keep the item master | `POST /catalog-items`, `POST /catalog-items/{catalogItemId}/discontinuations`, `GET /catalog`, `GET /catalog/{catalogItemId}`; GTIN and pack levels through `PUT /catalog-items/{catalogItemId}/packaging/{level}` and `POST .../packaging/{level}/retirements` |
| List an item on a node | `POST /listings` (`GET /listings` to read); the listing starts `LIVE` only if the node is `ACTIVE` in the local node status, otherwise `SUPPRESSED` with reason `NODE_NOT_SELLABLE` |
| Apply vendor inventory feeds | `POST /inventory-feeds`: full or partial, atomic per feed, ordered by `asOf` per node, recorded as `APPLIED`, `SUPERSEDED` or `FAILED` (`GET /inventory-feeds/{feedId}`) |
| Follow node lifecycle | Consumes `NodeRegistered`, `NodeActivated`, `NodeSuspended` and `NodeRetired` from `retailnetwork.node-topology.events` into a local `node_status_view`; suspending or retiring a node suppresses its `LIVE` listings and activating it restores the ones suppressed for that reason |
| Publish facts | Nine CloudEvents types on `retailnetwork.offer-availability.events` through a transactional outbox ([Event catalogue](/docs/api-reference/events)) |
| Serve agents | Read-only MCP (`cmd/mcp`, four tools) and read-only A2A (`cmd/a2a`, two skills) over the same read use cases as REST |
| Console | A read-only Module Federation remote (`web/`, [ADR 0008](/docs/adr/0008-console-remote)) over the REST reads |

Everything is unauthenticated: REST, MCP and A2A carry no credentials, by a fleet-wide decision. Access control is the
in-cluster boundary.

## The basis of a quantity

A vendor can give a number for two different reasons, and the listing keeps the reason next to the number and never
re-derives it:

| Basis | Meaning |
| --- | --- |
| `PHYSICAL` | The number is the stock the node can use |
| `THROUGHPUT_CONSTRAINED` | The node has more stock but can only move this many before its next cutoff |
| `UNKNOWN` | The vendor did not say (also the state of a listing that never received a feed) |

## Decisions this context makes

- A listing is sellable only while its node is `ACTIVE`; node events suppress and restore listings.
- A feed older than the last applied feed of that node is `SUPERSEDED` and changes nothing; within a feed, a line older
  than the availability the listing already holds is skipped and counted as stale.
- A full feed is the complete truth for its node: every listing of the node it omits is set to zero.
- One listing per catalogue item, node and owner; a GTIN identifies one item or pack level forever; a discontinued item
  suppresses all its listings, for good.

## What it does NOT own

| Not here | Where it lives |
| --- | --- |
| Whether a delivery date is feasible, and promise capacity | `delivery-promise`, which builds its own `AvailabilityView` from this context's events |
| Choosing a node for an order | `order-sourcing` (same event-fed view); this context never chooses |
| The vendor protocol: vendor authentication, asynchronous transactions, rate limits, the `202` answer | `vendor-connectivity`, which calls `POST /inventory-feeds` in-organization |
| Which node exists and whether it is active | `node-topology`; this context holds a copy (`node_status_view`) and conforms to it |
| Pricing, competing offers between sellers, inventory placement | Not modelled anywhere in this context (a listing carries only an optional `ownerRef`) |
| Measured feed freshness | `node-performance`, computed from `InventoryFeedApplied` |
| Stock inside a warehouse | The WMS and WES contexts of the fleet; their numbers reach this context only as a vendor feed |

This service calls no other service: there is no outbound REST, MCP or A2A client, and the fitness test
`TestNoOutboundA2AClient` keeps it that way. Everything it learns arrives as a Kafka event or as a REST call into it.

## Where to go next

| You want to | Read |
| --- | --- |
| Run it and make the first calls | [Quickstart](/docs/overview/quickstart) |
| Understand the layout and the binaries | [Architecture](/docs/overview/architecture), [Runtime](/docs/overview/runtime) |
| Set it up and operate it | [Configuration](/docs/operations/configuration), [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability), [Troubleshooting](/docs/operations/troubleshooting), [Before the first release](/docs/operations/before-first-release) |
| Test and change it | [Testing](/docs/development/testing) |
| Learn the domain | [Ubiquitous language](/docs/ddd/ubiquitous-language), [Use cases](/docs/ddd/use-cases), [DDD artifacts](/docs/ddd/ddd-artifacts) |
| Integrate with it | [API overview](/docs/api-reference), [Event catalogue](/docs/api-reference/events), [MCP tools](/docs/mcp/tools), [Integration](/docs/ecosystem/integration) |
| See why it is built this way | [Architecture decision records](/docs/adr) |

:::note[Disclaimer]
This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.
:::
