# Validating the Sky.Live numbers on real x86 hardware

[`skylive-interaction-cost.md`](skylive-interaction-cost.md) measured the
per-interaction and per-session cost of Sky.Live, and corrected the
project's sizing guidance in two places. One of those corrections was
large: sessions cost **~1.1 MB of RSS each**, where the docs had guessed
10–100 KB from the size of the Model gob. That is an 11–110× error in the
number that decides what instance someone buys.

But every one of those figures was taken on **ARM64 Linux on Apple
silicon**, under an integer CPU allocation, because Apple's `container`
v1.0.0 rejects fractional `--cpus` and the e2-micro (0.25) and e2-small
(0.5) baselines therefore could not be reproduced at all. That document
says plainly they must not be published as GCP numbers.

This document records what a real GCP instance says instead.

## The target

`sky-lang.org` is a live Sky.Live app on a single e2-micro, so it can
answer the hardware question without anything being provisioned:

| | |
|---|---|
| Instance | `sky-lang-org`, zone `us-central1-a`, machine type **e2-micro** |
| Kernel | `Linux 6.1.0-51-cloud-amd64` **x86_64** |
| CPUs | 2 (shared-core; 0.25 vCPU baseline entitlement) |
| MemTotal | 993,236 kB (**970 MB**) |
| Sky | 0.20.2, Go 1.26.1 |
| `[live]` | `store = "memory"`, `input = "debounce"`, no `ttl` → **default 30m** |
| `[database]` | `driver = "sqlite"` |
| systemd | `MemoryMax=768M`, `TasksMax=512` |

Because the session store is `memory` and the database is SQLite, this
instance exercises the **Sky.Live runtime half only** — sessions,
render/diff, SSE. It says nothing whatever about embedded PostgreSQL.

The GCP project is a **parameter**, never a default: every command below
passes `--project` explicitly. `gcloud`'s active project on the
workstation this was run from was an unrelated production estate, and a
`gcloud compute ssh` that silently picks up the wrong project is a class
of mistake worth designing out.

## Reproducing

```bash
# Passive observation. Read-only; safe against a live instance.
scripts/skylive-observe-remote.sh \
    --project <id> --instance sky-lang-org --zone us-central1-a
INTERVAL=20 DURATION=2700 scripts/skylive-observe-remote.sh --project <id>

# Remote load. Defaults to preflight; --load is required to send anything,
# and production hosts are refused even then. Point at a THROWAWAY.
scripts/skylive-load-remote.sh --url http://<bench-ip>:8000
scripts/skylive-load-remote.sh --url http://<bench-ip>:8000 --load \
    --project <id> --instance sky-lang-bench --zone us-central1-a
```

## What is actually reachable — and the gap that shapes everything

This was assumed to be a detail and turned out to be the finding that
determines what can be measured at all.

| Endpoint | Status on this deploy | Carries |
|---|---|---|
| `/_sky/buildinfo` | **200, unauthenticated** | `commit`, `builtAt`, `skyVersion`, `goVersion` |
| `/_sky/healthz` | **200, unauthenticated** | `{"status":"ok"}` — nothing else |
| `/_sky/readyz` | 200, unauthenticated | readiness only |
| `/_sky/metrics` | **401**; 200 with `Authorization: Bearer $SKY_ADMIN_TOKEN` | Prometheus exposition |
| `/_sky/console` (HTML) | **401** | — |
| `/_sky/console/api/overview` | 200 with the admin bearer | uptime, `requestsTotal`, `errorRate5xx`, buffer usage |
| `/_sky/console/api/{logs,traces}` | 200 with the admin bearer | ring buffers |
| `/_sky/console/api/{sessions,live,health,metrics}` | 401 | — |

The console being gated was anticipated — `deploy/setup-remote.sh` skips
the embedded console on this tier because it Go-builds a subapp the
e2-micro cannot host. The real obstacle is elsewhere:

> **There is no live-session count and no memory metric anywhere in the
> runtime's HTTP surface.**

Enumerated from the exposition the running app actually served (6,663
lines), the complete set of metric families is:

```
process_start_time_seconds
sky_http_response_bytes_{bucket,count,sum}
sky_live_msg_seconds_{bucket,count,sum}      sky_live_msg_total
sky_live_request_seconds_{bucket,count,sum}  sky_live_requests_total
sky_telemetry_buffer_used
```

`sky_live_sessions_active` **exists only as a help-text string**
(`runtime-go/rt/telemetry/prometheus.go:88`) and a unit-test fixture
(`telemetry/store_test.go:305`). Nothing in the runtime ever records it.
`sky_live_sse_connections_total` is declared and never recorded either.
There is no `runtime.ReadMemStats` on any served path, no
`go_memstats_*`, and no `net/http/pprof` mount. The `SessionStore`
interface (`runtime-go/rt/live_store.go:376`) has no `Count()`/`Len()`,
and `memoryStore.sessions` (`live_store.go:401`) is an unexported map
with no size accessor, so no backend could report one today.

Two consequences, and they are the reason this document is shaped the
way it is:

1. **RSS must come from `/proc/<pid>/status` over SSH.** That is what
   `scripts/skylive-observe-remote.sh` does.
2. **A session count cannot be obtained at all**, remotely or locally,
   except by counting held SSE connections — and that is a *lower bound*,
   not an equality, because a `memory`-store session outlives its SSE
   stream until the TTL sweep reaps it.

## The passive result

A 45-minute window at 20-second sampling, plus targeted probes before it.

### Base RSS on x86 is measured, and it is not 40 MB

134 samples over 2,697 s:

| | |
|---|---|
| **RSS, idle, x86 GCP e2-micro** | **52.9 – 58.1 MB, mean 56.1 MB** |
| RSS range across the window | 5.1 MB (GC sawtooth, no trend) |
| Process CPU | mean 0.2%, max 0.8% of one core |
| Host CPU | 0.8 – 5.1% |
| MemAvailable | 478 – 528 MB of 970 MB |

The 5.1 MB spread is the Go heap cycling, not growth: the series has no
trend, and it is worth noting because a single reading anywhere in that
band would have looked like a precise figure. The mean over 134 samples
is the number to quote.

For contrast, the local Phase 2 idle baseline
(`docs/perf/runs/phase2-rss.tsv`) was **34.2 MB**.

**These two numbers are not a clean ARM-vs-x86 comparison and must not be
quoted as one.** They are different applications: the local figure is
`examples/26-ui-showcase`, the remote one is sky-lang.org, which links
`sky-github`, opens SQLite, and serves a blog. The honest statement is
narrower and still useful: *a real Sky.Live app idles at ~56 MB RSS on
x86 Linux*, which is the first such figure taken on target hardware, and
it is comfortably inside the unit's `MemoryMax=768M`.

### The app is not the biggest thing on the box

Sizing an instance from the app's RSS alone overstates the headroom
badly. The full resident set on this e2-micro, at the same idle moment:

| Process | RSS |
|---|---|
| `otelopscol` (Ops Agent collector) | **86.2 MB** |
| **`app` (the Sky.Live binary)** | **55.8 MB** |
| `caddy` | 28.3 MB |
| `systemd-journal` | 22.8 MB |
| `fluent-bit` (Ops Agent logging) | 22.1 MB |
| `google_guest_agent` | 17.4 MB |
| `exim4` | 14.8 MB |

With `MemTotal` 970 MB, `MemAvailable` was 501 MB and `MemFree` 397 MB.
(Four `sshd` processes at ~16 MB each were the observation sessions
themselves and are excluded above — the observer is not free, which is
its own small argument for sampling in one SSH session rather than
re-dialling per tick.)

**The observability agent costs more than the application it observes** —
86 MB against 56 MB, and 108 MB once `fluent-bit` is counted. On a
970 MB instance the platform overhead is roughly 250 MB before the app's
first session, so the memory available for sessions is about **500 MB,
not 900**. The systemd unit caps the app at `MemoryMax=768M` in any case.

This matters for anyone sizing an e2-micro from the sizing table: the
budget its per-session arithmetic divides into is substantially smaller
than the instance's nominal RAM. That is independent of whether the
per-session figure itself is right, which the next section takes up.

### Whether ~1.1 MB per session holds: **not answered, and here is why**

The deliverable asked for was a single number with its conditions. The
honest answer is that this instance cannot produce it, and saying so is
worth more than a number computed from an idle box.

Over the 40.3 hours of process uptime preceding the window, the app had
served:

| Signal | Value |
|---|---|
| Total requests (`requestsTotal`) | 737 |
| Requests to `/` | 231 |
| `sky_live_requests_total{route="/sse"}` | **4** |
| `sky_live_msg_total{name="Navigate"}` | **4** |

Much of the remainder is bot noise — `/wp-admin/install.php` (19),
`/.env` (21), `/wp-login.php` (9), `/cgi-bin/..` (7).

The 45-minute window itself was more emphatic than the history:

| Signal, over 2,697 s | Value |
|---|---|
| Requests served | **6** (737 → 743) |
| **`sky_live_msg_total` delta** | **0** |
| Concurrent app connections | 1 in 88 samples, 2 in 47 |

**Zero Sky.Live interactions occurred during the entire window.** The
persistent connection is the Ops Agent's own metrics scrape; the second
is transient. There was never more than one thing talking to the app.

The arithmetic that closes it: the session TTL is the default **30
minutes**, sliding (`runtime-go/rt/live_store.go:16`, `:489`). At 231
page loads spread over 40 hours — about **5.8 per hour** — sessions are
reaped long before they accumulate. They essentially never coexist.

So:

- **The observation window did not contain enough activity to mean
  anything for per-session memory.** With zero interactions and a
  concurrent-connection span of 1, there is no x-axis to regress RSS
  against. `scripts/skylive-observe-remote.sh` detects exactly this and
  refuses to print a per-session slope, reporting `INSUFFICIENT
  ACTIVITY` instead of dividing by a number it does not have.
- **This neither confirms nor falsifies ~1.1 MB/session.** An idle
  e2-micro reporting low RSS is not evidence against a per-session cost;
  it is evidence of no sessions. The 1.1 MB figure remains ARM-measured
  and unvalidated on x86.

There is, however, one weak inference available, and it points the same
way the original document already did. If 231 sessions were resident at
1.1 MB each, RSS would exceed 250 MB; it is 56 MB. Given the 30-minute
TTL that is fully explained by reaping, so it is **not** evidence that
1.1 MB is too high. It does mean this instance's *steady state* is
nowhere near any ceiling: at ~6 sessions/hour against a 768 MB cap, the
e2-micro has orders of magnitude of headroom for its current traffic.

### What would answer it

A **known** session count on the x-axis and measured RSS on the y-axis,
which needs load applied to a target it is safe to load. That is
Priority 2 below, and it needs an instance that does not yet exist.

## Applying load — safely

The generator already spoke a base URL (`-url`). What it lacked was any
reason to trust the URL. Loading sky-lang.org would not "add some load":
the constrained runs put a 1-CPU target at **4.2 s p50 latency at 500
sessions**, so a sweep would take the site down for its duration.

The guards are in `tools/skyliveload/guard.go`, **inside the binary**
rather than only in the wrapper script, because a script guard is
bypassed the first time someone runs the binary by hand — which is
exactly what a person does while debugging a failed run. Three gates:

1. **Non-loopback targets require `-remote-load`.** This is what makes
   passive the default: load can only be applied off-box by *adding* a
   flag, never by forgetting one.
2. **Production hosts are refused even with `-remote-load`.** The list is
   `sky-lang.org` and the instance name `sky-lang-org`, matched exactly
   or as a subdomain suffix, case-insensitively and tolerant of a
   trailing-dot FQDN. Release requires
   `-yes-i-will-take-down-production`, whose name is deliberately
   unpleasant to type. Extend the list without a code change via
   `SKYLIVE_BENCH_DENY_HOSTS`.
3. **The resolved target is printed, with its DNS resolution and
   classification, and the hostname must be typed back** before the first
   request. A bare y/n is too easy to answer on autopilot, and
   inattention about *which host* is the failure being guarded against.
   `-assume-yes` skips the prompt for scripted runs.

`scripts/skylive-load-remote.sh` defaults to **preflight**: it identifies
the target via the two unauthenticated endpoints and sends no load.
`--load` is required to do anything else.

Both layers are covered by `tools/skyliveload/guard_test.go` (9 tests),
and both were **verified by mutation** rather than assumed:

| Mutation | Result |
|---|---|
| `matchesProduction` always returns `""` | 2 tests fail |
| Gate 1 never fires | 4 tests fail |

The end-to-end refusal was also exercised against the real host:
`scripts/skylive-load-remote.sh --url https://sky-lang.org --load
--assume-yes` builds the generator, reaches the first level, and exits
with `REFUSING TO LOAD A PRODUCTION TARGET` — **nothing was sent**.

`tools/skyliveload` is a standalone Go module outside the cargo
workspace, so **no CI job compiles or tests it** — the guards would
otherwise be a safety mechanism nothing ever exercised. Until a CI job
covers it, `scripts/skylive-load-remote.sh` runs `go test ./...` in that
module before it builds the generator, and refuses to proceed if the
guards are not green. That puts the check at the moment it matters, but
it is a weaker place than CI: a broken guard is caught by the next person
to run the harness rather than by the commit that broke it.

### Residual gap, stated rather than discovered later

The deny list matches **hostnames**. Pointing `-url` at the instance's
raw IP address would bypass gate 2, and gate 1 plus the typed
confirmation are all that stand in the way. Matching on resolved
addresses would close it, at the cost of a DNS lookup deciding whether a
run proceeds. Add the IP to `SKYLIVE_BENCH_DENY_HOSTS` when running
anywhere near it.

### Provisioning a bench instance safely

`scripts/skylive-bench-gcp.sh` creates and destroys throwaway instances.
Its design problem is not creation but **guaranteed** destruction: an
orphaned instance bills forever, and the process that created it is
exactly the one that cannot be relied on to clean it up. Three
independent layers, in decreasing order of trustworthiness:

1. **A hard TTL set at creation.** Every instance is created with
   `--max-run-duration` and `--instance-termination-action=DELETE`, so
   GCE deletes it even if this script, this session and this agent all
   cease to exist. Boot disks are created auto-delete, so they go too.
2. **Explicit teardown** (`down`), run unconditionally including on the
   failure path.
3. **Verification** (`verify`), which lists what survives and exits
   non-zero if anything matching the prefix remains.

Every instance is named `sky-bench-*`, and the script **refuses to
create or delete anything that is not**. That prefix check runs before
every mutating call, so production instances reachable with the same
credentials — `sky-lang-org`, `darraghstudio-vm`, `ringfence-cloud-1`,
`settleby-caddy`, `sky-pro-user-*`, `skydeploy-cp-dev` — cannot be named
by this script even deliberately. Verified:

```
$ scripts/skylive-bench-gcp.sh down --project <id> --name sky-lang-org
REFUSING to act on 'sky-lang-org' -- name does not start with 'sky-bench-'.
```

```bash
scripts/skylive-bench-gcp.sh up --project <id> \
    --name sky-bench-micro --machine-type e2-micro --ttl 3h
scripts/skylive-bench-gcp.sh up --project <id> \
    --name sky-bench-gen --machine-type e2-standard-2 --ttl 4h
# ... run the sweep ...
scripts/skylive-bench-gcp.sh down   --project <id>
scripts/skylive-bench-gcp.sh verify --project <id>
```

**The generator belongs in the same zone as the target.** Driving load
from a workstation over the internet would put a WAN round trip inside
every latency percentile, which is the number the sweep exists to
measure. A `e2-standard-2` in `us-central1-a` reaches the bench
instances over `default-allow-internal` with no firewall change.

**Ops Agent parity is a decision, not a detail.** The agent is installed
by `deploy/setup-remote.sh`, not by the GCE image, so a fresh instance
does **not** have it. That is ~86 MB — about 9% of an e2-micro — and a
bench box without it has materially more headroom than production.
`--ops-agent` installs it; running one instance with and one without
turns "the agent costs 86 MB" into "the agent costs N concurrent
sessions", which is the form a reader can act on.

**Status: the guards are tested, the lifecycle is not.** `up` and `down`
have **never been executed** — see *What was not run* below.

### The bench instance — the deploy.sh path

The load target must be a throwaway, stood up from the *same* tooling
that deploys the real site so that it is the same app on the same
machine type:

```bash
cd /path/to/sky-lang.org
deploy/deploy.sh \
    --project  <id> \
    --instance sky-lang-bench \
    --zone     us-central1-a \
    --account  <deploy-service-account>
```

Then, from this repo:

```bash
scripts/skylive-load-remote.sh \
    --url http://<bench-ip>:8000 \
    --load --assume-yes \
    --project <id> --instance sky-lang-bench --zone us-central1-a \
    --concurrency "1 50 100 250 500" --duration 30s --repeats 3
```

That command **also** starts `skylive-observe-remote.sh` against the
bench instance for the duration of the sweep, at 5-second sampling. That
pairing is the entire point: load supplies a known session count, `/proc`
supplies RSS, and the join of `summary.tsv` against
`observer/derived.tsv` on timestamp gives per-session memory **on x86**
— the measurement this instance could not provide.

Delete the instance afterwards. **This repo does not create it**:
provisioning cloud resources costs money and is the operator's call.

Two things to hold on to when that run happens:

- **Quote per-session memory with the view size.** The 1.1 MB figure was
  measured holding a 384-node view. A lighter view will be cheaper, and
  sky-lang.org's is much lighter.
- **The generator must be proven not to be the bottleneck**, as it was
  locally (0.25–1.6% of the machine). Over a network to `us-central1`,
  latency percentiles will include a WAN round trip that the local runs
  did not have — so remote p50/p95 are **not** comparable to the local
  table, only to each other.

## Postgres: no longer underived — but read the next paragraph

**This section has been superseded.** Embedded PostgreSQL was subsequently
measured on an e2-small, and the results — idle footprint, the derived
`max_connections` as actually rendered, backends under load, per-session
cost and the load curve — are in
[`skylive-interaction-cost.md`](skylive-interaction-cost.md), "Embedded
PostgreSQL, measured", with raw data under
`runs/gcp-embed-postgres-20260815/`.

Two headline corrections from that run, since they bear directly on the
recipe below: the `SKY_POSTGRES_BIN` route **works exactly as described
here** and exercises the whole runtime path, so the recipe was sound; but
the **bundle** path it works around is still untested on real hardware, and
the sizing table's "36 MB at `shared_buffers = 32MB`" turned out to describe
the *development* cluster profile, not the `--embed` one, which derives
`shared_buffers = 296MB` on a 2 GB host.

What follows is the original, pre-execution recipe, kept because it is what
was actually followed and because the run confirmed its reasoning.

Every PostgreSQL figure in the sizing table is inferred; none has been
observed on target hardware. This instance cannot help — it runs SQLite.

Settling it needs a third instance running the same app with embedded
PostgreSQL enabled. That work is **documented below and deliberately not
executed**: the feature lives on a different branch, and nothing here
deploys it.

### What the `--embed` instance would need

Written from `feat/embedded-postgres` (the feature is not on this
branch), read-only. **Nothing below was executed.** Every step is cited
so the recipe can be checked before anyone spends money on it.

**The blocker to solve first.** `docs/skydb/embedded-postgres.md:6-16`
states that no `postgres-bundle-v*` release is cut, and `git tag --list
'postgres-bundle*'` returns nothing. So `sky db provision --embed` cannot
fetch a bundle, and the binaries have to come from somewhere else.

`SKY_POSTGRES_BIN` is the way in, and it is **a directory** — the `bin/`
of a relocatable PostgreSQL tree, not a tarball and not an executable.
The runtime requires `initdb`, `pg_ctl` and `postgres` in it
(`runtime-go/rt/pg_embed_bundle.go:59`) and derives `../lib` and
`../share` from its parent, exporting `PGSHAREDIR` and
`LD_LIBRARY_PATH` (`pg_embed_bundle.go:229-252`, `:79-97`). Ship
`<root>/{bin,lib,share}` and point at `<root>/bin`. If it is set but
incomplete, that is a hard error, not a fall-through
(`pg_embed_bundle.go:115-123`).

Discovery order, when it is unset (`discoverPgBins`,
`pg_embed_bundle.go:113-159`): the `go:embed`ed bundle (only if built
with `sky build --embed`) → `$SKY_HOME/postgres/<version>/bin` → `$PATH`.
**There is no fetch at run time.**

**The cross-compilation trap.** This is the step most likely to waste a
day:

- The bundle must match the *target* triple — `linux-amd64` for a GCP
  x86 VM. `runtime-go/rt/pg_embed_bundle.go` performs **no** arch check
  at all; a `darwin-arm64` tree fails empirically at first app start
  with "the PostgreSQL binaries do not run", not at build time
  (`pg_embed_bundle.go:200-205`).
- **A linux-amd64 bundle cannot be built on an Apple-silicon Mac.**
  `scripts/skydb/build-postgres-bundle.sh:77-92` derives OS and arch from
  `uname`, building natively only, and
  `.github/workflows/postgres-bundle.yml:99-101` says cross-compiling
  PostgreSQL would defeat the purpose. Build on x86 Linux — the VM
  itself, a container, or CI.
- `sky db provision --embed --from <archive>` runs the extracted
  `postgres --version` before installing (`db_provision.rs:848-854`), so
  a linux-amd64 bundle **cannot** be staged from the Mac either.

Given all that, the cheapest path on a Debian VM is the distro's own
PostgreSQL. Debian installs to `/usr/lib/postgresql/18/bin`, which is not
on `$PATH`, so it becomes the `SKY_POSTGRES_BIN` case anyway:

```bash
sudo apt-get install -y postgresql-18
# in the unit's EnvironmentFile:
SKY_POSTGRES_BIN=/usr/lib/postgresql/18/bin
```

**The `sky.toml` change, and a refusal to plan around.** The embedded
surface is exactly two keys (`docs/sky-toml.md:322-332`):

```toml
[database]
embedded = true
postgresVersion = "18.6"
```

`embedded = true` alongside `path` or `url` — or `DATABASE_URL` or
`<PREFIX>_DB_PATH` in the environment — is a **refusal, not a precedence
rule** (`runtime-go/rt/pg_embed.go:370`). sky-lang.org's `sky.toml`
currently sets `driver = "sqlite"` and `path = "sky-lang.dev.db"`, and
its `.env` sets `SKYLANG_DB_PATH`. **Both must go**, or the app will not
start.

**Run it as the production path**, not the dev one: `sky run --embed` is
explicitly refused (`rust/crates/sky/src/main.rs:835-846`). Build with
`sky build --embed`, then run `./app --embed --data-dir /var/lib/<name>`.
The data directory may not be a temp path — `/tmp`, `/var/tmp`,
`/dev/shm` and `$TMPDIR` are rejected (`pg_embed.go:299-321`) — and
PostgreSQL listens on a **unix socket only** (`listen_addresses = ''`),
so there is no port to expose. There is **no shipped systemd unit** for
`./app --embed` (`embedded-postgres.md:1010`); the existing
`sky-lang-org.service` would need adapting, including raising its
`MemoryMax=768M`.

**Machine type: not an e2-micro.** Tuning is derived from the host at
every boot, never configured (`runtime-go/rt/pg_embed_conf.go:173-214`):
`shared_buffers` is **15% of RAM**, `effective_cache_size` 40%,
`max_connections` derived from CPU count. On the 970 MB e2-micro that is
~145 MB of shared buffers on top of a base the feature's own sizing
section puts at **~380 MB before any session**
(`embedded-postgres.md:931-1050`). Use an **e2-small (2 GB)** or larger,
and record that this makes it *not* a like-for-like comparison with
`sky-lang-org` — two variables move at once, so run the SQLite
configuration on the same machine type as a control.

**The point of the exercise.** That same sizing section already quotes
"Sky.Live sessions — ~1.1 MB RSS each, measured" and "1 GB carries
roughly 400–500 concurrent sessions", and flags at
`embedded-postgres.md:1002-1006` that the figures are ARM-on-Apple-silicon
and not a claim about any cloud instance. Everything in this document
says that caveat still stands: the x86 base RSS is now measured, and the
per-session figure those capacity numbers rest on is not.

## Conditions

| | |
|---|---|
| Observed | `sky-lang-org`, `us-central1-a`, e2-micro (project recorded in the run's `env.txt`) |
| Window | 45 min at 20 s sampling, plus targeted probes |
| Harness | `scripts/skylive-observe-remote.sh`, landed in `f1de081c` |
| `env.txt` `commit` field | `c0535659` — the branch HEAD when the run *started*. The harness was still uncommitted at that moment and landed minutes later, byte-identical to what ran. Recorded here rather than quietly reconciled. |
| Observer host | macOS arm64 (transport only — no measurement runs here) |
| Mode | **passive, read-only**; no load was applied to any instance |
| Raw data | `docs/perf/runs/observe-prod-45min/` |

Nothing from the instance's `.env`, and no credential, appears in this
repository. The admin token is read on the box, used on the box for the
localhost metrics scrape, and never transmitted or written down.

## The active result — load against two throwaway x86 instances

Everything above this heading was passive. What follows was measured by
applying load to instances created for the purpose and deleted
afterwards, and it settles the question the passive run could not.

### Conditions, attached to every number below

| | |
|---|---|
| Targets | `sky-bench-micro` (**e2-micro**) and `sky-bench-small` (**e2-small**), `us-central1-a`, project `settleby` |
| Both | Debian 12, `Linux 6.1.0-52-cloud-amd64` **x86_64**, 2 shared-core vCPU, 20 GB pd-standard |
| MemTotal | micro **993,232 kB (970 MB)** · small **2,023,888 kB (1.98 GB)** |
| Application | **`examples/26-ui-showcase`**, cross-compiled `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, Go 1.26.1 |
| `[live]` | `port = 8000`, store defaults to **`memory`**, TTL defaults to **30 min** |
| systemd | unit `skybench`, **no `MemoryMax`** (see below), `TasksMax=4096`, `LimitNOFILE=65535` |
| Ops Agent | **ABSENT** for every figure unless the row says `AGENT` |
| Generator | `tools/skyliveload`, on **macOS arm64, 8 cores, in the UK** — off-box, across the public internet |
| Think time | 1 s, jitter 0.3 · ramp 20 s · hold 75 s · warmup 5 s |
| Commit | `ba3c3b1d`, branch `perf/skylive-benchmark` |
| Raw data | `docs/perf/runs/gcp-x86-20260815/` |

The app was chosen to match the ARM runs. `examples/26-ui-showcase` is
what `docs/perf/runs/phase2-rss.tsv` measured, so the ARM and x86 numbers
here are the **same application** — which is exactly what the earlier
sky-lang.org comparison could not claim, and why that comparison was
refused above.

**The generator was never the bottleneck, and this is checked rather than
assumed.** Across all **29 load runs** the generator's own accounting
reports a maximum of **0.292% of the 8-core generator machine**, and
`generator_possibly_saturated` is `false` in every one of the 29 result
files. A saturated generator measures itself; these runs measure the
server.

**`MemoryMax` is deliberately unset.** Production caps the app at 768 M.
A cap turns the high-concurrency levels into an OOM cliff, and the
per-session slope would then be measured against a ceiling rather than
against demand. Its absence is a stated condition, not an oversight —
and one consequence of removing it is recorded below, where the e2-micro
exhausted the whole machine instead.

### Method: why each level restarts the app

`scripts/skylive-load-remote.sh` sweeps 1…500 continuously and sleeps 15 s
between levels "so sessions drain". **They do not drain.** The memory
store holds a session until the TTL sweep reaps it and the default TTL is
30 minutes (`runtime-go/rt/live_store.go:16`, `:489`); the SSE stream
closes, the session object stays. Over a 5-level × 3-repeat sweep that is
~2,700 sessions created and essentially none released, so RSS climbs with
*cumulative sessions created* and any regression against *concurrent*
sessions is confounded.

Each measurement here therefore restarts `skybench` first, so every level
starts from a genuinely empty store. The divisor is
`sessions_established` as the generator counted them, never the number
requested — at 500 on the e2-micro those differ (447 established), and
dividing by the request would have understated the per-session cost by
12%.

### Idle baselines

Sampled for 300 s at 5 s, `/proc/<pid>/status`, zero connections and zero
`sky_live_msg_total` throughout — genuinely idle, and recorded as such.

| | e2-micro | e2-small |
|---|---|---|
| App RSS idle | **22.72 MB** (spread 0.00 MB, n=42) | **21.96 MB** (21.87–23.87, n=42) |
| MemAvailable | 579 MB | 1,588 MB |
| Ops Agent | absent | absent |

The e2-micro's zero spread is worth a note against the production
observation above, which saw a 5.1 MB sawtooth: that sawtooth is the Go
heap cycling *under traffic*. With no traffic at all there is nothing to
collect, and RSS is a flat line.

For contrast the same app on ARM idled at **34.2 MB**
(`phase2-rss.tsv`). Same application, same version, so this one **is** a
fair ARM-vs-x86 comparison, and x86 idles ~34% lower.

### 1. Per-session RSS on x86 — **~1.4 MB, and 1.1 MB is falsified**

This is the deliverable. RSS was regressed against *established* sessions
over 26 (micro) and 30 (small) points spanning 1–500 sessions, each with
its own idle anchor:

| | slope | intercept | measured idle |
|---|---|---|---|
| **e2-micro** | **1,378.9 kB/session = 1.35 MB** | 24.4 MB | 22.72 MB |
| **e2-small** | **1,449.7 kB/session = 1.42 MB** | 21.2 MB | 21.96 MB |

**The intercept is an independent check, and it passes.** Nothing in the
fit knows the idle baseline, yet the fitted zero-session cost lands
within 1.7 MB of the separately measured idle RSS on both machines. A
linear model with a spurious slope would not recover it.

Per level, so the linearity can be inspected rather than taken on trust:

| sessions | e2-micro kB/sess | e2-small kB/sess |
|---|---|---|
| 1 | 13,128 | 11,076 |
| 25 | 1,671 (1,481–1,862) | 1,487 (1,386–1,588) |
| 50 | 1,430 (1,351–1,508) | 1,393 (1,259–1,527) |
| 100 | 1,383 (1,105–1,536) | 1,390 (1,350–1,419) |
| 250 | 1,498 (1,400–1,571) | 1,428 (1,402–1,459) |
| 500 | 1,291 (447 established) | 1,458 (1,444–1,481) |

**The n=1 row is not a per-session cost and must not be read as one.**
13 MB for one session is the fixed cost of the first request — arena
warm-up, buffers, GC headroom — divided by one. It is the clearest
argument for quoting the slope rather than a ratio at any single level:
a ratio charges the app's fixed load-time growth to the sessions and
overstates what the *next* session costs.

**Verdict on ~1.1 MB.** The ARM figure was 1,047 kB at 500 sessions
(1,047–1,357 across levels). On x86 the slope is **1,379–1,450 kB**, so
the per-session cost on real GCE hardware is **~30% higher than the ARM
measurement**, consistently on both machine types and at every level
above 25. Taken literally, **1.1 MB/session is falsified on x86; the
figure to size with is ~1.4 MB.**

Taken as the correction it was made to support, it survives easily. The
sizing docs had guessed 10–100 KB from the size of the Model gob; the
true cost is **~14–140× that**, and the ARM run's error was in the
conservative direction.

Restated as capacity, which is what the sizing table actually needs:

| | 1 GB of session budget |
|---|---|
| Docs' original guess (10–100 KB) | 10,000–100,000 sessions |
| ARM measurement (1.1 MB) | ~950 sessions |
| **x86 measurement (1.4 MB)** | **~730 sessions** |

### 2. The load curves, and where the knee actually falls

Throughput is interactions/sec; each interaction is one
`POST /_sky/event` returning a real patch set. Every outcome in these
runs was `ok` — no zero-patch replies inflating the count, which the
earlier microbenchmark had to discard and re-run for.

**e2-micro (970 MB, 2 shared-core vCPU, 0.25 baseline):**

| sessions | tput/s | p50 ms | p95 ms | p99 ms | err |
|---|---|---|---|---|---|
| 1 | 0.9 | 137 | 145 | 150 | 0 |
| 25 | 17.9 | 143 | 2,087 | 2,897 | 0 |
| 50 | 11.8 | 214 | 11,693 | 14,405 | 0 |
| 100 | 12.2 | 3,901 | 22,668 | 27,343 | 1.3% |
| 250 | 7.6 | 19,498 | 29,027 | 29,506 | **84%** |
| 500 | 12.9 | 17,245 | 26,741 | 29,201 | **96%** |

**e2-small (1.98 GB, 2 shared-core vCPU, 0.5 baseline):**

| sessions | tput/s | p50 ms | p95 ms | p99 ms | err |
|---|---|---|---|---|---|
| 1 | 0.9 | 137 | 146 | 150 | 0 |
| 25 | 21.5 | 142 | 184 | 216 | 0 |
| 50 | 35.3 | 179 | 2,027 | 2,821 | 0 |
| 100 | 29.9 | 1,059 | 8,606 | 12,252 | 0 |
| 250 | 21.4 | 5,830 | 24,570 | 27,901 | 1.8% |
| 500 | 16.0 | 17,217 | 28,495 | 29,596 | **79%** |

**The knee is far earlier than the ARM runs suggested.** The ARM 1-CPU
container knee sat between 100 and 500 sessions and saturated at 88–92
interactions/sec. On GCE:

- **e2-micro knees between 25 and 50 sessions** and never exceeds
  ~18/s. That is **5× lower throughput and a 4–10× earlier knee** than
  the ARM stand-in predicted.
- **e2-small knees between 50 and 100 sessions**, peaking ~35–42/s.

The ARM run said plainly that its 1-CPU profile was an *optimistic*
stand-in for the e2-small baseline, being twice the entitlement. That
caution is now quantified: it was optimistic by roughly **2.5× on
e2-small and 5× on e2-micro**.

At 250 sessions and above, both machines are past collapse — 79–96% of
interactions fail. Those throughput figures describe a failing server and
should not be read as capacity.

### 3. Burst-credit drain — visible, and it makes "variance" the wrong word

The instruction to run three repeats and report variance rather than a
mean turned out to matter for a reason other than noise. Repeats at a
fixed level **decline monotonically**:

| | r1 | r2 | r3 |
|---|---|---|---|
| e2-micro, n=100 | 17.5/s | 9.6/s | 9.5/s |
| e2-small, n=100 | 37.6/s | 25.8/s | 26.4/s |
| e2-micro, n=25 | 21.5/s | 14.3/s | — |
| e2-small, n=50 | 41.6/s | 28.9/s | — |

This is the **e2 burstable CPU credit model**, which the ARM container
explicitly could not reproduce — a fixed vCPU allocation has no such
dynamics. The first run against a rested instance spends accrued credits
and overstates sustained capacity by **~1.5–2×**.

The operational consequence is blunt: **a single benchmark run against a
fresh e2 instance measures the burst, not the service.** Sustained
capacity is the *later* repeats — ~9.5/s on e2-micro and ~26/s on
e2-small at 100 sessions. Any capacity plan built on a first run will be
roughly twice as optimistic as the machine can hold.

It also means the spread in the tables above is **not** a confidence
interval. Where a level's repeats decline in order, the range is a trend,
and the low end is the number to plan with.

### 4. Network latency — measured, and it dominates only before the knee

The generator ran in the UK against `us-central1-a`, so the wire is in
every latency figure. It was measured rather than assumed:

| | |
|---|---|
| ICMP RTT to both instances | **110.4–113.6 ms, mean 112.0 / 110.7 ms**, 0% loss, stddev 0.9 ms |
| p50 at n=1 (unsaturated) | **136.8 ms** on both machines |
| **Implied server time** | **~26 ms** |

So at the bottom of the curve the network is **~81% of p50 and ~74% of
p99**. Any latency figure at n=1–25 in the tables above is mostly the
Atlantic.

**It does not dominate p99, because p99 is where queueing lives.** At
n=50 and beyond, p99 is 2.8–29.6 s against a fixed 111 ms wire — the
network is **under 1%** and everything else is the server queueing.

The clean split: **network dominates below the knee, queueing dominates
at and above it.** The knee itself is unaffected, and so are all the
memory figures. The absolute latencies at n≤25 should be read as
"UK→us-central1"; subtract ~111 ms for a same-region client.

### 5. The e2-micro ran out of memory before it ran out of sessions

At n=500 the e2-micro established only **447** of 500 sessions, and the
sampler recorded `MemAvailable` falling from 617 MB to **43.5 MB** with
app RSS at 591 MB. The following repeat pushed it over: the instance
stopped answering SSH on both the direct and IAP paths while still
reporting `RUNNING`, and had to be `reset`.

The arithmetic that predicts it: 500 × 1.4 MB ≈ 700 MB of sessions, plus
~22 MB of app baseline and ~180 MB of OS, against 970 MB total.

**This is honestly a partially-evidenced claim and is flagged as such.**
No `oom-kill` line was recoverable — journald stopped writing at the
onset, which is itself consistent with memory exhaustion but is not the
kernel saying so. What *is* directly measured is the `MemAvailable`
collapse to 43.5 MB. The e2-small under the identical run never dropped
below **870 MB** free.

The practical ceiling therefore differs by resource:

| e2-micro | limit |
|---|---|
| **Memory** ceiling | ~450 sessions (measured: 447 established, 43 MB left) |
| **Usable** ceiling | **~25–50 sessions** (CPU; beyond it, latency is seconds) |

**CPU binds roughly 10× before memory does.** Sizing an e2-micro from
RAM alone overstates its capacity by an order of magnitude.

### 6. The Ops Agent's cost, in sessions

Both instances started without the agent — it is installed by
`sky-lang.org/deploy/setup-remote.sh`, not by the GCE image — so this is
a true A/B rather than a comparison against a differently-configured box.
It was then installed on `sky-bench-micro` alone, with production's exact
config (journald logging, the authenticated `/_sky/metrics` scrape, the
OTLP receiver).

**RSS overstates the cost, so RSS is the wrong number.** Resident:

| process | RSS |
|---|---|
| `otelopscol` | 151–156 MB |
| `fluent-bit` | 31–33 MB |
| total | **~190 MB** |

But an A/B on `MemAvailable` **within a single boot** — stop the agent,
wait, re-read — puts the real cost far lower:

| | MemAvailable |
|---|---|
| Agent running | 513.1 MB (mean of 5) |
| Agent stopped | 599.5 MB (mean of 5) |
| **Cost** | **86.4 MB** |

The 104 MB gap between the two methods is shared and file-backed pages
that RSS counts and the machine does not lose. Quoting agent RSS would
have overstated its cost by **2.2×**.

At the measured 1.35 MB/session on this machine, 86.4 MB is:

> **The Ops Agent costs an e2-micro ~64 concurrent sessions of memory
> headroom.**

**And that is the wrong thing to worry about.** The e2-micro's usable
ceiling is 25–50 sessions on CPU, so the 64 sessions of memory it gives
up are sessions the machine could never have served. The honest
statement is:

> On an e2-micro the Ops Agent costs **~86 MB, ~64 sessions of memory
> headroom, and approximately nothing you can use** — because the box
> saturates on CPU at roughly a fifth of that.

This also answers the assumption production made and never checked. The
embedded console was skipped on this tier as too expensive, and the Ops
Agent adopted in its place. On memory the swap is defensible; the agent's
86 MB is real but lands in headroom this machine cannot spend.

**Per-session cost is unchanged by the agent**, which is the result that
makes the headroom arithmetic above legitimate — the agent takes a fixed
block, it does not make each session more expensive:

| e2-micro | kB/session, no agent | kB/session, `AGENT` |
|---|---|---|
| 25 | 1,481 / 1,862 | 1,541 / 1,954 |
| 50 | 1,508 / 1,351 | 1,488 / 1,412 |
| 100 | 1,105–1,536 | 1,252 / 1,261 |

#### The throughput difference is NOT attributed to the agent

Throughput with the agent installed was markedly lower — at n=100,
**~5.0/s against ~9.5/s** for the sustained (post-credit-drain) runs
without it. It would be easy, and wrong, to publish that as the agent's
CPU cost.

Two things prevent that claim:

1. **The runs are not credit-comparable.** The with-agent runs
   necessarily followed the without-agent runs on the same instance, and
   e2 burst credits do not reset between them (§3). Credit state is a
   confound of the same order as the effect.
2. **The agent's measured CPU is far too small to explain it.** Sampled
   directly from `/proc/<pid>/stat` over a 30 s window with the app
   stopped, `otelopscol` + `fluent-bit` together consume **1.87% of one
   core** (0.93% of the 2-core box). Even several times that under load
   does not account for halving the throughput of the machine.

So the honest verdict is that **the agent's CPU cost at the knee was not
measured**, and the throughput gap above is reported as unattributed.
Separating it needs the two configurations run in alternation on
credit-matched instances, or two instances measured simultaneously — 
neither of which this run did.

### What still could not be measured — stated, not manufactured

1. ~~**The `--embed` (embedded-PostgreSQL) variant was not run.**~~ —
   **closed by a later run.** It was executed on a third instance
   (`sky-bench-embed`, e2-small) and is written up in
   [`skylive-interaction-cost.md`](skylive-interaction-cost.md), "Embedded
   PostgreSQL, measured". What remains underived there is the *bundle*
   delivery path, not the runtime.
2. **The Ops Agent's CPU cost at the knee is not separated from
   burst-credit drain** — see §6. Its idle CPU is measured (1.87% of one
   core) and its memory cost is a clean within-boot A/B, but the
   throughput gap is left unattributed rather than credited to it.
3. **`sky_live_sessions_active` is still never recorded**, so the session
   count is still the generator's count and not the server's. Everything
   in the gap analysis above stands; nothing here fixed it.
4. **No same-region client was measured.** The ~111 ms wire is
   characterised and subtractable, but a us-central1 generator was not
   run, so the sub-knee latencies are UK-specific.
5. **The e2-micro OOM is inferred from `MemAvailable`, not from a kernel
   message** — see §5.

### Teardown

Both instances carried `maxRunDuration=14400s` with
`instanceTerminationAction=DELETE` as a backstop, and were additionally
deleted explicitly. The temporary firewall rule opened for the generator
(`sky-bench-load-8000`, scoped to a single source address) was deleted
with them. Verification output is recorded in
`docs/perf/runs/gcp-x86-20260815/teardown.txt`.
