---
name: architecture-review
description: Bounded-context boundary and ADR-compliance review of a change (expensive, post-integration): hexagonal direction, cross-context coupling, contradicted ADRs. Invoke explicitly: /architecture-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a bounded-context boundary and ADR-compliance review of the
current changes (or `$ARGUMENTS` if given, e.g. a branch/PR diff range).

This is EXPENSIVE relative to `/code-review` — it reasons about
cross-repo/cross-context implications, not just this diff's local
correctness. Use it post-integration (before merging a PR that touches
architecture, not on every small commit) or when asked explicitly to
check a design decision against this fleet's standing architecture.

## What to check, in priority order

1. **Hexagonal dependency direction.** Domain depends on nothing;
   application depends on domain+ports; adapters depend on
   application+domain; only `cmd/` wires every layer together. Run
   `go test ./internal/architecture/... -v` first — if it's already red,
   report that and stop; don't hand-review what a fitness test already
   caught.
2. **Context boundary.** offer-availability consumes four node-topology events and
   calls no sibling (`docs/adr/0003-consumed-contracts-and-local-node-status.md`;
   model `sync_calls: []`). Any further consumed type, topic or outbound call is a
   new architectural decision that needs its own ADR in `docs/adr/` (superseding or
   amending ADR 0003), not a code change slipped in silently. Sibling data is never
   read live: it arrives as events into the local `NodeStatusView`.
3. **Runtime rule.** `cmd/**` must not import `internal/adapters/outbound/memory`
   or `repocontract` (`TestRuntimeBinaryHasNoInMemoryAdapters`,
   `TestRuntimeBinaryDoesNotImportTheRepositoryContract`); the relay is built with
   `WithMaxAttempts` (`TestOutboxRelaysPassAttemptCap`); `/readyz` keeps its
   empty-topic Kafka metadata check. This repo has no MCP adapter and no analytics
   read side.
5. **Kafka consumer-group pattern correctness.** The node-topology consumer keeps
   its read model in Postgres, shared by every instance, so it uses ONE stable group
   id from `NODE_TOPOLOGY_CONSUMER_GROUP`. A new consumer must pick the pattern that
   matches its replay behaviour: a stable group when the state is persisted, a
   per-process-unique id only for an in-memory cache that replays history on every
   start. Flag a mismatch — it is a correctness bug, not a style issue (see
   `.claude/rules/fleet/kafka-testing-and-consumers.md`).
6. **A new bounded-context integration with no companion documentation.**
   If this change introduces or changes a cross-repo contract (a new
   REST call, a new Kafka topic subscription, a new MCP tool consumed by
   a sibling), check whether an ADR documents the decision — and whether
   a companion ADR should exist in the OTHER repo too, per this fleet's
   companion-ADR convention (see `.claude/skills/how-to-write-an-adr/SKILL.md`).
7. **Auth-reintroduction and sibling-call bans**, same as `/code-review`
   items 6-8, but reasoned about more thoroughly here — check not just
   "is there a Bearer literal" but "does this change's INTENT require
   re-litigating the fleet-wide auth-removal decision," which would need
   its own ADR, not a silent code change.

## Output format

State clearly: PASS (no architectural concerns), CONCERNS (list them,
each tied to the specific rule/ADR it would violate), or NEEDS-ADR (the
change is architecturally sound but undocumented — name what the ADR
should cover). Cite the specific file/rule/ADR for every finding; a
finding with no citation is not actionable.

This is advisory. It never blocks a merge on its own, and it never
modifies files. If a finding conflicts with a decision explicitly stated
in this repo's own AGENTS.md/CLAUDE.md, defer to that document and say
so — this command reasons about the fleet's general conventions, not a
higher authority than the repo's own explicit guidance.
