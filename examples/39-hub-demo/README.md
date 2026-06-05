# examples/39-hub-demo — two apps → one hub

End-to-end demonstration of the v0.16.6 `sky console serve` hub
model: **two independent Sky.Live apps push telemetry to one
central hub** that aggregates their signals into a multi-service
dashboard.

This is the example that exercises the v0.16.5 / v0.16.6 #493
tenant-isolation work (Identity → Session → SQL-WHERE) in a
fully local cluster.

## Topology

```
    ┌─────────────────────────┐
    │   billing-app  (:8039)  │──┐
    │   service.name:         │  │
    │   customer-42-billing   │  │ OTLP/HTTP
    └─────────────────────────┘  │ POST → :8025
                                 │
    ┌─────────────────────────┐  │
    │   frontend-app (:8040)  │──┤
    │   service.name:         │  │
    │   customer-42-frontend  │  │
    └─────────────────────────┘  │
                                 ▼
                       ┌──────────────────┐
                       │ sky console serve│
                       │     (:8025)      │
                       │                  │
                       │  SQLite hot db   │
                       │  Bundled console │
                       └──────────────────┘
                                ▲
                                │ browser
                       http://localhost:8025/
```

Both apps share the operator-controlled tenant prefix
`customer-42-`.  The bundled console at the hub renders an
Overview cards strip listing both services side-by-side; click
either card to drill into its logs / metrics / traces / errors.

## Quickstart

From the repo root, with the `sky` binary at `sky-out/sky`:

```bash
examples/39-hub-demo/run-demo.sh
```

The script:

1. Spawns `sky console serve --port 8025` with a temporary hot-db
   dir (cleaned up on Ctrl-C).
2. `sky build` + launches `billing-app` on port 8039.
3. `sky build` + launches `frontend-app` on port 8040.
4. Both apps export their telemetry to the hub via
   `SKY_CONSOLE_HUB=http://localhost:8025/v1/otlp` +
   `SKY_CONSOLE_HUB_TOKEN`.

Browse:

| URL                      | Surface                                          |
|--------------------------|--------------------------------------------------|
| `http://localhost:8025/` | hub bundled console (Overview / Logs / etc)      |
| `http://localhost:8039/` | billing-app UI (Sky.Live)                        |
| `http://localhost:8040/` | frontend-app UI (Sky.Live)                       |

Ctrl-C tears the cluster down (background-task hygiene per
CLAUDE.md Non-Negotiable #2 — every spawn is tracked + cleaned up).

## What to look for

**Overview** (Hub at `/`) — two service cards stacked left:
`customer-42-billing` (3-second tick cadence, info logs with
`amount_cents` + `currency`) and `customer-42-frontend` (2-second
tick cadence, info logs with `path` + `session_id`).  Each card
shows status + req/s + p95 + error rate sparklines computed over
the last 60 s.

**Logs tab** — filter to one service via the picker; the structured
log body shows the attrs (`amount_cents=4231 currency=USD` for
billing; `path=/checkout session_id=sess-9123` for frontend).

**Tenant prefix in action** — both `customer-42-billing` and
`customer-42-frontend` share the `customer-42-` prefix.  In a
real multi-tenant deployment where the hub's `consoleAuth` callback
sets `claims["tenant"]` from the signed-in user's session, every
`Hub_readFiltered*` call appends `AND service_name LIKE 'customer-42-%'`
at the SQL layer.  A `customer-99-*` app pushing to the same hub
would be filtered out — the SQLite engine, not the bundled
console, enforces the row scope.

## Tenant-mode toggle (advanced)

The default `run-demo.sh` runs the hub with `SKY_CONSOLE_AUTH=token`
(single shared bearer cookie, both apps' data visible without
per-tenant scoping — useful for the demo's visibility).

To exercise the multi-tenant path:

1. Wrap the hub in your own Sky.Live binary that wires a
   `consoleAuth : Request -> Task Error (Maybe Identity)` callback.
2. The callback returns `Identity` with
   `claims.tenant = "customer-42-"` for signed-in users in that
   tenant.
3. Set `SKY_CONSOLE_AUTH=app` on the hub.

The bundled console's `init` issues
`Cmd.perform (Hub.currentIdentity hubDbPath) GotIdentity` — the
arrived identity drives both UI rendering (tenant pill in the
header) AND the SQL-WHERE clause.

See [`docs/v0.16.x-console/HUB.md`](../../docs/v0.16.x-console/HUB.md)
"Tenant isolation — defense-in-depth" for the full three-layer
model.

## Cleanup

If you Ctrl-C the launcher, it kills every spawned process by PID
and by port.  Stale processes from an aborted run can also be
swept manually:

```bash
for port in 8025 8039 8040; do
    lsof -ti tcp:"$port" | xargs -r kill -9
done
```

The temporary hub data dir is in `$TMPDIR` (or `/tmp` on Linux);
`rm -rf /tmp/sky-hub-demo*` reclaims it.
