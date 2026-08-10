# CI/test overhaul — Phase 2 and Phase 3 results

Companion to [`ci-test-architecture-v2.md`](ci-test-architecture-v2.md). v2 is the
design; this is what happened when it met the compiler. It records the measured
numbers, the three v2 premises that proved wrong, and the branch that fires at the
Phase 3 → Phase 4 boundary.

Measured 2026-08-10, on the dev host (macOS), release build,
`env -u CARGO_TARGET_DIR`. Every number below is reproducible with the commands
named beside it.

---

## 1. The headline

| | per case | vs baseline |
|---|---|---|
| Baseline (`xtask reject`, 63 cases in 75 s) | **1 190 ms** | — |
| After C-1 (`hir::resolve` memo) | **34.4 ms** | 34.6× |
| After C-1 + C-2 (shared world) | **1.02 ms** | **1 167×** |

`c_measured` = **1.02 ms/case**, fitted over five corpus sizes, R² = 1.00000.

Against v2 §1.3's break-even table:

| Cases | Break-even, 1 thread | Break-even, 4-way | `c_measured` | Verdict |
|---|---|---|---|---|
| 1,500 | 80 ms | 320 ms | 1.02 ms | under by 78× |
| 5,000 | 24 ms | 96 ms | 1.02 ms | **under by 23.5×** |

**The `PROCEED` branch of v2 §2.2 fires**, and not marginally. See §5.

---

## 2. What was built

### C-1 — `hir::resolve` memoised per module

`ty::sig::resolve_type_names` calls `db.resolve(m)` **once per type annotation and
once per alias body**, and `World::build_decls` runs it across all 87 stdlib
modules — thousands of full module resolutions per world build, of a pure
function. That is the 75.4 % the profile found.

The memo lives on `SourceDb` with the measurement quoted in its docstring, because
this memo was removed once before under the claim that resolution "never sat on a
hot loop".

**The invalidation rule is wider than v2 specified** — see §3.1.

Reproduce: `cargo test -p hir resolve_memo` (5 tests, including both
invalidation directions and a proof that invalidation is exact rather than a
blanket clear).

### C-2 — `World::build` admits an incremental module set

`World::build_decls` splits into `empty_seeded()` + `extend_decls(db, seed_static)`,
and passes 5–8 become `extend_bodies(db)`. `ty::shared::ScopedDb` narrows **only**
`module_ids()` and delegates every other query — the sig passes iterate
`module_ids()` to decide *what to process* but reach other modules through
`resolve` / `module_exports` / `classify_import` to decide *what things mean*, so
narrowing the first restricts the work without restricting visibility.

`check_modules_with_world` is the seam that lets the checker take an assembled
world rather than demand a fresh one.

Two constructions make a prebuilt world invalid for a case. Both are detected
**before forking** and fall back to a full rebuild as a **counted, reported**
state:

| Fallback | Why |
|---|---|
| `shadows-stdlib-module` | `add_module` overwrites on a name collision, so a case declaring `Std.Log` must not meet a world holding the real `Std.Log`'s declarations. The #164 axis. |
| `bare-alias-collision` | The bare `aliases` table is last-writer-wins and is complete (pass 1a) *before* any signature expands (pass 2), so a colliding case alias can change how a **stdlib** signature expands. A world whose stdlib pass 2 already ran cannot represent that. |

Over the combined reject + infer corpora, **1 of 121 items** falls back
(`36-composite-server`, bare-alias-collision), charged at the measured
34.35 ms/case isolated rate.

---

## 3. v2 premises that proved wrong

v2 had been grilled twice but never tested against a compiler. Three of its claims
did not survive contact.

### 3.1 The `resolve` memo's invalidation obligation is wider than "drop that entry"

v2 §1.3 C-1: *"`add_module` overwrites the parse when a name collides, so the memo
entry for that `ModuleId` must be dropped. That is a three-line invalidation and a
unit test."*

`resolve(m)` is a pure function of `m`'s parse **plus** `classify_import(p)` and
`module_exports(dep)` for each import path `p` in `m`. So there are two more
invalidation obligations, and dropping only the overwritten entry leaves both open:

1. **Overwriting a module invalidates its dependents**, not just itself — they
   resolved against the *old* parse's exports.
2. **Adding a NEW module invalidates modules that already resolved an import of
   that name as `Kernel` or `Foreign`**, because `classify_import` prefers a
   parsed dep. Those modules were never touched, and would keep serving a
   resolution built on the pre-registration classification.

Both are implemented and both have a test
(`overwrite_invalidates_dependents`, `new_module_invalidates_prior_foreign_importers`).
Invalidation is kept **exact** rather than a blanket `clear()` precisely so a fork
carrying memoised stdlib resolutions can have case modules appended without
discarding them — a blanket clear would have silently destroyed C-2's benefit.

### 3.2 The `corpus.defid-disjoint` gate is unsatisfiable, and disjointness is the wrong property

v2 §1.4(a) prescribes: *"run two consecutive cases that both declare `Main.main`;
assert the `DefId` sets they intern are **disjoint**."*

Measured: **forking does not produce disjoint `DefId`s, and cannot.**
`DefTable::intern` keys on `(module.index(), name, kind)`; a fork clones the base
interner; two forks each adding a module named `Main` at the same next index mint
the *same* `DefId` for `Main.main` by construction. Written as specified, the gate
fails a correct implementation — it did, on first run.

The ids coinciding across two **disjoint universes** is harmless. The property the
hazard is actually about — and what the shipped gate asserts — is that case *N+1*'s
**world** carries none of case *N*'s entries.

The gate carries an inline falsifier that exhibits the genuine leak: a naive path
reusing **one** world across both cases judges case B's *unannotated* `main`
against case A's *declared* `main : Int`, and reports a type error for a program
that is clean on its own. That is the real bug class; forking removes it.

### 3.3 C-1's expected effect was understated by ~9×

v2 §1.3: *"Expected effect: removes the 75.4 % term. **1.293 s → ~0.318 s per
case.**"* and §1.2: *"C-1 alone lands at ~318 ms/case — exactly at the 4-way
break-even for 1,500 cases, with zero headroom, and 3.3× over budget at 5,000."*

Measured C-1 alone: **34.4 ms/case** — 9.2× better than predicted, and already
**under every entry** in the break-even table.

The arithmetic error is in treating the profile's frame attribution as a partition
of *kinds of work*. The 19.5 % labelled "the rest of `World::build_decls`" and the
4.9 % labelled "passes beyond declarations" are **also** dominated by `db.resolve`
calls — reached through `record_union → resolve_type_names`, and through passes
5–8, each of which iterates `db.module_ids()` calling `db.resolve(m)`. The memo
removed those too. Only the frame *label* was specific to `resolve_type_names`;
the cost was not.

This does not change the conclusion — it strengthens it — but it is the same class
of reasoning error that produced v1's cost model, and it is worth naming: a
profile tells you where time is spent, not which single change reclaims it.

**Consequence for v2 §1.3's "Why C-2 is not optional".** On the measured numbers,
C-2 is not load-bearing for the T1 budget: C-1 alone clears every break-even entry.
C-2 remains valuable — it is a further 33.7× and it is proven verdict-neutral — but
the claim that the combinatorial layer is *impossible* without it is not supported
by measurement.

---

## 4. The differential result (v2 §11-U1)

v2 §11-U1 named this the design's largest technical risk with an explicit exit
criterion: identical per-item verdicts over the reject + infer corpora, or C-2 is
not viable as specified.

```
$ xtask shared-world
  items compared     : 121
  shared world used  : 120
  full-rebuild falls : 1
      36-composite-server  [bare-alias-collision]
  stdlib base modules: 87
SHARED-WORLD GATE: PASS  (121 items, identical verdicts
                          — counts, diagnostics and inferred type tables)
```

The compared fingerprint is deliberately stronger than the gate verdict, because a
gate can agree on counts while the checker silently inferred a different type:

* `type_errors`, `name_errors`, `exhaustiveness_warnings`
* every diagnostic as `code|severity|message`, sorted
* every def type as `module.name|declared|rendered`, sorted

No tolerance, no allowlist.

**The harness is falsifiable, and was falsified.** `--inject-divergence` skips the
case's body-derived passes:

```
$ xtask shared-world --inject-divergence
---- 18 divergence(s) ----
  reject/d1_any_result_length.sky: type_errors 1 vs 0
  reject/f10_apply_wrong_arg.sky:  type_errors 1 vs 0
  infer/44-record-update: def_types differ:
      whole-program-only ["Main.older|false|{ active : Int, age : Int, name : String }"]
      shared-only        ["Main.older|false|{ r6 | age : Int }"]
  …
SHARED-WORLD GATE: injected divergence DETECTED in 18/121 items
```

The divergences land in exactly the channels the skipped passes populate —
`d1_any_result_*` is pass 6, `f10_*` is pass 5, the record-update type tables are
passes 7/8. The comparison is live and channel-sensitive.

---

## 5. `c_measured`, and which branch fires

```
$ xtask corpus-bench --sizes=63,250,1000,2000,4000 --reps=5

  base world assembly (once per process): 39 ms

      N   shared/ms      median         max    spread        total/s
     63        1.01        1.02        1.04      2.8%           0.06
    250        1.01        1.01        1.02      0.9%           0.25
   1000        1.02        1.02        1.02      0.6%           1.02
   2000        1.02        1.02        1.02      0.5%           2.04
   4000        1.02        1.02        1.03      1.3%           4.08

  total_seconds = -0.002 + 0.00102 * N        R² = 1.00000
  c_measured    = 1.02 ms/case
  c_isolated    = 34.35 ms/case   (33.7× the shared rate)
```

Five sizes, so the linear model is **fitted**, not extrapolated from two points —
the failure mode that produced v1's cost model. The intercept is −0.002 s, i.e.
indistinguishable from zero: per-case cost genuinely does not vary with corpus
size, which is what makes `N_max = B_L1 × P / c_measured` a legitimate model.
Run-to-run spread is ≤ 2.8 % at the smallest size and ≤ 1.3 % everywhere else.

`c_isolated` = 34.35 ms/case is the rate the counted fallbacks are charged at, and
it is the static-case analogue of v2 §3.3's `N_iso` term.

### The branch

v2 §2.2 allows exactly three outcomes. With `c_measured` = 1.02 ms:

**`N_max ≥ N_min` → PROCEED.**

At v2's tightest budget entry — 5,000 cases against a 24 ms/case
single-threaded break-even — the measured cost is **23.5× under**. Inverting
§2.1's `N_max = (B_L1 × P) / c_measured` against the same budget that produced the
24 ms figure gives `N_max` in the **hundreds of thousands** of static cases,
single-threaded, before parallelism.

**What this means for Phase 4's case counts.** The static-case cost has stopped
being the binding constraint on Layer 1's size. `N_min` — computed by the
generator from the coverage guarantee (v2 §2.1: S1 full-cross triples + the
pairwise covering array + the distance-1 neighbourhood of every pinned coordinate)
— now sets the corpus size on its own, with headroom to spare. Phase 4 should
size the corpus from coverage and record `N_max` as the (very large) ceiling,
rather than trading coverage against budget.

Two constraints move to the front instead, and Phase 4 should treat them as the
real limits:

1. **`N_iso × c_u`** — the families that need their own compilation unit
   (v2 §3.2). Those pay `go build`, not 1.02 ms, and v2's own estimate of
   `N_iso ≈ 130` units at warm `c_u` already dominates the static term by orders
   of magnitude. **This is now the whole cost model.**
2. **The red rate** (v2 §2.3, Phase 3.5's 100-case spike). Unchanged by anything
   here, and still mandatory before a corpus size is committed.

### What the number is, and is not

The pool is the 63-file reject corpus cycled to each size. The generated Layer-1
corpus does not exist yet — that is Phase 4. This is the **same corpus** v2's
X = 1.293 s/case was measured on, so the improvement is apples-to-apples, but a
re-measure on the real generated corpus belongs in Phase 4, and a re-measure on
the CI runner class is still open as **U2** (this is a host number; every v2
number is too).

---

## 6. Verdict-neutrality

`xtask reject`, `infer`, `roundtrip` and `divergences` produce **byte-identical
output** before and after both changes.

| Gate | Baseline | After C-1 + C-2 | Output |
|---|---|---|---|
| `reject` | 75 s | 2 s | identical (63/63) |
| `infer` | 65 s | 2 s | identical |
| `roundtrip` | 0 s | 0 s | identical (173/173) |
| `divergences` | 2 s | 0 s | identical |

The gates still run the whole-program path; C-1 is what speeds them up. Migrating
them onto the shared path is a Phase 4 decision, and the differential harness is
the evidence it can be taken safely.

---

## 7. Reproducing all of it

```bash
cd rust && env -u CARGO_TARGET_DIR cargo build --release -p xtask

xtask shared-world                     # differential: 121 items, identical
xtask shared-world --inject-divergence # proves the differential can fail
xtask corpus-bench --sizes=63,250,1000,2000,4000 --reps=5

env -u CARGO_TARGET_DIR cargo test -p hir resolve_memo        # C-1 invalidation
env -u CARGO_TARGET_DIR cargo test -p ty --test shared_world  # C-2 hazard gates
```
