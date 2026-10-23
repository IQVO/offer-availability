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
# No env var the binary does not read (extraEnv is empty by default).
while read -r name; do
  [[ " ${vars[*]} " == *" $name "* ]] || fail "the Deployment renders $name, which cmd/api does not read"
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

echo "PASS: offer-availability chart render assertions"
