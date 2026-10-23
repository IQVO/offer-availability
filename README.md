# offer-availability

Retail network, bounded context **Offer & Availability** (Supporting, commerce tier).

Knows what the network can sell and from which node: the catalogue item, one listing per (catalogue item, node), and the quantity each node currently claims it can ship, with the basis the vendor gave for that number (physical stock or throughput-constrained).

Status: under construction. The design is proposed in
[retail-network-docs](https://iqvo.github.io/retail-network-docs/contexts/offer-availability); `model/model.yaml` there is the single source of truth.
This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message
(`com.retailnetwork.commerce.offer-availability.<entity>.<Event>`), transactional outbox.

See `docs/adr/` for decisions and `docs/before-first-release.md` for the packaging checklist.
