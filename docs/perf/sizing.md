# Sizing a Sky deployment: memory, CPU and sessions

This page holds the measured sizing facts for a Sky.Live app, with and without
embedded PostgreSQL. It moved here from `AGENTS.md` (decision 5, "Deployment
target") in v0.29.0, so that the agent guide stays short. The text is unchanged.
`AGENTS.md` keeps a summary and links here.

Every number cites the run under `docs/perf/runs/` that produced it. Quote a
number with its run, its commit, its view size and its `GOGC`, or do not quote
it. For the embedded PostgreSQL design and its platform matrix, see
[`../skydb/embedded-postgres.md`](../skydb/embedded-postgres.md).

A Sky.Live app plus its own embedded PostgreSQL fits comfortably in **1 GB**,
and most people over-provision:


| | RAM |
|---|---|
| Sky app binary (Go), idle | **~21–27 MB** (measured, `26-ui-showcase` on an e2-small) |
| Sky app, settled under real traffic | **~56 MB** (measured mean, sky-lang.org on an e2-micro; a 45-min window after ~40 h of process uptime) |
| Embedded PostgreSQL — the whole tree, as `MemAvailable` actually falls | **+21.9 MB** idle · **+28.4 MB** once sessions are written through it |
| Sky.Live sessions, **625–650 kB each on x86, PostgreSQL store, stock `GOGC=100`** (the shipped default raises this — see below) | ~65 MB at 100 concurrent |
| **Base, before sessions — measured whole-machine, not a sum of the rows** | **~382 MB** app alone · **~410 MB** with embedded PostgreSQL carrying the sessions |

Every row: `docs/perf/runs/gcp-embed-postgres-20260815/sweep.tsv`, analysed
in `docs/perf/skylive-interaction-cost.md:1044-1100`; the settled-app row is
that document's row 7. The base line is `MemTotal − MemAvailable` on the
measured machine (2,023,888 kB total; median idle `MemAvailable` 1,632,340
/ 1,603,636 kB for the two configurations), so it already contains the OS
and needs no OS row added to it.

> **This table used to open with "Minimal Linux | ~250 MB" and close with
> "Base, before sessions | ~380 MB", and the rows between them no longer
> added up to it** — the ~380 was left in place when the PostgreSQL rows
> were corrected downward, so it had become an orphan of an earlier
> arithmetic. The OS row is deleted rather than adjusted: no run in
> `docs/perf/runs/` measured a bare Linux image, and on the machine that
> *was* measured the OS plus an idle app came to ~382 MB, so ~250 MB was
> not a conservative allowance either. A "PG backends" row also sat here
> carrying no MB value at all; its content was a process count, not RAM,
> and it has moved into the paragraph below — with the count corrected.

**PostgreSQL is a fixed block, not a per-session tax.** Its tree's RSS
slope against established sessions is −10 kB / +22 kB per session — zero
within noise (`docs/perf/skylive-interaction-cost.md:156-158`), because the
pool holds a flat **6-connection pool** (`dbSharedAuxPoolSizeFor(2) = 6`) at
25, 50 and 100 concurrent sessions; `pg_backends_max` reads **7** — the pool
plus the 1-Hz sampler's own psql
(`gcp-embed-postgres-20260815/sweep.tsv`: 7 in the config-C rows at 50 and
100 and one of the three at n=25, the other two a documented sampler bug,
`README:78-82`) — and **7, occasionally 8, at 100 / 300 / 500**
(`gcp-x86-capacity-20260816/README.md:49-53`, against
`max_connections = 56`). Earlier text here and in
`skylive-interaction-cost.md` said `pg_backends_max` was **6**; the column
reads 7 (the 6 is the pool).

**Sessions are the number that decides the instance** — and the per-session
figure moves with the collector, which is why it changed. At the stock
`GOGC=100` a session costs **625–650 kB on x86 with a PostgreSQL session
store** and 451–531 kB with the memory store, measured as an OLS-free slope
across n = 100 → 500 on `examples/19-skyforum` at a 94-element view
(`docs/perf/runs/gcp-x86-capacity-20260816/`). Sky now ships **`GOGC=400`
under a derived `GOMEMLIMIT`** (below), and `GOGC` multiplies the slope, not
just the baseline: 100 → 400 scales it **2.9×** on the same app and store
(`docs/perf/runs/gogc-postgres-20260816/`). The x86 slope at the shipped
default is **unmeasured** — do not multiply the two runs together and quote
the product; this programme's projections have been wrong by several-fold,
repeatedly. What bounds memory at the shipped default is the derived
`GOMEMLIMIT` itself, not a per-session figure.

> Quote a per-session number **with its view size and its `GOGC`**, or it
> will be wrong. This table long carried ~1.35–1.42 MB, which was measured
> on a *different app* — `26-ui-showcase` at 384 elements, memory store — and
> is not the cost of a session in general.

**CPU binds a full order of magnitude before memory on real GCE
instances.** The e2-micro's ~450-session memory ceiling against its
25–50-session CPU knee is a **≈9–18× gap** — a ratio of two measured
observations, not itself a directly measured quantity. An
e2-micro *holds* ~450 sessions and is **unusable past ~50** (knee 25–50,
peak ~18 interactions/sec, and at 250 sessions it fails 74–91% of
interactions across three repeats, 96% at 500; commit `ba3c3b1d`,
`docs/perf/runs/gcp-x86-20260815/micro-noagent.tsv`). The
~450 is an **observation, not a division**: asked for 500, that machine
established **447** (`gcp-x86-20260815/micro-noagent.tsv`, n=500 row, which
also records 96% of interactions failing there) with `MemAvailable` down to
**~43 MB** (`micro-rss-n500-r1-memexhaustion.txt`) — so it does not move
when the per-session slope is restated. No comparable memory ceiling was
ever *reached* on e2-small: it established **500 of 500** in all three
repeats of the same sweep (`small-noagent.tsv`).
Re-measured at commit `3ed83c08` with embedded PostgreSQL carrying the
sessions (`docs/perf/runs/gcp-x86-capacity-20260816/`): an **e2-small
sustains 64.3 int/s at 300 sessions** (failure knee between 100 and 300),
an **e2-medium 261.5** (knee above 500). Quote throughput with its commit
— these figures predate later optimisation work. Sizing on memory alone
overstates an e2-micro by roughly an order of magnitude (≈9–18×).

**Count physical cores, not vCPUs.** A GCE vCPU is an SMT thread:
`e2-standard-8` is **4 cores × 2 threads**, not 8 cores. Four threads on four
distinct physical cores serve **1,568 int/s**; the same four threads on two
physical cores serve **1,097** — 70%. The second thread on a core is worth
~1.27×, not 2× (`docs/perf/runs/gomaxprocs-scaling-20260816/`). Any capacity
number derived from a vCPU count overstates the machine by roughly that
factor. Counted in physical cores, throughput scales at **79–80% efficiency
per doubling** (same run) — a larger instance is a legitimate route when
sized on cores.

**The GC default is derived, not configured.** At startup the runtime sizes
`GOMEMLIMIT` from detected machine memory — the cgroup limit first, so a
container is not sized to its host — after subtracting the OS and, under
`--embed`, the cluster's own `shared_buffers`, and sets `GOGC=400` under it.
Measured: **+19% throughput at 759 MB peak RSS** at 500 sessions on the
PostgreSQL store, against 1,827 MB for a bare `GOGC=800`. An explicit `GOGC`
or `GOMEMLIMIT` in the environment always wins, and a machine too small to
hold the bound is left on Go's defaults entirely. There is no `sky.toml`
knob: the Go env vars are the escape hatch, and they work even when the
process is launched by something that never reads `sky.toml`.

Two things to tell a user picking a burstable e2 instance: **the first run
after idling overstates sustained capacity by ~2.7×** — a rested e2-small
measured 183.5 int/s, then 58–71 for six consecutive runs (seven-run soak,
`docs/perf/runs/gcp-x86-capacity-20260816/`; the same decay on e2-micro
read 17.5 → 9.6 → 9.5/s, `docs/perf/runs/gcp-x86-20260815/`) — so plan
with the sustained figure, never the first number they see. And a figure
measured under a container CPU quota was optimistic against real hardware
by **2.5–5×**.

The **diff** is not the cost — ~128 ns per VNode, under 1% of an
interaction — but the interaction as a whole **does track view size**,
because `view(model)` re-runs in full every interaction:
`cost_ms ≈ 0.124 + 0.018 × elements` over the three smallest views (30–94
elements) on one core (`docs/perf/runs/forum-rebaseline-20260816/`; the
all-seven-sizes fit is `−0.147 + 0.0197 × elements`). Optimising the differ buys
nothing; trimming a large view is a real lever. And **a single instance
has no replica**:
`sky db provision --shared` generates a backup timer, a single `--embed` app
does not, so a `pg_dump` schedule is the operator's to add. Sizing detail:
`docs/skydb/embedded-postgres.md`.
