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
| 5 | **Helm chart `charts/offer-availability/`** with `Chart.yaml` `name: offer-availability`; chart directory, `Chart.yaml` name and the `helm package` file name must all be `offer-availability`. | [ ] **follow-up** (out of scope of the service build) | `helm lint` and `helm template` with the required values pass. | `helm-lint`, `release`. |
| 6 | **`DATABASE_URL` comes from a Secret**, never from the ConfigMap. | [ ] follow-up (chart) | `helm template` shows `secretKeyRef`. | chart test (row 8). |
| 7 | **Pod hardening and shutdown room.** `terminationGracePeriodSeconds` above the binary's bounds: `SHUTDOWN_DRAIN_DELAY` (5 s) + HTTP drain (10 s) + the three loops (10 s) = 25 s, so 30 s or more. | [ ] follow-up (chart) | Values and Deployment carry it. | chart test (row 8). |
| 8 | **Chart tests in `charts/offer-availability/tests/`.** Every env var `cmd/api` reads must be rendered: `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`, `KAFKA_BROKERS`, `NODE_TOPOLOGY_CONSUMER_GROUP`, `HTTP_ADDR`, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `RETENTION_INTERVAL`, `SHUTDOWN_DRAIN_DELAY`, `LOG_LEVEL`. | [ ] follow-up (chart) | `bash charts/offer-availability/tests/render_test.sh` prints `PASS`. | `chart-tests` job (only warns when the directory is empty). |
| 9 | **Consumer group id is a chart value**, stable and service-prefixed, never derived from the pod name. | [ ] follow-up (chart) | `NODE_TOPOLOGY_CONSUMER_GROUP` is rendered from `config.*` and refuses to render empty. | chart test (row 8); `kafka-testing-and-consumers.md` rule. |
| 10 | **Optional CI jobs match the service.** `web` and `docs-api-drift` are not in `ci.yml` (no `web/`, no generated docs); `api-lint` runs the two Spectral rulesets on `apis/openapi.yaml` and `apis/asyncapi.yaml`. | [x] done | No job in `ci.yml` is permanently red or skipped for a missing directory. | first PR's CI run. |
| 11 | **Repository secrets** `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`; GHCR push allowed for the Actions token. | [ ] ask the repo admin | A dry run of `docker-publish` on a throwaway branch. | none. |
| 12 | **`govulncheck` is clean** before the PR to `main`. | [x] done | `make vuln` exits 0 after bumping `golang.org/x/crypto` to v0.56.0, `golang.org/x/text` to v0.41.0 and `github.com/moby/go-archive` to v0.3.0 (GO-2026-6355, GO-2026-6354, GO-2026-6629, GO-2026-6253), the bumps the sibling services use. | `vuln` job. |

Follow-ups left for after the service build: the Helm chart and its tests (rows 5-9), the repository secrets (row 11). Keep this file: it is the
service's packaging record. It prevents three failures: a runtime that can run without Postgres/Kafka (row 1), Go files at the root (row 2), a release
that fails on the first push to `main` because packaging was never built (rows 3-8).
