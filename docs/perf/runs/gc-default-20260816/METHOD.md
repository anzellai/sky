# Method — verifying the shipped GC default

This run does not repeat `gogc-postgres-20260816`. That run chose the setting;
this one verifies that the setting **as derived and shipped** holds its bound,
and closes the falsifier that run named and could not test.

## What is being verified, and what is not

| | |
|---|---|
| Verified here | The derivation runs in a real compiled Sky app; the bound HOLDS at n = 100/300/500 on the PostgreSQL store; the measured workload fits an e2-small with margin; a live heap that EXCEEDS the bound degrades rather than dying. |
| Verified by unit test, not here | The RAM → limit arithmetic itself, across fourteen machine sizes, including the cgroup path a container takes (`runtime-go/rt/gc_tuning_test.go`). |
| **Not** verified here | Throughput. See below — this host could not resolve it today, and it is not what the safety property needs. |

## The host could not measure throughput today, and that is recorded, not hidden

Two sibling agents ran their own benchmarks throughout. `LOCKS.md` in the parent
run measured the consequence directly: **24–44% within-arm throughput spread**
under exactly these conditions, against effects of a few per cent. Every
throughput number in `results.tsv` is therefore reported and **not relied on**;
the arms record `load1` so the reader can see the conditions.

**Peak RSS is the quantity this run needs and it is the robust one** — the
parent run measured ≤6% run-to-run spread at `GOGC ≤ 400`, against 68% at 800.
An arm's session ESTABLISHMENT is the part a loaded host really can break, so
`analyse.sh` marks any arm that established fewer sessions than it requested
**invalid** rather than averaging it in.

## Why an e2-small is simulated rather than detected

The rule derives its bound from detected machine memory. This host has 16 GB,
so the rule derives ~9.9 GB under `--embed` — correct for a 16 GB machine, and
a bound that never binds at these session counts. Measuring "does the bound
hold" therefore requires a machine on which it binds.

So the arms are split:

- **`default-*`** run the shipped binary with nothing in the environment. They
  prove the derivation executes in a real app and record what a 16 GB host
  actually does — which is the `GOGC=400`-unbounded case.
- **`e2small-*`** supply `GOGC=400` and `GOMEMLIMIT=996MiB` through the
  environment: **the exact figures the rule derives for a 1.93 GiB instance
  running `--embed`** (1977 MiB − 256 MiB OS − 296 MiB `shared_buffers` −
  96 MiB cluster working set, three-quartered = 996.5 MiB). The runtime state is
  identical to what a real e2-small would produce; what is simulated is the
  machine, not the setting.
- **`overbound-*`** supply a 192 MiB limit, below the app's own working set at
  every session count measured, so the collector is running against a target it
  cannot reach. This is the parent run's named, untested falsifier.

The 996 MiB figure is not hand-computed for this doc — it is printed by the
shipped code (`gcTuningFor`) and pinned by
`TestAnE2SmallFitsWithTheMeasuredWorkload`.

## Guards each arm applies, refusing rather than reporting

Inherited from `gogc-postgres-20260816/harness/runone.sh`, with the three
changes the new subject forced:

1. **Port and data directory are derived from this agent's pid.** The parent
   harness hardcodes port 8541 and `~/.skyperf-gogc`; a sibling agent was using
   8541 at the time this ran, and a port collision between two agents already
   cost a wrongly-killed run today.
2. **The GC readback asserts the banner, not the environment.** The shipped
   default sets the limit from *inside* the process, so `ps -E` shows no `GOGC`
   and no `GOMEMLIMIT` — the parent harness's "read it back from the live
   process environment" guard would have rejected every treatment arm. The
   equivalent evidence is the app's own `[sky.gc]` line, which is the shipped
   mechanism stating what it derived. `analyse.sh` marks an arm with no banner
   **invalid**.
3. **`--data-dir` points somewhere durable.** The first attempt put it under
   `/tmp` and `--embed` refused to start — correctly, and the refusal is a
   feature working, so it is recorded here rather than worked around.

Retained unchanged: the port is free before start and the listening pid is the
pid launched; the store is read from the app's own banner (Sky.Live's dev
fallback silently degrades to memory on an unreachable store); the view is
94 `sky-id` elements; a patching self-check runs before the window opens; and an
RSS watchdog aborts an arm above 5 GB so a relaxed pacer cannot take a 16 GB
host down.

## Conditions

| | |
|---|---|
| Host | M1 Mac, 8 physical cores, 16 GB, macOS 25.5 — **shared with two sibling benchmark agents** |
| App | `forumbench` — `examples/19-skyforum` + the `init`-only view-size lever, `FORUM_POSTS=5`, 94 `sky-id` elements |
| Store | **postgres**, the app's own embedded cluster (`--embed`), PostgreSQL 14.21 Homebrew, `fsync=on` |
| Load | closed loop, `-think 0`, 20 s ramp, 8 s warmup, 45 s window |
| Binary | built by overlaying the changed `rt` sources onto the emitted `sky-out/` package, so the ONLY delta against the parent run's binary is this change. It **predates** the startup-banner half of the commit, which touches no allocation path. |

## Layout

```
METHOD.md       this file
README.md       the result and the verdict
results.tsv     one row per arm, with the analysis-time validity verdict
runs/<tag>/     per-arm acct.txt, 1 Hz rss.tsv, app.log (with the [sky.gc] banner)
harness/        runone.sh, sweep.sh, analyse.sh, and both mutation provers
```
