#!/usr/bin/env bash
# Chart render assertions: Postgres and Kafka are the only runtime, so the
# mandatory configuration has no silent default, no *_MODE is ever rendered and
# the env cmd/api reads is exactly the env the chart renders.
# Run: bash charts/offer-availability/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=offer-availability
group=offer-availability-node-topology
# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and the consumer group. None is ever dialled.
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092
  --set "config.nodeTopologyConsumerGroup=$group")

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v helm >/dev/null || fail "helm is not installed"

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}

# --- the ConfigMap is mounted --------------------------------------------------------
grep -A3 'envFrom:' <<<"$deployment" | grep -q 'configMapRef:' \
  || fail "the Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$deployment" | grep -q "name: $configmap_name" \
  || fail "the Deployment envFrom does not reference ConfigMap $configmap_name"

# --- chart vs binary: every env var cmd/api reads is rendered, and nothing else -------
# The names come from the env* constants of cmd/api/config.go and the Env*
# constants of the Postgres adapter, so a new variable in the binary fails this
# test until the chart renders it.
binary_env() {
  {
    grep -ohE '^[[:space:]]*env[A-Za-z]+[[:space:]]*=[[:space:]]*"[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/api/config.go"
    grep -ohE '^[[:space:]]*Env[A-Za-z]+[[:space:]]*=[[:space:]]*"[A-Z][A-Z0-9_]*"' "$repo_dir/internal/adapters/outbound/postgres/config.go"
  } | sed -E 's/.*"([A-Z0-9_]+)"/\1/' | sort -u
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
vars=($(binary_env))
(( ${#vars[@]} == 10 )) || fail "found ${#vars[@]} env constants (${vars[*]}), want the 10 of docs/before-first-release.md row 8; the scan is looking in the wrong place or the binary changed"
for var in DATABASE_URL MIGRATIONS_DATABASE_URL KAFKA_BROKERS NODE_TOPOLOGY_CONSUMER_GROUP HTTP_ADDR \
  OUTBOX_RELAY_INTERVAL OUTBOX_MAX_ATTEMPTS RETENTION_INTERVAL SHUTDOWN_DRAIN_DELAY LOG_LEVEL; do
  [[ " ${vars[*]} " == *" $var "* ]] || fail "the binary no longer reads $var (update the chart and this test)"
done
for var in "${vars[@]}"; do
  grep -q "name: $var\$" <<<"$deployment" || fail "the Deployment does not render $var, which cmd/api reads"
done
# No env var the binary does not read (extraEnv is empty by default). The
# telemetry env is read in cmd/api/telemetry.go (and ENVIRONMENT by telemetry.Setup).
telemetry_vars=" OTEL_SERVICE_NAME SERVICE_VERSION OTEL_EXPORTER_OTLP_ENDPOINT ENVIRONMENT "
while read -r name; do
  [[ " ${vars[*]} " == *" $name "* || "$telemetry_vars" == *" $name "* ]] || fail "the Deployment renders $name, which cmd/api does not read"
done < <(awk '/^          env:/ {e=1; next} e && /^          [a-z]/ {e=0} e && /^            - name:/ {print $3}' <<<"$deployment")
# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys).
while read -r key; do
  [[ " ${vars[*]} " == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$configmap")
# No *_MODE anywhere (the service has none), and no in-memory wording.
render_all="$(helm template "$release" "$chart_dir" "${base[@]}")"
if grep -qE '[A-Z]_MODE' <<<"$render_all"; then fail "the render carries a *_MODE variable"; fi
if grep -qiE 'in-memory|silently runs|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then
  fail "the render or NOTES.txt mentions an in-memory / permissive mode"
fi

# --- observability (ADR 0009, fleet standard item 7) ----------------------------------
# The api renders the fleet pipeline's env: the Collector endpoint, the service
# name, the version (image tag, appVersion when empty) and the environment.
otel_endpoint="otel-collector.observability.svc.cluster.local:4317"
[[ "$(env_value "$deployment" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] \
  || fail "the api Deployment does not render OTEL_EXPORTER_OTLP_ENDPOINT from otel.endpoint"
[[ "$(env_value "$deployment" OTEL_SERVICE_NAME)" == "offer-availability" ]] \
  || fail "the api Deployment does not render OTEL_SERVICE_NAME=offer-availability from otel.serviceName"
[[ "$(env_value "$deployment" SERVICE_VERSION)" == "0.1.0" ]] || fail "SERVICE_VERSION must default to the chart appVersion"
[[ "$(env_value "$deployment" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from environment"
for var in OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT; do
  grep -q "$var" "$repo_dir"/cmd/api/telemetry.go || fail "cmd/api no longer reads $var (update the chart and this test)"
done
tagged="$(helm template "$release" "$chart_dir" "${base[@]}" --set image.tag=v9.9.9 --set otel.serviceName=offers --set otel.endpoint=http://collector:4317 \
  --set environment=staging --show-only templates/deployment.yaml)"
[[ "$(env_value "$tagged" SERVICE_VERSION)" == "v9.9.9" ]] || fail "SERVICE_VERSION must follow image.tag"
[[ "$(env_value "$tagged" OTEL_SERVICE_NAME)" == "offers" ]] || fail "OTEL_SERVICE_NAME must follow otel.serviceName"
[[ "$(env_value "$tagged" OTEL_EXPORTER_OTLP_ENDPOINT)" == "http://collector:4317" ]] || fail "OTEL_EXPORTER_OTLP_ENDPOINT must follow otel.endpoint"
[[ "$(env_value "$tagged" ENVIRONMENT)" == "staging" ]] || fail "ENVIRONMENT must follow environment"
# otel.enabled=false renders no OTEL_* env (SERVICE_VERSION stays: it also labels the build).
otel_off="$(helm template "$release" "$chart_dir" "${base[@]}" --set otel.enabled=false --set mcp.enabled=true --set a2a.enabled=true)"
if grep -q 'name: OTEL_' <<<"$otel_off"; then fail "otel.enabled=false must render no OTEL_* env in any component"; fi
grep -q 'name: SERVICE_VERSION$' <<<"$otel_off" || fail "SERVICE_VERSION must still render with otel.enabled=false"
# The values are chart-owned: extraEnv may not set them a second time.
for owned in OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT; do
  err="$(helm template "$release" "$chart_dir" "${base[@]}" --set "extraEnv[0].name=$owned" --set 'extraEnv[0].value=x' 2>&1)" \
    && fail "extraEnv must not be able to set $owned"
  grep -qF "extraEnv sets $owned" <<<"$err" || fail "the refusal for extraEnv $owned does not name it: $err"
  err="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set "mcp.extraEnv[0].name=$owned" --set 'mcp.extraEnv[0].value=x' 2>&1)" \
    && fail "mcp.extraEnv must not be able to set $owned"
  grep -qF "mcp.extraEnv sets $owned" <<<"$err" || fail "the refusal for mcp.extraEnv $owned does not name it: $err"
  err="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --set "a2a.extraEnv[0].name=$owned" --set 'a2a.extraEnv[0].value=x' 2>&1)" \
    && fail "a2a.extraEnv must not be able to set $owned"
  grep -qF "a2a.extraEnv sets $owned" <<<"$err" || fail "the refusal for a2a.extraEnv $owned does not name it: $err"
done

# --- the mandatory configuration is rendered from values ------------------------------
[[ "$(env_value "$deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
grep -q '^  KAFKA_BROKERS: "kafka.example.invalid:9092"' <<<"$configmap" || fail "the ConfigMap does not carry KAFKA_BROKERS"
# Row 9: the consumer group id is a stable chart value, in both places.
[[ "$(env_value "$deployment" NODE_TOPOLOGY_CONSUMER_GROUP)" == "$group" ]] || fail "NODE_TOPOLOGY_CONSUMER_GROUP is not rendered from config.nodeTopologyConsumerGroup"
grep -q "^  NODE_TOPOLOGY_CONSUMER_GROUP: \"$group\"" <<<"$configmap" || fail "the ConfigMap does not carry NODE_TOPOLOGY_CONSUMER_GROUP"
if grep -q 'NODE_TOPOLOGY_CONSUMER_GROUP' <<<"$(grep -B1 -A3 'fieldRef' <<<"$deployment")"; then
  fail "the consumer group must never be derived from the pod (fieldRef)"
fi
# A tuned config.* value reaches both the ConfigMap and the Deployment.
tuned_args=("${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 --set config.retentionInterval=30m
  --set config.logLevel=debug --set config.httpAddr=:9000 --set config.shutdownDrainDelay=2s)
tuned_cm="$(helm template "$release" "$chart_dir" "${tuned_args[@]}" --show-only templates/configmap.yaml)"
tuned_dep="$(helm template "$release" "$chart_dir" "${tuned_args[@]}" --show-only templates/deployment.yaml)"
for kv in OUTBOX_RELAY_INTERVAL=7s OUTBOX_MAX_ATTEMPTS=5 RETENTION_INTERVAL=30m LOG_LEVEL=debug HTTP_ADDR=:9000 SHUTDOWN_DRAIN_DELAY=2s; do
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$tuned_cm" || fail "${kv%%=*} is not rendered into the ConfigMap"
  [[ "$(env_value "$tuned_dep" "${kv%%=*}")" == "${kv#*=}" ]] || fail "${kv%%=*} is not rendered into the Deployment"
done
# OUTBOX_MAX_ATTEMPTS=0 (unlimited) is rendered as is.
unlimited="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"

# The chart defaults repeat the binary's defaults: they must agree.
[[ "$(env_value "$deployment" OUTBOX_MAX_ATTEMPTS)" == "$(grep -ohE 'DefaultMaxAttempts[[:space:]]*=[[:space:]]*[0-9]+' "$repo_dir"/internal/adapters/outbound/outbox/relay.go | grep -oE '[0-9]+$')" ]] \
  || fail "the chart's OUTBOX_MAX_ATTEMPTS default differs from outbox.DefaultMaxAttempts"
grep -qE 'DefaultInterval[[:space:]]*=[[:space:]]*time.Second' "$repo_dir/internal/adapters/outbound/outbox/relay.go" \
  && [[ "$(env_value "$deployment" OUTBOX_RELAY_INTERVAL)" == "1s" ]] || fail "the chart's OUTBOX_RELAY_INTERVAL default differs from outbox.DefaultInterval"
grep -qE 'defaultShutdownDrain[[:space:]]*=[[:space:]]*5 \* time.Second' "$repo_dir/cmd/api/config.go" \
  && [[ "$(env_value "$deployment" SHUTDOWN_DRAIN_DELAY)" == "5s" ]] || fail "the chart's SHUTDOWN_DRAIN_DELAY default differs from defaultShutdownDrain"
grep -qE 'defaultRetentionEvery[[:space:]]*=[[:space:]]*time.Hour' "$repo_dir/cmd/api/config.go" \
  && [[ "$(env_value "$deployment" RETENTION_INTERVAL)" == "1h" ]] || fail "the chart's RETENTION_INTERVAL default differs from defaultRetentionEvery"
grep -qE 'defaultHTTPAddr[[:space:]]*=[[:space:]]*":8080"' "$repo_dir/cmd/api/config.go" \
  && [[ "$(env_value "$deployment" HTTP_ADDR)" == ":8080" ]] || fail "the chart's HTTP_ADDR default differs from defaultHTTPAddr"

# --- row 6: DATABASE_URL comes from a Secret, never the ConfigMap ---------------------
grep -A4 'name: DATABASE_URL$' <<<"$deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$deployment" | grep -q 'optional: true' || fail "MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$deployment" | grep -q 'key: MIGRATIONS_DATABASE_URL' || fail "MIGRATIONS_DATABASE_URL must read database.migrationsExistingSecretKey"
# An existing Secret is referenced and no Secret is created.
existing="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db --set kafka.brokers=k:9092 \
  --set "config.nodeTopologyConsumerGroup=$group")"
grep -A4 'name: DATABASE_URL$' <<<"$existing" | grep -q 'name: my-db' || fail "database.existingSecret is not the DATABASE_URL secretKeyRef"
if grep -q 'kind: Secret' <<<"$existing"; then fail "no Secret may be created when database.existingSecret is set"; fi
grep -q 'kind: Secret' <<<"$render_all" || fail "database.url must render the Secret"

# --- row 7: hardening, probes, shutdown room -------------------------------------------
for needle in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault'; do
  grep -q "$needle" <<<"$deployment" || fail "the Deployment lacks '$needle'"
done
grep -A1 'drop:' <<<"$deployment" | grep -q 'ALL' || fail "the Deployment must drop ALL capabilities"
grep -A3 'livenessProbe:' <<<"$deployment" | grep -q 'path: /healthz' || fail "liveness must probe /healthz"
grep -A3 'readinessProbe:' <<<"$deployment" | grep -q 'path: /readyz' || fail "readiness must probe /readyz"
grace="$(awk '/terminationGracePeriodSeconds:/ {print $2}' <<<"$deployment")"
(( grace >= 30 )) || fail "terminationGracePeriodSeconds is $grace, want >= 30 (drain 5s + HTTP 10s + loops 10s)"
# The bounds are read from the binary: the budget the chart checks is the real one.
grep -qE 'shutdownTimeout[[:space:]]*=[[:space:]]*10 \* time.Second' "$repo_dir/cmd/api/config.go" || fail "cmd/api shutdownTimeout is no longer 10s: update the chart's budget"
grep -qE 'workerDrainTimeout[[:space:]]*=[[:space:]]*10 \* time.Second' "$repo_dir/cmd/api/config.go" || fail "cmd/api workerDrainTimeout is no longer 10s: update the chart's budget"
# The default pod with every optional off: HPA, Ingress and HTTPRoute do not render.
if grep -qE 'kind: (HorizontalPodAutoscaler|Ingress|HTTPRoute)' <<<"$render_all"; then fail "HPA/Ingress/HTTPRoute must be off by default"; fi

# --- refusals: the mandatory values name themselves ------------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':
$err"
}
expect_ok() { # expect_ok <why> <helm args...>
  local why="$1"; shift
  helm template "$release" "$chart_dir" "$@" >/dev/null || fail "the chart must render: $why"
}
no_db=(--set kafka.brokers=k:9092 --set "config.nodeTopologyConsumerGroup=$group")
no_kafka=(--set database.url=postgres://u@h/db --set "config.nodeTopologyConsumerGroup=$group")
no_group=(--set database.url=postgres://u@h/db --set kafka.brokers=k:9092)
expect_refusal "no database source" "database.url or database.existingSecret" "${no_db[@]}"
expect_refusal "no kafka.brokers" "kafka.brokers" "${no_kafka[@]}"
expect_refusal "blank kafka.brokers" "kafka.brokers" "${no_kafka[@]}" --set 'kafka.brokers= \, '
expect_refusal "no consumer group" "config.nodeTopologyConsumerGroup" "${no_group[@]}"
expect_refusal "an empty consumer group" "NODE_TOPOLOGY_CONSUMER_GROUP" "${no_group[@]}" --set config.nodeTopologyConsumerGroup=
expect_refusal "a blank consumer group" "config.nodeTopologyConsumerGroup" "${no_group[@]}" --set 'config.nodeTopologyConsumerGroup=   '
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=NODE_TOPOLOGY_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "any *_MODE through extraEnv" "extraEnv sets DEMAND_MODE" "${base[@]}" --set 'extraEnv[0].name=DEMAND_MODE' --set 'extraEnv[0].value=kafka'
expect_refusal "a grace period under 30s" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=29
expect_refusal "no grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=0
expect_ok "a 30s grace period (drain 5s + 25s)" "${base[@]}" --set terminationGracePeriodSeconds=30
# A longer drain delay raises the floor: 20s drain needs 45s.
expect_refusal "a grace period below the drain delay + 25s" "want at least 45" "${base[@]}" --set config.shutdownDrainDelay=20s --set terminationGracePeriodSeconds=44
expect_ok "a 45s grace period with a 20s drain" "${base[@]}" --set config.shutdownDrainDelay=20s --set terminationGracePeriodSeconds=45
expect_ok "a zero drain delay with a 25s grace period" "${base[@]}" --set config.shutdownDrainDelay=0 --set terminationGracePeriodSeconds=25
expect_refusal "an unparsable drain delay" "config.shutdownDrainDelay" "${base[@]}" --set config.shutdownDrainDelay=soon

# --- optional components (ADR 0007 mcp, ADR 0010 a2a, ADR 0008 web remote) -------------
# Every optional component is OFF by default: a default render is the api alone.
for c in mcp a2a frontend; do
  if grep -q "app.kubernetes.io/component: $c\$" <<<"$render_all"; then
    fail "the $c component must not render while its enabled flag is false"
  fi
done

# binary_env_of <main.go...>: the env literals one binary reads.
binary_env_of() {
  local f
  for f in "$@"; do
    grep -hoE '([Gg]etenv|env)\("[A-Z][A-Z0-9_]*"' "$f" || true
  done | sed -E 's/.*"([A-Z0-9_]+)".*/\1/' | sort -u
}
# assert_env_rendered <rendered> <label> <vars...>
assert_env_rendered() {
  local rendered="$1" label="$2"; shift 2
  local var
  for var in "$@"; do
    grep -q "name: $var\$" <<<"$rendered" || fail "$label does not render $var, which its binary reads"
  done
}

# mcp: its own env, never the api ConfigMap, never Kafka (ADR 0007).
mcp_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-deployment.yaml)"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_dep" || fail "the mcp Deployment was not rendered with mcp.enabled=true"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_dep" || fail "the mcp Deployment must run /app/mcp"
if grep -q 'configMapRef:' <<<"$mcp_dep"; then fail "mcp-deployment.yaml must not mount the api ConfigMap"; fi
if grep -Eq 'name: (KAFKA_BROKERS|OUTBOX_[A-Z_]+|[A-Z]+_CONSUMER_GROUP)$' <<<"$mcp_dep"; then
  fail "the mcp Deployment must not get Kafka/relay/consumer env (read-only, never dials Kafka)"
fi
# shellcheck disable=SC2207
mcp_vars=($(binary_env_of "$repo_dir/cmd/mcp/main.go" "$repo_dir/cmd/mcp/telemetry.go"))
(( ${#mcp_vars[@]} == 7 )) || fail "found ${#mcp_vars[@]} env literals in cmd/mcp (${mcp_vars[*]}), want the 4 of MCP_ADDR/LOG_LEVEL/DATABASE_URL/MIGRATIONS_DATABASE_URL plus OTEL_SERVICE_NAME/SERVICE_VERSION/OTEL_EXPORTER_OTLP_ENDPOINT; the scan is looking in the wrong place"
assert_env_rendered "$mcp_dep" "the mcp Deployment" "${mcp_vars[@]}" ENVIRONMENT
[[ "$(env_value "$mcp_dep" OTEL_SERVICE_NAME)" == "offer-availability-mcp" ]] || fail "the mcp component must export as <otel.serviceName>-mcp"
[[ "$(env_value "$mcp_dep" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] || fail "the mcp component must export to otel.endpoint"
[[ "$(env_value "$mcp_dep" SERVICE_VERSION)" == "0.1.0" ]] || fail "the mcp SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$mcp_dep" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from mcp.environment"
mcp_named="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set otel.serviceName=offers --set mcp.serviceVersion=dev --show-only templates/mcp-deployment.yaml)"
[[ "$(env_value "$mcp_named" OTEL_SERVICE_NAME)" == "offers-mcp" ]] || fail "the mcp service name must follow otel.serviceName"
[[ "$(env_value "$mcp_named" SERVICE_VERSION)" == "dev" ]] || fail "SERVICE_VERSION must come from mcp.serviceVersion"
# The mcp Service and route render together with the component.
mcp_all="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set mcp.route.enabled=true \
  --show-only templates/mcp-service.yaml --show-only templates/mcp-route.yaml)"
grep -q 'kind: Service' <<<"$mcp_all" || fail "the mcp Service was not rendered with mcp.enabled=true"
grep -q 'kind: HTTPRoute' <<<"$mcp_all" || fail "the mcp HTTPRoute was not rendered with mcp.route.enabled=true"
grep -q 'replacePrefixMatch: /mcp' <<<"$mcp_all" || fail "the mcp HTTPRoute must rewrite the prefix to /mcp"
if grep -q 'kind: HTTPRoute' <<<"$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true)"; then
  fail "the mcp HTTPRoute must stay off while mcp.route.enabled is false"
fi

# a2a: its own env, never the api ConfigMap, never Kafka (ADR 0010). The agent is
# read-only like the mcp server, so it gets no KAFKA_BROKERS / EVENT_PUBLISHER either.
a2a_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --show-only templates/a2a-deployment.yaml)"
grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_dep" || fail "the a2a Deployment was not rendered with a2a.enabled=true"
grep -q 'command: \["/app/a2a"\]' <<<"$a2a_dep" || fail "the a2a Deployment must run /app/a2a"
if grep -q 'configMapRef:' <<<"$a2a_dep"; then fail "a2a-deployment.yaml must not mount the api ConfigMap"; fi
if grep -Eq 'name: (KAFKA_BROKERS|EVENT_PUBLISHER|OUTBOX_[A-Z_]+|[A-Z]+_CONSUMER_GROUP)$' <<<"$a2a_dep"; then
  fail "the a2a Deployment must not get Kafka/relay/consumer env (read-only, never dials Kafka)"
fi
# shellcheck disable=SC2207
a2a_vars=($(binary_env_of "$repo_dir/cmd/a2a/main.go" "$repo_dir/cmd/a2a/telemetry.go"))
(( ${#a2a_vars[@]} == 8 )) || fail "found ${#a2a_vars[@]} env literals in cmd/a2a (${a2a_vars[*]}), want the 5 of A2A_ADDR/A2A_AGENT_URL/LOG_LEVEL/DATABASE_URL/MIGRATIONS_DATABASE_URL plus OTEL_SERVICE_NAME/SERVICE_VERSION/OTEL_EXPORTER_OTLP_ENDPOINT; the scan is looking in the wrong place"
assert_env_rendered "$a2a_dep" "the a2a Deployment" "${a2a_vars[@]}" ENVIRONMENT
# Every variable cmd/a2a refuses to boot without ("<VAR> is required") is rendered: the
# regex allows digits, or A2A_AGENT_URL would be read as A_AGENT_URL and the check would
# fail for the wrong reason.
# shellcheck disable=SC2207
a2a_required=($(grep -ohE '[A-Z][A-Z0-9_]+ is required' "$repo_dir"/cmd/a2a/*.go | sed -E 's/ is required//' | sort -u))
[[ " ${a2a_required[*]} " == *" A2A_AGENT_URL "* && " ${a2a_required[*]} " == *" DATABASE_URL "* ]] \
  || fail "the mandatory-env scan of cmd/a2a found (${a2a_required[*]}), want DATABASE_URL and A2A_AGENT_URL"
assert_env_rendered "$a2a_dep" "the a2a Deployment (mandatory env)" "${a2a_required[@]}"
# The Agent Card URL defaults to the in-cluster Service DNS (namespace-derived, so match the
# shape) and is overridable. The assignments end in `|| true`: under `set -e` a missing
# variable would abort the script silently, before the message below could print.
a2a_url="$(env_value "$a2a_dep" A2A_AGENT_URL || true)"
[[ "$a2a_url" =~ ^http://offer-availability-a2a\.[a-z0-9-]+\.svc\.cluster\.local:8093/$ ]] \
  || fail "A2A_AGENT_URL must default to http://<release>-a2a.<ns>.svc.cluster.local:8093/, got '$a2a_url'"
a2a_ns="$(helm template "$release" "$chart_dir" --namespace warehouse-systems "${base[@]}" --set a2a.enabled=true --show-only templates/a2a-deployment.yaml)"
[[ "$(env_value "$a2a_ns" A2A_AGENT_URL || true)" == "http://offer-availability-a2a.warehouse-systems.svc.cluster.local:8093/" ]] \
  || fail "A2A_AGENT_URL must follow the release namespace"
a2a_over="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --set a2a.agentUrl=https://agents.example/api/a2a/offer-availability/ --show-only templates/a2a-deployment.yaml)"
[[ "$(env_value "$a2a_over" A2A_AGENT_URL || true)" == "https://agents.example/api/a2a/offer-availability/" ]] \
  || fail "a2a.agentUrl must override A2A_AGENT_URL"
grep -qE 'getenv\("A2A_ADDR", ":8093"\)' "$repo_dir/cmd/a2a/main.go" \
  && [[ "$(env_value "$a2a_dep" A2A_ADDR || true)" == ":8093" ]] || fail "the chart's A2A_ADDR default differs from cmd/a2a's"
[[ "$(env_value "$a2a_dep" OTEL_SERVICE_NAME || true)" == "offer-availability-a2a" ]] || fail "the a2a component must export as <otel.serviceName>-a2a"
[[ "$(env_value "$a2a_dep" OTEL_EXPORTER_OTLP_ENDPOINT || true)" == "$otel_endpoint" ]] || fail "the a2a component must export to otel.endpoint"
[[ "$(env_value "$a2a_dep" SERVICE_VERSION || true)" == "0.1.0" ]] || fail "the a2a SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$a2a_dep" ENVIRONMENT || true)" == "production" ]] || fail "ENVIRONMENT must come from a2a.environment"
a2a_named="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --set otel.serviceName=offers --set a2a.serviceVersion=dev --show-only templates/a2a-deployment.yaml)"
[[ "$(env_value "$a2a_named" OTEL_SERVICE_NAME || true)" == "offers-a2a" ]] || fail "the a2a service name must follow otel.serviceName"
[[ "$(env_value "$a2a_named" SERVICE_VERSION || true)" == "dev" ]] || fail "SERVICE_VERSION must come from a2a.serviceVersion"
# The a2a Service and route render together with the component; the route rewrites to the
# JSON-RPC root, so the Agent Card stays reachable under the same Kong prefix.
a2a_all="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --set a2a.route.enabled=true \
  --show-only templates/a2a-service.yaml --show-only templates/a2a-route.yaml)"
grep -q 'kind: Service' <<<"$a2a_all" || fail "the a2a Service was not rendered with a2a.enabled=true"
grep -q 'kind: HTTPRoute' <<<"$a2a_all" || fail "the a2a HTTPRoute was not rendered with a2a.route.enabled=true"
grep -q 'port: 8093' <<<"$a2a_all" || fail "the a2a Service and route must use port 8093"
grep -q 'value: /api/a2a/offer-availability$' <<<"$a2a_all" || fail "the a2a HTTPRoute must match /api/a2a/offer-availability"
grep -q 'replacePrefixMatch: /$' <<<"$a2a_all" || fail "the a2a HTTPRoute must rewrite the prefix to /"
if grep -q 'kind: HTTPRoute' <<<"$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true)"; then
  fail "the a2a HTTPRoute must stay off while a2a.route.enabled is false"
fi
# The web remote: its own nginx Deployment + ClusterIP Service, no env at all,
# and no route in this chart (the Nginx web gateway owns /mfes/offer-availability/).
frontend_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set frontend.enabled=true --show-only templates/frontend-deployment.yaml)"
frontend_svc="$(helm template "$release" "$chart_dir" "${base[@]}" --set frontend.enabled=true --show-only templates/frontend-service.yaml)"
grep -q 'app.kubernetes.io/component: frontend' <<<"$frontend_dep" || fail "the frontend Deployment was not rendered with frontend.enabled=true"
grep -q 'type: ClusterIP' <<<"$frontend_svc" || fail "the frontend Service must stay ClusterIP"
if grep -Eq '^\s+env:' <<<"$frontend_dep"; then fail "the frontend Deployment must render no env: nothing about the remote is configured in the cluster"; fi
if grep -Eq 'name: (DATABASE_URL|KAFKA_BROKERS)$' <<<"$frontend_dep"; then fail "the frontend Deployment must not get database/Kafka env"; fi

# ALL optional components render TOGETHER (ct lint's all-components values file).
everything=(-f "$chart_dir/ci/all-components-values.yaml")
together="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}")"
for component in api mcp a2a frontend; do
  grep -q "app.kubernetes.io/component: $component\$" <<<"$together" || fail "the full render lacks the $component component"
done
if grep -E 'kind: (Ingress|HTTPRoute)' <<<"$together" | grep -q frontend; then
  fail "frontend routing must not live in this chart"
fi
if grep -q 'DATABASE_URL: ""' <<<"$together"; then fail "an empty DATABASE_URL must never be rendered"; fi

echo "PASS: offer-availability chart render assertions"
