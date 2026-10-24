# ADR 0008: the console remote (offer_availability_mfe) — read-only screens over the existing REST

## Status

Accepted (2026-10-24).

## Context

This context is a commerce-tier backend with no user-facing screen of its own:
operators who needed to see what the network can sell had to read the API (or
ask an agent). Every other context of the fleet is being mounted in the
warehouse-console shell as a Module Federation remote, and offer-availability
was one of the last without one. This ADR records why a remote exists, who
consumes it and what it is allowed to do.

## Decision

### The remote

`web/` is a Vite + React Module Federation remote, container
`offer_availability_mfe`, exposing `./App` only (no props, relative routes),
built on the fleet's reference recipe (task-interleaving's web/) rather than
re-derived:

- PINNED host/remote contract: federation container `offer_availability_mfe`,
  gateway mount `/mfes/offer-availability/`, dev/preview port **4018**
  (strictPort) — deliberately off the console's 5181–5197 ladder, whose every
  slot an existing remote holds, and clear of 4010/4011 taken by earlier
  off-ladder remotes; 4018 collides with no remote and no service port.
- The Vite config stays a STATIC object (never the `({ command }) =>` callback
  form): a callback cannot be merged by `mergeConfig`, which has killed whole
  test suites elsewhere in the fleet. `base` is `/mfes/offer-availability/`
  only on `vite build`, read off `process.argv`.
- The API base is resolved at RUNTIME from the console's `/config.json`
  (`window.__WAREHOUSE_CONFIG__.apiOrigin` →
  `${apiOrigin}/api/offer-availability`); a production build throws when it is
  missing instead of falling back to a dev port. The remote calls ONLY its
  own context's API, and GETs only.
- Packaged as `nginxinc/nginx-unprivileged` (`web/Dockerfile`,
  `web/nginx.conf`), served under `/mfes/offer-availability/` by
  warehouse-infra's Nginx web gateway. Kong never serves these bytes; no
  ingress/httproute; the chart Service stays ClusterIP.
- `@warehouse/ui-kit` is a sibling checkout consumed as a named BuildKit
  context (`docker build --build-context uikit=../../warehouse-ui-kit`); the
  image never installs from a foreign `node_modules`. CI checks the ui-kit
  repo out beside this one and builds it first.

### The screens — existing REST reads only, no new endpoints

Three surfaces, exactly what `apis/openapi.yaml` serves today (all GET):

1. **Catalogue** — `GET /catalog`, one keyset page at a time (the opaque
   `nextCursor` drives Next/Previous); each row links to
2. **Item detail** — `GET /catalog/{catalogItemId}` (the footprint, flags and
   state) with the listings of the item from `GET /listings?catalogItemId=`
   (state, suppression reason, the availability the last feed set, and a link
   into the feed).
3. **Listings** — `GET /listings?nodeId=&catalogItemId=` (both filters
   optional, applied on submit) with the same paging as the catalogue.
4. **Inventory feed lookup** — `GET /inventory-feeds/{feedId}`: the outcome
   (APPLIED with its counters, SUPERSEDED, or FAILED with the failure code).

Where a screen wanted data the API does not serve, the screen was cut, not
the API: there is no node view and no feed-list read here, so a feed is
reached by id (the lookup) or from a listing's "last feed" link. Every write
(creating items and listings, applying feeds, discontinuing) stays an API /
vendor operation, not a console one.

### The deployment surface

The Helm chart grows a `frontend` component (`frontend-deployment.yaml`,
`frontend-service.yaml`, `_frontend.tpl`), default **off**; the Service
selects `component=frontend` so no Service ever selects two Deployments, and
the frontend Deployment renders NO env — nothing about the remote is
configured in the cluster. CI grows a `web` job (lint, typecheck, vitest,
production build) on every push and PR. Registering the remote in the console
shell (its `vite.config.ts` entry, route, nav) is the console repo's change,
not this repo's.

## Consequences

- Operators see what the network can sell in the console; the API surface is
  unchanged (`apis/openapi.yaml` untouched — the api-lint job stays green by
  construction).
- The remote being read-only is a design decision, not a limitation to fix:
  if operators ever need to create listings from the console, that is a new
  decision with its own ADR (and the Idempotency-Key handling it needs).
- The dev port 4018 must stay unique across the fleet's remotes; any
  collision is fixed HERE, in the remote's repo.
