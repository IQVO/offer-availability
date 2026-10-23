---
name: how-to-test
description: Write or review tests and diagnose a failing coverage, mutation, bdd or integration job in this repo: the test layers, the 90% gate, gremlins threshold semantics, the testcontainers rule. Use when adding tests, killing a surviving mutant, or fixing a red check.
---

# How to test

## The layers, in order of what they prove

1. **Domain and use-case unit tests** (`go test ./...`): table-driven, one row per
   invariant and per legal and illegal transition
   (`internal/domain/*/*_test.go`, `internal/application/usecases/*_test.go`).
   Use cases run over the memory test doubles in `internal/adapters/outbound/memory`
   behind fault-injecting wrappers (`usecases/helpers_test.go`). Never a real
   network or database call.
2. **Coverage** (`make coverage`, 90% gate over `./internal/domain/...` and
   `./internal/application/...`): proves lines ran, not that anything was asserted.
3. **Mutation** (`make mutation`, gremlins, one run per `internal/domain/<pkg>`):
   proves the tests assert. A survivor (LIVED) is a bug no test would catch.
4. **Behaviour** (`make bdd`, godog): `features/*.feature` with the steps in
   `features/world_test.go`; the runner is `features/features_test.go` (never at the
   repo root). The scenarios drive the use cases and name refusals by the model's
   `Err` name.
5. **Adapter tests**: the HTTP adapter with `httptest`
   (`internal/adapters/inbound/http/routes_test.go`), the encoders with golden exact
   JSON (`internal/adapters/outbound/kafka/encoder_test.go`), the relay over a fake
   store (`internal/adapters/outbound/outbox/relay_test.go`), the consumer over a fake
   reader (`internal/adapters/inbound/kafka/node_consumer_test.go`).
6. **Integration** (`make integration`, build tag `integration`, needs Docker):
   the same repository suite (`internal/adapters/outbound/repocontract`) on Postgres
   (`postgres_integration_test.go`), `Drain`, the attempt cap and retention
   (`outbox_integration_test.go`), the relay against Postgres and Kafka
   (`outbox/relay_integration_test.go`), the Idempotency middleware
   (`inbound/http/idempotency_integration_test.go`), and the real-wiring test
   `cmd/api/e2e_integration_test.go`.

## Mutation testing: `<=` fails, not `>=`

gremlins fails when the measured value is `<=` the threshold, so
`.gremlins.yaml` thresholds sit strictly BELOW what the package measures. Domain
packages measure 100%: keep it. Run one package:
`gremlins unleash ./internal/domain/listing` (single worker, config in `.gremlins.yaml`).
Kill a survivor with a test that fails on that exact change; lower a threshold
only with a justification in the PR. A new `internal/domain/<pkg>` is gated the
moment it exists (the Makefile and CI loop over the glob).

## Pitfalls that cost real CI failures

- `Restore()` boundary tests kill the last `CONDITIONALS_BOUNDARY` mutants: test
  the value just inside and just outside every bound.
- A tagless `switch { case ... }` under-reports mutator coverage: write the
  classification as an if-chain (`.claude/memory/MEMORY.md`).
- Instants are microsecond-precise (`shared.UTC`, ADR 0002 A21): a test that adds a
  nanosecond to an instant compares equal instants.
- Split a lint-complex test with named assertion helpers; never thin the
  assertions to satisfy `cyclop`/`gocognit`/`funlen`.
- `ports` holds interfaces only (filters live in `internal/application/repository`,
  DTOs in `internal/application/views`).
- Memory doubles round-trip aggregates through `Snapshot`/`Restore`; a miss returns
  `ports.ErrNotFound`.
- The pre-commit hook runs format, vet and lint, not the tests: run `make check`
  before committing (the pre-push hook does, and rejects the push).
- A NUL byte must be tested through the HTTP adapter (`nul.go`), not only in the
  domain.

## Postgres/Kafka integration tests: testcontainers, never a skip-gate

Integration files end in `_integration_test.go`, start with `//go:build integration`
and boot their own containers through `internal/testing/pgtest` (one Postgres per
test binary, a database cloned per test from a migrated template) and
`internal/testing/kafkatest` (one broker per binary). The sensors in
`internal/architecture/fitness_test.go` fail the build on `os.Getenv("DATABASE_URL")`
gates, `t.Skip`, a hardcoded broker address, or a Kafka-touching file that neither
imports the testcontainers kafka module nor uses `kafkatest`. Name the pool type
as `pgtest.Pool` so the test file imports no driver. The first write to a topic
that does not exist yet can fail once with an unknown-topic error: wait for the
events, do not assert on the first pass.

Run the suite as a background job and read the file:
`go test -tags=integration ./... -race -count=1 -timeout 900s > /path/out.txt 2>&1`.

## Verify before opening the PR

`make check`, `make coverage`, `make mutation` (when domain code changed),
`make arch-test`, `make integration`.
