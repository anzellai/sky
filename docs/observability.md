# Observability — tracing & spans (user guide)

> Live doc for the v0.16.x observability surface (telemetry primitives,
> Sky Console, hub federation).  For hub-specific deep-dives see
> [`v0.16.x-console/`](v0.16.x-console/).  Pre-v0.16 design notes are
> archived at [`archive/observability-design.md`](archive/observability-design.md).

Sky traces your app automatically. You get a useful trace tree with
**zero configuration and zero code** — and an opt-in API when you
want application-level spans.

## What you get for free

Every Sky.Live / Sky.Http.Server app, with no env vars and no
spans written, produces a trace per HTTP request:

```
GET /checkout                                  240ms   server span (root)
├─ session.load   store=redis                   12ms
├─ msg SubmitOrder                              180ms   the TEA update
│  ├─ db.query   "SELECT … FROM cart WHERE …"    40ms
│  ├─ db.exec    "INSERT INTO orders …"          85ms
│  └─ http POST  api.stripe.com/v1/charges       50ms   outbound, traceparent injected
└─ render        vnode-diff                       15ms
```

Auto-instrumented (Tier 1 — always on):

- HTTP request (server span, the root)
- `Db.query` / `Db.exec` / `Db.insertRow` / `Db.withTransaction`
- `Auth.login` / `Auth.register`
- `Http.get` / `Http.post` (outbound — also injects W3C
  `traceparent` so the downstream service joins the trace)
- `File.readFile` / `File.writeFile` / `File.append`
- Sky.Live Msg dispatch + `Cmd.perform` tasks

## Where the traces go

- **No config** → traces land in an in-process ring buffer.
  Open `/_sky/console` → **Traces** tab. No Jaeger, no collector.
- **`OTEL_EXPORTER_OTLP_ENDPOINT` set** → *also* exported OTLP to
  that collector (Tempo / Jaeger / Honeycomb / Datadog / Cloud
  Trace — anything that speaks OTLP).

## Opt-in: application-level spans

When you want a named, logical span that groups the auto-spans
underneath it, use `Std.Trace`:

```elm
import Std.Trace as Trace

checkout : Cart -> Task Error Receipt
checkout cart =
    Trace.span "checkout"
        (reserveStock cart
            |> Task.andThen chargeCard
            |> Task.andThen issueReceipt)
```

The `db.*` / `http.*` spans opened inside `reserveStock` /
`chargeCard` / `issueReceipt` nest under `checkout` in the trace.

| Function | Type | Use |
|---|---|---|
| `Trace.span` | `String -> Task e a -> Task e a` | Wrap a Task in a named span. Value flows through untouched. |
| `Trace.event` | `String -> Task Error ()` | Mark a point in time on the current span ("cache miss", "retry"). |
| `Trace.attr` | `String -> String -> Task Error ()` | Annotate the current span (`sky.trace.<key> = <value>`). |

## What is captured — and what is not

Captured (OTEL semantic conventions):

- `http.route` / `http.method` / `http.status_code`
- `db.system` / `db.operation` / `db.statement` — the
  **parameterised** SQL (`WHERE id = $1`)
- `sky.session.store` / `sky.session.op`
- `sky.msg` — the Msg constructor name
- error status + `exception.*` on failure

**Never** captured (hard default — not a config knob):

- Passwords, tokens, secrets
- SQL bind *values* (PII risk)
- Request / response bodies
- Session contents

## Sampling

| Mode | Default |
|---|---|
| dev (`ENV` unset / dev / local) | 100% |
| serverless | 100% |
| production | 5% (interim — a rate-limited head sampler lands in a later release) |

Override with `OTEL_TRACES_SAMPLER_ARG=<0.0–1.0>`.

## Environment variables

| Env | Default | Meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset) | Export OTLP here in addition to the in-process ring. |
| `OTEL_TRACES_SAMPLER_ARG` | (mode default) | Fixed sample fraction `0.0–1.0`. |
| `SKY_SERVICE_NAME` / `OTEL_SERVICE_NAME` | `sky-app` | `service.name` the backend groups by. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (unset) | Comma-separated `k=v` headers (auth tokens for managed collectors). |
| `SKY_CONSOLE_DB_PATH` | (unset) | When set, dual-writes every log / metric / span to the SQLite file at this path so the bundled console mini-app can render history beyond the 10 k-line / 1 k-span in-RAM caps. WAL mode, 24 h log/span retention, 7 d metric retention. Unset keeps the pure in-RAM path. |

## How analytics and telemetry reach the database

Both sinks write the same way, and it is deliberately not the way the app's
own data is written.

**Batched behind a single writer.** An event does not become an `INSERT`. It is
marshalled on the goroutine that emitted it, put on a bounded queue, and
written by one flusher goroutine as a multi-row `INSERT` when the batch fills
(256 rows for analytics, 128 for telemetry) or when the flush interval expires
(250 ms / 200 ms) — whichever comes first.

The reason is that each sink used to pay a transaction, and therefore an fsync,
per event, on the request goroutine. That is a ceiling set by the disk rather
than by the code, and it put the disk on the request path: a page view is
tracked while the page renders, so a stalled analytics store showed up as a
slow page. Measured against a live PostgreSQL, 2000 analytics events cost 2000
statements and ~17 k events/s row-at-a-time, and 16 statements and ~172 k
events/s batched.

**Events can be dropped, and drops are counted.** The queue is bounded (4096
for analytics, 1024 for telemetry). When it is full the incoming event is
dropped rather than blocking the caller, because blocking would apply a stalled
disk's back-pressure to every request handler — analytics must never be able to
take the app down. Dropping is correct for this data; dropping *silently* is
not, so drops increment `sky_analytics_events_dropped_total`, are warned about
once per process with a running total, and are visible at `/_sky/console`.

Three things count as a drop, and the counter covers all three: an event
rejected by a full queue, a **batch that failed to persist** (it is not
retried, so those events are lost), and an event emitted after the shutdown
drain has finished. A store that is *down* therefore raises the counter just as
a store that cannot keep up does — the two used to be distinguishable only by
the second leaving the counter at zero, which is the wrong way round for the
series an operator alerts on.

To tell the two apart, read `sky_analytics_write_failures_total` alongside it.
Rising drops with **zero** failures is back-pressure: the store is up and
cannot keep up — check its disk or its server. Rising drops **with** failures is
an outage: the writes are being rejected, and the most recent error is in the
`analytics.write_failed` log line. Both counters are republished on every flush
attempt, including the failing ones.

The policy is **drop-newest**. Drop-oldest would cost a lock on the hot path to
buy a property this data does not want: under sustained overload it discards the
beginning of an incident, which is the part that explains it, and leaves the
retained window with a hole in it rather than a contiguous prefix.

**The queue is flushed on shutdown.** Both writers register a shutdown hook, so
a deploy does not lose the events still queued — without that, a buffered writer
loses the last fraction of a second of data on *every* deploy, which is a
silent, recurring, correlated loss rather than a random one. Under `sky db
provision --embed` the hooks run in the supervisor's drain phase, strictly
before PostgreSQL is stopped.

An unclean kill — SIGKILL, an OOM, a crash — does not run hooks, so it loses up
to one flush interval of events. That is the bound the interval is chosen for.

**Reads see queued writes.** The console's Analytics tab, `Analytics.openStore`
and `Analytics.erase` all drain the queue before they read. For `erase` that is
a compliance property rather than a freshness one: a right-to-erasure request
that deleted the rows on disk while the same subject's events sat in the queue
would re-materialise them a moment later.

### `synchronous_commit` is off for these two sinks

On PostgreSQL both writers run their flush inside a transaction that has asked
for `synchronous_commit = off`. PostgreSQL then acknowledges the commit once
the WAL record is in memory, without waiting for it to reach durable storage.

This does **not** risk corruption and does not relax atomicity or isolation. A
crash cannot leave a torn row or half a batch. What it risks is exactly one
thing: a crash of the *server* can lose the last few hundred milliseconds of
committed telemetry. For data the app already drops under queue overflow by
design, paying an fsync per batch to protect it is spending write throughput on
the wrong thing.

It is applied with `SET LOCAL`, inside the transaction, so it reverts when the
transaction ends and cannot reach the next user of a pooled connection. It is
never set cluster-wide: `sky db provision` refuses to put it in
`postgresql.conf`, because there it would silently weaken durability for the
app's own data too.

Set `SKY_ANALYTICS_SYNCHRONOUS_COMMIT=on` / `SKY_TELEMETRY_SYNCHRONOUS_COMMIT=on`
if you want these sinks fully durable.

### Connections: one pool per database, not one per subsystem

A Sky process opens a pool for the app's own `Db.connect`, and one each for
analytics, the Sky.Live session store and telemetry. When those resolve to the
same connection string — the normal case under one `DATABASE_URL`, and always
under `--embed` — the three runtime pools share a single `*sql.DB`, so the
server sees one set of connections rather than three.

The app's own pool stays separate, by design: on PostgreSQL it uses pgx's simple
query protocol so that apps written against SQLite (which bind stringified
integers) keep working, and a pool can only have one query exec mode.

Sharing does not remove the isolation separate pools gave. Analytics and
telemetry each carry a concurrency cap, and the shared pool is sized as the
session store's own pool *plus* those caps — so however hard the background
writers work, the request path can still obtain everything it could before.

If you point a sink at a different database, it gets its own pool, and the
cluster sizing assumes that worst case: the `max_connections` Sky generates
covers the app's pool plus **what each runtime consumer actually asks for** —
the shared size for analytics and the session store, telemetry's own fixed
four — doubled for the restart-overlap window, plus the superuser and operator
slots. It is not one "aux pool size" multiplied by the number of consumers;
that under-counted by ten backends on a single-core host, and by four at eight
cores, which made the restart-overlap claim printed into the generated
`postgresql.conf` false at every core count.

That arithmetic lives in `runtime-go/rt/db_pool.go` and is mirrored — under a
gate, not a comment — by `rust/crates/sky/src/db_pool_sizing.rs`, which sizes
clusters before any Go has run.

### Retention

Old rows are deleted on a schedule: analytics on the window given by
`SKY_ANALYTICS_RETENTION` (unset keeps everything), telemetry at 24 h for logs
and spans and 7 d for metrics.

On PostgreSQL these are `DELETE`s, which leave dead tuples for autovacuum to
reclaim. Declarative range partitioning with retention by `DROP` of whole
partitions — instant, and no vacuum debt — is the right shape for append-only
event tables and is **not** implemented yet. It is not a drop-in change:
`analytics_events` and `telemetry_log` carry a `BIGSERIAL PRIMARY KEY`, and
PostgreSQL requires the partition key to be part of every unique constraint, so
the primary key would have to become `(id, ts)`; an existing table has to be
renamed, recreated partitioned, copied and dropped, which is a data migration
running at app startup under a lock; partitions have to be created ahead of
time by a maintenance task, because a write that creates its own partition
takes a lock on the parent; and SQLite has no declarative partitioning at all,
so the two dialects would stop sharing a schema. Until that migration is
written and gated, the schema stays unpartitioned on both backends rather than
diverging silently.
