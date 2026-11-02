---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Run the three binaries against a throwaway Postgres and a throwaway Kafka broker, then make the first calls on REST, MCP
and A2A. Every response below was captured from a real run of the binaries built from this branch; only ports and the
generated ids differ from yours. The capture used ports `28080`, `28090` and `28093`, a Postgres on `55481` and a
broker on `29392`; the commands below use the defaults (`8080`, `8090`, `8093`) and a broker on `localhost:9092`.

## Prerequisites

- Go (the version in `go.mod`), `make`, `curl`, Docker (for the throwaway Postgres and Kafka, and for
  `make integration`, which starts its own containers).
- A reachable PostgreSQL 16 and **one** Kafka broker. In the fleet's kind cluster the single broker is reachable from
  the host at `localhost:9092`; the docker-compose Kafka is retired. For a stand-alone run use the throwaway
  containers below and stop them afterwards.

## Make targets

| Target | What it does |
| --- | --- |
| `make help` | Lists every target (the default). |
| `make check` | `fmt-check`, `vet`, `build`, `lint`, `test` (the pre-push gate). |
| `make test` | `go test ./... -race`: unit, handler and BDD tests; no database needed. |
| `make bdd` | Only the godog features (`go test ./... -run TestFeatures -v`). |
| `make coverage` | Coverage over `internal/domain` and `internal/application` with a 90 % gate. |
| `make arch-test` | The fitness tests in `internal/architecture`. |
| `make integration` | testcontainers Postgres and Kafka (needs Docker). |
| `make mutation` | gremlins on every `internal/domain/<pkg>`. |
| `make vuln` | govulncheck. |

There is no `make run`: the binaries start with `go run ./cmd/<name>` and read only environment variables
([Configuration](/docs/operations/configuration)).

## 1. Start a throwaway Postgres and Kafka

```sh
docker run -d --name oa-pg -e POSTGRES_USER=oa -e POSTGRES_PASSWORD=oa -e POSTGRES_DB=offer_availability -p 5432:5432 postgres:16
docker run -d --name oa-kafka -p 9092:9092 \
  -e KAFKA_NODE_ID=1 -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:9092 \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  apache/kafka:3.9.0
```

Skip the broker container when the kind cluster's broker already listens on `localhost:9092`, and never point a local
process at a shared topic with a shared consumer group: pick your own `NODE_TOPOLOGY_CONSUMER_GROUP`.

The service never creates topics through an admin API; the producer and the dead-letter writer set
`AllowAutoTopicCreation`. **Create the consumed topic before you start `cmd/api`** (or produce one event to it first):
in the capture, a `cmd/api` started while `retailnetwork.node-topology.events` did not exist never received events that
were produced later, and a restart fixed it (see [Troubleshooting](/docs/operations/troubleshooting)).

```sh
docker exec oa-kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --topic retailnetwork.node-topology.events
```

## 2. Start the three binaries

```sh
export DATABASE_URL='postgres://oa:oa@localhost:5432/offer_availability?sslmode=disable'
KAFKA_BROKERS=localhost:9092 NODE_TOPOLOGY_CONSUMER_GROUP=offer-availability-local go run ./cmd/api   # :8080
MCP_ADDR=:8090 go run ./cmd/mcp                                                                       # :8090
A2A_AGENT_URL=http://localhost:8093/ go run ./cmd/a2a                                                 # :8093
```

Run each in its own terminal. `cmd/api` logs, as JSON, `postgres adapters configured`, `outbox relay running`,
`node-topology consumer running` and `http server listening`. The embedded migrations run at boot (all three binaries
run them; the advisory lock makes concurrent starts safe).

```sh
curl -i localhost:8080/healthz
curl -i localhost:8080/readyz
```

```text
HTTP/1.1 200 OK
Content-Type: application/json

{"status":"ok"}

HTTP/1.1 200 OK
Content-Type: application/json

{"status":"ready"}
```

## 3. First calls on REST

Every state-changing route needs an `Idempotency-Key`. Create a catalogue item with a GS1 GTIN, then repeat the
identical request: the second answer is the stored first answer with `Idempotency-Replayed: true`.

```sh
curl -i -X POST localhost:8080/catalog-items -H 'Content-Type: application/json' -H 'Idempotency-Key: qs-item-1' \
  -d '{"title":"Cotton tote bag","lengthMm":400,"widthMm":300,"heightMm":50,"weightG":180,"hazmat":false,"fragile":false,"identifiers":{"gtin":"06141410000120"}}'
```

```text
HTTP/1.1 201 Created
Content-Type: application/json

{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","title":"Cotton tote bag","lengthMm":400,"widthMm":300,"heightMm":50,"weightG":180,"hazmat":false,"fragile":false,"state":"ACTIVE","identifiers":{"gtin":"06141410000120"}}
```

The replay (same key, same body):

```text
HTTP/1.1 201 Created
Content-Type: application/json
Idempotency-Replayed: true
```

List the item on a node. The local node status is empty, so the listing starts `SUPPRESSED` with reason
`NODE_NOT_SELLABLE`:

```sh
curl -i -X POST localhost:8080/listings -H 'Content-Type: application/json' -H 'Idempotency-Key: qs-listing-1' \
  -d '{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","nodeId":"node-a"}'
```

```json
{"listingId":"lst-db10bd4e-0f2d-473f-a00c-fbb529374996","catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","nodeId":"node-a","state":"SUPPRESSED","suppressionReason":"NODE_NOT_SELLABLE","availability":{"availableQty":0,"basis":"UNKNOWN"}}
```

## 4. Activate the node with a node-topology event

The service learns node status only from `retailnetwork.node-topology.events`. Produce a `NodeActivated` CloudEvent
as the node-topology context would:

```sh
echo '{"specversion":"1.0","id":"11111111-2222-4333-8444-555555555555","source":"/retailnetwork/node-topology","type":"com.retailnetwork.logistics.node-topology.node.NodeActivated","subject":"node-a","time":"2026-10-31T12:00:00Z","datacontenttype":"application/json","data":{"nodeId":"node-a","activatedAt":"2026-10-31T12:00:00Z"}}' \
  | docker exec -i oa-kafka /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server localhost:9092 --topic retailnetwork.node-topology.events
curl localhost:8080/listings?nodeId=node-a
```

```json
{"listings":[{"listingId":"lst-db10bd4e-0f2d-473f-a00c-fbb529374996","catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","nodeId":"node-a","state":"LIVE","availability":{"availableQty":0,"basis":"UNKNOWN"}}]}
```

The `cmd/api` log shows `event processed` for the event and the listing is `LIVE`.

## 5. Apply an inventory feed

```sh
curl -i -X POST localhost:8080/inventory-feeds -H 'Content-Type: application/json' -H 'Idempotency-Key: qs-feed-1' \
  -d '{"feedId":"feed-1","nodeId":"node-a","fullUpdate":false,"asOf":"2026-10-31T13:00:00Z","lines":[{"listingId":"lst-db10bd4e-0f2d-473f-a00c-fbb529374996","availableQty":42,"basis":"PHYSICAL"}]}'
```

```json
{"feedId":"feed-1","nodeId":"node-a","fullUpdate":false,"asOf":"2026-10-31T13:00:00Z","state":"APPLIED","lineCount":1,"appliedCount":1,"staleCount":0,"zeroedCount":0,"receivedAt":"2026-10-31T23:45:24.077926Z","appliedAt":"2026-10-31T23:45:24.080234Z"}
```

A feed that names a listing of another node is refused with `422` and still recorded as `FAILED`:

```text
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/problem+json

{"type":"listing-not-on-node","title":"A feed line names a listing of another node","status":422,"detail":"listing not on node: listing lst-unknown is not a listing of node node-a","instance":"/inventory-feeds/feed-2"}
```

`GET /inventory-feeds/feed-2` then returns `"state":"FAILED"` with the failure code. Errors are RFC 9457
`application/problem+json`; an unknown id is `404 not-found`, a missing key `400 idempotency-key-required`.

```sh
curl localhost:8080/catalog?limit=10
curl localhost:8080/catalog/item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e
curl localhost:8080/inventory-feeds/feed-1
```

## 6. See the published events

Every change was written to the outbox in the same transaction and relayed to `retailnetwork.offer-availability.events`
with the aggregate id as Kafka key:

```sh
docker exec oa-kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 \
  --topic retailnetwork.offer-availability.events --from-beginning --max-messages 5 --property print.key=true
```

```json
{"specversion":"1.0","id":"0abbfe0e-6dbc-4e0c-a308-b57866ee7d78","source":"/retailnetwork/offer-availability","type":"com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated","subject":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","datacontenttype":"application/json","dataschema":"urn:retailnetwork:offer-availability:events:CatalogItemCreated:v1","time":"2026-10-31T23:39:24.401377Z","data":{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","title":"Cotton tote bag","lengthMm":400,"widthMm":300,"heightMm":50,"weightG":180,"hazmat":false,"fragile":false,"gtin":"06141410000120"}}
```

The capture then showed `ListingCreated`, `ListingSuppressed`, `ListingRestored` (after the node activation) and
`ListingAvailabilityChanged` (after the feed). The first relay pass in the capture failed once with `Unknown Topic Or
Partition` because the output topic did not exist yet; the next pass auto-created it and published (at-least-once,
nothing lost).

## 7. First calls on MCP

`cmd/mcp` speaks Streamable HTTP at `/mcp` (and `/`). Initialise a session, send `notifications/initialized` with the
returned `Mcp-Session-Id`, then call tools:

```sh
curl -si -X POST localhost:8090/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"docs","version":"0"}}}'
```

The response carries `Mcp-Session-Id: <id>` and `serverInfo` `{"name":"offer-availability-mcp","version":"1.0.0"}`. A
`tools/list` before the session is initialised is refused (`method "tools/list" is invalid during session initialization`).

```sh
curl -s -X POST localhost:8090/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H 'Mcp-Session-Id: <id>' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
curl -s -X POST localhost:8090/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H 'Mcp-Session-Id: <id>' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_inventory_feed","arguments":{"feedId":"feed-1"}}}'
```

```text
event: message
data: {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"appliedAt\":\"2026-10-31T23:45:24.080234Z\",\"appliedCount\":1,\"asOf\":\"2026-10-31T13:00:00Z\",\"feedId\":\"feed-1\",...,\"state\":\"APPLIED\",\"zeroedCount\":0}"}],"structuredContent":{...}}}
```

An unknown id comes back as a tool error: `{"content":[{"type":"text","text":"not-found: not found"}],"isError":true}`. The four
tools are listed in [MCP tools](/docs/mcp/tools).

## 8. First calls on A2A

`cmd/a2a` serves the Agent Card and JSON-RPC 2.0 on `/` and `/a2a`:

```sh
curl -s localhost:8093/.well-known/agent-card.json
curl -s -X POST localhost:8093/ -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m-1","role":"ROLE_USER","parts":[{"data":{"skill":"check_availability","arguments":{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e"}},"mediaType":"application/json"}]}}}'
```

```json
{"jsonrpc":"2.0","id":1,"result":{"message":{"messageId":"...","contextId":"...","parts":[{"data":{"skill":"check_availability","data":{"availability":{"item":{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","title":"Cotton tote bag","state":"ACTIVE","identifiers":{"gtin":"06141410000120"}},"listings":[{"listingId":"lst-db10bd4e-0f2d-473f-a00c-fbb529374996","nodeId":"node-a","state":"LIVE","availability":{"availableQty":42,"basis":"PHYSICAL","asOf":"2026-10-31T13:00:00Z","feedId":"feed-1"}}]}}}},{"text":"item-fe9bdd3a-... (ACTIVE): 1 listings (1 LIVE, 0 SUPPRESSED), 42 units reported on the LIVE ones"}],"role":"ROLE_AGENT","taskId":"..."}}}
```

(Fields elided with `...` are generated ids and the unchanged dimensions.) The card advertises the URL you gave in
`A2A_AGENT_URL`; the two skills are described in [API overview](/docs/api-reference).

## 9. Clean up

Stop the three `go run` processes, then:

```sh
docker rm -f oa-pg oa-kafka
```
