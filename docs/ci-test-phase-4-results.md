# CI/test overhaul — Phase 4 results (Layer 1, the combinatorial corpus)

Companion to [`ci-test-architecture-v2.md`](ci-test-architecture-v2.md) (the
design) and [`ci-test-phase-2-3-results.md`](ci-test-phase-2-3-results.md) (the
cost measurement Phase 4 is built on). This records what happened when v2 §3's
Layer 1 met the compiler.

Measured 2026-08-10 on the dev host (macOS), release build,
`env -u CARGO_TARGET_DIR`.

---

## 1. The headline

| | |
|---|---|
| `N_min` (computed by the generator, not chosen) | **206** cases |
| `N_iso` (forbidden from batching) | **44**, against a declared ceiling of 44 |
| Class V (independently verifiable) | **206 / 206** |
| Class D (change-detector) | **0** |
| Full-corpus wall-clock | **103.3 s** at 4 workers (**0.50 s/case**) |
| Unexpected red | **0** |
| **Blocked-red (a LIVE product defect this corpus found)** | **1** |

**The spike's red rate: 6 % on the first honest run, and every one of the six was
a generator artefact.** Chasing them found a real documentation defect. The
corpus then found a real *compiler* defect — a runtime panic from code that
`sky build` calls "Types OK" — but only after the generator was corrected on a
point v2 had stated and the first cut had ignored.

---

## 2. The spike (v2 §2.3 / §3.5)

Mandatory before committing to counts. Three runs, because the first two measured
the harness rather than the compiler — worth recording, since both failure modes
would have produced a confidently wrong number.

| Run | Red rate | What it actually measured |
|---|---|---|
| 1 | **100 %** (100/100) | Cases were materialised under `.skycache/`, i.e. INSIDE the repo. `sky`'s project discovery walks up to the nearest ancestor holding `sky-stdlib/` + `runtime-go/`, so every case resolved its project root to the REPO root and reported `no .sky under src/`. Zero compiler signal. |
| 2 | **6 %** (6/100) | All six in `type_nesting`, all `elem = string`. Two generator bugs: `Result.withDefault` used against `String.toInt`, and a character-level `replace('e', "i")` that mangled `Result.withDefault` into `Risult.withDifault`. |
| 3 | **0 %** (0/24, then 0/202 over the full corpus) | The compiler. |

**A 100 % red rate is not a finding about the product.** It is worth stating
plainly because the honest-looking move at run 1 would have been to report "the
generator finds reds everywhere", and the honest-looking move at run 2 would have
been to narrow the generator. v2 §2.3 forbids the second; the first is just
wrong.

### What run 2 found: a live documentation defect

`String.toInt` is `String -> Maybe Int` (verified: `sky doc Sky.Core.String`).
**Four documents said `String -> Result Error Int`**, including the agent-facing
source of truth:

| Site | Defect |
|---|---|
| `AGENTS.md:79` | wrong signature in the effect-boundary legend |
| `docs/learn/09-effects-and-task.md:16` | same legend, same error |
| `docs/learn/18-coming-from-other-languages.md:43` | same legend, same error |
| `docs/learn/07-maybe-and-result.md:41-47` | **an example that cannot compile** — `case String.toInt text of / Ok n -> … / Err _ -> …` pattern-matches `Ok`/`Err` on a `Maybe` |
| `docs/learn/07-maybe-and-result.md:63` | `Result.map … (String.toInt "21") -- Ok 42` — `Result.map` over a `Maybe` |

This is why the generator wrote the wrong code: it followed the documentation.

**The live-docs gate does not catch these.** `scripts/doc-examples.sh` only
`sky check`s full-module examples (those with a `module Main …` header); every
FRAGMENT is unverified. Two uncompilable fragments sat in the learning path.
All five sites are fixed in this phase; the fragment-coverage gap is reported,
not fixed.

---

## 3. The corpus as built

Axes are mined from this repository's defect history — each is the dimension a
real bug moved along (`rust/crates/xtask/src/corpus/axes.rs`).

| Stratum | Axes (full cross) | Cases | Isolation | Mined from |
|---|---|---|---|---|
| `record_update` | position × annotation × row × carrier | 96 | batch | #166, #171 |
| `type_nesting` | outer × inner × elem | 36 | batch | #173 |
| `destructure` | erasure × position | 30 | batch | #170 / #172 |
| `fieldset_collision` | collision × erasure | 20 | **unit** | `goty.rs` fieldset collision |
| `import_shape` | import_shape × collision | 20 | **unit** | #164 |
| `fieldset_ctor` | construction × collider | 4 | **unit** | *added by this phase — see §5* |

`N_min` is **computed and printed**, never chosen: it is the sum of the
full-cross strata, and each pinned coordinate's distance-1 neighbourhood is a
subset of its own stratum's cross (asserted by
`corpus::tests::every_pinned_neighbourhood_is_covered`).

### Which cases carry a real oracle

**All 206 are class V.** The generator *constructs* the expected value before any
compiler runs: it writes `42` into a field the case never updates and asserts it
reads back as `42`; it writes `7` into the updated field and asserts `7`. Neither
number is observed. That is precisely the assertion that catches #166/#171 (an
un-updated field silently zeroed) and the fieldset collision (a field read
resolving against the wrong struct).

**No case is a change-detector**, so nothing is excluded from the coverage
number on class-D grounds. This is a consequence of scope, not virtue: the six
strata were chosen because each admits a constructible answer. Families that do
not — and v2 §4.4's class-D bucket exists for them — are not yet built.

### Value is the oracle; emitted Go is the axis witness

For most of these axes the value is deliberately axis-INVARIANT — that is the
property under test (moving a record update into a tuple must not change what it
computes). So the value cannot also witness the axis. The `corpus-witness` gate
(v2 §4.4) builds each case AND its axis-neutralised twin and requires the
**emitted Go to differ**. A case whose Go is byte-identical to its twin did not
reach the compiler along the axis it claims to vary, and fails by name.

### What the witness gate caught: `import_shape` does not cover #164

On its first honest run the gate reported **11 of 16 sharded cases NOT
WITNESSED**, all `import_shape`. Every one of that stratum's 20 cases emits
**byte-identical Go** across `plain` / `aliased` / `alias_not_last_segment` /
`exposing_list` / `exposing_all`.

**That is the compiler being right.** Import syntax is erased by name
resolution; two spellings of the same import must produce the same program. So
the emit-shape witness cannot apply, and the stratum is **exempt** — explicitly,
counted, and with the reason printed on every run (v2 §5.5: *"Exemptions are
explicit, counted, and owned"*).

**The honest consequence is a real weakness in the generator, not in the
compiler.** The `collision` axis is **inert**: its non-`none` values add an
unrelated local binding (`answer2`, `label2`) that collides with nothing, so no
case ever creates the name conflict #164 was about. These 20 cases still carry a
genuine class-V value assertion — the imported `answer` must read back as 42,
which does prove the import resolved to the right symbol — but

> **`import_shape` must NOT be counted as covering the #164 defect class until
> the `collision` axis actually collides.**

That correction is the witness requirement earning its cost on its first run:
without it, 20 cases would have been counted as covering a defect class they
never touch, and the coverage percentage would have been unfalsifiable in exactly
the way v2 §4.4 warns about.

Corrected accounting: **206 cases carry a class-V value assertion; 145 are
subject to the emit-shape witness; 20 (`import_shape`) are exempt with a stated
reason and a known coverage gap.**

---

## 4. The isolation gate (v2 §3.2), and a premise that did not reproduce

`corpus-isolation` runs a rotating sample in three configurations — alone,
in-batch, and in a **shuffled** batch (module order perturbed, because the
TEA-Model heuristic depends on `(module, name)` order).

```
CORPUS ISOLATION GATE — v2 §3.2 (alone / in-batch / shuffled)
  batchable cases : 162
  sample          : 24 (seed offset 22, rotates with the commit sha)
  alone      : 24 values
  in-batch   : 24 values
  shuffled   : 24 values
ISOLATION GATE: PASS (24 cases, identical verdicts alone / in-batch / shuffled)
```

### v2 premise that did not reproduce

v2 §3.2 states, from source, that batching is not semantics-preserving:

> Batch N TEA-shaped cases into one compilation unit and **N−1 of them resolve
> their subset records against the wrong Model.**

`xtask corpus --prove-isolation-needed` runs that experiment — it force-batches
the `isolation = unit` families and compares against their alone verdicts:

```
  forbidden-family cases with a batchable body : 20
  diverged when batched : 0/20
```

**Batching the forbidden families changed no verdict on this compiler.** The
source-level hazard is real and correctly cited (`lower.rs:246-266` builds
`record_fieldsets` over the whole compilation; `:256-258` documents the
collision), but the predicted *observable* consequence did not materialise for
these cases. Reported as measured rather than as assumed. The `unit` marking is
kept — the hazard is structural and the probe is a sample, not a proof of
absence — but it should not be described as demonstrated until something
demonstrates it.

---

## 5. The defect the corpus found

**A ten-line Sky program that `sky build` accepts and that panics at runtime.**

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)


type alias Kv =
    { key : String, value : String }


mk : String -> String -> Kv
mk k v =
    { key = k, value = v }


main =
    println (mk "a" "42").value
```

```
   Types OK (1 module(s))
Sky lowering succeeded
Build complete, running...
Sky panic: CoerceFailure
  panicMsg=rt.Coerce: expected rt.SkyADT, got string (42)
  main.Main_mk(...) at sky-out/main.go
```

**Mechanism.** `record_fieldsets` (`rust/crates/lower/src/lower.rs:246-266`) is
keyed on the **sorted field-NAME vector**; its own comment at `:256-258` records
that records with identical field names and different field types collide there.
The stdlib contains

```
Std.Analytics.EventProp = { key : String, value : PropValue }
    (sky-stdlib/Std/Analytics.sky:85-88)
```

so a user record `{ key : String, value : String }` lands on the same
`[key, value]` key. At the construction site inside an annotated function the
wrong candidate is selected and the `value` parameter is coerced to `PropValue`,
an ADT.

**Why it is severe.**

* **No import of `Std.Analytics` is needed.** The stdlib is always in the
  compilation, so any app defining a `{ key, value }` record with a constructor
  function is affected — and `{ key, value }` is an extremely common shape.
* **No erasure is needed.** The previously-recorded form of this bug (2026-08-03)
  was characterised as requiring `fst`/`snd`/tuple destructure to erase the field
  types. It does not: a plain annotated constructor is sufficient. This is a
  strictly broader class than previously recorded.
* It violates two non-negotiable rules simultaneously: *"if it compiles, it
  works"* and *"no runtime panic from well-typed Sky code"*.

**The twins isolate it.** The `fieldset_ctor` stratum is a 2×2 cross and exactly
one cell is red:

| | `construction = inline` | `construction = via_ctor_fn` |
|---|---|---|
| `collider = local` (`gkey`/`gvalue`) | green | green |
| `collider = stdlib_eventprop` (`key`/`value`) | green | **RED** |

Both neutralised twins pass, which is what proves the collision is the cause
rather than an unrelated error. This is v2 §4.4's paired-twin requirement doing
the work it was specified for.

**Landing.** `BLOCKED`, per v2 §7.2's contract applied per case
(`corpus::gen::blocked_reason`): the case **still runs**, it **never contributes
PASS**, its transition to green is reported, and it **FAILS the gate after
2026-11-10**. It is deliberately not a skip — "SKIP counted as pass" is one of
the defects this overhaul exists to remove. Repro is checked in at
`corpus/repro/fieldset-ctor-stdlib-collision.sky`.

### Why the first cut of the generator missed it

v2 §3.1 says, correctly:

> a generated hostile module graph that collides against *fictional* stdlib names
> cannot reproduce #164 or the fieldset collision, both of which required **real
> stdlib names in scope**.

The first `fieldset_collision` stratum collided two LOCAL aliases against each
other and passed 20/20. Colliding against the REAL `Std.Analytics.EventProp`
found the bug immediately. Two axes were missing, and both are now present:

* **`collider`** — `local` vs `stdlib_eventprop`, i.e. what the fieldset collides
  WITH.
* **`construction`** — `inline` vs `via_ctor_fn`, i.e. the construction SITE.
  The defect lives in the constructor's parameter coercion; an inline literal
  carries its types with it and never takes that path.

This is the mandate's own thesis demonstrated on the corpus itself: *the simple
case compiles clean, one axis changes, and it breaks* — including the part where
the axis nobody thought of is the one that finds the bug.

---

## 6. Denominators (v2 §5)

One committed script, `xtask denominators`, produces every denominator and writes
`docs/coverage/denominators.json`. Measured independently twice (HEAD-built
binary and installed v0.19.13) with identical results:

| | Measured | v2 as written |
|---|---|---|
| stdlib entries | **1,746** | 1,744 |
| modules | **87** | 87 |
| values | **1,625** | 1,623 |
| types | **121** | 121 |
| `SyntaxKind::KINDS` | **124** (80 constructs / 44 non-constructs) | 124 |

**v2's own quoted denominator was already stale by 2 values** — which is the
failure mode §5.3 exists to end ("no document, ledger, or verdict may quote a
number this script did not produce"). v2 §5.1 is corrected in the same commit
that made it checkable.

Confirmed by measurement: the 6 `exposing (..)` modules contribute **593 entries,
34.0 %** of the denominator unfiltered — matching v2 §5.2's estimate exactly.

Five silent-shrink paths are closed (v2 named four; a fifth was found at
`doc.rs:188`, where an unreadable file DEGRADED to an empty module via
`unwrap_or_default()` rather than being skipped). `sky doc --export` now fails on
a parse error, a header-less module, or an unreadable module.

---

## 7. Reject-face reconciliation (v2 §1.5, the Phase-0 obligation)

Both faces now call ONE declared criterion (`ty::reject_corpus`), use ONE
recursive discovery, and assert an EXACT corpus count (63) instead of `>= 13`.

**The live defect was not the one v2 led with.** v2 emphasised the parse-severity
divergence and the flat-vs-recursive discovery split. Measured at this commit,
both are **latent, not live**: the `syntax` crate has exactly one diagnostic
construction site and it is unconditionally `Error` severity
(`parser.rs:232`), and 0 of 63 corpus files sit in subdirectories. Neither was
mislabelling anything.

What WAS live: **neither face asserted a diagnostic code**, so any of the 63
files could have been passing for the wrong reason, and the `>= 13` floor against
an actual 63 meant deleting 50 corpus files kept both faces green.

Asserting codes for the first time surfaced 2 apparent mismatches, and the
resolution is a category distinction worth recording: the `-- oracle: reject
[CODE]` header documents what the **Haskell oracle** does, not what **Rust**
emits. Rust's `E2007` (a dedicated arity diagnostic) is *more specific* than the
oracle's generic `E2001` unify clash — Rust is better there, and the assertion
must not punish it. The corpus already carried a `-- rust: reject [CODE]`
convention (used in 1 file); it is now the precedence rule, and the two headers
coexisting IS the record of the divergence.

---

## 8. A harness defect found while verifying this

`--verify-falsifiers` applies its textual mutation and re-runs the gate **without
rebuilding**. Every gate registered before this phase happened to mutate a DATA
file (a corpus `.sky`, an example, a test suite), so the hole never showed. The
Layer-1 gates are driven by generator logic in Rust, where a source mutation is a
silent no-op — and a no-op mutation reports `VACUOUS`, which is
indistinguishable from a gate whose assertion is genuinely dead.

v2 §7.5 had already costed falsification at *"~13–24 s cold build per
mutation"* — the design assumed this rebuild; the implementation had not done it.

Fixed: a mutation whose target ends in `.rs` triggers a bounded
`cargo build --release -p xtask` before the mutated run, and **another after the
revert** — because reverting the file is not enough once a mutation can reach the
compiled image; the artefact outlives the patch.

---

## 9. The gate set, and every new gate's proof that it can fail

The harness registry goes from **7 gates to 12**. `shared-world` was deliberately
left unregistered by Phase 3 so as not to change the five-gate set that phase was
verified against; registering it is a Phase-4 step, taken now that the
incremental world is load-bearing for the corpus.

| Gate | Tier | Assertions | Wall-clock | Mutation | Outcome |
|---|---|---|---|---|---|
| `shared-world` | T1 | 121 | 4.9 s | route the shared path through the deliberately-wrong check that skips the case's body-derived passes | **PROVEN** — 18/121 diverge |
| `corpus-manifest` | T1 | 206 | 0.0 s | alter the checked-in manifest so it no longer matches the generator | **PROVEN** |
| `corpus` | T2 | 206 | 95.4 s | corrupt the EXPECTED value the generator constructs, leaving the program correct | **PROVEN** |
| `corpus-isolation` | T2 | 24 | 36.8 s | make the batched build report a different value than the alone build | **PROVEN** |
| `corpus-witness` | T2 | 16 | 44.6 s | build the "neutralised twin" from the case's OWN axes, so the fingerprints are identical by construction | **PROVEN** |

Added to T1: **4.9 s**. The whole new T2 tier: **177 s** (`HARNESS VERDICT: PASS`).

The canary reports `VACUOUS`, as a correct runner must.

### The witness gate's own first run was INCONCLUSIVE, and that was the harness working

Adding the `fieldset_ctor` stratum without a matching entry in the witness gate's
`axis_under_test` table made the gate body **panic**. The harness did the right
thing at every step: the panic was caught, the baseline was red, and the
falsifier reported **INCONCLUSIVE** rather than a false `PROVEN` — "both sides
failing proves nothing" (v2 §4.2) working exactly as specified. A gate that
cannot establish a verdict did not pass.

It is nonetheless a `cargo test` failure, not a gate-runtime discovery, so
`witness::tests::every_stratum_declares_an_axis_under_test_with_a_valid_neutral`
now asserts the table is total over `STRATA` and that each neutral is a real
value of its axis. Re-run after the fix: **PROVEN**.

### Two pre-existing gates are INCONCLUSIVE in a fresh worktree

`verify-cli` and `sky-verify` report `INCONCLUSIVE — baseline is red` here,
because a fresh worktree has no built examples and no `sky-out/sky`. This is
environmental and pre-existing, not introduced by this phase (the same two are
red in the Phase-4 baseline harness run at `68b16742`). It is worth recording
because it means **the falsifier set cannot be fully verified from a clean
checkout** — those two gates need a provisioned tree, and the harness correctly
refuses to claim anything about them without one.

---

## 10. Reproducing all of it

```bash
cd rust && env -u CARGO_TARGET_DIR cargo build --release -p xtask -p sky

xtask corpus --spike=100              # the red-rate spike
xtask corpus --run                    # the full corpus, built and run
xtask corpus --isolation              # alone / in-batch / shuffled
xtask corpus --prove-isolation-needed # the necessity experiment
xtask corpus --witness                # the axis-witness gate
xtask corpus --check-manifest         # membership authority
xtask denominators                    # every denominator, one script
xtask shared-world                    # the differential (now a registered gate)

xtask harness --verify-falsifiers --only shared-world,corpus-manifest
xtask harness --verify-falsifiers --tier t2
```
