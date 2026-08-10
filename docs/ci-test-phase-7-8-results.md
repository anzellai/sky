# CI/test overhaul — Phase 7 + 8 results (the coverage ledger, and the migration)

Companion to [`ci-test-architecture-v2.md`](ci-test-architecture-v2.md) (the
design, whose §0.0 amendments table this phase wrote),
[`ci-test-phase-2-3-results.md`](ci-test-phase-2-3-results.md) (the cost
measurement) and [`ci-test-phase-4-results.md`](ci-test-phase-4-results.md)
(Layer 1).

Measured 2026-08-10 on the dev host (macOS), release build.

Every number below is reproduced by a committed script. Where a number here and
a number in `docs/coverage/*.json` disagree, **the JSON wins** — the same rule
§5.3 imposes on every other document, applied to this one.

---

## 1. Phase 7 — the ledger

`xtask coverage-ledger` writes `docs/coverage/ledger.json` (canonical) and
`ledger.md` (a view). It measures, from the tree: the stdlib surface via the
exact `sky doc --export` code path; 75 corpus units and their imports and
`sky.toml` surfaces; the gate registry and `falsifier-proofs.json`.

### 1.1 One ordered scale, so the two columns are comparable

| n | class | meaning |
|---|---|---|
| 0 | `None` | nothing covers it |
| 1 | `Builds` | something compiles it, nothing runs it |
| 2 | `Runs` | something builds AND runs it; verdict = exit status only |
| 3 | `Asserted` | explicit counted assertions |
| 4 | `Falsified` | assertions in a REGISTERED gate whose falsifying mutation is recorded PROVEN |

The distinction between 3 and 4 is the whole point of the harness: a gate that
asserts is not the same as a gate somebody has proven can fail.

### 1.2 Headline

| | |
|---|---|
| Surfaces | **141** (87 stdlib modules · 21 CLI verbs · 33 cross-cutting) |
| Covered at `>= Asserted` | **119 (84.4 %)** |
| `stronger` / `equal` / `weaker` | **111 / 30 / 0** |
| Corpus units | 75 (57 `examples/*` · 7 Layer-2 · 9 root `tests/` · conformance · Layer-1) |

**`weaker` is 0, and it was not tuned to zero.** It reached 0 through three
corrections, each of which fixed a *measurement* that was wrong:

1. `examples/` are retained (§2), so Example units contribute to `cover_new` —
   capped at `Runs`, or `Asserted` when the example owns an asserting `tests/`
   suite run by the registered `sky-verify` gate, never `Falsified`.
2. A gate that is CI-wired but not harness-registered scores `Asserted` (3),
   not 0. `xtask coerce-floor`, `fuzz`, `lsp`, `repro`, `welltyped`,
   `divergences` and the `scripts/*.sh` verifiers all still run; they simply
   have no proven falsifier. A `CI_SURFACES` table matched against
   `.github/workflows/**` at run time carries this, so **deleting a CI step
   deletes its coverage claim**.
3. One false weakening was a bug in the scanner itself: `docs.examples-gate`
   read `Asserted -> None` because rust-ci.yml invokes
   `../scripts/doc-examples.sh` and the matcher anchored on the `scripts/`
   prefix.

### 1.3 Sole ownership — generated, and smaller than the hand-count implied

| table | entries |
|---|---|
| stdlib modules imported by exactly one `examples/*` | **16** |
| stdlib modules imported by exactly one unit of any role | 11 |
| `sky.toml` sections owned by exactly one unit | 2 |
| **lost if `examples/` were retired — modules** | **2** |
| **lost if `examples/` were retired — config sections** | **1** |

| module / section | sole owner |
|---|---|
| `Sky.Core.Process` | `examples/17-skymon` |
| `Std.Cli` | `examples/20-cli-counter` |
| `[analytics]` | `examples/52-blog-analytics` |

The 16-versus-2 gap is the finding: 14 of the 16 are *also* imported by
conformance or a Layer-2 member, so they only look sole-owned if you scan
`examples/` alone — which is exactly what a hand-count does. v2 §9.2's
"11 examples solely own stdlib modules" was **not reproduced**.

Reconciliation of the other measured facts the phase was handed:

| claim | verdict |
|---|---|
| `26-ui-showcase` owns four `Std.Ui.*` | **CONFIRMED** — `Animation`, `Grid`, `Transform`, `Transition`; all four now also in `apps/fieldbook`, so none is lost |
| `[log]` owned only by `39-hub-demo` | **CONFIRMED** — its two sub-projects, and no Layer-2 app declares it |
| `[analytics]` + `withAnalytics` only by `52-blog-analytics` | **CONFIRMED**, and it is one of the three genuine losses |
| `[dependencies]` / `.skydeps` only by `13-skyshop` | **CONFIRMED** — and `13-skyshop` IS Layer-2 member D, so it was never a deletion candidate |
| `55-store-partial-update` is the only implicit FFI driver-resolution coverage | **CONFIRMED** — `import Modernc.Org.Sqlite as _` is the single repo-wide hit, undeclared in any `sky.toml`, resolving through the inherited `runtime-go/go.mod` require |
| 8 examples declare `[database]`, all sqlite | **CONFIRMED** (9 including `apps/ledger`). v2 §6.1's "7" is stale; zero Postgres holds |
| `13-skyshop` owns 10 modules incl. Firestore/Firebase/Stripe | **NOT REPRODUCED as stdlib modules** — those are FFI-generated surfaces, not `sky-stdlib/` modules, so they are outside the 87-module denominator |

### 1.4 What is still uncovered — reported, not defined away

| | count | % of denominator |
|---|---|---|
| stdlib modules imported by **nothing** | **11 of 87** | 12.6 % |
| symbols with **zero qualified references** (STRICT) | **821 of 1746** | **47.0 %** |
| symbols unreferenced under the generous rule | 741 of 1746 | 42.4 % |
| modules imported ONLY by a root `tests/` suite | 1 (`Sky.Core.WebSocket`) | — |
| surfaces with zero `cover_new` | 18 | — |

The eleven modules nothing imports: `Sky.Core.Basics`, `Sky.Core.Char`,
`Sky.Core.Io`, `Sky.Core.Path`, `Std.Db.Migrate`, `Std.Db.Schema`,
`Std.Db.Table`, `Std.Email`, `Std.Jobs`, `Std.Live.Console`, `Std.Markdown`.

The brief quoted "15 of 87 imported by nothing, including `Std.Config`". After
Layer 2 landed it is **11**: `Std.Config`, `Std.Trace` and
`Sky.Http.Middleware` are now imported by `apps/relay`, `Std.Db.Decode` by
`apps/ledger`, `Std.Ui.Events` by `apps/fieldbook`. `Std.Jobs`,
`Std.Db.Schema`, `Std.Db.Migrate`, `Std.Markdown` and `Std.Email` remain
uncovered as stated.

**Both reference numbers are reported and never averaged.** STRICT
(qualified-only) is the number any *uncovered* claim uses, because
over-counting references understates uncovered surface, and understating it is
the direction the mandate forbids.

Seven CLI verbs have zero cover in **both** columns: `check`, `console`,
`console-serve`, `fmt`, `lsp`, `upgrade-claude`, `verify`.
`scripts/verify-cli.sh` mentions only `sky install` and `sky build`, and no
workflow invokes it.

### 1.5 Denominators — re-measured, not copied

Phase 4's numbers were re-derived rather than trusted, and the test bodies moved
under this phase's own work:

| metric | value |
|---|---|
| stdlib entries / modules / values / types | **1746 / 87 / 1625 / 121** |
| unfiltered entries (hidden by `exposing`) | 1969 (223) |
| `SyntaxKind::KINDS` / constructs / non-constructs | **124 / 80 / 44** |
| conformance cases / assertions / vacuous | 772 / 769 / 7 |
| example-suite cases / assertions / vacuous | **126 / 148 / 9** (was 63 / 84 / 11) |

**The ratchet itself had a defect, found by running it.** Converting two
unconditional `Test.pass` calls into real assertions moved
`tests.examples.vacuous_pass` 11 → 9, and `xtask denominators --check` FAILED,
demanding two `[[removal]]` entries. v2 §4.1 lists those calls as a *defect*;
the ratchet was charging a fee for fixing exactly what the design asks to be
fixed, which over time trains people to leave vacuous tests in and to write junk
entries when they do not.

`vacuous_pass` is now ratcheted the **other way**: a fall is the improvement, a
rise FAILS, and no number of removals entries satisfies it — a new case that
cannot fail is not a denominator move to account for, it is a defect to fix.
`by_assertion_fn.*` is excluded as diagnostic, because rewriting one `Test.fail`
as a `Test.equal` moves two of them while the aggregate rises. The aggregate
`assertions` and `cases` counts remain fully ratcheted; a test pins that they
still fail on a fall.

---

## 2. Phase 8 — the migration decision

**`examples/` is RECLASSIFIED, NOT DELETED.** The full argument and the
retained/moved contract are recorded in v2 §9.6 and `AGENTS.md`. In brief: the
motivation dissolved (1,293 ms → 1.02 ms per case; the "duplicated reject face"
re-measured at ~22 s, not ~1,114 s; `sky check` was never cheap), and the cost
did not — 52 of `coerce_floor.golden`'s 57 rows are examples carrying **7,197 of
9,346 locked tokens (77 %)**, all 24 stdout goldens key to examples,
`roundtrip`'s 173 assertions come from walking `examples/`, and
`shared-world`'s 121 items include 58 example `src` trees.

**Nothing was deleted. Reclassified: all 58 example directories.** Not even
`simple` and `test_pkg`, which are already excluded from every corpus and so
would return zero cost and zero coverage.

### 2.1 The soundness ratchet was re-keyed first — and it was RED

`xtask coerce-floor` **was already failing at the base commit**: 8 examples had
silently widened the runtime-narrowing floor and were never re-blessed. Each is
a justified consequence of a product fix shipped earlier on the branch (+3 on
the DB users from `Store.insert` returning the assigned id; +1 on two UI
composites from the `Std.Ui.Events` and `Input.checkbox` corrections — stdlib
Sky source compiles into each example's emitted `main.go`, so a stdlib change
moves the floor).

Layer 2 contributed **zero** to the ratchet while being the corpus that now
carries product regression duty. Re-keyed:

| | before | after |
|---|---|---|
| rows | 52 | **57** (`apps/ledger` 592 · `apps/fieldbook` 883 · `apps/relay` 190 · `sky-bundled/console` 461 · `sky-bundled/doc` 23) |
| tokens | 7,177 | **9,346** (+2,169) |

**The conservation assertion v2 specifies is insufficient — demonstrated.**
§9.2 asks for `sum(tokens)` and `count(rows)` non-decreasing. Implemented
literally, the first re-key PASSED both — 52 → 55 rows, 7,177 → 9,333 tokens —
while silently dropping the rows for `03-tea-external` and `11-fyne-stopwatch`.
Three large arrivals paid for two small departures. A ratchet that can lose a
locked floor because some *other* row grew is not a ratchet.

A third clause now bites: **no key that had a row may end up without one.** The
proximate cause was fixed at the root too — `--bless` dropped any project that
did not emit in *this* environment, so a bless on a machine without the Go FFI
deps fetched retired those floors silently. Such rows are now carried forward at
their last measured value, and the carry is printed.

### 2.2 `35-composite-generics` — migrated to hand-written values

Commit `7425fd00` records that its golden **froze** the #173 zeroed-aggregation
bug. Checking rather than assuming: every number in the current golden was
re-derived by hand from `Decode.sampleCsv` and the rules in `Compute.classify` /
`amountToCents` / `Report.summariseBucket`, without running the program, and all
of them match. **The golden is correct now**; `7425fd00` did fix the value. The
residual defect is narrower and still real — the golden was the *sole* witness,
and re-blessing was the only available response to a diff.

| | before | after |
|---|---|---|
| cases | 21 | **84** |
| `Test.equal` | 10 | **73** |
| `Test.pass` (vacuous) | 2 | **0** |

`fixtureSuite` is listed first and is the accept-parity guard (the
`inferred_sig_snapshot.rs:110-126` template): it proves `parseCsv sampleCsv`
returns `Ok` with 20 intact rows *before* anything below pins a value — without
it a decode failure reads as "the aggregation is empty", which is the shape of
the bug. `groupedRecordSuite` asserts the #173 shape directly, reading *through*
the `Dict k (List Record)` to nested fields and re-summing each group, because a
length check cannot see fields being dropped. Four values are deliberately NOT
pinned because they cannot be hand-derived; their derivable consequences are.
The stdout golden is **kept** — it is now a second, independent witness.

---

## 3. Open items closed

| item | outcome |
|---|---|
| `release.yml` ran **no test gate** | A `gate` job now runs `cargo test --workspace`, `preflight-tag.sh`'s own rust-gate set, the harness at T1 with `--require-proofs`, and both coverage ratchets. `release: needs: [build, gate]` — assets may build, nothing PUBLISHES until it is green. It does not `needs: build`, so it costs no critical path |
| the harness had **no `BLOCKED` state** despite v2 §7.2 | **Implemented**, with a compile-time-mandatory issue link and expiry, self-expiring to FAIL (a malformed date reads as expired), never `PASS`, and — the property that makes it affordable — **counted as UNCOVERED by the ledger**. A registry test forbids blocking a product-tier gate. Falsifier demonstrated: moving the expiry to 2000-01-01 flips `BLOCKED` → `FAIL` |
| `tests/Db/DbTest.sky` three-type-arg signatures | Fixed, and the finding generalised: **all 22 root suites had never compiled**. 320 cases now run behind a registered gate |
| `ci-green` RED on budget | See §4. Reported, not closed. The ceiling was not raised |
| template + doc sync | `AGENTS.md` (corpus topology + examples contract + the three ledgers), `templates/AGENTS.md` (two warnings drawn from this cycle's defects), `docs/tooling/gate-harness.md` (BLOCKED), v2 §0.0 + §9.6. `templates/CLAUDE.md` imports `AGENTS.md`, so it is in sync by construction |

---

## 4. The T1 budget gap — reported, ceiling unchanged

The ceiling is **990 s**: `T1_CEILING_SECONDS: '900'` plus
`T1_GRACE_PERCENT: '10'` (`.github/workflows/rust-ci.yml:55-56`), computed at
`scripts/ci/assert-tier-budget.sh:99` and asserted by `ci-green` over GitHub's
per-job `completed_at - started_at`, so queue time is not charged.

**It is not measurable from this host.** The assertion runs against a parallel
job graph on GitHub's runner class; nothing local reproduces it, and the harness
runs its gates **sequentially** by deliberate Phase-1 choice. Any local number
would be a different measurement wearing the same name. What can be said
exactly:

- **Nothing relates gate budgets to the tier ceiling.** Σ T1 gate `budget_s` is
  **12,420 s** across 12 gates — 12.5× the ceiling. That is not a contradiction:
  `budget_s` is a per-gate *kill ceiling* sized for a cold runner, and the tier
  is asserted over measured job elapsed. But no code connects them, so a gate
  budget can grow without anything noticing.
- **What would close the gap**, in the order the evidence supports:
  1. **Shard the corpus job across runners.** §2.2 already names raising `P` as
     the cheapest lever. The tier formula is `setup + max(job elapsed)`, so
     splitting the longest job is the only change that moves `max` rather than
     the sum.
  2. **The `conformance` / `verify-cli` / `sky-verify` trio dominates T1** —
     2,400 s of declared budget each, and all three shell out to real builds.
     They are the jobs to split first.
  3. **`setup` is on the critical path by construction.** 6a-1 measured the
     dev-profile change at +72 s cold build for a 5.9-7.0× run saving; the
     build cost is paid once in `setup` and lands entirely on `max`'s left
     term.
- **Not done, and deliberately:** the ceiling was not raised. `ci-green` remains
  an additional, non-required check publishing the real number every run, which
  is what v2 §8.2 and the workflow's own header comment ask for.

---

## 5. Premise failures this phase found

Every prior phase found two or more. This one found nine in v2 (tabulated in
§0.0 there) and four in the phase brief itself:

| brief said | measured |
|---|---|
| the harness has **30 gates** | **20** at the base commit; **23** now |
| `coerce_floor.golden` holds **54 entries / 9,510 tokens** | **52 entries / 7,177 tokens** at the base |
| `35-composite-generics`' golden **froze** the bug | It did, historically. It encodes the **correct** value today (`7425fd00`); the live defect was that it was the *sole* witness |
| **15 of 87** modules imported by nothing, incl. `Std.Config` | **11**; `Std.Config`, `Std.Trace`, `Sky.Http.Middleware`, `Std.Db.Decode` and `Std.Ui.Events` gained their first coverage from Layer 2 |

And two findings that belong to nobody's premise:

- **`xtask coerce-floor` was RED at the base commit** (§2.1).
- **`xtask denominators` is invoked by no workflow.** The denominator every
  coverage percentage divides by could drift silently while CI stayed green.
  Both ratchets are now in the release gate, and `meta.coverage-accounting` is a
  ledger row whose `cover_today` is, honestly, `0 ("nothing")`.

---

## 6. New defects opened, not closed

Two real compiler defects, both of which break `sky check == sky build`, found
by running suites that had never run. Both are declared in
`bodies::SKY_SUITES_BLOCKED` with a file:line origin, a repro, and an expiry of
**2026-11-08**, after which the gate fails on its own.

1. **Point-free alias of a kernel.** `tickle : String -> String` /
   `tickle = String.toUpper` type-checks, then `go build` fails: it emits
   `rt.String_toUpper` (`func(any) any`) into a `func(string) string` slot.
   `lower.rs:2721` (kernel-alias `Res::Def`) and `lower.rs:2798`
   (`Res::Kernel`) stamp the caller's expected type onto a bare `Ident` without
   routing through `kernel_partial` (`lower.rs:3925`) — the eta-expansion the
   CALL path already performs at `lower.rs:3350-3358`. This is a Rust-rewrite
   regression of #398, and `Sky/Core/PointFreePolyTest` is that issue's
   regression fence, so it must not be rewritten to a lambda.
2. **Nullary kernel in a `Task` slot.** `Task.perform (Pure.uuidV7 ())` emits
   `rt.Uuid_v7()` returning `any` into `rt.SkyTask[Sky_Core_Error_Error,
   string]`. `nullary_kernel_value` (`lower.rs:2828`) coerces only when the slot
   is a concrete-key map (`:2829`), so the `rt.TaskCoerceT` wrap that
   `codegen/src/lib.rs:407` already knows how to render is never requested.
   Blast radius is small — `Uuid.v4`/`v7` are the only nullary kernels with a
   `Task`/`Result`/`Maybe` Sky type.

Neither is fixed here.
`docs/rust-rewrite/13-change-verification-and-edge-cases.md` requires the FULL
example sweep plus a real app before any `lower` change counts as verified, and
this phase was scoped not to run it. Fixing them blind would be the exact
pattern that doc catalogues.

A third, smaller: **`sky-stdlib/Sky/Core/String.sky:109`'s docstring for
`containsIn` is inverted** — `"hello world" |> String.containsIn "world"` is
documented as `True` and evaluates to `False`, because `containsIn` is
haystack-first. `startsWithIn` and `endsWithIn` carry the same pattern and
should be checked together. It is a stdlib change and is reported rather than
made here.
