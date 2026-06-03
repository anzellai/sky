# examples/34-multi-tier-console

> Sky Console telemetry-flow showcase. Demonstrates how the embedded
> console aggregates signals across multiple logical namespaces in a
> single Sky binary, with namespace labels driving the UI filter
> chips. v0.16.1 PR10-I.

## What this demonstrates

The headline value of Sky Console is *"one pane of glass for every
signal your app generates"*. This example exercises that claim:

- A host Sky.Live app at `/`
- Three logical sub-tiers — `billing`, `jobs`, `auth` — each routed
  under its own prefix (`/billing`, `/jobs`, `/auth`)
- Each tier emits its own logs / metrics with a `service.namespace`
  tag so the console can filter signals per-tier
- The inline console at `/_sky/console` shows the aggregated stream
  AND a namespace selector

The four telemetry topologies covered by Sky Console are documented
in [`docs/v0.16.x-console/TELEMETRY_FLOW.md`](../../docs/v0.16.x-console/TELEMETRY_FLOW.md).
This example focuses on **Topology 2** — a single process containing
multiple logical sub-apps. (Topology 1 is just any single-page Live
app; Topology 3 is `MountSubApp` fork+exec; Topology 4 is the
distributed hub.)

## Running it

```bash
cd examples/34-multi-tier-console
rm -rf sky-out .skycache .skydeps   # clean slate
sky build src/Main.sky
SKY_LIVE_PORT=8034 \
SKY_CONSOLE_AUTH=token \
SKY_CONSOLE_TOKEN=local-dev-token \
SKY_CONSOLE_TOKEN_SECRET=$(openssl rand -hex 32) \
  ./sky-out/app
```

Then in your browser:

- `http://localhost:8034/` — host app (counters, navigation)
- `http://localhost:8034/billing` — billing tier (emits Stripe-style logs)
- `http://localhost:8034/jobs` — jobs tier (emits background-task logs)
- `http://localhost:8034/auth` — auth tier (emits sign-in / token logs)
- `http://localhost:8034/_sky/console` — Sky Console (login with
  the token above)

In the console's Logs tab, the `service.namespace` column distinguishes
each tier's emissions. Visit each tier's page a few times to seed
non-empty data, then navigate to the console.

## Architecture sketch

```
                     Process P1 (port 8034)
                  ┌──────────────────────────┐
                  │                          │
   GET /          │  hostApp                 │
   ─────────────► │   pages: / /about        │
                  │   Log.info service.ns="" │
   GET /billing   │                          │
   ─────────────► │  billingPage             │
                  │   Log.info ns=billing    │
   GET /jobs      │   metrics ns=billing     │
   ─────────────► │                          │
                  │  jobsPage                │
   GET /auth      │   Log.info ns=jobs       │
   ─────────────► │                          │
                  │  authPage                │
                  │   Log.info ns=auth       │
   GET /_sky/     │                          │
   console        │  inline console reads   │
   ─────────────► │   telemetry.Default()    │
                  │                          │
                  └──────────────────────────┘
```

All tiers write to the SAME `telemetry.Default()` Store. The console
reads it back. The `service.namespace` labels are what distinguish
the per-tier signals.

## Why one-binary instead of `MountLiveSubAppInProcess`

The current example uses **explicit namespace tags** on per-page logs
because Sky stdlib doesn't yet expose `MountLiveSubAppInProcess` as
a first-class `Std.Live` surface — it's a Go-level runtime primitive
(`runtime-go/rt/subapp_inprocess.go`) that ships in v0.16.1 PR10-C.

The user-visible aggregation experience is **identical** whether you:

1. Compose multiple logical tiers as pages in one Live.app cfg
   (this example's shape), or
2. Mount each tier as its own `MountLiveSubAppInProcess` sub-app
   (the v0.16.2+ shape, once the Sky surface lands)

In both shapes, signals from every tier write to
`telemetry.Default()`. The console's UI is unchanged. The
`MountLiveSubAppInProcess` form will provide stronger per-tier
isolation (separate session stores, separate broker, separate sky-id
namespace), but the OBSERVABILITY behaviour is the same.

When the Sky-side `Std.Live.mountSubApp` lands, this example's
README will be updated to show both shapes side-by-side.

## Tests / verification

```bash
# Single-shot verification: build, boot, hit every tier, check
# console aggregates each tier's logs under the correct namespace.
node ../../scripts/playwright-multi-tier-console.mjs
```

(The Playwright script is shipped alongside as part of PR10-J.)

## See also

- [`docs/v0.16.x-console/TELEMETRY_FLOW.md`](../../docs/v0.16.x-console/TELEMETRY_FLOW.md)
- [`docs/v0.16.x-console/RFC-v0.16.1-pr10-architecture.md`](../../docs/v0.16.x-console/RFC-v0.16.1-pr10-architecture.md)
- [`docs/v0.16.x-console/EMBEDDED.md`](../../docs/v0.16.x-console/EMBEDDED.md)
