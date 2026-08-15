# What a Sky.Live interaction actually costs

Sizing guidance quoted **"a complex Sky.Live view costs 2–10 ms per
interaction"**. That was an inference, never a measurement, and every
capacity number downstream of it inherited the guess. This document
records the measurement that replaces it, the harness that produced it,
and — just as importantly — what the measurement does not cover.

> **Rule for quoting anything here:** no number in this document may be
> repeated without the conditions attached to it. Every run writes an
> `env.txt` recording host, commit SHA, load average and container
> flags, precisely so a figure cannot travel without them.

## How to reproduce

```bash
# Phase 1 — the microbenchmark (no container, no network, no database)
scripts/skylive-bench.sh                       # 5 runs, refuses on a busy machine
COUNT=15 BENCHTIME=400ms scripts/skylive-bench.sh

# Phase 2 — the load harness, speaking the real protocol
scripts/skylive-load.sh --app examples/26-ui-showcase
CONCURRENCY="100 500 1000" DURATION=60s scripts/skylive-load.sh

# Phase 3 — constrained runs (read the caveats in the script header)
scripts/skylive-load-constrained.sh --profiles "1x2g 2x2g 1x1g"
```

## What is measured, and what is not

The server-side per-interaction path, as `dispatch` runs it
(`runtime-go/rt/live.go:5022`) and as the `/_sky/event` handler replies
(`live.go:4702`):

| Step | Where | Measured here? |
|---|---|---|
| `update(msg, model)` | compiled Sky — user code | **No** |
| `view(model) -> Html` | compiled Sky — user code | **No** |
| `HtmlToVNode` | `live.go:108` | not yet |
| `assignSkyIDs` | `live.go:573` | **Yes** |
| `diffTrees` | `live.go:1295` | **Yes** |
| JSON encode of the reply | `live.go:4715` | **Yes** |

The two Sky-compiled steps cannot be driven from a Go benchmark — they
are the user's own functions, and their cost is app-specific. This is a
real limit on the result and is stated rather than folded in: the
measured figure is the **runtime's** share of an interaction, and a
pathological `view` can dominate it.

That said, the seam is clean. `VNode` (`live.go:51`) is a plain exported
Go struct, `diffTrees` takes two of them, and the whole path below the
Sky boundary is ordinary Go. No app needs to be stood up to benchmark
it — which is why Phase 1 needs no container, network or database.

## The finding that matters most: mutation class, not node count

`diffNodes` (`live.go:1301`) is a non-keyed positional walk with two
very different cost regimes:

- **Text or attribute change** — walk the tree, emit a small patch.
  Cost is proportional to node count, with a small constant.
- **Any change to a child count** — the parent's entire child list is
  re-serialised through `renderVNode` into one HTML patch
  (`live.go:1419-1461`). Cost is proportional to the *subtree*, not to
  the size of the change, and allocates heavily.

So "the cost of an interaction" is not one number. Adding a row to a
list costs far more than editing a label in that same list, at the same
node count. Any sizing figure that does not say which regime it is in
is unusable — which is the specific defect in "2–10 ms".

## Reference view sizes (measured, not assumed)

Counted from the HTML the apps actually serve at this commit:

| App | Addressable elements (`sky-id`) | Total tags |
|---|---|---|
| `examples/19-skyforum` — canonical Sky.Live form flow | 94 | 135 |
| `examples/26-ui-showcase` — every `Std.Ui` primitive | 384 | 422 |

`TestBenchTreeSizesMatchReferenceApps` pins the benchmark fixtures to
these counts and fails if they drift more than 10%. It caught a 36%
drift the first time it ran.

## Guarding against measuring nothing

A benchmark can be wrong quietly, reporting a confident `ns/op` for work
it never did. The guards:

- **`TestBenchFixturesAreNonVacuous`** asserts each mutation class still
  produces the patch shape it is named for. If `list_append` stopped
  emitting an HTML patch, the benchmark would become a no-op walk and
  this fails instead.
- **`skyliveload -self-check`** drives one session end-to-end and prints
  the exchange — cookies, CSRF, scraped handler id, SSE frames, reply
  size — so the client is shown to speak the protocol before any
  throughput is believed.
- **Run validity gates** (`tools/skyliveload/main.go`) mark a run invalid
  rather than reporting it when no session established, no interaction
  produced a patch, no SSE frame arrived, the error rate exceeded its
  limit, or the generator used more than 70% of host CPU.
- **A load gate** in `scripts/skylive-bench.sh` refuses to summarise on a
  busy machine. This is not hypothetical: the first benchmark run in
  this work was taken at load average 15.8 on 8 cores and reported
  `noop` (which produces zero patches) as *slower* than `text_one`, and
  2000 nodes as faster than 1000. Both impossible; both would have read
  as plausible numbers in isolation.

## Limits of the constrained runs

Stated at the top of `scripts/skylive-load-constrained.sh` and repeated
here because it is the easiest thing in this document to misuse:

1. **Not a GCP e2-small.** Apple's `container` runs an **ARM64 Linux VM
   on Apple silicon**; e2-small is a shared-core **x86** instance. These
   numbers are good for *relative* comparisons — before/after, where the
   knee is, memory per session — and **must not** be published as "you
   will serve N users on an e2-small". Only a run on real GCP settles
   that.
2. **Not GCP's burstable credit model.** A fixed vCPU allocation has no
   credit accrual or drain. Real e2 behaviour moves between the floor
   and the ceiling over time in a way this cannot reproduce.
3. **The fractional quotas could not be run.** Apple's `container`
   v1.0.0 accepts only **integer** `--cpus` — it allocates whole vCPUs
   to a VM rather than applying a CFS quota. `--cpus 0.5` and
   `--cpus 0.25` are rejected outright, so the e2-small baseline (0.5)
   and e2-micro baseline (0.25) cannot be reproduced with this tool.
   The 1-CPU run is an **optimistic** stand-in for the e2-small
   baseline — twice its entitlement. Reproducing the real floor needs
   Docker (`--cpus 0.5`), a Linux host with cgroup v2 `cpu.max`, or a
   real VM.
4. **The VM's virtual NIC adds a latency floor**, so constrained runs
   compare with each other, not with bare-host runs.
