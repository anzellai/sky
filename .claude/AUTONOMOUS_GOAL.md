# Autonomous mandate — BlueDB v1 (production-ready, seamless for Sky apps)

Set: 2026-08-03. Branch: `exp/bluedb`.

## User's goal (verbatim — the authority on "done")

> "implement + grill until fully e2e for a full v1 bluedb ready for sky app to
> use seamlessly with reliability + scaling + good throughput as must achieve
> goal. fully autonomous"

## What v1 means (the gradeable criteria)

**Must-achieve: reliability + scaling + good throughput.** Grill + e2e each tier.
Distributed/horizontal (Raft) is honestly v2 — v1 is the **embedded** engine made
production-grade, with the v2 path documented, not hand-waved.

- **T-A Reliability (keystone).** A crash-consistency FUZZ harness: random op
  sequences + random crash points (torn writes / abandon-and-reopen) → reopen →
  invariant holds (every ACKED op present, no corruption, no resurrection, matches
  an oracle). Plus resource bounds (max value size; working-set-in-RAM documented
  + guarded), and the ForEach-holds-lock stall fixed (scans don't block writes).
- **T-B Throughput + scaling (embedded).** Go benchmarks: cached point-read,
  durable group-committed write, concurrent, mixed — prove the capacity.md numbers
  order-of-magnitude. Fix anything the bench reveals. Document the single-instance
  ceiling + the v2 distributed path.
- **T-C Seamless Sky-app use.** Beyond sessions: a Sky app stores its OWN typed
  data in bluedb. A `Std.BlueDB` KV/document module (Codec-typed get/put/delete/
  scan) + kernel bindings + runtime, e2e-verified in a real app. (If the kernel
  surface proves too large/destabilising to finish cleanly, ship the engine +
  session use as v1 and document the app-data API as the remaining surface — do
  NOT ship a half-wired kernel.)
- **T-D Judge.** Fresh-context adversarial Judge verifies the verbatim goal:
  reliability (fuzz/crash proven), scaling (concurrency + bounds + documented v2),
  throughput (benchmarked), seamless (real app uses it). No "but/except/mostly".

## Hard rules

1. **Grill design + implementation each tier** (fresh-context agent) — fix what it
   finds before committing. This caught a CRITICAL engine bug + a deploy-breaker
   already; keep doing it.
2. Every reliability claim = the three-leg stool (runtime test + fault/fuzz +
   real-app e2e), not one leg.
3. Narrow gate per change; full engine test + `-race` + benchmarks at tier
   boundaries. Commit per tier; push at tier boundaries.
4. No-deferral: a real bug the fuzz/grill surfaces is fixed at root cause.
5. Honesty: v1 = embedded. Distributed scaling is v2 with a documented path — do
   not claim horizontal scale that isn't built.

## Progress ledger

- [x] T-A Reliability — crash-consistency FUZZ harness (random ops + torn-tail
      crash-append + concurrent disjoint-keyspace → reopen matches oracle),
      MaxValueBytes + MaxKeys/ErrFull guards, ForEach snapshot-under-short-lock.
      GRILLED: fixed F1 (fuzz did clean-shutdown not crash → added torn-tail
      append), F2 (serial → batch always 1 → added concurrent fuzz), F4/F5/F6.
      -race clean.
- [x] T-B Throughput — benchmarks (bench_test.go): cached read ~8.7M/sec;
      DURABLE writes SCALE with concurrency via group commit (4→326 writes/fsync,
      ~0.8k→~51k/sec); NoSync ~319k/sec. Measured numbers in capacity.md
      (honest, macOS F_FULLFSYNC floor).
- [ ] T-C Seamless — Std.BlueDB app-data store (or documented if kernel too large)
- [ ] T-D Judge — verbatim-goal verification

Prior (done, grilled): engine core + snapshot/recovery + session store +
app-level opt-in (autoBlueDB / [bluedb] / [live] store). See [[bluedb_exp]].
