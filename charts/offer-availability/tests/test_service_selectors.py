#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are
identical on every pod a release creates. A Service that selects on those two
alone selects ALL of them. Every Deployment here carries
`app.kubernetes.io/component` in selector.matchLabels and its pod labels, and
every Service selects on it, so each Service selects EXACTLY ONE Deployment.
This test fails if that ever stops being true.

This chart has one component, api (Deployment + Service); the HPA, Ingress and
HTTPRoute are optional and default off. They are enabled TOGETHER here.

Run: python3 charts/offer-availability/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "offer-availability"

# Dummy values, no password: the chart refuses to render without a database
# source, the Kafka brokers and the consumer group. None is ever dialled.
BASE = [
    "--set", "database.url=postgres://u@example.invalid:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
    "--set", "config.nodeTopologyConsumerGroup=offer-availability-node-topology",
]
ENABLE_EVERYTHING = BASE + [
    "--set", "autoscaling.api.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
]

# Every env var cmd/api reads (cmd/api/config.go + the Postgres adapter).
API_ENV = (
    "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS", "NODE_TOPOLOGY_CONSUMER_GROUP", "HTTP_ADDR",
    "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "RETENTION_INTERVAL", "SHUTDOWN_DRAIN_DELAY", "LOG_LEVEL",
)


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    if set(deployments) != {RELEASE}:
        failures.append(f"want exactly one Deployment ({RELEASE}), got {sorted(deployments)}")
    if set(services) != {RELEASE}:
        failures.append(f"want exactly one Service ({RELEASE}), got {sorted(services)}")
    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # The api HPA owns replicas when enabled.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    # Ingress and HTTPRoute route to the api Service.
    for kind in ("Ingress", "HTTPRoute"):
        if not any(d.get("kind") == kind for d in docs):
            failures.append(f"{kind} was not rendered when enabled")
    route = next((d for d in docs if d.get("kind") == "HTTPRoute"), None)
    if route is not None:
        for rule in route["spec"]["rules"]:
            for ref in rule["backendRefs"]:
                if ref["name"] not in services:
                    failures.append(f"HTTPRoute backend {ref['name']} is not a rendered Service")
    ingress = next((d for d in docs if d.get("kind") == "Ingress"), None)
    if ingress is not None:
        for rule in ingress["spec"]["rules"]:
            for path in rule["http"]["paths"]:
                if path["backend"]["service"]["name"] not in services:
                    failures.append("Ingress backend is not a rendered Service")

    api = deployments.get(RELEASE)
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    names = [e["name"] for e in env_of(api)]
    for want in API_ENV:
        if want not in names:
            failures.append(f"the api Deployment does not render {want}")
    duplicates = sorted({n for n in names if names.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    if any(n.endswith("_MODE") for n in names):
        failures.append("the api Deployment renders a *_MODE variable")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy an HPA, an Ingress or a route.
    defaults = render(BASE)
    stray = [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: the api Service selects exactly one Deployment (component=api); HPA, Ingress and HTTPRoute are off "
          "by default and route to that Service when enabled; every env var cmd/api reads is rendered once")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
