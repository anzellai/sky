# Build speed: measurements behind the deferred levers

These files are the measurements that `docs/perf/build-speed-decisions.md`
cites. They were taken during the v0.27.9 build-speed work on 2026-10-15, on a
16 GB Apple silicon Mac (8 CPUs), go1.26.1. They are copies of the working
notes and raw outputs of each work stream. A results file sometimes names a
log or a script that stayed in the session's scratch directory; those logs are
not in the repository.

## `go-compile-bench/`

Raw output of `go build -gcflags='sky-app=-c=<C> -bench=<file>'` for the
generated app's package `main`.

- `caf-before-backend-<N>-c<C>.txt`: the native backend of a generated Sky.Spa
  app (genwhole shape from `examples/62-app-notes`) at N = 100 and N = 200,
  before the CAF-initialiser fix, at `-c=1` and `-c=8`. At N = 200, `-c=8`,
  `be:compilefuncs` is 81.50 % of 54.3 s with 100,681 functions.
- `genwhole-<leg>-<base|new>-200-c8.txt`: frontend (wasm) and backend legs at
  N = 200, before (`base`) and after (`new`) the generic-shell fix to
  `rt.Coerce`.

## `streams/`

- `architect-PLAN.md`: the architect's plan, including Phase 2 (per-module Go
  packages) and Phase 3 (parallel front end, stdlib world snapshot,
  interface-hash cache).
- `grill-safety.md`, `grill-effectiveness.md`: the adversarial reviews of
  that plan (M6, M7, M8 are the Phase 2 and Phase 3b risks).
- `caf-outline-RESULTS.md`, `caf-outline-results.tsv`: the CAF-initialiser
  fix, with the compiled-function counts before and after.
- `wasm-generic-shell-RESULTS.md`: the wasm frontend compile profile, before
  and after the `rt.Coerce` generic-shell fix.
- `shared-types-RESULTS.md`, `unifier-copies-RESULTS.md`: the type-table and
  unifier work, with the counters of the still-ignored ratchet
  `unifier_work_is_linear_in_record_size`.
- `unifier-linear-PLAN.md`: the ground-stamp design for a linear unifier (not
  built).
- `front-end-once-RESULTS.md`: running the front end once per direct Sky.Spa
  build, and the two loads that remain on the `--target web:app` path.
