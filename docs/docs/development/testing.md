---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

The test pyramid of `offer-availability` as it is in the repository: unit tests per layer, 64 godog scenarios, a shared
repository contract, testcontainers integration tests, architecture fitness tests and mutation testing on the domain.
Docker is needed only for `make integration`; every other target runs offline.

## The pyramid

| Level | Where | What it proves | Runs in |
| --- | --- | --- | --- |
| Unit, domain | `internal/domain/{catalog,feed,listing,nodestatus,shared,shared/gs1}` (`*_test.go`) | Aggregate invariants, state machines, GTIN and SSCC check digits, snapshots | `make test` |
| Unit, application | `internal/application/usecases/*_test.go` | Every use case over the in-memory adapters: events raised, ordering, outcomes | `make test` |
| Unit, adapters | `internal/adapters/inbound/http` (handlers, idempotency middleware, telemetry), `inbound/mcp`, `inbound/a2a`, `inbound/kafka` (consumer, tracing), `inbound/requestlog`, `outbound/kafka` (encoder, writer, relay sink, health), `outbound/outbox`, `outbound/telemetry`, `outbound/postgres` (outbox trace), `kafka/cloudevents`, `bootretry`, `cmd/*` (config, telemetry, routers) | Wire contracts, status codes, problem slugs, CloudEvents envelope, retries | `make test` |
| Acceptance (godog / Gherkin) | `features/*.feature` + `features/*_test.go` | End-to-end behaviour through the REST handlers over in-memory adapters | `make bdd`, and inside `make test` |
| Repository contract | `internal/adapters/outbound/repocontract` | One behavioural suite that both the in-memory adapter (`memory_test.go`) and the Postgres adapter (`postgres_integration_test.go`) must pass | `make test`, `make integration` |
| Integration (testcontainers) | files tagged `//go:build integration`: `cmd/api/e2e_integration_test.go`, `cmd/api/main_integration_test.go`, `cmd/mcp/main_integration_test.go`, `cmd/a2a/main_integration_test.go`, `internal/adapters/inbound/http/idempotency_integration_test.go`, `internal/adapters/outbound/outbox/relay_integration_test.go`, `internal/adapters/outbound/postgres/{postgres,outbox,outbox_trace}_integration_test.go` | Real Postgres and Kafka: repositories, outbox drain and trace propagation, the relay, the idempotency middleware, the real-wiring boot and the consumer flow | `make integration` |
| Contract against the specs | `internal/architecture/catalogue_fitness_test.go` (`TestEventCatalogueMatchesContract`), `internal/adapters/inbound/mcp/governance_test.go` (`TestToolRegistryGolden`) and the Spectral lint of `apis/*.yaml` in CI | The event catalogue matches `apis/asyncapi.yaml`; the MCP tool registry matches its golden file | `make arch-test`, `make test`, CI `api-lint` |
| Fitness (architecture) | `internal/architecture/*_test.go`, 14 files | Layering and runtime rules, see below | `make arch-test` |
| Mutation | `.gremlins.yaml`, gremlins v0.6.0 on `internal/domain` | The domain tests assert rather than merely execute | `make mutation`, `make mutation-full`, CI |

Nine of the `*_test.go` files carry the `integration` build tag; everything else runs in `make test`.

## Acceptance features

Eight feature files, 64 scenarios, run by `features/features_test.go` (`TestFeatures`) against the REST handlers wired to
the in-memory adapters (`features/world_test.go`); no database or broker is needed.

| File | Scenarios | Covers |
| --- | --- | --- |
| `features/http_api.feature` | 18 | Routes, status codes, problem slugs, pagination, probes-level wire behaviour |
| `features/inventory_feeds.feature` | 10 | Partial and full feeds, stale lines, supersede, `FAILED` and re-send, counters |
| `features/packaging.feature` | 10 | GTINs, pack levels, immutability of `unitsPerPack`, retire, ordering |
| `features/node_status.feature` | 8 | Node events suppressing and restoring listings, older events ignored |
| `features/listings.feature` | 5 | Listing creation, duplicate pair, suppression at creation |
| `features/idempotency.feature` | 5 | Replay, key reuse, missing key |
| `features/catalog.feature` | 4 | Item creation, discontinuation cascade |
| `features/ownerref.feature` | 4 | Seller-owned listings next to the operator-owned one |

## Make targets

| Target | What it runs |
| --- | --- |
| `make check` | `fmt-check`, `vet`, `build`, `lint` (golangci-lint, `--allow-parallel-runners`), `test` (`go test ./... -race`). The pre-commit and pre-push gate. |
| `make check-all` | `check` plus `coverage`, `arch-test`, `bdd`; run before pushing a change that touches Go code |
| `make check-fast` | `fmt-check`, `vet`, `arch-test` and the tests of the packages changed against `HEAD` (the agent Stop hook) |
| `make test` | `go test ./... -race` |
| `make coverage` | `go test ./... -race -coverprofile` with `-coverpkg=./internal/domain/...,./internal/application/...` and a **90 %** gate |
| `make bdd` | `go test ./... -run TestFeatures -v` |
| `make arch-test` | `go test ./internal/architecture/... -v` |
| `make integration` | `go test -tags=integration ./... -race -count=1`; Docker only, each test boots its own Postgres or Kafka via testcontainers (no `DATABASE_URL`, no compose file, no `t.Skip` gate) |
| `make mutation` | gremlins on each `./internal/domain/*/` package |
| `make mutation-full` | gremlins on `./internal/domain`, one worker, timeout coefficient 30 (the scheduled run) |
| `make vuln` | `govulncheck ./...` |
| `make guide-lint`, `make harness-test` | Lint the agent guides; unit-test the agent hooks |

`.gremlins.yaml` sets one worker, timeout coefficient 30 and **efficacy and mutant-coverage thresholds of 90**
(gremlins fails at or below the threshold). Measured on 2026-10-23: `catalog` 14/14, `feed` 67/67, `listing` 37/37,
`nodestatus` 15/15, `shared` 31/31 mutants killed.

## Fitness tests (`internal/architecture`)

| Test | Rule |
| --- | --- |
| `TestHexagonalArchitecture` | Domain imports nothing of the application or adapters; the application imports no adapter; inbound and outbound adapters never import each other |
| `TestMCPAdapterDependencyRule`, `TestMCPOptionalToolInputsCarryOmitempty` | The MCP adapter depends on the read use cases only; optional tool inputs carry `omitempty` |
| `TestA2AAdapterDependencyRule`, `TestA2AAdapterIsReadOnly`, `TestA2ABinaryIsReadOnlyAndKafkaFree`, `TestNoOutboundA2AClient`, `TestA2ASDKOnlyInTheA2AAdapterAndBinary` | A2A is read-only, `cmd/a2a` never touches Kafka, no outbound A2A client exists, the SDK stays in its adapter |
| `TestNoAuthMiddlewareReintroduced` | No authentication middleware reappears |
| `TestRuntimeBinaryHasNoInMemoryAdapters`, `TestRuntimeBinaryDoesNotImportTheRepositoryContract` | `cmd/**` never imports the memory adapter or the repository contract: no in-memory runtime |
| `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope`, `TestEventCatalogueMatchesContract` | CloudEvents on every message; no flat or toggled envelope; the catalogue equals `apis/asyncapi.yaml` |
| `TestKafkaConsumerGroupNeverHardcodedInline` | The consumer group comes from the environment, never a literal |
| `TestReplayConsumersSetCommitInterval` | Full-replay consumers (unique group per process) set a commit interval |
| `TestKafkaWritersSetRequiredAcks`, `TestKafkaWriterTopicNotSetTwice`, `TestDeadLetterCallsGuardCancellation` | Writers set acks, never set the topic twice, dead-letter calls guard cancellation |
| `TestOutboxRelaysPassAttemptCap` | Every relay passes the attempt cap |
| `TestKafkaIntegrationTestsUseTestcontainers`, `TestPostgresIntegrationTestsUseTestcontainers` | Integration tests boot their own containers |
| `TestNoGoFilesAtRepoRoot` | No Go file at the repository root |

Several `...SensorBites` / `...SensorFailsOnBadFixtures` tests prove each sensor fails on a deliberately bad fixture.

## CI jobs (`.github/workflows/ci.yml`)

Triggers: push to `main` and `develop`, pull requests to both, a weekly schedule (Monday 06:00 UTC) and manual dispatch.

| Job | When | What |
| --- | --- | --- |
| `lint` | always | golangci-lint v2.14.0 |
| `guide-lint` | always, advisory | `scripts/harness/guide_lint.py`, `test_hook.py` |
| `complexity` | always | gocyclo, cyclop, gocognit, nestif, funlen (gate 15) |
| `test` | always | build, vet and `make coverage` (90 % gate) |
| `bdd` | always | `go test ./... -run TestFeatures -v` |
| `integration` | always | `go test -tags=integration ./... -race -count=1` |
| `mutation-fast` | always | gremlins on every domain package |
| `mutation` | schedule and manual | exhaustive gremlins on `./internal/domain`; opens or closes the `harness:red` issue |
| `drift` | schedule and manual | dead code, unused `go.mod` entries, unused frontend exports, coverage quality |
| `api-lint` | always | Spectral on `apis/openapi.yaml` and `apis/asyncapi.yaml` |
| `vuln` | always | govulncheck |
| `helm-lint` | pull requests | `ct lint` and the Service-selector test |
| `chart-tests` | always | `charts/offer-availability/tests` |
| `arch-test` | always | `go test ./internal/architecture/... -v` |
| `web` | always | the console remote (`web/`): lint, `tsc -b`, tests, build |
| `docker-publish` | push to `main` | build, push, sign and attest the image |
| `release` | push to `main` | tag the image, package and push the chart, create the release |

The documentation site has its own workflow, `.github/workflows/docs.yml` (build on pull requests, deploy on push to
`main`). To check it locally run `npm ci`, `npx tsc --noEmit -p .` and `npm run build` in `docs/`.

## Running one thing

```sh
go test ./internal/domain/feed/... -race               # one package
go test ./features/... -run 'TestFeatures' -v           # all scenarios
go test -tags=integration ./cmd/api/... -race -count=1  # the real-wiring test (Docker)
```
