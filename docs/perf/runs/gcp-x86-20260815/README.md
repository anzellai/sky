# Raw data — Sky.Live under load on x86 GCE, 2026-08-15

Analysis and every stated condition live in
[`../../skylive-remote-validation.md`](../../skylive-remote-validation.md),
"The active result". These are the files it was derived from.

Targets were `sky-bench-micro` (e2-micro) and `sky-bench-small`
(e2-small), `us-central1-a`, project `settleby`, both running
`examples/26-ui-showcase` at commit `ba3c3b1d`. Both instances were
deleted; see `teardown.txt`.

| file | what |
|---|---|
| `idle-{micro,small}-samples.tsv` | 300 s idle baseline, 5 s sampling, **no Ops Agent** |
| `{micro,small}-noagent.tsv` | 100/250/500 sessions × 3 repeats |
| `{micro,small}-lowN.tsv` | 1/25/50 sessions × 2 repeats |
| `micro-AGENT.tsv` | 25/50/100 × 2, **with the Ops Agent installed** |
| `micro-rss-n500-r1-memexhaustion.txt` | 1 Hz trace of the run where the e2-micro ran out of memory |
| `teardown.txt` | deletion + verification, verbatim |

## Column contract

`*-noagent.tsv`, `*-lowN.tsv`, `micro-AGENT.tsv`:

```
level         sessions requested
repeat        repeat index at that level
idle_rss_kb   app RSS after restart, before load (median of 5 reads)
load_rss_kb   app RSS under load (median of last 40 of ~110 1 Hz samples)
delta_kb      load_rss_kb - idle_rss_kb
established   sessions the generator actually established -- THE DIVISOR
kb_per_session delta_kb / established
conn_app_max  peak established TCP connections on :8000 (~2 per session)
throughput    interactions/sec
p50/p95/p99   interaction latency, ms, INCLUDING a ~111 ms UK->us-central1 RTT
err_rate      fraction of interactions not returning a patch set
valid         generator's own validity flag
```

`idle-*-samples.tsv` is `scripts/skylive-observe-remote.sh` output:

```
ts rss_kb vmsize_kb threads conn_app conn_pub proc_jiffies
cpu_total cpu_idle load1 req_total msg_total mem_avail_kb
```

`micro-rss-n500-r1-memexhaustion.txt` is `epoch rss_kb conn_app
mem_avail_kb` at 1 Hz.

## Two traps in this data

**`conn_app` is not a session count.** It runs at ~2× sessions: each
session holds an SSE stream *and* a keep-alive connection for its event
POSTs. Use `established`.

**`kb_per_session` at `level=1` is not a per-session cost.** It is the
app's fixed first-request growth (~11–13 MB) divided by one. The
per-session figure is the *slope* across levels, not any single row's
ratio; see the analysis doc.
