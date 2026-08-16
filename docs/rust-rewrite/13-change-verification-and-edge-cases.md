# 13 — Change verification & the edge-case matrix

> **Read this before landing any change to `hir` (resolution), `ty`
> (inference/sigs), or `lower` (type-directed lowering / codegen).**
> It exists because the corpus gates are *necessary but not sufficient*:
> more than once, a change passed every gate and still regressed a real
> app. This doc catalogues the dimensions a type/resolution/codegen change
> must survive, and the verification protocol that actually catches the
> misses.

## 0. Why this exists — the failure mode

The gate suite (`infer` / `roundtrip` / `reject` / `repro` / `divergences`
/ `resolve` / `s8`) byte-matches against the oracle on ~50 curated
examples, and `build-run` go-builds **8 non-FFI** examples. That corpus
does **not** exercise every real-world pattern. Concretely, this session:

| Change | Passed | Regressed (corpus was GREEN) |
|---|---|---|
| #164 alias fix v1 (last-segment qualifier heuristic) | 40/40 oracle + its own test | skydeploy control-plane — `import Github.Api as Github` (alias ≠ last segment) |
| #164 follow-up (global `union_names` block) | synthetic repro | roovo's app — `type alias Event` collided with the stdlib `type Event` union in `Std.Html.Attributes` |
| #166 fix A (seed annotated params on the lowering path) | infer/roundtrip/reject/repro all PASS | `12-skyvote` + `16-skychess` — a `List (Dict String String)` field collapsed to `any` |
| #166 fix B (erase row-poly records to `any`) | both repros | same two Std.Db examples — over-erased a concrete `Model` sharing an instantiation row var |

The pattern is always the same: **the corpus lacks the specific
combination** (an import alias that isn't a path segment; a stdlib type
name colliding with an app type; a `Dict`-valued record field in a Std.Db
app). A synthetic repro that "matches the shape" is not enough — it
reproduces the shape you *thought of*, not the one the compiler actually
mishandles.

**Rule: a `ty`/`hir`/`lower` change is not "verified" until it has built
the FULL example sweep + at least one real app + the relevant dimensions
below. Green corpus gates are a pre-condition, not a conclusion.**

## 1. The edge-case matrix

A change should be reasoned about — and, where plausible, tested —
against every dimension it could touch. The bug column cites where each
case actually bit us, so the list stays honest rather than theoretical.

### D1 — Type-reference resolution (`hir`, `ty::sig`)

| Case | Why it's a trap | Bit us |
|---|---|---|
| Same-named `type alias` in two modules | bare-name keying conflates them | #164 |
| Same name as **alias in one module, union in another** | union stays a bare nominal; a same-named alias's expansion can capture it | #164 follow-up |
| Import alias where alias ≠ last path segment (`import A.B.C as X`) | last-segment heuristics break | #164 v1 (control-plane) |
| `exposing (T)` bare import | the bare name must resolve to the dep's `T` | #164 |
| **Stdlib** type sharing a name with an app type (`Event`, `Model`, `Msg`) | a global set/table keyed by bare name captures the stdlib decl | #164 follow-up |
| Kernel-implicit types (`Decoder`, `Value`, `Cmd`, `Route`…) | synthetic sentinel module — `module_name` panics if indexed | #164 (the `DefKind::TypeAlias` gate) |
| Nested module paths (`Page.A`, `Page.Sub`) | `[source] root = "."` + dotted module names | #164, #166 repros |
| Cross-module ADT constructor payload (`Active PageSub.SubModel`) | the variant payload's alias must resolve to the *other* module | #164 follow-up |
| Two bare imports binding the same auto-qualifier | E1001 collision path | import-qualifier rules |

**Guiding principle:** identity of a type comes from HIR's resolver
(`ResolveResult::type_refs` → `def_loc(con).module`), never a syntactic
heuristic or a global bare-name table.

### D2 — Record shapes & row polymorphism (`ty::infer`, `lower::goty`)

| Case | Why it's a trap | Bit us |
|---|---|---|
| Full closed record | baseline | — |
| Genuine row-poly (`bump r = { r | age = … }`) | must lower to `any` + reflective `rt.RecordUpdate` | existing mechanism |
| **Concrete record sharing an instantiation row var** (`Model` in param *and* result of an annotated fn) | looks row-poly to the shared-var heuristic but must stay the nominal `_R` | #166 fix B |
| Record with an **ADT/union field** | a dropped field is a nil interface → `case` panic (not silent) | #166 |
| Record with a **`Dict String String` field** (`map[string]string`) | must stay concrete; erasing to `any` breaks a downstream `List.head → Ok[…, Maybe Dict]` | #166 fixes A+B (skyvote) |
| Record with a **parametric-ADT field** (`Status a`) | the panic trigger in the reporter's app | #166 |
| **Record update on a param, returned in a tuple** (`( { model | f = v }, Cmd )`) | the row leaks → narrow struct drops un-updated fields | #166 (core) |
| Update of ALL fields vs a SUBSET | all-fields never drops (README counter — 1 field); subset is the bug surface | #166 |
| Nested-TEA sub-model inside a parent `AppModel` union variant | the real-app shape the minimal repro missed | #166 |

**Guiding principle:** a record update must preserve the base's full
record; a record only erases to `any` when it is *genuinely*
row-polymorphic AND its fields never flow to a concrete context — the
shared-row-var heuristic alone is NOT that proof.

### D3 — Annotation state (`ty::db` vs `ty::check`)

| Case | Why it's a trap |
|---|---|
| Fully annotated top-level def | the check path seeds params from the sig; the **lowering path does not** — the two infer differently (#166 root cause) |
| Unannotated def | body-only inference; row vars leak / generalise unpredictably |
| Bare-type-var annotation (`msg`, `a`) | gives no more than the inferred type; must not be treated as concrete |
| Head-position alias annotation (`view : Renderer Msg`) | alias must unfold at the head |

**Open architectural debt (surfaced by #166):** `ty::check` (accept/reject)
seeds annotated params from the signature; `ty::db::compute_body_types`
(the lowering feed) does not. So "what type-checks" and "what is emitted"
can diverge. Unifying them is the principled fix — but naive seeding
perturbs unrelated inference (skyvote). Any fix here MUST run §2 in full.

### D4 — Module & dependency structure

| Case | Bit us |
|---|---|
| Single module | — |
| Internal project modules (`src/**`) | #164/#166 |
| External deps (`.skydeps/<slug>/src/**`) | diagnostic-filename ModuleId shift |
| Same-named module in project AND a dep | diagnostic-filename test |
| Import alias + `exposing` in the same file | qualifier rules |

### D5 — App shape (runtime surface)

| Shape | Distinctive risk | Example |
|---|---|---|
| Sky.Cli | argparse, `Task.run` entry | `07-todo-cli`, `20-cli-counter` |
| Sky.Live | record updates in `update`, SSE, sessions | `09-live-counter`, `10-live-component`, `12-skyvote` |
| Sky.Tui | gob serialisation round-trip of the model | roovo's app |
| **Std.Db** | `Dict`-valued rows, typed decode, `SqlValue` | `12-skyvote`, `16-skychess`, `18-job-queue`, `36-composite-server` |
| Nested TEA | sub-page models in a parent union | roovo's app |

## 2. Verification protocol (MANDATORY for `hir`/`ty`/`lower` changes)

Run in order; do not claim "verified" until all pass.

1. **Targeted spec** proving the change (`cargo test -p <crate> <name>`).
2. **Corpus gates** — `cargo run -p xtask -- {infer,roundtrip,reject,repro,divergences,resolve,s8}`. Pre-condition only.
3. **FULL example sweep** — `scripts/example-sweep.sh` builds (and runs the
   runnable shapes for) **every** example under `examples/` from a wiped
   slate, and preserves skyshop's FFI cache. **Not** just `build-run`'s 8.
   This is the step that catches Std.Db/`Dict`/FFI regressions
   (`12-skyvote` / `16-skychess` were invisible to the corpus gates but
   fail this sweep immediately). Running the corpus gates without this
   sweep is the exact hole that let #166's fixes look "verified".
4. **Real-app builds** — the reporter's repro app (clone the branch they
   give) + at least one of `skydeploy/control-plane`, `sky-lang.org`.
   These exercise import aliases + large module graphs the corpus lacks.
5. If the change touches resolution or record types, walk the **D1/D2**
   rows above and add a case to the sweep app (below) for any not covered.

**`build-run`'s 8-example limit is a known hole.** Until it is widened to
FFI examples (requires `sky install` in CI), step 3 is done by hand /
script and is non-optional.

### 2a. Do not verify by tailing

`cargo test -p sky -p project 2>&1 | tail -40` is not a verification. Cargo
prints one summary block per test binary, so the tail shows the LAST few
binaries and nothing else — a crate whose unit tests live in the first binary
is invisible. An embedded-PostgreSQL grill round found this concretely: the
~70 `db_cluster.rs` unit tests are in `-p sky`'s first binary, and every
verdict reached by tailing had verified almost none of them while reporting
green.

The same trap applies to `go test ./rt/... | tail -N`, to `-p` runs whose
crate list you did not choose deliberately, and to any `| tail` on a command
that emits more than one PASS/FAIL summary.

Read the whole output, or ask for a machine-checkable verdict instead:

```bash
# the exit status is the verdict — no scrolling, no tail
cargo test -p sky -p project >/tmp/t.log 2>&1; echo "exit=$?"
grep -c '^test result:' /tmp/t.log      # how many binaries actually reported
grep '^test result:' /tmp/t.log         # and what each of them said
```

If the count of `test result:` lines is smaller than the number of test
binaries the crates own, the run did not cover what you think it did.

## 3. The sweep/kitchen-sink app (the durable test surface)

The most reliable guard is a single real app that combines the hard
patterns, so one `sky build` exercises most of the matrix. Target
contents (a Std.Db + nested-TEA + multi-module Sky.Live app):

- Two internal modules each exposing a same-named `type alias Model`
  (D1) + one importing module that ALSO defines `type Model` (D1) via
  an import alias whose name ≠ last segment (D1).
- A record with a `Dict String String` field, an ADT field, and a
  parametric-ADT field (`Status a`) (D2), updated field-by-field in an
  **annotated** and an **unannotated** helper returning `( model, Cmd )`
  (D2/D3).
- A Std.Db query returning `List (Dict String String)` consumed by
  `List.head → Ok[…, Maybe Dict]` (D5 — the skyvote shape).
- A cross-module ADT variant wrapping another module's record alias (D1).
- A stdlib-name collision: a user `type alias Event` alongside
  `Std.Html.Attributes.Event` (D1).

Building it clean = most of the matrix in one gate. When a new bug
arrives that this app doesn't reproduce, add the missing case to it —
that is how the corpus stops being insufficient over time.

## 4. Companion references

- `docs/rust-rewrite/11-testing-and-verification.md` — the gate suite.
- `docs/rust-rewrite/05-name-resolution.md` — D1 mechanism.
- `docs/rust-rewrite/06-type-system.md` — D2/D3 mechanism.
- `docs/rust-rewrite/07-lowering-and-ir.md` — the `goty` / row-poly erasure.
- CLAUDE.md §0.3 (architectural-mechanism citation) — the reasoning gate
  this doc operationalises for verification.
