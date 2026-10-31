# Before the first release: offer-availability

The first merge to `main` runs `docker-publish` and `release`. Two services built from
this template (pick-face-replenishment, task-interleaving) each needed a rework because
packaging was missing or wrong when that moment came. Work through this list on a feature
branch, **before** the first `develop` → `main` PR. Delete a row only when the service
really does not have that part, and say so in the PR.

`new-service.sh` already generated `Dockerfile` and `.dockerignore` for you (verbatim from
the fleet reference). The chart is NOT generated: it is shaped by what the service runs
(consumer groups, MCP, analytics, frontend), so copy the closest sibling's
`charts/<repo>/` and adapt it. References on `origin/develop`:
`IQVO/pick-face-replenishment` (Kafka consumers, MCP, analytics, frontend) and
`IQVO/task-interleaving`.

| # | Item | Status | Done when | Guarded by |
|---|------|--------|-----------|------------|
| 1 | **Real runtime only.** `cmd/api` wires Postgres and Kafka and nothing else; the test doubles under `internal/adapters/outbound/memory` are never imported by `cmd/**`. `DATABASE_URL`, `KAFKA_BROKERS` and `NODE_TOPOLOGY_CONSUMER_GROUP` are mandatory. | [x] done | `cmd/**` has no import of the memory or repocontract packages; `run(ctx)` returns a boot error naming the missing variable before anything opens. | `TestRuntimeBinaryHasNoInMemoryAdapters`, `TestRuntimeBinaryDoesNotImportTheRepositoryContract`; boot errors in `cmd/api/config_test.go`. |
| 2 | **No Go file at the repo root.** The godog runner is in `features/`. | [x] done | `ls *.go` prints nothing; `make bdd` finds `TestFeatures`. | `TestNoGoFilesAtRepoRoot`. |
| 3 | **`Dockerfile` builds every `cmd/*`.** `ENTRYPOINT ["./api"]` is `cmd/api`, `EXPOSE 8080` is the default `HTTP_ADDR`. | [x] done | `docker build .` succeeds and `docker run --rm <image>` exits 1 with `missing required environment variable: DATABASE_URL` (verified). The build stage digest was moved to the one the sibling services pin, because the template's older digest ships Go 1.27.2 while `go.mod` requires 1.27.3. | `trivy-scan` / `docker-publish` jobs. |
| 4 | **`.dockerignore`** excludes docs, harness, test output. | [x] done | Build context is small. | none: review once. |
| 5 | **Helm chart `charts/offer-availability/`** with `Chart.yaml` `name: offer-availability`; chart directory, `Chart.yaml` name and the `helm package` file name must all be `offer-availability`. | [x] done | `helm lint charts/offer-availability -f charts/offer-availability/ci/default-values.yaml` (Helm 4.3.1 and `ct lint`) passes, `helm template` renders with the required values and `helm package` writes `offer-availability-<version>.tgz`; chart dir, `Chart.yaml` name and package name are all `offer-availability`. One component only (`api`): Deployment, Service, ConfigMap, Secret or `database.existingSecret`, ServiceAccount, and HPA, Ingress and HTTPRoute that default off. | `helm-lint` (PR), `chart-tests`, `release`. |
| 6 | **`DATABASE_URL` comes from a Secret**, never from the ConfigMap. | [x] done | `DATABASE_URL` is a `secretKeyRef` (the chart's Secret from `database.url`, or `database.existingSecret`); `MIGRATIONS_DATABASE_URL` is an optional `secretKeyRef` (`optional: true`, the binary falls back to `DATABASE_URL`); neither is ever in the ConfigMap. | `render_test.sh`. |
| 7 | **Pod hardening and shutdown room.** `terminationGracePeriodSeconds` above the binary's bounds: `SHUTDOWN_DRAIN_DELAY` (5 s) + HTTP drain (10 s) + the three loops (10 s) = 25 s, so 30 s or more. | [x] done | `terminationGracePeriodSeconds` defaults to 60 and the chart refuses anything under `SHUTDOWN_DRAIN_DELAY` + 25 s (30 s at the default 5 s drain; the floor follows `config.shutdownDrainDelay`). Pod: `runAsNonRoot`, uid/gid 1000, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, capabilities drop `ALL`, seccomp `RuntimeDefault`; probes `/healthz` (startup, liveness) and `/readyz` (readiness). | `render_test.sh`. |
| 8 | **Chart tests in `charts/offer-availability/tests/`.** Every env var `cmd/api` reads must be rendered: `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`, `KAFKA_BROKERS`, `NODE_TOPOLOGY_CONSUMER_GROUP`, `HTTP_ADDR`, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `RETENTION_INTERVAL`, `SHUTDOWN_DRAIN_DELAY`, `LOG_LEVEL`. | [x] done | `bash charts/offer-availability/tests/render_test.sh` prints `PASS` and `python3 charts/offer-availability/tests/test_service_selectors.py` prints `PASS`. The render test scans `cmd/api/config.go` and the Postgres adapter for every env constant (exactly the ten listed), asserts each is rendered into the Deployment, that the ConfigMap has no key the binary does not read, that no `*_MODE` exists, and that every refusal fires. | `chart-tests` job. |
| 9 | **Consumer group id is a chart value**, stable and service-prefixed, never derived from the pod name. | [x] done | `NODE_TOPOLOGY_CONSUMER_GROUP` is rendered from `config.nodeTopologyConsumerGroup` (no default); empty or blank refuses to render, naming the value. `ci/default-values.yaml` uses `offer-availability-node-topology`. | `render_test.sh`. |
| 10 | **Optional CI jobs match the service.** `web` and `docs-api-drift` are not in `ci.yml` (no `web/`, no generated docs); `api-lint` runs the two Spectral rulesets on `apis/openapi.yaml` and `apis/asyncapi.yaml`. | [x] done | No job in `ci.yml` is permanently red or skipped for a missing directory. | first PR's CI run. |
| 11 | **Repository secrets** `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`; GHCR push allowed for the Actions token. | [ ] ask the repo admin | A dry run of `docker-publish` on a throwaway branch. | none. |
| 12 | **`govulncheck` is clean** before the PR to `main`. | [x] done | `make vuln` exits 0 after bumping `golang.org/x/crypto` to v0.56.0, `golang.org/x/text` to v0.41.0 and `github.com/moby/go-archive` to v0.3.0 (GO-2026-6355, GO-2026-6354, GO-2026-6629, GO-2026-6253), the bumps the sibling services use. | `vuln` job. |

Follow-up left: the repository secrets (row 11, ask the repo admin). Keep this file: it is the
service's packaging record. It prevents three failures: a runtime that can run without Postgres/Kafka (row 1), Go files at the root (row 2), a release
that fails on the first push to `main` because packaging was never built (rows 3-8).
