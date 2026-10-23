---
name: how-to-add-an-integration-event
description: Publish or consume a cross-service Kafka event in this service: CloudEvents 1.0 type naming, AsyncAPI, the type catalogue ADR, the encoder, the transactional outbox, the node-topology consumer. Use when touching internal/adapters kafka or outbox code, a domain event, or apis/asyncapi*.yaml.
---

# How to add an integration event

offer-availability publishes seven events and consumes four of node-topology
(ADR 0003, ADR 0004). The envelope is CloudEvents 1.0 only; read
`.claude/rules/integration-events.md` and `.claude/rules/fleet/cloudevents.md` first.
The worked example is `ListingRestored`.

## Publishing a new event

1. **Domain event.** A typed struct in `internal/domain/<pkg>/events.go`
   (`listing.ListingRestored`): embeds `shared.Header`, fields exactly the model's
   `data` list, methods `EventName()`, `Entity()` (`catalogitem`, `listing`,
   `inventoryfeed`, lowercase, no separators) and `SubjectID()` (the aggregate id),
   plus a `var _ shared.Event = ...` assertion. The aggregate command
   (`Listing.Restore`) returns it; `Version()` after the command is the version it
   carries.
2. **Contract, one commit.** Add the message and its `data` schema to
   `apis/asyncapi.yaml`, AND one `type: com.retailnetwork.commerce.offer-availability.<entity>.<Event> stream: events`
   line to `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`.
   `TestEventCatalogueMatchesContract` fails when the two disagree, so the push
   fails when they are not in the same commit. Lint:
   `npx --yes @stoplight/spectral-cli lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml`.
   Evolution is additive only; a breaking change is a new `:v2` dataschema.
3. **Encoder.** In `internal/adapters/outbound/kafka/encoder.go` add the payload
   struct (camelCase json tags) and a `case` in `payloadFor`. The envelope itself is
   built by `internal/adapters/kafka/cloudevents` only; never hand-build one. Add the
   event to `goldenCases()` in `encoder_test.go` and run
   `UPDATE_GOLDEN=1 go test ./internal/adapters/outbound/kafka/` once to write its
   file in `testdata/`; review the diff of the golden file by eye.
4. **Publish from the use case** through `writer.publish` inside the same
   `ports.UnitOfWork` as the `Save` (`internal/application/usecases/writer.go`):
   the aggregate row and the outbox row commit or roll back together. Never
   publish after the commit and never call Kafka from a use case.
5. **Nothing else.** The relay (`internal/adapters/outbound/outbox/relay.go`)
   already drains every row; the topic is `retailnetwork.offer-availability.events`
   (`kafka.Topic`). An analytics-only event would need its own topic constant,
   stream and ADR.
6. **Prove it.** `go test -tags=integration ./internal/adapters/outbound/outbox/ -race -count=1`
   reads the events back from a real broker; extend `cmd/api/e2e_integration_test.go`
   (`expectedTypes`) when the event is part of a REST or consumer flow.

## Consuming an event from a sibling context

The consumer is `internal/adapters/inbound/kafka/node_consumer.go`; the use case is
`usecases.ApplyNodeEvent`. To consume one more node-topology type:

1. Restate the producer's message and the fields you read in `apis/asyncapi.yaml`
   (a `receive` operation); never import the producer's code or schema.
2. Add the type to the `typeNode...` constants and to `nodeEventFrom` (a full-`type`
   dispatch, tolerant decoding: read only what you need).
3. Make the use case idempotent through the inbox (`ports.ProcessedEvents`, keyed by
   the CloudEvents id, claimed in the same unit of work as the effects).
4. A payload that can never be applied is `errPoison` and goes to the DLQ at once;
   everything else that fails is retried. Keep the cancellation guard in the loop
   (`TestDeadLetterCallsGuardCancellation`).
5. Test: `node_consumer_test.go` (mapping, skip, poison, retry, DLQ) and a Kafka step
   in `cmd/api/e2e_integration_test.go`. The group id stays
   `NODE_TOPOLOGY_CONSUMER_GROUP`; a consumer of a new topic needs its own mandatory
   variable (a boot error naming it) and an ADR superseding ADR 0003.

## Verify before opening the PR

`make check`, `go test ./internal/architecture/ -count=1` (catalogue, CloudEvents-only,
relay cap), the spectral lint of the AsyncAPI, and the integration command above.
