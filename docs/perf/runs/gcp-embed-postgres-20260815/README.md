# Raw data — embedded PostgreSQL under load on x86 GCE, 2026-08-15

Analysis and every stated condition live in
[`../../skylive-interaction-cost.md`](../../skylive-interaction-cost.md),
"Embedded PostgreSQL, measured". These are the files it was derived from.

The target was `sky-bench-embed` (**e2-small**), `us-central1-a`, project
`settleby`, running `examples/26-ui-showcase` built from `8e166eaf`
(`feat/embedded-postgres`). The instance was deleted; see `teardown.txt`.

## The three configurations

One binary, three runtime configurations, so nothing but the configuration
differs between them:

| cfg | invocation | session store | PostgreSQL |
|---|---|---|---|
| **A** | `./app` | `memory` | none — the SQLite/no-DB control |
| **B** | `./app --embed --data-dir …` | `memory` | embedded, idle |
| **C** | `./app --embed --data-dir …` + `SKY_LIVE_STORE=postgres` | `postgres` | embedded, in the request path |

`SKY_POSTGRES_BIN=/usr/lib/postgresql/15/bin` throughout — Debian's
PostgreSQL **15.19**, not a Sky bundle. See the analysis document's
"What this run does not cover" for what that does and does not settle.

| file | what |
|---|---|
| `sweep.tsv` | the main sweep: 25/50/100 sessions × 3 repeats × 3 configs = **27 runs**, configs interleaved within each (level, repeat) |
| `counterbalance.tsv` | 4 runs at n=50 with the config order **reversed** (C,B,A), which is what shows the n=50 throughput spread to be run position, not configuration |
| `idle_ab.tsv` | 5 s sampling across three phases — config B running, everything stopped, config A running — for the floor A/B |
| `idle_ab.notes` | PSS breakdown per PostgreSQL process, and app PSS in both configs |
| `final-evidence.txt` | the rendered `postgresql.conf`, the full process tree, `/proc/meminfo`, versions |
| `teardown.txt` | deletion + verification, verbatim |
| `samples/<cfg>-n<N>-r<R>.tsv` | 1 Hz trace for each run |
| `json/<cfg>-n<N>-r<R>.json` | the generator's own result record for each run |
| `sweep.sh`, `counterbalance.sh`, `remote_setup.sh`, `remote_sampler.sh`, `analyse.sh` | exactly what ran |

## Column contract

`sweep.tsv` / `counterbalance.tsv`:

```
cfg                 A / B / C, as above
level               sessions requested
repeat              repeat index at that level
idle_app_kb         app RSS after restart, before load (median of 5 reads)
idle_pg_rss_kb      SUM of RSS over the postgres process tree, idle
idle_pg_pss_kb      SUM of PSS over the postgres process tree, idle  <- the honest one
idle_app_pss_kb     app PSS, idle
idle_pg_nproc       postgres processes
idle_mem_avail_kb   MemAvailable after restart, before load
load_app_kb         app RSS under load (median of last 40 1 Hz samples)
load_pg_rss_kb      postgres tree RSS under load (median of last 40)
delta_app_kb        load_app_kb - idle_app_kb
delta_pg_kb         load_pg_rss_kb - idle_pg_rss_kb
pg_backends_max     peak `client backend` rows in pg_stat_activity
established         sessions the generator actually established -- THE DIVISOR
kb_per_session      delta_app_kb / established
tput                interactions/sec
p50/p95/p99         interaction latency, ms, INCLUDING a ~111 ms UK->us-central1 RTT
err                 fraction of interactions not returning a patch set
valid               generator's own validity flag (all 27 runs: true)
gen_cpu_pct         generator CPU as a share of the 8-core generator machine
store               the session store the app logged at startup
```

`samples/*.tsv` is `epoch  app_rss_kb  pg_tree_rss_kb  pg_nproc  client_backends
mem_avail_kb  conn_8000  all_backends`.

## Four traps in this data

**`idle_pg_rss_kb` is not what PostgreSQL costs.** It sums RSS across
processes that all map the same shared-memory segment, so `shared_buffers`
is counted once per process. At idle it reads 76–90 MB against a PSS of
29–32 MB — an overstatement of **2.6×**. Use `idle_pg_pss_kb`, or the
`MemAvailable` deltas, which are what the analysis quotes.

**`pg_backends_max` for the first six runs is 0 and is wrong.** The sampler
guarded its query with `test -S` on a socket in a `0700` directory owned by
another user, so it reported no backends for a cluster that had six. Fixed
mid-sweep; rows from `B-n25-r3` onward are correct. The affected rows are
`{A,B,C}-n25-r{1,2}`.

**The config order within each level is always A,B,C in `sweep.tsv`**, so
config A always ran on the freshest burst credits. That is why
`counterbalance.tsv` exists and why no throughput comparison *between
configs* should be read off `sweep.tsv` alone.

**`kb_per_session` is a ratio, not a slope.** The per-session figures the
analysis quotes are OLS slopes of `load_app_kb` against `established` across
levels; a single level's ratio charges the app's fixed load-time growth to
that level's sessions.
