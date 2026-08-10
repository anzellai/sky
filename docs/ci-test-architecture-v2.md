# Sky CI/CD + Test Architecture — v2 (reconciled)

> **Status:** design v2. **Supersedes** `docs/ci-test-architecture.md` (topology)
> and `docs/ci-corpus-proposal.md` (corpus). Those two documents remain readable
> for their evidence; where they conflict with this one, **this one wins**.
> **Mandate:** `.claude/AUTONOMOUS_GOAL.md` — verbatim user goal + the 2026-08-09
> two-layer REFINEMENT. **Branch:** `feat/ci-test-overhaul`.
>
> This document is the output of two parallel designs, each adversarially
> grilled, each returning 5 blocking findings. All 10 blocking findings and the
> ~25 majors are resolved here, or are named in §11 as measurements that must
> land before a specific phase commits. Nothing is deferred to a later document.

---

## 0. What changed from v1, and why

Both v1 documents were substantially right about *what* is broken and
substantially wrong about *what it costs to fix*. The corrections that reshape
the design:

| # | v1 claim (both docs) | v2 correction | Consequence |
|---|---|---|---|
| **C1** | The per-case tax is fresh `SourceDb` construction + re-parsing 87 stdlib modules inside the item loop | **False.** The stdlib parses are already hoisted (`reject_gate.rs:66`, `infer_gate.rs:33`) and the per-item `SourceDb::new()` + `add_module` clones are Arc-clones costing **0.2 %**. The tax is `World::build` (`ty/src/db.rs:111-112`) demanded by `check_modules` (`ty/src/check.rs:193,198`) | **Route A is a no-op on the dominant term.** §1 |
| **C2** | Route A (hoist world construction out of ~6 call sites) or Route B (salsa via `skydb`) | Both insufficient. **Route C** = per-module `hir::resolve` memo + an incremental-module-set `World::build`. §1.3 | §1 is a compiler change, not a gate refactor |
| **C3** | "Three measurements, one consistent model" corroborates X = 1.293 s | Sampling artefact — **dropped**. X survives on its own structure: third-gate prediction within 3.7 %, and per-case cost is independent of item size | §1.1 |
| **C4** | `resolve` costs 58 world rebuilds | **Falsified**: measured 0.164 s total, ~400× off. `resolve_gate` builds no world | §1.1 |
| **C5** | Layer 1 = ~1,500 static cases (topology) *or* ~7,000 assertions in ~62 units (corpus) | Incompatible units, and **T1 does not close if both land**. §3 defines ONE mechanism and ONE cost formula with three terms | §3 |
| **C6** | Batch many cases per compilation unit | **Not semantics-preserving** for the exact families the corpus exists to catch (`lower.rs:246-266`, `goty.rs:186-196`). Four families are forbidden from batching | §3.2 — a new, budget-visible cost term |
| **C7** | Case counts are ~1,500 growing to ~5,000 | Counts are **DERIVED** from a measured per-case cost, with an explicit abort branch | §2 |
| **C8** | `examples/` drops to `sky check` — cheap, no `go build` | **False**: `sky check` ≡ `sky build`, both run `go build` (`sky/src/main.rs`, and `ci-corpus-proposal.md:66-68`). The change drops *run*, not *build* — an unledgered coverage removal | §9.3 |
| **C9** | Falsifying mutation per gate | Gate-granular mutation over aggregate stdout **survives the one-byte golden**. Falsifiability is **per item** | §4 |
| **C10** | `timeout-minutes` = 1.5× the tier budget enforces the tier | It does not (13.5 × 2 jobs = 26 min passes a 9-min tier). Per-job timeout = 1.5× *that job's* estimate, plus an explicit elapsed assertion in `ci-green` | §8.2 |
| **C11** | T1 = 9 min | **`setup` was absent from the arithmetic** and is on the critical path. T1 = `setup + max(jobs)` | §8.1 |
| **C12** | Generated cases are coverage | Without an independent oracle most are **change-detectors**. Value assertions are confined to families whose answer the generator *constructs*; the rest are labelled and **excluded from the coverage number** | §4.4 |
| **C13** | Adopt the BlueDB gate harness wholesale | Precedent is **41 of 48 gates stubbed, 7 of 48 mutations verified**, its timeout kills nothing (zero `kill`/`setsid`/`killpg`/`pgid` hits) and its mutation probe has no timeout. Phase 1 is re-scoped from **adopt** to **build** | §7 |
| **C14** | Gate bodies on worker threads | Must be **child processes**. A thread's children are unreachable by `killpg`, and an orphaned thread can write a result after its gate was recorded FAIL, corrupting a *later* gate's verdict | §7.3 |
| **C15** | `DEGRADED` state for a cache-cold P1 | A silent scope reduction. **`DEGRADED` is deleted as a state**; tiering is declared, never chosen at runtime | §7.2 |
| **C16** | The "100 %" denominator (1,762 / 1,640 / 122 vs 1,744 / 1,623 / 121 — both hand-counts, both wrong; the truth is **1,746 / 1,625 / 121**) | Two disagreeing hand-counts over a **gameable** export with FIVE silent-shrink paths, all exit 0. ONE committed script — `xtask denominators` — owns every denominator | §5 |
| **C17** | "772 assertions" / "575 assertions" | Neither. **Measured here**: conformance = **772 cases / 776 assertion calls**, 7 of which are `Test.pass` (unconditional). "Assertion" and "case" are defined once, in §5.4 | §5.4 |

### 0.1 Where the grills adjudicated between the two documents

Recorded so the reconciliation is auditable, not re-litigated:

| Question | Winner | Amendment carried into v2 |
|---|---|---|
| `sky check` cost | **corpus** | topology's "cheap, no go build" premise deleted repo-wide |
| Corpus size | **corpus's number** | + topology's per-row ledger discipline + a **mechanically generated** sole-ownership table |
| Rename to `docs-samples/` | **topology** | keep the path `examples/`; change the *contract* |
| Generated vs hand-written kitchen-sink | **corpus** | generator must draw collision types and field-name sets from the **real stdlib symbol table**; Layer 2 remains the accrete-by-accident witness |
| Layer 2 shape | **corpus** (its Fleet scenario cannot rot into a mock) | + topology's **CLI-verb owner**: `rust/crates/sky/tests/*_flow.rs`, not an app |
| `sky-bundled/console` | **corpus** (topology never mentions it) | joins Layer 2 as a gated member |
| Storefront / P1 tier | **corpus** | pre-release, not per-push |

---

## 1. The cost mechanism — corrected

### 1.1 What was measured, and what survives

A griller profiled `xtask reject` with `sample(1)` — **10,953 samples**:

| Frame | Share of total |
|---|---|
| `ty::check::check_modules` → `TyDb::check_world` | **99.8 %** |
| ↳ `ty::sig::World::build_decls` | **94.9 %** |
| ↳↳ `resolve_type_names` → `SourceDb::resolve` (re-running `hir::resolve` per stdlib module) | **75.4 %** |
| The case's own parse / resolve / infer **and** `SourceDb` construction | **0.2 %** |

Against the measured X = **1.293 s per case**, that decomposes to:

```
0.975 s   hir::resolve, re-run per stdlib module inside resolve_type_names   (75.4 %)
0.252 s   the rest of World::build_decls                                     (19.5 %)
0.063 s   World::build's passes beyond declarations                          ( 4.9 %)
0.003 s   the case's own work + SourceDb construction                        ( 0.2 %)
```

**Two v1 claims die here.** The 87 stdlib parses are *already* hoisted
(`reject_gate.rs:66`, `infer_gate.rs:33`); the per-item `SourceDb::new()`
(`reject_gate.rs:187`, `infer_gate.rs:98,148`, `ty/tests/reject.rs:85`) and its
`add_module` loop are **Arc-clones in the 0.2 % bucket**. And the `resolve` gate
was claimed to cost 58 world rebuilds; it was **measured at 0.164 s total** —
off by ~400× — because it builds no world at all.

**What survives, and why.** X = 1.293 s/case is kept, but *not* on the
"three measurements, one model" corroboration — that was a sampling artefact and
is dropped. X survives because its **structure** is verified: the two-point model
predicted a third gate within 3.7 %, and per-case cost is independent of item
size (a 27-line fixture and a 1,911-line app cost within 50 % of each other,
`ci-corpus-proposal.md:71-78`). A per-case cost that does not scale with the case
is by definition fixed overhead, and the profile says exactly which overhead.

Cited sites, verified on this branch:

- `ty/src/db.rs:111-112` — `impl TyDb for hir::SourceDb { fn check_world() { Rc::new(World::build(self)) } }`
- `ty/src/check.rs:193,198` — `check_modules` demands `db.check_world()`
- `ty/src/sig.rs:142` `World::build`, `:339` `World::build_decls`, `:399,425,447` the `resolve_type_names` call sites, `:1527` its definition
- `hir/src/db.rs:39-43` — the removed memo's docstring: *"this eager `SourceDb` (LSP + test path) simply recomputes on demand — cheap, and it never sat on a hot loop."* **The profile disproves that sentence.** It sits on 75.4 % of the hottest loop in CI.

### 1.2 Why Routes A and B are both insufficient

**Route A** (hoist world construction out of the ~6 gate call sites) targets the
0.2 % bucket. It is a **no-op on the dominant term** — and worse than a no-op:
implementing it and measuring would show "sharing the world doesn't help", which
is the *opposite* of the truth, and would very likely be read as evidence that
Layer 1 is infeasible.

**Route B** (route gates through `skydb::SkyDatabase`'s salsa memoisation) fails
on the crate's own docstring. `skydb/src/lib.rs:216-227` says `check_world_query`
*"reads every app def's body (passes 5-6), so it does NOT backdate on a body
edit"*. Every Layer-1 case **is** a new body. Salsa would re-execute
`check_world_query` for every case, i.e. Route B pays exactly X per case.

### 1.3 Route C — the only route that closes the term

Two independent changes, in this order.

**C-1 — per-module `hir::resolve` memoisation on `SourceDb`.**
A module's resolution is a pure function of its parse. `SourceDb` is
append-mostly, so a `RefCell<HashMap<ModuleId, Rc<Resolved>>>` beside `defs`
(`hir/src/db.rs:44-49`) is trivially safe **with one invalidation obligation**:
`add_module` (`hir/src/db.rs:72-79`) **overwrites** the parse when a name
collides (`self.modules[id].parse = parse; return id`), so the memo entry for
that `ModuleId` must be dropped on that path. That is a three-line invalidation
and a unit test — but it is a *correctness* obligation, not an optimisation
detail, and it is the exact path Layer 1's same-named-module axis will exercise
constantly.

*Expected effect:* removes the 75.4 % term. **1.293 s → ~0.318 s per case.**

**C-2 — `World::build` admits an incremental module set.**
Prebuild the stdlib-only `World` once per process. Per case, run the declaration
passes over **only the case's modules** and merge into a fork of the prebuilt
world; run passes 5–8 restricted to the case's modules
(`ty/src/sig.rs:142,339`). The reader contract does not change: `check_modules`
still receives a `World` covering stdlib + case.

*Expected effect:* the residual O(87-module) assembly becomes O(case). Magnitude
**unknown** — see §11-U1. This is the design's single largest technical risk and
it is measured before Phase 4 commits to any case count.

**Why C-2 is not optional.** Break-even per case, derived from the T1 corpus-job
budget:

| Cases | Break-even, single-threaded | Break-even at 4-way in-job parallelism |
|---|---|---|
| 1,500 | 80 ms | **320 ms** |
| 5,000 | 24 ms | **96 ms** |

C-1 alone lands at ~318 ms/case — **exactly at the 4-way break-even for 1,500
cases, with zero headroom, and 3.3× over budget at 5,000.** The best plausible
reading of the profile leaves ~290 ms/case of O(87-module) `World` assembly after
the resolve memo, which **fails T1 outright at 5,000 cases**. C-2 is what makes
the mandate's combinatorial layer possible; C-1 alone does not.

### 1.4 Correctness constraints on any shared world — both are concrete

Neither is a hazard to "watch out for". Both are designed for, and each ships a
gate.

**(a) `DefId` leakage across cases — a soundness bug in a gate suite.**
`DefTable::intern` (`hir/src/ids.rs:99-103`) keys on
`(module.index(), name, kind)`, and `ModuleId` is the **insertion index**
(`hir/src/db.rs:78`). Reusing one db across cases makes case *N* and case *N+1*
intern the **same `DefId`** for `Main.main`. Every `World` channel keyed by
`DefId` (`value_sigs`, `app_check_sigs`, `record_result_sigs`, …) then leaks
state between cases: case *N+1* can be checked against case *N*'s signature.

> **Design:** each case is checked against a **fork of a pristine base**, never a
> mutated shared db. The base carries the stdlib modules and their memoised
> resolutions; the fork owns a fresh `DefTable` region for the case's modules.
>
> **Gate `corpus.defid-disjoint`:** run two consecutive cases that both declare
> `Main.main`; assert the `DefId` sets they intern are **disjoint**. Falsifier:
> reuse the db without forking — the gate must go red.

**(b) A prebuilt stdlib world is WRONG for a shadowing case.**
`add_module` **overrides** on name collision (`hir/src/db.rs:72-79`), and
Layer 1's A1/A2 axes *explicitly* include same-named and shadowing modules — that
is the #164 class. A case that declares `Std.Log` must not be checked against a
world already containing the real `Std.Log`'s declarations.

> **Design:** the runner detects shadowing (case module name ∈ stdlib name set)
> **before** forking and falls back to a **full rebuild** for that case. The
> fallback is a **counted, reported state** (`REBUILT`), surfaced in the corpus
> job summary, and the cost model charges those cases at the full-rebuild rate.
> A gate caps the shadowing-case count against the manifest, so the axis cannot
> silently become the dominant cost.

### 1.5 Reconciling the two reject faces — **before** anything generates against them

`ty/tests/reject.rs` and `xtask reject` are *not* the same check, and v1's
"remove one face, never both" instruction is correct but under-specified. The
divergence, verified line-by-line on this branch:

| | `ty/tests/reject.rs` | `xtask/src/reject_gate.rs` |
|---|---|---|
| Parse-error criterion | `:94` — `!parse.errors().is_empty() \|\| error_node_count() > 0` → **any severity** | `:200-205` — filters `Severity::Error`, `.max(error_node_count().min(1))` → **Errors only** |
| Exhaustiveness | `:107` counts `exhaustiveness_warnings` | `:43-46,242` counts `exhaustiveness_warnings` — **same** |
| Corpus discovery | `:120` **recursive** `collect_sky` | `:76` **flat** `read_dir` |
| Floor | `:121,147` `>= 13` against an actual 63 | count reported, no floor |

So the test calls a file "rejected" on a **Warning-severity parse diagnostic**
where the gate would not, and a corpus file placed in a **subdirectory** is seen
by the test and invisible to the gate. v1's summary ("the test counts Warnings,
the gate only Errors") was right about the parse criterion and silent about the
discovery divergence, which is the one that can hide a case entirely.

> **Phase 0 obligation:** reconcile to a single declared criterion + a single
> recursive discovery, keep **both faces** running it, and replace `>= 13` with
> the **exact** expected count from the manifest. Layer 1's reject generator
> (§3.1 family R) generates against the reconciled criterion; generating against
> the current pair would bake the divergence into thousands of cases.

---

## 2. Case counts are DERIVED, with an abort branch

v1 asserted "~1,500 growing to ~5,000". That is a number chosen before the cost
was known. v2 derives it.

### 2.1 The derivation

```
N_max = (B_L1_seconds × P) / c_measured
```

- `B_L1` — the Layer-1 slice of the corpus job's budget, in seconds, **on the CI
  runner class**, not the dev host
- `P` — in-job parallelism actually achieved (measured, not `nproc`)
- `c_measured` — per-static-case cost measured **after C-1 and C-2 land**, on the
  runner class, on the real generated corpus (not a microbenchmark)

`N_min` is derived independently, from the coverage guarantee in §5:
the S1 full-cross triples + the pairwise covering array + the distance-1
neighbourhood of every pinned coordinate + the pinned coordinates themselves.
`N_min` is **computed by the generator** and printed; it is not a target chosen
by a human.

### 2.2 The three branches — no fourth

At the Phase 3 → Phase 4 boundary, with `c_measured` in hand:

- **`N_max ≥ N_min` → PROCEED.** The corpus is generated at `N_min`, the
  headroom `N_max − N_min` is recorded as the growth budget, and a gate fails if
  the corpus exceeds `N_max`.
- **`N_min ≤ N_max × 2` → ESCALATE, with named options.** The gap is closed by
  raising `B_L1` (a larger T1), raising `P` (sharding the corpus job across
  runners — the cheapest lever and the first proposed), or a further compiler
  change. **The user chooses.** No option is taken unilaterally.
- **`N_max < N_min / 2` → ABORT the combinatorial layer as designed.** Report the
  measured cost, the residual profile, and the shortfall to the user. Do **not**
  silently shrink the corpus to fit; do **not** re-derive `N_min` downward. The
  mandate's "don't cut corners" makes shrinking-to-fit the forbidden move, and
  §0 rule 3 of `CLAUDE.md` makes "shipped for the scope I could afford" a drift
  phrase.

> **What this replaces.** v1's Route-A-then-measure plan would have measured a
> no-op and concluded the wrong thing. The abort branch exists because the honest
> answer to "does the combinatorial layer fit" is currently **unknown**, and the
> design says so rather than asserting a number.

### 2.3 The red-rate spike — before counts, not after

A generator aimed at the neighbourhoods of every historical bug **will find
reds**. If 15 % of 5,000 generated cases fail on day one, the corpus cannot land
as a required check and there is no policy for the reds.

> **Phase 3.5 (mandatory, ~1 day):** generate a **~100-case spike** across the
> declared families, run it, and **measure the red rate**. The spike's output
> determines: the initial corpus size, whether an xfail/quarantine tier is
> needed at all, and how many real defects Phase 4 is actually opening.
>
> **Quarantine policy** (only if the spike demands it): a quarantined case
> **still runs**; it carries an owner and an expiry; it **never contributes
> PASS**; its transition to green is reported. Expiry passed → FAIL. This is
> `BLOCKED`'s contract (§7.2), not a new escape hatch.

---

## 3. Layer 1 — ONE mechanism, ONE cost line

### 3.1 The single mechanism

The two v1 documents proposed *different units of work* and therefore
irreconcilable budgets: topology counted **static cases**, corpus counted
**behavioural assertions in built-and-run compilation units**. They are not
alternatives — they are two assertion economics. v2 unifies them under one
manifest and one formula.

**One manifest.** `corpus/manifest.toml` is the only membership authority. No
gate calls `read_dir` on the corpus (the discovery-by-listing model has already
produced two live defects: `39-hub-demo` invisible to all six gates, and the
reject-face discovery divergence in §1.5). Every entry declares:

```toml
[[case]]
id        = "lang/record_update/in-tuple/annotated"
family    = "record_update"
mode      = "static"        # static | emit_shape | behavioural
isolation = "batch"         # batch | unit          (see §3.2)
axes      = { position = "in_tuple", annotation = "annotated", row = "subset" }
witness   = "value"         # value | shape | diagnostic   (see §4.4)
class     = "V"             # V = independently verifiable | D = change-detector
coordinate = "anzellai/sky#166"   # optional; drives neighbourhood expansion
```

**Five families**, each matched to what it covers — this is the corpus doc's F1–F4
plus the topology doc's L1e, reconciled:

| Family | Mode | What it asserts | Where |
|---|---|---|---|
| **S** — stdlib behaviour | `behavioural` | every public symbol's value at its edge classes | `corpus/stdlib/*.sky`, `Sky.Test` |
| **L** — language matrix | `static` + `behavioural` | accept/reject, inferred type, and (class V only) the computed value | `corpus/lang/`, generated |
| **E** — emit shape | `emit_shape` | assertions on the *generated Go*: no stray `any` in a fully-typed expression; record update keeps every field; the right struct is selected | in-process, **no `go build`** |
| **R** — reject matrix | `static` | rejection **by diagnostic code**, with a paired accepted twin (§4.4) | `ty/tests/reject/corpus`, extended |
| **F** — deep sampler | `behavioural` | randomised deep space; **the only mechanism that finds unconceived values** | nightly, promotion path to L |

Family **E** is the highest-leverage idea either document had and it survives
intact: #166, #171, #173 and the `goty.rs` fieldset collision are all "compiles
clean, behaves wrong" — invisible to `build-run` and to the differential oracle,
but **visible in the emitted Go**. Emit-shape assertions cost no `go build`.

Family **F** is retained explicitly against the objection that the axes are mined
only from known bugs. Mined axes cannot produce a value nobody conceived; the
random deep sampler can. Its promotion path is mechanical: a find is pinned as a
coordinate in `manifest.toml`, and the generator auto-expands its distance-1
neighbourhood. The matrix ratchets.

**The generator draws from the real stdlib symbol table.** Collision types and
field-name sets are sampled from `api/symbols.json` (§5), not invented. This is
the amendment the grills attached to choosing generation over a hand-written
kitchen-sink app: a generated hostile module graph that collides against
*fictional* stdlib names cannot reproduce #164 or the fieldset collision, both of
which required real stdlib names in scope.

### 3.2 Batching is not semantics-preserving — the forbidden families

**This is a blocking correctness finding, verified in source.**

`record_fieldsets` is built over the **whole compilation**
(`lower/src/lower.rs:246-266`), keyed on the sorted field-**name** vector, and its
own comment at `:256-258` records that *"Two records with identical field names
but different field types collide here."* The TEA-Model heuristic then picks the
first `(Record, Cmd _)` candidate in a stable `(module, name)` order
(`lower.rs:267-278`), and `goty.rs:186-196` resolves **any** strict-subset record
whose field names are all in the Model's set to that nominal Model `_R`.

> Batch N TEA-shaped cases into one compilation unit and **N−1 of them resolve
> their subset records against the wrong Model.** The batching optimisation
> silently destroys the exact defect class the corpus exists to catch.

**Four families are `isolation = "unit"` — one compilation unit each, no batching:**

1. **fieldset collision** (same field names, different field types, incl. the
   erased-`any` recurrence via `fst`/`snd`/tuple destructure)
2. **TEA / subset-record** (anything producing a `(Record, Cmd _)` candidate)
3. **import-shape / module-graph** (the whole-program name resolution is the
   subject under test)
4. **anonymous-record field ordering** (canonical struct field order is a
   whole-compilation property)

**The isolation gate** — `corpus.isolation`. A deterministic sample of cases
(seed derived from the commit sha, so the sample rotates) is run in **three
configurations**: alone, in-batch, and in a **shuffled** batch (shuffling
perturbs the `(module, name)` order the Model heuristic depends on). All three
must produce **identical verdicts**. A divergence is a FAIL, and it is the only
mechanism that will notice when a new family starts depending on
whole-compilation state.

### 3.3 The one cost line

```
                N_s·c_s   +   N_iso·c_u   +   ceil(N_b / K)·c_u
    T_L1   =   ─────────────────────────────────────────────────
                                    P
```

| Term | Meaning | Source of the number |
|---|---|---|
| `N_s` | static + emit-shape cases (in-process, shared world) | topology's ~1,500 lives here |
| `c_s` | per-static-case cost | **§1.3 C-2 output — currently unknown** |
| `N_iso` | cases in a forbidden-from-batching family — **one compilation unit each** | the term **both v1 documents missed** |
| `N_b` | batchable behavioural assertions | corpus's ~7,000 lives here |
| `K` | assertions per compilation unit | corpus's ~62 units ⇒ K ≈ 110 |
| `c_u` | per-compilation-unit cost (build + run), **warm**, on the runner class | host-measured 0.83 s warm / 4.53 s cold (`ci-corpus-proposal.md:82-86`); runner value **unknown** |
| `P` | achieved in-job parallelism | measured, not `nproc` |

**`N_iso` is the term that can break the budget**, and it is therefore **capped by
the manifest**: a gate fails if `N_iso` exceeds its declared ceiling. Growing a
forbidden family is a *budget decision*, made visibly, not a side effect of
adding cases. First-cut estimate from the corpus doc's family sizing
(module_graph 7 × 9 ≈ 63; fieldset collision ≈ 20; TEA/subset ≈ 30; anon field
order ≈ 15) puts `N_iso` ≈ **130 units** — at warm `c_u` and P = 4 that is
27–49 s on host numbers, and it must be re-measured on the runner.

**The `corpus` job is already over budget before Layer 1 exists** — measured at
~142 s on host, ≈ 5 min derated, against v1's 4-min budget. So `B_L1` is not
"whatever is left"; the corpus job's budget is re-derived in Phase 6 from
measured pre-Layer-1 content, and `N_max` follows from that (§2.1).

### 3.4 What Layer 1 structurally cannot do

Stated so it is never claimed: SSE lifecycle, cookie expiry against wall-clock,
cross-process gob, multi-replica session routing, reverse-proxy topology, a real
SQL engine's behaviour, a browser's DOM. That is Layer 2's entire charter (§6),
and it is why Layer 2 is not optional.

---

## 4. Falsifiability is PER ITEM

### 4.1 Why per-gate is not enough — the one-byte golden survives it

v1's model was one falsifying mutation per gate, verified by a substring match
over aggregate stdout. That model has **no notion of per-item vacuity**, so:

- `rust/crates/xtask/golden/55-store-partial-update.stdout` is **one byte** (a
  bare newline) — verified. No successful path through that program produces
  empty stdout, so the golden encodes a run where the Task chain died before any
  `println`. `bless_goldens` only refuses on rust ≠ oracle, and the oracle failed
  identically in that environment. **The item is green on total failure**, and a
  gate-level mutation elsewhere in the corpus keeps the gate red-able while this
  item asserts nothing.
- `35-composite-generics`' golden **did not catch #173**. Commit `7425fd00` says
  in its own words that it **froze** the zeroed-aggregation bug, blessed because
  rust == oracle. "Both sides agree" is not evidence when both sides are wrong.
- Measured vacuity in the live suites: **18 unconditional `Test.pass` calls** —
  7 in `tests/conformance/`, 11 in `examples/*/tests/` — each a test case that
  cannot fail.

### 4.2 The per-item contract

Every corpus item declares a **family-typed perturbation** — mechanical, declared
once per family, never hand-authored per item:

| Item kind | Perturbation | Must go red |
|---|---|---|
| accept + value assertion | mutate the **expected value** | that item's own comparison |
| accept + emit-shape | mutate the item's **source** along its declared axis | that item's own shape assertion |
| reject | **neutralise the axis** (→ the paired twin, §4.4) | the twin must be **accepted**; the original must reject **with the declared code** |
| golden stdout | append a sentinel line to the program's output | that item's own golden comparison |
| behavioural assertion | negate the expected value | that specific `Test` leaf |

**An item whose own comparison cannot go red is `VACUOUS`, and the ITEM fails** —
not the gate, the item, by name. That is the structural kill for the one-byte
golden, for `Test.pass`, and for the 35-composite class.

**Three additional hard rules:**

1. **`bless_goldens` refuses empty or whitespace-only captures.** A golden that
   would encode "prints nothing" is an error at bless time, not a passing item at
   run time.
2. **"Both sides failing identically proves nothing" generalises to every
   oracle-differential assertion.** If both sides are in a failure state, the
   assertion is `INCONCLUSIVE` — never PASS. This covers `divergences`,
   `welltyped`, and `bless_goldens`. Note `welltyped_gate.rs:41-45` currently
   **SKIPs with exit 0** when no oracle binary is discoverable, and the oracle is
   unavailable in CI — so `welltyped` is a permanently green no-op on every CI
   run today.
3. **`35-composite-generics`' expectations migrate to hand-written values, never
   a re-bless.** Its `tests/` directory already carries **21 `Test.test` cases /
   10 `Test.equal` calls** (measured) — the migration target exists. The template
   is `ty/tests/inferred_sig_snapshot.rs:110-126`, whose accept-parity guard
   asserts the fixture type-checks clean **before** the snapshot pins anything,
   so a snapshot can never encode a broken program.

### 4.3 Affording it

Per-item self-falsification doubles work. It is tiered by mode:

- **`static` / `emit_shape` items** — perturbation is in-process at `c_s`. **All
  of them, every push.** Cost: `N_s × 2 × c_s`, already in §3.3's model.
- **`behavioural` / `unit` items** — perturbation costs `c_u`. A **rotating
  deterministic shard** runs per push (`hash(item) % D == commit_seq % D`), and
  **all of them run in T3**. An item whose last successful self-falsification is
  older than its declared window renders **`UNVERIFIED-SINCE`** — a non-PASS
  state, distinct from FAIL because the proof is unrevalidated, not known-broken.
  Conflating the two trains people to ignore the signal.

**Prerequisite that does not exist today:** `Sky.Test`
(`sky-stdlib/Sky/Test.sky`) exposes `run` / `summarise` / `runMain` and has **no
JSON reporter** — verified. Per-item attribution inside a batched unit is
impossible without one. Phase 1 adds a machine-readable reporter
(`SKY_TEST_JSON=<path>` honoured by `runMain`, emitting one record per leaf with
its name, outcome, and assertion count). This is also what makes §7.4's
`assertions == expected_count` contract implementable for behavioural gates.

### 4.4 Generated cases: independent oracle, class, and axis witness

**The problem.** Generated cases have no independent oracle. A generated case
whose "expected" value is whatever the compiler produced is a **change-detector,
not a correctness test** — it would not have caught #173 on the day #173 shipped.
The Haskell oracle cannot close this: it is unavailable in CI, and
`welltyped_gate.rs:41-45` SKIPs with exit 0 without it.

**Class V — independently verifiable.** The generator *constructs* the answer.
The enumeration is exhaustive and closed; a family not on this list may not carry
a value assertion:

1. constant-folded arithmetic over literals the generator emitted
2. `List.length` of a list the generator constructed
3. record-field identity — `.f { f = k, … } == k` for a generator-chosen `k`
4. tuple projection identity
5. `Dict.get` of a key the generator just inserted
6. string concatenation of generator-chosen literals
7. `List.map identity` round-trip over a constructed list
8. ADT constructor → `case` round-trip on a generator-chosen variant
9. `Maybe` / `Result` wrap → unwrap round-trip

**Class D — change-detector.** Everything else generated. Carries accept/reject
and emit-shape assertions only, is **labelled `D` in the manifest**, and is
**excluded from the coverage number** in §5. This is the honest accounting the
mandate's "do not quietly redefine the target" demands.

**The axis witness — what makes the coverage percentage falsifiable.**
A case that varies an axis but asserts something independent of that axis does
not cover the axis; it only spends budget. So:

- **Accept-case witness:** the generator must produce, for the same case with the
  axis **neutralised**, a *different* expected value or a *different* emit-shape
  fingerprint. If neutralising the axis leaves the assertion identical, the case
  **does not witness its axis** and fails the witness gate.
- **Reject-case witness:** assert the **diagnostic code**, not merely "rejected"
  — this also closes the audit's "reject_gate accepts rejection for the wrong
  reason" defect — **and** ship a **paired twin** with the axis neutralised that
  **must be accepted**. The pair is what proves the rejection is caused by the
  axis under test and not by an unrelated error elsewhere in the generated
  program.

Without the witness requirement, a coverage percentage over generated cases is
unfalsifiable. With it, "this axis is covered" is a claim that can be shown false.

---

## 5. Denominators — one script, one definition, no hand-counting

### 5.1 The measured truth, and the three wrong numbers

`sky doc --export` writes `api/symbols.json`. Measured by `xtask denominators`:
**1,746 entries / 87 modules / 1,625 values / 121 types.**
The topology doc's 1,762 / 1,640 / 122 is wrong; so was the corpus doc's
1,744 / 1,623, and so was this section until the script existed to check it.
That is the point — **every one of these was a hand-count, and every hand-count
was wrong or went stale.** The number above is now reproduced by
`xtask denominators` and checked in at `docs/coverage/denominators.json`; if a
number here and a number there ever disagree again, the JSON wins.

### 5.2 FIVE ways the denominator silently shrinks — all exit 0

Every one of these makes "100 % covered" easier to claim by making the
denominator smaller. Paths 2–5 are now **hard failures**; path 1 is legitimate
for the published docs and is instead reported twice, filtered and unfiltered.

| # | Path | Site (pre-fix) | Status |
|---|---|---|---|
| 1 | a module narrows its `exposing` list | `is_exported` filters against `exposing_set` | reported BOTH ways — `stdlib.entries` vs `stdlib.unfiltered.entries` |
| 2 | a module loses its header | `if let Some(name) = header_name(&src)` with **no `else`**; the module vanishes | **FAILS** |
| 3 | a file becomes unreadable | `let Ok(src) = … else { continue; }` | **FAILS** |
| 4 | a module fails to **parse** | `module_symbols` called `syntax::parse` and **never inspected its errors** — `grep -n "errors()" doc.rs` returned zero hits in all 1,622 lines | **FAILS** |
| 5 | a file becomes unreadable *between* enumeration and render | `read_to_string(path).unwrap_or_default()` on the export hot path — the file degraded to an EMPTY module (zero symbols), not a dropped one | **FAILS** (the bytes are now read once, in the strict enumeration, and passed along) |

Path 5 was missed by the first draft of this section. All five now live in
`collect_module_sources` (`rust/crates/project/src/doc.rs`), which reports rather
than swallows. The one explicit exemption: strictness applies to `sky-stdlib/`
(the surface that IS the denominator), not to a user's project `src/`, so a
developer with a half-written module can still run `sky doc`.

Additionally, **6 modules use `exposing (..)`** — `Sky.Core.Error`, `Std.Css`,
`Std.Html`, `Std.Html.Attributes`, `Std.Html.Events`, `Std.Ui` — contributing
**593 entries, 34.0 % of the 1,746**. For those, the denominator is "every
top-level declaration", including helpers never intended as API. The remaining
**81 modules contribute 1,153** curated entries, and their `exposing` lists hide
a further **222** declarations (1,968 unfiltered in total). These are three
different numbers answering three different questions and the ledger never
averages them.

### 5.3 The contract

- **ONE committed script** — `xtask denominators` — produces **every** denominator
  the design quotes, and writes `docs/coverage/denominators.json`, **checked in**.
  No document, ledger, or verdict may quote a number this script did not produce.
- **A gate fails on any decrease** without a matching entry in
  `docs/coverage/removals.toml` (symbol, reason, owner, commit).
- **`sky doc --export` fails** on: a parse error in any stdlib module, a
  header-less module, an unreadable module. Silence becomes an error.
- **The 6 `exposing (..)` modules are reported separately** with their unfiltered
  count, and migrating them to explicit `exposing` lists is a tracked task. Until
  then the ledger reports two numbers — filtered and unfiltered — and never
  averages them.
- **The language denominator is `SyntaxKind::KINDS`**, which is **macro-generated
  and total (124)** — *not* the topology doc's hand-counted 72. `KINDS` is now
  `pub` (it was private, so no test could reach it), and the committed
  classification table `syntax::kind_class::KIND_CLASSES` assigns every kind to
  `Construct` or `NonConstruct(reason)`: **80 constructs, 44 non-constructs**.
  `kind_class::assert_total()` is the gate — it runs as a `cargo test` and again
  inside `xtask denominators`, which refuses to report a language denominator
  computed from an incomplete table. A newly added kind fails the build until
  classified. This also makes detectable the live hole that the `can_cast`
  `matches!` lists (`syntax/src/ast.rs:155,208,297,364`) are not compiler-checked:
  a new node kind that someone forgets to add to `Expr::can_cast` still has to be
  classified here, and classifying it `Construct` puts it in the denominator.

**Running it.** `xtask denominators` recomputes, ratchet-checks, and rewrites
`docs/coverage/denominators.json`. `xtask denominators --check` is the CI form:
it never writes, and fails if the checked-in file is stale or if any denominator
fell without an accounting entry.

### 5.4 "Assertion" and "case" — defined once, measured here

The two v1 documents' "772" and "575" were **the same body of tests counted two
different ways**. Measured on this branch:

| Body | **Cases** (`Test.test`) | **Assertion calls** | Of which `Test.pass` (vacuous) |
|---|---|---|---|
| `tests/conformance/tests/` | **772** | **776** | 7 |
| `examples/*/tests/` (6 suites) | **63** | **95** | 11 |

Breakdown for conformance: `equal` 567 · `err` 79 · `isTrue` 56 · `fail` 24 ·
`isFalse` 23 · `ok` 12 · `notEqual` 8 · `pass` 7.

> **Definitions, used everywhere from here on:**
> a **CASE** is one `Test.test` leaf. An **ASSERTION** is one call to
> `Test.{equal, notEqual, ok, err, expectErrorKind, isTrue, isFalse, fail}`.
> `Test.pass` is **not** an assertion — it is counted separately and reported as
> **vacuous**. The topology doc counted cases; the corpus doc counted
> `equal`+`notEqual` only. Both are superseded.

The 776 and 95 in the table above **include** `Test.pass`, which the definition
box excludes — the two readings were never distinguished, so the ledger now
reports both explicitly and neither can be mistaken for the other:
`tests.conformance.assertions` = **769** (strict, `pass` excluded) and
`tests.conformance.assertion_calls_incl_pass` = **776**; for the example suites,
**84** and **95**. `expectErrorKind` has **0** call sites in either body — a real
zero, reported rather than omitted. All of these come from `xtask denominators`.

### 5.5 What "100 % coverage" will and will not mean

Reachable and reported exactly:

- **Unary, 100 %:** every one of the 1,625 public stdlib values and every one of
  the 80 classified `SyntaxKind` constructs has ≥ 1 **class-V** assertion.
- **Pairwise:** 100 % of all-pairs across the structural axes, reported as a
  computed percentage.
- **Defect neighbourhoods:** exhaustive at distance 1 from every pinned
  coordinate.

Explicitly **not** 100 %, and named rather than hidden:

- 3-way and higher interactions not adjacent to a known defect (sampled by
  family F, not enumerated).
- **Class-D generated cases are excluded from the numerator** — they are
  change-detectors, and counting them would inflate the number with cases that
  cannot establish correctness.
- Semantics only a real runtime exhibits (Layer 2's charter).
- Third-party Go FFI surfaces — covered by scale, not by matrix.

Exemptions are **explicit, counted, and owned** — the in-tree precedent is
`rust/crates/project/tests/kernel_surface.rs:14-16`, which records *why* it is an
allowlist rather than a total scan. Totality requires an exception list, not just
an enumeration.

---

## 6. Layer 2 — reconciled

The corpus doc's shape wins (four apps + one topology **scenario**, because a
scenario over the real app cannot rot into a mock), with the topology doc's
CLI-verb owner grafted on and two members it never mentioned.

| | Member | Kind | Uniquely owns | Tier |
|---|---|---|---|---|
| **A** | **Ledger** | Sky.Live full-stack | sessions/CSRF/SSE · `Std.Auth` · `Std.Db.{Schema,Migrate}` with a **committed `migrations/`** · `Std.Jobs` · `Std.Money`/`Decimal` · **Postgres arm** | T1 (SQLite) / T3 (Postgres) |
| **B** | **Relay** | headless HTTP | `Sky.Http.Middleware` · `RateLimit` · WebSocket **both halves** · SSE emit+consume · `Std.{Config,Trace,Cache,PubSub,Csv}` | T1 |
| **C** | **Fieldbook** | one `Std.Ui` view, four backends | cross-backend `Std.Ui` parity (Live/Tui/Webview/Cli) · `Std.Ui.Events` | T2 (Live+Tui) / T3 (Webview) |
| **D** | **Storefront** | Go FFI at scale | 76 k-symbol FFI · `.skydeps` · external Sky package · OAuth · **the LSP real-codebase corpus** | **T4 pre-release** |
| **E** | **Fleet** | topology **scenario over Ledger** | multi-replica · Redis broker · sticky sessions · console hub · `ENV=production` | T3 |
| **F** | **`sky-bundled/console` + `sky-bundled/doc`** | shipped Sky source | **5,746 lines linked into every emitted binary**, gated by nothing today | T1 build / T2 drive |
| **G** | **CLI verbs** | `rust/crates/sky/tests/*_flow.rs` — **not an app** | `db init/gen/migrate/status/seed/push` · `doctor` · `upgrade` · `init` · `watch` · `clean` · `add/remove/install/update` | T1 |

**Why G is not an app.** Making a project responsible for `sky doctor` couples a
CLI verb's coverage to an app's build health. Flow tests own the verbs directly,
in-process, in seconds.

**Layer 2 keeps the accrete-by-accident property.** #166's reporter could not
reproduce it in isolation and had to list the co-occurring legs. Layer 1 covers
combinations it *enumerates*; Layer 2 covers combinations that arise because
products accrete. Neither subsumes the other, and Layer 2's cost must not be
allowed to crowd out Layer 1's completeness — which is why D is T4 and E is T3.

### 6.1 Layer-2 obligations the grills added

- **Postgres coverage is zero today.** Measured: 58 example directories, **7**
  declare `[database]`, **7** use `driver = "sqlite"`, **0** use `postgres`. The
  Ledger Postgres arm is *new* coverage, not a replacement.
- **`liveInto` on the Postgres arm must assert a verdict** — deliver or fail
  loudly. "No crash" is not an assertion.
- **TTL-shortening and real-duration idle are different tests. Keep both.**
  A short `SKY_LIVE_TTL` tests *expiry logic*; it does **not** test the 60–90 s
  SSE/proxy idle behaviour that produced the CSRF-idle incident. Short-TTL runs
  in T1/T2; the real-duration idle hold runs in T3.
- **`sky db migrate` needs a destructive-diff refusal assertion** — a migration
  that would drop a column must be refused, and the refusal asserted.
- **`55` is the only implicit FFI driver-resolution coverage** in the repo. Its
  replacement must own that explicitly before it is deleted.
- **`sky verify` is invoked by ZERO scripts and ZERO workflows** — verified: every
  occurrence in `scripts/` and `.github/` is a comment. It is the runner for the
  `examples/*/tests/` suites (**6 suites / 63 cases / 95 assertions**).
  **Wire it before any migration depends on it** (Phase 1), or the migration will
  "move" coverage into a runner that never runs.

---

## 7. The harness — built, not adopted

### 7.1 Re-scoping Phase 1

The BlueDB gate registry on `feat/bluedb-v2` was v1's stated precedent for
wholesale adoption. Measured by a griller: **41 of 48 gates stubbed**, **7 of 48
mutations verified**, its timeout **kills nothing** (zero hits for
`kill|process_group|setsid|pgid`), and its **mutation probe has no timeout**.

> **What is adopted:** the *shape* — a static registry, five states, and the
> const-evaluated non-empty `Mutations` constructor (a gate declaring no mutation
> fails the **build**). That property is genuinely good and is kept.
>
> **What is built:** every behaviour. Phase 1 is **build**, and the harness ships
> with self-tests proving each behaviour, because a harness whose own timeout
> does not kill is the "green-lie generator" risk realised.

### 7.2 States — four, not five

| State | Meaning | Effect on the area verdict |
|---|---|---|
| `PASS` | ran, all assertions held, **and `assertions > 0`** | PASS |
| `FAIL` | an assertion broke, **or** the budget was exceeded, **or** an item was `VACUOUS` | **FAIL** |
| `NOT RUN` | registered but not executed (harness error, wrong tier) | **UNKNOWN → exit non-zero** |
| `BLOCKED` | structurally impossible now; requires an issue link **and an expiry** | never PASS; **FAIL after expiry** |

Plus two **proof** states, orthogonal to the run states: `UNVERIFIED-SINCE`
(§4.3) and `NOT APPLICABLE` (below).

**Deleted from v1: `DEGRADED`.** A runtime scope reduction that still reports
non-failure is the silent-scope-reduction class the mandate exists to kill. The
cache-cold-P1 scenario that motivated it is solved by **declaration**: Storefront
is T4 (§6). Tiering is declared in the registry, never chosen at runtime.

**`NOT RUN → UNKNOWN` exits non-zero.** v1 left this ambiguous. A run that cannot
say whether a gate passed has not passed.

**`--only` produces `NOT APPLICABLE`, not `UNKNOWN`.** Deliberate selection is not
an unknown. Conflating them makes local development produce `UNKNOWN` constantly,
which trains people to ignore it — the same failure mode as a soft `BLOCKED`.

**Rows are rendered from the REGISTRY, not from the run's results.** A gate cannot
disappear by not executing. That single property kills the "SKIP counted as pass"
class at the root.

### 7.3 Gate bodies run in a CHILD PROCESS

Not a worker thread. Two reasons, both fatal to the thread model:

1. **"Kill the process group" is unimplementable from a thread.** A thread's
   spawned children are not reachable as a group; our gates spawn servers,
   `go build`s, browsers and PTYs. A timeout that leaks a process holding a port
   poisons every later gate.
2. **An orphaned thread can write a result after its gate was recorded FAIL** —
   corrupting a **later** gate's verdict. This is worse than the timeout leak
   because it produces a *wrong green* attributed to the wrong gate.

Design: each gate body is `fork`/`setsid`'d into its own process group; the
runner waits with a deadline; expiry → `killpg(SIGTERM)` then `SIGKILL`; the
child's result is read from a file stamped with a **generation counter**, and a
result whose generation does not match the currently-awaited gate is
**discarded**. Timeouts live in the harness, never in GNU `timeout` — which is
absent on every macOS runner, the exact hole that leaves `conformance.sh` running
unbounded there today.

### 7.4 No `grep` in a verdict path — and the wrapping contradiction, resolved

v1 said both "no grep in a verdict path" (§5.3d) and "no verifier is rewritten;
they are wrapped" (§7.5). **Wrapping a text-emitting script means parsing text.**
The two rules contradict.

> **Resolution:** each wrapped verifier gains a `--json <path>` output mode — a
> small, additive change, not a rewrite — and the gate reads the **file**, never
> stdout. The gate asserts `assertions == expected_count`, an **exact** count from
> the registry, never a `>=`. (`ty/tests/reject.rs:121,147` asserts `>= 13`
> against an actual 63: deleting 50 corpus files keeps it green today.)
>
> Adding `--json` is the price of the rule. It is paid once per verifier and it
> is what makes the verdict mechanical.

This also kills the unanchored-substring class by construction: `grep -qE "0 fail"`
matching inside `"10 fail"`, and `grep -qF "sky v$TAG"` where `v0.19.1` is
satisfied by `v0.19.10`.

### 7.5 `--verify-falsifiers` — costed, and moved

Uncosted in v1. Measured: **~13–24 s cold build per mutation × ~50 gates = 2–3 h.**
It cannot sit inside T3's 90-minute ceiling.

- **Batch by crate set** so the dependency build is shared across mutations
  touching the same crate; only the mutated crate rebuilds.
- **Move it to its own scheduled workflow** with its own ceiling and its own
  required-check semantics. Its result decays: a mutation not re-verified within
  its declared window renders `UNVERIFIED-SINCE`.
- **The mutation probe gets a timeout** (the precedent's has none).
- **The canary is retained**: a permanent gate asserting `true`, paired with a
  no-op patch. A correct verifier must report `VACUOUS` for it; reporting
  `PROVEN` is a harness failure. It is the one place a *passing* gate is the
  failure signal, and it is the only construction that catches a verifier whose
  every answer is "green".

### 7.6 Concurrency — a persistent semaphore, and `EAGAIN` = FAIL

v1's `outer_workers × go_build_p ≤ cores` model misses the multipliers that
actually exhaust the process table: **Playwright browser contexts, per-project
servers, PTYs, nested `cargo`**, and `xcrun` under `go build` on macOS.

- **A persistent semaphore** (a file-lock counter in the runner's temp dir, held
  across nesting levels and across gate processes) budgets **total live spawned
  processes**, not workers. Every spawn point in the harness acquires; every
  teardown releases; a crashed holder's slot is reclaimed by lock expiry.
- **`RLIMIT_NPROC` pre-flight is TOCTOU** and is removed. It is replaced by the
  only reliable signal: **`EAGAIN` on spawn is a gate FAIL** — never a retry
  loop, never a silent skip. A run that could not fork did not test anything.
- Ports: the harness binds `:0`, reads the actual port, passes it in the env; a
  gate forbids bind-position port literals in project source; teardown is
  `killpg` **and asserts the port is released**.

---

## 8. Tiers — with `setup`, and a mechanism that enforces them

### 8.1 The table

**T1 = `setup` + `max(jobs)`.** v1's 9-minute figure omitted `setup` entirely
while making it the fan-in root of the job graph. Dropping the fan-in would
restore the six duplicate `cargo build --workspace` steps, so `setup` is kept and
**budgeted**.

| Tier | Trigger | Ceiling | Decomposition | Status |
|---|---|---|---|---|
| **T0** pre-commit | local hook | **60 s** | changed-file work only | firm |
| **T1** per-push / PR | `push`, `pull_request` | **15 min** | `setup ≤ 6 min` (≤ 3 cache-warm) + `max(job) ≤ 9 min` | **PROVISIONAL — §11-U2** |
| **T2** merge queue | `merge_group` | **25 min** | `setup` + `max(job) ≤ 19 min` | provisional |
| **T3** nightly | `schedule` | **90 min** | matrices, long-hold timing, visual snapshots, full per-item falsification | firm |
| **T4** pre-release | `push: tags: v*` + manual preflight | **3 h** | every platform, asset build, **install-and-run the built asset** | firm |
| **(separate)** falsifier verification | `schedule` | own ceiling | §7.5 | firm |

T1 is **15 min, not 9**, because 9 was arithmetic that omitted its own critical
path. A griller's realistic estimate was 13–20 min. 15 is provisional and is
either confirmed or triggers §2.2's escalation at Phase 6 — the same abort
discipline as case counts, applied to time.

### 8.2 Enforcement — because `timeout-minutes` alone does not

**The v1 defect:** `timeout-minutes` = 1.5× the *tier* budget cannot enforce a
tier ceiling. 13.5 min per job × 2 sequential jobs = 26 min, and everything
passes a "9-minute tier".

**Two mechanisms, both required:**

1. **Per-job `timeout-minutes` = 1.5× that job's OWN estimate.** A job that
   doubles its own cost fails, locally, where the cause is.
2. **`ci-green` asserts the tier.** Each job records its own start/end and emits
   them as outputs; `ci-green` computes
   `setup_elapsed + max(job_elapsed) ≤ tier_ceiling` and **FAILS** otherwise.
   Job *elapsed*, not wall clock — queue time between jobs is not ours to
   control and must not be charged to the budget. A 10 % grace absorbs runner
   variance; beyond that it fails, because a budget that does not fail is not a
   budget.

### 8.3 A TZ matrix — a UTC-only runner is blind to a shipped bug class

`Time.timeString`'s host-TZ defect cannot be seen by a UTC-only runner, and every
GitHub runner is UTC. The time-sensitive conformance suites therefore run under
**≥ 3 `TZ` values**: `UTC`, a non-integer offset (`Asia/Kolkata`), and a
DST-transitioning zone (`America/New_York`). Cost is an env var and a re-run of
one suite — the cheapest coverage in this document.

---

## 9. Migration — nothing deleted before its replacement is proven

### 9.1 The ordering rule, and the rollback story

> **Deletions land as their own tagged commit, immediately after the commit that
> proves the replacement.**

- The proving commit is tagged `pre-delete/<phase>`.
- The deletion commit touches **only** deletions.
- **Rollback** = `git revert` of the deletion commit alone. Coverage returns
  without reverting the replacement. v1 had no rollback story at all.

### 9.2 Coverage conservation — proven, not asserted

**The sole-ownership table is generated mechanically** by a committed script,
never hand-maintained. It must carry, at minimum, the measured facts: **11
examples solely own stdlib modules**; **zero Postgres coverage exists today**
(7 of 58 declare `[database]`, all 7 sqlite); **`55` is the only implicit FFI
driver-resolution coverage**.

**Goldens are re-keyed BEFORE any deletion.** Measured: `coerce_floor.golden` is
**59 lines summing 9,510 tokens**, concentrated in examples slated for deletion;
the 40–55 fixture block is only **6.2 %** of it. Deleting those examples first
would retire the soundness ratchet over ~94 % of its measured surface.

> **The conservation assertion**, run once, on the re-keying commit:
> `sum(new tokens) ≥ sum(old tokens)` **and** `count(new rows) ≥ count(old rows)`.
> The normal FAIL-ON-INCREASE ratchet resumes after it. (During normal operation
> a decrease is good; across a re-keying it means measured surface was *lost*,
> not moved. The two must not be conflated.)

The same applies to the 24 stdout goldens: re-keyed onto the new corpus, count
proven non-decreasing, **before** any directory is removed.

### 9.3 `examples/` — the correction

v1's "drop `examples/` to `sky check` — cheap, no `go build`" is **false**:
`sky check` ≡ `sky build`, both invoke `go build`. So the change does not save
the build; it drops the **run**, and "an example that compiles but no longer
runs" becomes a state only the nightly sweep catches.

> **That is an unledgered coverage removal and it gets a ledger row**, with its
> replacement named (Layer 2 drives + the L1b behavioural family) and proven,
> like every other row.

The **example-reachability gate** proposed in v1 (every example referenced by a
doc page or deleted) is **already green today**, and its only red is
`35-composite-generics`. A gate that is green on arrival asserts nothing about
the future unless it can go red: it ships with a falsifier (add an unreferenced
directory → must go red) and its scope is stated honestly as *anti-rot*, not as
coverage.

### 9.4 Phases

Each phase is independently shippable, leaves the repo green, and names what is
deleted, what is kept, and what must be proven first.

| Phase | Content | Deletes | Proof obligation |
|---|---|---|---|
| **0** | Apply the 777-line pending gate-fix patch + the four higher-blast-radius defects it omitted. **Reconcile the two reject faces** (§1.5). Add `timeout-minutes` to all 15 jobs, `-D warnings` to clippy. **Wire `sky verify`** (§6.1) | nothing | each fixed gate goes RED on its named reproduction before the fix, GREEN after |
| **1** | **Build** the harness (§7): four states, child-process bodies + `killpg` + generation counters, JSON results, manifest test, `SyntaxKind::KINDS` `pub`, **`Sky.Test` JSON reporter** (§4.3). Register every existing gate with its body unchanged | nothing | verdict-identical results against 3 known-good + 3 known-bad commits. Any divergence is a port bug |
| **2** | Per-item falsification model (§4). Author family-typed perturbations. Run the canary | nothing | zero `VACUOUS`; canary reports `VACUOUS`; **the one-byte golden and all 18 `Test.pass` sites are found by the mechanism, not by audit** |
| **3** | **Route C** (§1.3): the `hir::resolve` memo (+ its `add_module` invalidation), then the incremental `World::build`. Fork-per-case + the `DefId`-disjointness gate + the shadowing fallback | duplicate emissions, **not their assertions** | a differential harness asserts **identical per-item verdicts** old vs new over the reject + infer corpora. **Publish `c_measured` against the 1.293 s baseline** |
| **3.5** | **The ~100-case spike** (§2.3): measure the red rate | nothing | red rate reported; quarantine policy adopted only if the data demands it |
| **4** | Layer 1 at the **derived** count (§2.1). Generator, manifest, families S/L/E/R/F, class-V/D labelling, axis witnesses, isolation gate, ledgers | the migrated example directories, **after** each case reproduces its original defect on a reverted fix | every migrated case fails on the reverted fix; coverage reported as a number with class-D excluded |
| **5** | Layer 2 (§6), members built one at a time | an example leaves the corpus only when the ledger shows its surface owned elsewhere | the §9.2 conservation assertion, every row green |
| **6** | CI topology: `setup` + artifacts, `ci-green` fan-in **with the tier assertion**, cache keys, gating `release.yml`. **Measure T1 on ten real PRs** | the six duplicate `cargo build --workspace` steps | T1 holds 15 min, or §2.2's escalation fires |
| **7** | Reconnect the unreachable tiers: `setup-node` + `npm ci` + browser caching; wrap the Playwright verifiers, `example-e2e.sh`, `welltyped`, `sky-hub` in registered gates with `--json` | orphan scripts with no caller **and** no unique assertion, only after listing what each asserted | every orphaned assertion either runs or carries a `BLOCKED` row with an issue and an expiry |
| **8** | Prune `examples/` to its documentation contract | duplicative chains, `simple`, `test_pkg` | every survivor is doc-referenced and builds |

### 9.5 `ci-green` and branch protection — the ordering that cannot un-gate

Never remove-then-add. The sequence is:

1. Add `ci-green` as an **additional** required check.
2. Run the old jobs **alongside** the new graph for a stated window (N green
   pushes / M days).
3. Remove the old job names from branch protection.
4. Delete the old jobs.

Any other order leaves a window in which `main` is ungated.

---

## 10. What both grills marked NON-ISSUE — kept unchanged

These survive from v1 and are **not** re-opened:

- **`repro` + `golden` + `coerce-floor` + `conformance` on BOTH platforms.** They
  exist to catch platform-dependent divergence; one platform is not a weaker
  version of the gate, it is a different gate.
- **The reject corpus keeps both faces** (after §1.5's reconciliation). Only the
  world rebuild inside each is removed.
- **`repro`'s ≥ 2 fresh-process emissions.** The assertion *is* the multiplicity;
  collapsing it to a shared artifact deletes the assertion.
- **`coerce-floor`'s FAIL-ON-INCREASE.** A ratchet only works if never relaxed.
- **`fuzz`** — the only robustness gate. Budget exhaustion becomes a FAIL rather
  than a printed note.
- **The sweep's clean-slate wipe + forced `sky install`** — the only thing proving
  a fresh clone builds. Moves to T4, keeps the wipe.
- **`verify-all-web` / `verify-cli`'s "click is a no-op" coverage** — the class
  that shipped the v0.13 `Std.Ui` event-emission regression. Wrapped, never
  rewritten.
- **Release-built gates.** Already justified in-tree (`preflight-tag.sh:65-74`:
  *"an unoptimized xtask made them ~10× slower"*); `reject` measures 780 s debug
  vs 74 s release for an identical verdict. CI never got the fix.
- **The gate-name manifest test.** Parse gate ids out of `.github/workflows/*.yml`
  and assert each exists in the registry — a typo'd gate name is a permanently
  green no-op today. It is a file read, so it runs in T0.
- **The platform coverage ledger.** T4 asserts every registered gate `PASS`es on
  at least one platform, making "verified nowhere" (`11-fyne-stopwatch`)
  impossible to express.
- **Content-addressed corpus artifacts** keyed on `hash(compiler) + hash(item)`,
  so a docs-only PR does no corpus work.
- **One `corpus()` / `collect_sky` / `load_dir`.** Six copies is how two gates
  silently come to disagree about what the corpus is — §1.5 is that in action.

---

## 11. Uncertainty register — what must be measured, before which phase

Stated explicitly, per the mandate. Each row names the phase it gates.

> **U1 is RESOLVED and three premises in §1.3/§1.4 proved wrong when this design
> met the compiler.** See [`ci-test-phase-2-3-results.md`](ci-test-phase-2-3-results.md)
> before acting on §1.3, §1.4(a), or the "why C-2 is not optional" argument in
> §1.2. In brief: C-1's invalidation obligation is wider than "drop that entry";
> §1.4(a)'s `corpus.defid-disjoint` gate is unsatisfiable as worded and asserts
> the wrong property; and C-1 alone measured **34.4 ms/case**, not the predicted
> ~318 ms, which already clears every break-even entry. `c_measured` with C-1 +
> C-2 is **1.02 ms/case**, and the differential over 121 corpus items is
> identical. The `PROCEED` branch of §2.2 fires.

| # | Uncertainty | Gates | Resolution |
|---|---|---|---|
| **U1** | **Can `World::build` be made incremental without changing verdicts?** Passes 5–8 read app def bodies and the world is whole-program. Restricting them to the case's modules may change results for cases that shadow or extend stdlib. This is the design's largest technical risk | **Phase 4** | **RESOLVED (Phase 3, 2026-08-10): YES.** `xtask shared-world` compares both paths per item over the reject + infer corpora on error counts, every diagnostic, and every inferred type — **121 items, identical**, 120 shared / 1 counted full-rebuild fallback. `--inject-divergence` is caught in 18/121, so the comparison is live. Two constructions genuinely cannot use a prebuilt world (stdlib-module shadowing; bare-alias collision); both are detected before forking and fall back as counted, reported states |
| **U2** | **`c_u` — behavioural cost per compilation unit on the CI runner class.** Every number in both v1 docs is host-measured, and the 2× derating factor is itself unverified. `N_iso` (§3.3) is budgeted against it | **Phase 4, Phase 6** | **Still open, and now the WHOLE cost model.** The static term collapsed to 1.02 ms/case, so `N_iso × c_u` is the binding constraint on Layer 1's budget rather than a correction to it. Host `c_u` re-measured in Phase 3; the runner-class value and the corpus job budget remain Phase 6 on ten real PRs |
| **U3** | **Is per-item self-falsification affordable for behavioural items, and are family-typed perturbations strong enough?** A perturbation that *any* assertion in a batched unit catches does not prove **this item's** assertion is live. Per-item attribution needs the `Sky.Test` JSON reporter (§4.3), which does not exist yet | **Phase 2** | Build the reporter in Phase 1; in Phase 2, prove attribution on a batched unit by perturbing one leaf and asserting **exactly that leaf** goes red. If attribution cannot be made precise, behavioural items move to `isolation = "unit"`, which raises `N_iso` and re-enters §3.3's budget |
| U4 | The red rate of a neighbourhood-expanding generator | Phase 4 | Phase 3.5's 100-case spike |
| U5 | Whether the 6 `exposing (..)` modules can be migrated to explicit lists without breaking consumers | Phase 4 | ledger reports filtered + unfiltered until migrated |
| U6 | Whether Playwright + browser binaries fit T2's ceiling once cached | Phase 7 | measured in Phase 7; T3 is the fallback tier, **declared**, never a runtime degrade |

### 11.1 The three I am least sure about

**U1**, **U2**, **U3** above, in that order. U1 decides whether the mandate's
combinatorial layer is affordable at all; U2 decides how much of Layer 1 is
behavioural rather than static; U3 decides whether the falsifiability model —
the thing that makes any of the coverage numbers trustworthy — is affordable at
the granularity that actually kills the one-byte-golden class.

---

## 12. Summary of the reconciliation

- The cost fix is a **compiler change** (`hir::resolve` memo + incremental
  `World::build`), not a gate refactor. v1's Route A targets 0.2 % of the cost.
- **Case counts are derived** from a measured per-case cost, with a named abort
  branch. No number in this document that depends on an unmeasured cost is
  presented as decided.
- **One Layer-1 mechanism, one manifest, one cost formula** with three terms —
  including `N_iso`, the forbidden-from-batching term both v1 documents missed
  because batching turns out not to be semantics-preserving for the exact
  families the corpus exists to catch.
- **Falsifiability is per item.** Gate-granular mutation lets a one-byte golden
  and 18 unconditional `Test.pass` calls survive.
- **Tiers include `setup`** and are enforced by a real elapsed assertion, not by
  a per-job timeout that cannot express a tier.
- **Nothing is deleted before its replacement is proven**, deletions are their own
  revertable commit, and the goldens are re-keyed with a conservation assertion
  first.
