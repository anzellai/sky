# The four contended locks: contention removed, throughput unchanged — a null

**Recorded null. The changes were implemented, tested, proven red by mutation,
measured, and then REVERTED.** `runtime-go/` at the tip of this branch is
byte-identical to `628c08c5`. The implementation survives in history at
`94d116ca` + `80745975` and is one `git revert` from being restored.

## What was measured

`docs/perf/runs/gomaxprocs-scaling-20260816/` named four locks from a mutex
profile at GOMAXPROCS=8, against **40 microseconds** of contention in total at
GOMAXPROCS=1 — so the cost is entirely a parallelism effect. All four were
fixed:

| lock | share | fix |
|---|---|---|
| store `memCache` write-lock | 39.6% | read-locked fast path: `handleEvent` calls `Set(sid, sess)` with the pointer `Get` just returned, so the write was a **no-op self-assignment** under a process-wide write lock |
| goroutine→session `sync.Map` | 26.5% | sharded plain map; applied to the trace-context map too, which has the identical shape |
| `sessionLocker` map guard | 23.0% | sharded by sid |
| otel `TracerProvider.Tracer` | 6.0% | cached on provider identity |

## The lock work succeeded. The application did not get faster.

Mutex profile, delta across the measurement window, same load, same store,
GOMAXPROCS=8, instrumented binaries built from the same emitted package:

| | total mutex delay |
|---|---|
| control | **219.33 s** |
| treatment | **35.54 s** — **−84%** |

Named frames, control → treatment:

| frame | control | treatment |
|---|---|---|
| `(*postgresStore).Set` | **131.71 s** | **gone** |
| `setGoroutineLiveSession` | **39.01 s** | **gone** |
| `(*liveApp).dispatch` | 71.73 s | 25.51 s |
| `currentGoroutineID` | 22.10 s | 21.52 s — now **60% of what remains** |
| `WithMsgSpanTraced` | 20.31 s | 15.77 s |

Throughput, plain (uninstrumented) binaries, interleaved control/treatment so
host drift hits both arms equally, n=100, postgres store:

| arm | runs (int/s) | median | within-arm spread |
|---|---|---|---|
| control | 2227, 2379, 2582, 2717, 2752 | 2,582 | **24%** |
| treatment | 1905, 2113, 2611, 2752 | 2,362 | **44%** |

**The result is a null, and the host could not have resolved it either way.**
The within-arm spread is 24–44% against a prize the profile bounds at 4.7% of
CPU — noise exceeds the entire available effect by 5–9×. Two sibling agents
were running their own benchmarks throughout. The paired instrumented runs, the
closest thing to a controlled comparison here, read **2,726 vs 2,733**.

## Why this is a real finding and not just a failed measurement

The mutex profile's `delay` metric went to near-zero while throughput did not
move. Those are consistent, and the reconciliation is the point:

**Mutex delay is queueing time, and queueing that overlaps useful work is not
lost throughput.** With 8 threads and hundreds of goroutines, a goroutine parked
on `memMu` is descheduled and another runs; the delay accumulates in the profile
without the CPU ever going idle. Removing the contention returns the *waiting*,
not the *work*.

There is also a plausible mechanism for the fast path being neutral-to-negative:
replacing an exclusive `Lock` with an `RLock` does not remove the cache-line
contention, because `RWMutex.RLock` is an atomic increment on **one shared
counter**. Blocking disappears from the profile while the coherence traffic
stays. If the lock work were ever revisited, **sharding `memCache` — which gives
each shard its own mutex on its own cache line — is the tactic that would
address that**, and it is the one this implementation argued against on
risk grounds.

## Answering the design question directly

> Should the goroutine→session registry exist at all? Passing the session
> explicitly beats making a global goroutine-id-keyed lookup faster.

**Agreed in principle, and it cannot be done without an FFI ABI change.**

Every *writer* has the session in hand (`dispatch(sess, msg)` stamps it).
Every *reader* is inside a Sky FFI kernel thunk, whose signature is
`func(any) any` — there is no session parameter and no context argument to
thread one through. That is precisely why the mechanism was built
(`live_session_ctx.go:1-21`): `Sky.Core.Http.Stream.open/close` must register a
stream handle on the current session so disconnect can sweep it.

So the choice is not "faster lookup vs. pass it explicitly". It is "faster
lookup vs. change the kernel FFI signature to carry a context". The latter is a
legitimate design question — it would also delete the trace-context and
request-id registries, and remove `currentGoroutineID`, which this measurement
shows is now the **dominant remaining contention frame** because it calls
`runtime.Stack` on every lookup. But it is a compiler-and-runtime-wide change,
not a refactor, and nothing in this measurement justifies it: the whole prize is
4.7% of CPU.

## What was dropped

- **Sharding `memCache`.** Considered and rejected during implementation: it
  would repeat across four store types and both whole-map sweeps, and each of
  the documented TOCTOU fixes (#1, #2, #6, #8) rests on `memMu` being mutually
  exclusive with the idle-evict pass. Given the null, it was not attempted after
  the fact either.
- **Per-change isolation.** Only control-vs-all-four was measured. With the
  combined effect inside noise, bisecting it further could not have produced a
  signal.
- **A quiet host.** The A/B was gated on load < 4.0 with no `xcrun` running, and
  still could not get below a 24% within-arm spread.

## What the gates would not have caught

The four gates (all proven red by mutation, `harness/mutate.sh`) assert
**correctness under sharding** — mutual exclusion per session, no entry leak,
no corpse resurrection, provider-swap visibility. **None of them asserts that
the change is faster**, and none would have gone red if the fix were a pure
pessimisation. That is the right division — a performance claim belongs in a
measurement, not a unit test — but it means the green suite in `94d116ca` and
`80745975` is not evidence the work was worth landing. This document is.

One gate defect worth recording: the two distribution gates initially derived
their expectation from the very constant their falsifying mutation changes
(`used != sessionLockerShards`, which reads `1 != 1` at the mutated value), so
both stayed green under their own mutation while asserting nothing. They were
caught only because the mutation run reported `ok` in 0.023 s — too fast to have
executed 4096 hashes and 512 goroutine spawns. **A threshold computed from the
constant under test cannot detect a change to that constant.**
