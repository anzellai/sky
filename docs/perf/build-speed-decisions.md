# Build speed: levers deferred from v0.27.9

v0.27.9 makes builds faster on every target (see the v0.27.9 entry in
`CHANGELOG.md` and `docs/perf/runs/build-speed-final-20261015/`). Five further
levers were studied during that work and not shipped. The user chose to ship
v0.27.9 with the work done and to record why each of these waits. This file is
that record.

Each section says what the lever is, what was measured, what it is expected to
gain, its risk, why it waits, and what would make it worth doing.

**Measured** means a run produced the number, and the section names the run.
**Estimate** means reasoning from measured numbers, not a run of the change
itself. An estimate is not a measurement. No lever below has been built, so
every gain below is an estimate.

The measurement files are in `docs/perf/runs/build-speed-deferred-20261015/`
(the "deferred run dir" below), `docs/perf/runs/build-speed-final-20261015/`
and `docs/perf/runs/medium-webapp-20261015/`. The machine for all of them is a
16 GB Apple silicon Mac, 8 CPUs, go1.26.1.

## Where a build spends its time now

- The `xtask bench-gen` app at N = 200, `--target web:app`: a one-module edit
  rebuild takes 12.8 s, and `precompress brotli (max)` is 10.0 s of it (rep 1,
  `build-speed-final-20261015/README.md`).
- A medium production web:app (a private project, names withheld): a rebuild
  of unchanged source at `5d30f9127` took 21.8 s in total, with `go build` and
  brotli both cache hits. Parent type check 1.99 s, parent lowering 1.80 s,
  split 3.33 s, frontend leg 5.79 s, backend leg 8.15 s, peak 1.25 GB
  (`medium-webapp-20261015/README.md`). So on that app, with the Go and
  brotli caches warm, almost all of the time is in Sky's own Rust front end,
  lowering and split, in the parent and in each leg.

## (a) One Go package per Sky module (plan Phase 2)

**What it is.** Today the emitted program is one Go `package main`. Phase 2
would emit one Go package per Sky module (`sky-app/sky/m/<prefix>`), keep the
entry module and the registration `init()`s in `main`, and dot-import between
module packages. After an edit, Go would recompile the changed module's
package, the packages that import it, and `main`, instead of the whole
program. The design is in `docs/history/build-speed-v0.27.9/streams/architect-PLAN.md` ("Phase 2") in the
deferred run dir.

**What was measured.** The large cost that a split was meant to address turned
out to have other causes, and those are fixed in v0.27.9:

- Backend package `main` of a generated Sky.Spa app at N = 200 (200 Model
  fields), before the CAF fix: 100,681 compiled functions, `be:compilefuncs`
  81.50 % of 54.3 s at `-c=8`
  (`go-compile-bench/caf-before-backend-200-c8.txt`). The Go inliner copied
  each CAF initialiser, with all its closures, into every reader. After the
  fix: 13,123 functions, 4.62 s, 1,111 MB (`docs/history/build-speed-v0.27.9/streams/caf-outline-RESULTS.md`).
- Wasm frontend package `main` of a generated app at N = 200 (400 Model
  fields, 400 RPCs), before the `rt.Coerce` generic-shell fix: 13.65 s at
  `-c=8`, 4,327 MB; after: 3.70 s, 1,055 MB, with `compilefuncs` 2.05 s for
  19,753 functions (`docs/history/build-speed-v0.27.9/streams/wasm-generic-shell-RESULTS.md`,
  `go-compile-bench/genwhole-fe-*-200-c8.txt`). After the fix no class of
  function dominates. The largest single functions are `Main_update` (0.39 s)
  and `Main_view` (0.33 s), and they compile in parallel with the rest at
  `-c=8`.
- On the `bench-gen` app at N = 200, package `main` compiles in 1.13 s
  (frontend) and 2.03 s (backend) at `-c=8` (same results file, section 4).

**Expected gain: estimate.** A split saves, per edit, at most the part of the
package-`main` compile that belongs to unchanged modules. On the measured
shapes that whole compile is now 1.1 to 4.6 s per leg, so the saving is at
most a few seconds per leg on an edit, and less on a cold build (Go already
compiles one package's functions in parallel with `-c`, and a cold build does
the same total work). This bound comes from the measured package-`main`
times above. The split itself was not built or measured.

**Risk.** High, and it reaches running apps.

- The runtime reads Go type and function names in many places: gob
  registration of session state, the Msg ADT key, wire codecs, handler
  identity, profile demangling, and `%T` in 18 files
  (`docs/history/build-speed-v0.27.9/streams/architect-PLAN.md`, correction 4). A missed site changes
  behaviour at run time, not at build time. A mixed naming of gob types
  panics inside `tryGobRegisterVal`, the panic is swallowed, and session saves
  fail (`docs/history/build-speed-v0.27.9/streams/grill-safety.md`, M6).
- Session data written by an app built with one layout must decode in an app
  built with the other, across a deploy.
- Synthesised items (eta and coerce adapters, lifted lambdas, codec code that
  names user types) have no owning module, so a naive split can create Go
  import cycles (`grill-safety.md`, M7).
- Closure names used by handler identity can shift with inlining.

**Why deferred.** The super-linear Go compile costs it was aimed at are fixed
by smaller, contained changes in v0.27.9. What remains for package `main` is a
few seconds, and the risk falls on session state and event dispatch in
deployed apps.

**What would trigger it.** A measured app where, with the deps warm, the
package-`main` compile of an edit rebuild is the largest stage of that
rebuild. The first step would then be Phase 2a from the plan: route every
runtime name read through one function (`rt/sky_names.go`) with a source-scan
test, which is safe on today's single-package layout.

## (b) Parallel per-module front end and a stdlib world snapshot (plan Phase 3b, 3c, 3d)

**What it is.**

- 3b: after the world is built, type-check modules in parallel, then join the
  results in a fixed order.
- 3c: cache the checked standard-library world on disk and load it in each leg
  child, instead of building it again in every `sky` process of a build.
- 3d: an interface-hash cache that skips re-checking a module whose inputs did
  not change.

**What was measured.**

- The medium web:app at `5d30f9127`: with `go build` and brotli cache hits,
  the 21.8 s rebuild is almost all Rust front end, lowering and split (see
  above). How that time divides across modules was not measured.
- The generated Sky.Spa backend (`unifier-copies-RESULTS.md`): type check
  0.50, 1.73 and 6.65 s and lowering 0.67, 2.27 and 8.66 s at 100, 200 and 400
  whole-model arms. A `sample` profile of the 400-arm leg put 1.4 k of 12.5 k
  samples in the check world build, which is the work 3c would cache.
- The effectiveness review found that on the generated Sky.Spa shapes the cost
  is concentrated in one or two modules (the generated `Shared` module and the
  module with `update`), which per-module parallelism does not split
  (`docs/history/build-speed-v0.27.9/streams/grill-effectiveness.md`).
- The `bench-gen` app at N = 200: the parent's front half takes 0.28 s
  (`docs/history/build-speed-v0.27.9/streams/front-end-once-RESULTS.md`).

**Expected gain: estimate.** 3b gains only as far as independent modules carry
the cost. Where one module carries most of it, as in the measured generated
shapes, the gain is small. 3c saves the world build in each leg child; from
the profile above that is in the order of a tenth of a leg's front-end time,
but no leg was timed with a snapshot. 3d gains on edit rebuilds in proportion
to the modules that did not change, which was not measured. None of these
figures is a measurement of the change.

**Risk.** Medium to high, all of it on correctness of the checker's output.

- Determinism: `DefKey` is interned by salsa and exported as `DefId`; lowering
  iterates `BTreeMap<DefId, _>`; the alias fixpoint depends on order. Parallel
  work that interns in a different order changes emitted names and, through
  them, emitted Go (`grill-safety.md`, M8).
- `Rc` to `Arc` across `skydb` and `ty`.
- A snapshot or interface cache keyed on too little serves a stale world: a
  poisoned cache would type-check a program against the wrong stdlib
  (`grill-safety.md`, N4). The plan requires a differential gate (cached ==
  clean) and a poisoned-cache falsifier for each.

**Why deferred.** The measured cost on the generated shapes sits in one or two
modules, which 3b does not reach. The real app's front-end time is now
seconds, not minutes, and how it divides across modules is not measured.

**What would trigger it.** A per-module timing of the medium web:app (or any
real app) showing several modules each with a large share of the type check,
for 3b; or a measured leg where the stdlib world build is a large share, for
3c. 3d comes last and only if 3b and 3c are not enough.

## (c) A linear unifier (ground-stamp design) and the ignored ratchet

**What it is.** Two costs in the unifier still grow faster than the size of
the record: the occurs check walks the whole structure a variable is bound
to, and each call that passes the Model to an annotated function instantiates
the declared Model record and unifies it field by field. The design in
`docs/history/build-speed-v0.27.9/streams/unifier-linear-PLAN.md` gives each union-find root a ground stamp, so
the occurs check skips classes known to hold no variables, and instantiates
ground types lazily. The ratchet test that would hold it is
`unifier_work_is_linear_in_record_size` in
`rust/crates/skydb/tests/typed_table_growth.rs` (around line 184). It is
`#[ignore]`d because it fails today.

**What was measured** (`docs/history/build-speed-v0.27.9/streams/unifier-copies-RESULTS.md`).

- Ratchet ratios per doubling of record size (2.0 is linear): `codec_chain`
  occurs 3.92 / 3.92 and record_fields 3.85 / 3.91 (type check / lowering);
  `model_app` lowering occurs 3.73 and record_fields 3.62. `model_app` type
  check is already linear (2.00).
- Generated Sky.Spa backend: type check 0.50 → 1.73 → 6.65 s at 100 → 200 →
  400 arms, still super-linear. A 1,200-field record codec type-checks in
  0.41 s.
- The medium web:app after the harvest fix: parent type check 1.99 s, parent
  lowering 1.80 s (`medium-webapp-20261015/README.md`). An earlier profile of
  that app, before the harvest fix, put most of its time in unification
  called from the call-site harvest. The harvest fix removed that work. The
  unifier's share after the fix was not measured.

**Expected gain: estimate.** The plan estimated the 400-arm type check would
fall from 9.35 s to about 3 s. That estimate was made before the stream U
changes, which already took it to 6.65 s, and the design was not built. On
the medium web:app the upper bound is the type check and lowering time
itself (1.99 s and 1.80 s in the parent, plus the legs' share), not a
measured saving.

**Risk.** High. It changes the core of type inference. Errors, the names of
type variables in messages, obligations and read-back must stay identical.
The design rests on four invariants (I1 to I4 in the plan), including state
left behind by a failed unification, the Error arm that replaces content, and
the App arm that renames a class. A stamp that is wrong in one of those cases
lets an occurs check pass that should fail, which is a soundness defect, not
a speed defect. The plan requires a differential mode (every skip also runs
the full walk and asserts the same answer) over the stdlib, every example and
the corpora.

**Why deferred.** After the v0.27.9 type-table and unifier changes, type check
and lowering of the measured real app take about 2 s each. The remaining
super-linear growth shows on generated shapes of several hundred Model
fields. The change carries soundness risk in the checker's core, and the
release does not need it.

**What would trigger it.** A real app where type check or lowering is the
largest stage of a build, or a Model of several hundred fields in real use.
The gate would be to un-ignore `unifier_work_is_linear_in_record_size`.

## (d) Two front-end loads on the `--target web:app` path

**What it is.** A direct `Spa.app` build now loads and checks the program once.
A `--target web:app` build of a `Std.App` entry still loads and checks twice:
once for the user's entry, once for the derived client entry synthesised from
it, because they are two programs.

**What was measured** (`docs/history/build-speed-v0.27.9/streams/front-end-once-RESULTS.md`, median of 3 warm
runs).

| App | Path | Split step | Whole build | Loads, checks |
|---|---|---|---|---|
| genwhole 200 | direct `Spa.app`, before | 0.62 s | 4.70 s | 2, 2 |
| genwhole 200 | direct `Spa.app`, after | 0.15 s | 4.20 s | 1, 1 |
| genwhole 200 | `--target web:app` | 0.62 s | 4.79 s | 2, 2 |
| bench-gen 200 | `--target web:app` | 0.20 s | 1.63 s | 2, 2 |

On the medium web:app the split step took 3.33 s of the 21.8 s rebuild. How
much of that is the second load was not measured.

**Expected gain: estimate.** On the generated shapes, the same change as on the
direct path: about 0.5 s on the 4.8 s genwhole build, taken from the direct
path's measured saving. On the medium web:app, part of the 3.33 s split step;
the size of that part was not measured.

**Risk.** Medium. The derived client entry is a different program from the
user's entry. Reusing the first check needs a proof that the synthesised
`Spa.app` checks to the same result, and diagnostics must stay byte for byte
the same (the direct-path change was verified over 111 diagnostic runs).

**Why deferred.** The measured saving on the generated shapes is about half a
second, and the medium app's share is not known.

**What would trigger it.** A measurement on the medium web:app (with
`--timings`, which prints `note: front end: N source load(s), M type
check(s)`) showing that the second load is a large part of its split step.

## (e) brotli `max` on edit rebuilds (a deploy-size policy)

**What it is.** `sky build` of a client target writes `.br` files at `max`
(quality 11) by default. `--precompress=fast|off` (or `SKY_PRECOMPRESS`) is
available, and `sky run` / `sky watch` write none. The deferred decision is
whether a plain `sky build` used for edit rebuilds should default to `fast`
and keep `max` for what is deployed.

**What was measured.**

- `bench-gen` app, N = 200: `precompress brotli (max)` is 10.0 s of a 12.8 s
  edit rebuild (`build-speed-final-20261015/README.md`).
- The same app on an earlier commit: an edit rebuild at `fast` took 7.5 s
  against 16.9 s at `max`, with a `.br` 16 % larger (v0.27.9 entry in
  `CHANGELOG.md`).
- A 52 MB Go wasm: `max` 26 s and 2.77 MB; `fast` about 1.5 s and 3.25 MB,
  18 % larger (`docs/tooling/cli.md`, `--precompress` table).
- The medium web:app at `58fec7490`: brotli 27.6 s of a 142.5 s build
  (`medium-webapp-20261015/README.md`).

**Expected gain: measured** for the bench app on an earlier commit (16.9 s →
7.5 s per edit rebuild). The gain on the final commit and on the medium app is
an estimate: most of the brotli time, since `fast` at 1.5 s replaces 26 s on a
52 MB wasm.

**Risk.** Low for the build, but it is a deploy-size policy. A `.br` 16 to 18 %
larger is downloaded by every user of a deployed app if a `fast` build is
deployed. A default that differs between edit and deploy needs Sky to know
which build is which, and a plain `sky build` is what most projects deploy
today.

**Why deferred.** Changing the default would make deployed clients larger
without the user asking. The flag already gives the fast loop to anyone who
wants it, the brotli cache means an unchanged wasm is never compressed again,
and the `max` work runs beside the backend leg when memory allows.

**What would trigger it.** A separate deploy step in Sky (for example a
release mode for `sky build`, as `sky package --release` is for native
targets), so that `max` can be tied to deploys and `fast` to edits without
changing what is deployed.
