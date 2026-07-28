# Production setup — one Postgres for everything

The **same app code** runs in dev and production — only configuration changes.
This is the point: no code fork, no "prod build". Dev is zero-config; production
points one `DATABASE_URL` at a Postgres and everything lands there.

## Dev (default — nothing to set up)

```sh
sky run src/Main.sky
```

- App data → a local SQLite file (`blog.db`)
- Sessions → in-memory
- Analytics → a local SQLite file (auto-created)
- Console telemetry → in-memory

Perfect for iterating. Survives nothing across restarts, and doesn't scale past
one process — which is exactly what you want while developing.

## Production — one command, one connection string

```sh
docker compose up -d                      # Postgres on host port 5452
cp .env.production.example .env            # then edit the secret
sky run src/Main.sky                       # or your built binary
```

That's it. The single `DATABASE_URL` in `.env` wires **all four** stores into
one database:

| Data | Store | Table(s) |
|---|---|---|
| App data (posts, admin) | `Std.Db` | your tables |
| Sky.Live sessions | session store (`SKY_LIVE_STORE=postgres`) | `sky_sessions` |
| Product analytics | `Std.Analytics` | `analytics_events` |
| Console logs/metrics/traces | telemetry | `telemetry_log` / `_metric` / `_span` |

Each store falls back to `DATABASE_URL` when its own path isn't set, so you
configure **one** connection string, not four. Verify:

```sh
docker exec sky-blog-pg psql -U blog -d blog -c "\dt"
```

## What `ENV=production` changes

- Dev console + floating banner are removed; `/_sky/metrics` goes behind auth.
- `SKY_AUTH_TOKEN_SECRET` (≥ 32 bytes) is required — Sky refuses to start without
  a real one. Generate: `openssl rand -hex 32`.

## Scaling notes

- **Single instance** (one container/VM) + this Postgres handles a lot — see the
  capacity notes in the repo. Postgres removes SQLite's single-writer ceiling.
- **Multiple replicas**: sessions must be shared (they are — Postgres here) AND
  the load balancer needs **sticky sessions** keyed on the `sky_sid` cookie (a
  Sky.Live session is single-owner). Add a Redis broker
  (`SKY_LIVE_BROKER_URL`) if broadcasts must cross replicas.
- **Analytics growth**: `SKY_ANALYTICS_RETENTION=180d` prunes old events so the
  table stays bounded. For very high analytics volume, add TimescaleDB
  (continuous aggregates + compression) or push to an external sink.
- **Timestamps**: use `BIGINT`, not `INTEGER`, for millisecond columns —
  Postgres `INTEGER` is 4-byte and overflows at ~2.1 s of millis. (SQLite's
  dynamic typing hides this in dev.)

## Portability

SQLite-era apps run on Postgres unchanged: Std.Db rewrites `?`→`$n` and binds
string params leniently (pgx simple protocol) so `String.fromInt n`-style params
still land in `INTEGER` columns. For the hottest paths, typed `SqlValue` params
(`SqlInt` / `SqlMoney` / …) bind precisely and keep prepared statements.
