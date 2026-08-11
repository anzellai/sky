# CI Corpus Proposal — what the regression corpus should actually be

> ## ⚠️ SUPERSEDED by [`docs/ci-test-architecture-v2.md`](ci-test-architecture-v2.md)
>
> This document was one of **two** parallel designs (the other is
> `docs/ci-test-architecture.md`). Both were adversarially grilled and both
> returned 5 blocking findings. **v2 is the single reconciled design; where this
> document conflicts with v2, v2 wins.** This file is retained for its evidence
> — the measured economics, the 58-example inventory, the coverage census, and
> the Layer-2 app designs — not for its conclusions.
>
> Where the grills adjudicated **in this document's favour** (carried into v2):
> `sky check` ≡ `sky build`; the corpus-size numbers; the stdlib denominator
> (1,744 / 1,623 / 121); generating the kitchen-sink rather than hand-writing it;
> the Layer-2 shape incl. the Fleet scenario; `sky-bundled/console` as a member;
> Storefront at the pre-release tier.
>
> Corrections v2 makes to this document (see v2 §0):
> - **§4.4's batching premise is unsound** for four case families —
>   `record_fieldsets` is whole-compilation and the TEA-Model heuristic is
>   order-sensitive, so batching N TEA-shaped cases resolves N−1 against the
>   wrong Model (v2 §3.2). Those families are one compilation unit each, and
>   that is a new, budget-visible cost term.
> - **Generated cases have no independent oracle.** Only families whose answer
>   the generator *constructs* may carry value assertions; the rest are labelled
>   change-detectors and **excluded from the coverage number** (v2 §4.4).
> - Every generated case needs a positive **axis witness**, or the coverage
>   percentage is unfalsifiable (v2 §4.4).
> - The ~7,000-assertion Layer-1 cost line and the topology doc's ~1,500-case
>   line are incompatible; v2 §3.3 replaces both with one formula.
> - "575 assertions" counted only `Test.equal`/`notEqual`; v2 §5.4 defines
>   **case** and **assertion** once (conformance = 772 cases / 776 assertions,
>   7 of them vacuous `Test.pass`).
> - Keep `examples/` as the path; change only its contract (v2 §0.1).
>
> **Scope.** This document answers one question: *what should the Sky regression
> corpus contain?* It deliberately does **not** design the CI job graph, tiering
> harness, or gate plumbing — a second architect owns that. Where a number here
> bears on tiering it is stated as an input, not a decision.
>
> Branch `feat/ci-test-overhaul` @ `a2baa5ee`. Every claim below is measured or
> cited; measurements were taken on this checkout with
> `rust/target/release/sky` (v0.19.11) and are reproducible.

---

## 0. Summary

The corpus today is **58 project directories under `examples/`**, auto-discovered
by six independent gates via `read_dir("examples")`. It is simultaneously the
documentation set, the compiler regression corpus, the codegen golden corpus, the
fuzzer seed corpus, and the LSP smoke corpus. It serves none of them well, and
the reason is economic, not editorial.

**The five numbers this proposal is built on:**

| # | Measurement | Value | Source |
|---|---|---|---|
| 1 | Cold `sky check` of a **27-line** compiler fixture (`44-record-update`) | **4.53 s** | measured, §1 |
| 2 | Cold `sky check` of a **1,911-line** real Live app (`19-skyforum`) | **6.79 s** | measured, §1 |
| 3 | Cold `sky check` of `00-standard-libs` (983 lines, **131 assertions**) + run | **4.84 s + 0.07 s** | measured, §1 |
| 4 | Stdlib public functions/values with **any** reference in any Sky source in the repo | **949 / 1623 = 58 %** | measured, §2.3 |
| 5 | Stdlib modules imported by **no** Sky source anywhere in the repo | **15 / 87** | measured, §2.3 |

Numbers 1–3 say: **compilation units are expensive and assertions are free.**
A 27-line fixture and a 1,900-line app cost within 50 % of each other, because
per-project cost is dominated by fixed overhead (stdlib parse+infer from cold,
`go build` of the runtime), not by program size. Meanwhile 131 behavioural
assertions ride inside one 4.84 s compile for free.

Today's corpus spends its entire budget on the expensive axis (58 compilation
units) and almost nothing on the cheap one: **124 assertions exist across all 58
examples combined** — fewer than the 131 in `00-standard-libs` alone.

Numbers 4–5 say the corpus is not merely inefficient, it is **incomplete**:
42 % of the stdlib's public surface is never called by any Sky code in the repo,
and 15 whole modules — including `Std.Jobs`, `Std.Db.Schema`, `Std.Db.Migrate`,
`Std.Markdown`, `Std.Email`, `Std.Config`, `Sky.Http.Middleware` — have zero
lowering-path coverage.

**The proposal in one line:** stop treating "a directory under `examples/`" as
the unit of coverage. Split the corpus into three declared artifacts —

1. **`docs-samples/`** (was `examples/`, cut 58 → 16): documentation only. Gated
   on *builds clean*, nothing more.
2. **`corpus/`** — **Layer 1**: a combinatorial, assertion-dense matrix over
   language constructs × import shapes × type shapes × erasure contexts ×
   stdlib edge cases. Mostly generated, batched many-cases-per-module, fast,
   deterministic, per-push. This is where "100 % coverage" is bought.
3. **`apps/`** — **Layer 2**: four deliberately-designed real-world projects plus
   one deployment-topology scenario, exercising surfaces *in combination* at
   runtime. Slower, tiered.

---

## 1. The economics — measured, not assumed

All timings: macOS, `rust/target/release/sky` v0.19.11, projects copied to a
scratch dir, `rm -rf sky-out .skycache .skydeps` before each run. `sky check` ≡
`sky build` (both run `go build`), so this is the true per-project cost the
sweep and every xtask gate pays.

```
example                 LOC    cold check
44-record-update         27      4.53 s     ← fixture
40-generic-adt-field     45      3.26 s     ← fixture
09-live-counter         251      7.40 s
19-skyforum           1,911      6.79 s     ← real multi-module Live app
26-ui-showcase        1,238      7.16 s
00-standard-libs        983      4.84 s     ← + 131 assertions, 0.07 s to run
```

Decomposition of the fixture cost (`44-record-update`, same project, three states):

```
cold everything                       4.53 s
warm go-build cache, cold .skycache   1.53 s
fully warm                            0.83 s
```

**Consequences that drive every decision below:**

- **A tiny fixture is not cheap.** `40-generic-adt-field` pins one narrow codegen
  behaviour in 45 lines and costs 3.26 s — 67 % of what a 1,900-line app costs.
  Sixteen such fixtures cost ~55 s per gate pass, per gate, and there are six
  gates that enumerate the corpus.
- **An assertion inside an already-compiling module costs ~0.5 ms.**
  `00-standard-libs` runs 131 assertions in 0.07 s. The marginal cost of case
  #132 is invisible.
- Therefore: **maximise assertions per compilation unit; minimise compilation
  units.** Today's corpus does the exact opposite.

### 1.1 Why the multiplier exists (and why it is a corpus problem)

Six gates independently discover the corpus by directory listing:

| Gate | Discovery site | What it does per example |
|---|---|---|
| `infer` | `rust/crates/xtask/src/infer_gate.rs:42-43` | full typecheck |
| `repro` | `rust/crates/xtask/src/repro_gate.rs:318-325` | build **twice**, byte-compare |
| `coerce-floor` | `rust/crates/xtask/src/coerce_floor_gate.rs:460-467` | emit + count tokens |
| `build-run` | `rust/crates/xtask/src/build_run_gate.rs:282-289` | build + run + golden |
| `fmt` / `s8` | `fmt_gate.rs:32`, `s8_gate.rs:38` | parse |
| `fuzz` | `fuzz_gate.rs:441-442` | seed corpus |

Every one of them takes *the entire directory listing*. There is no way for a
gate to say "I need the ten programs that stress lowering" or for a project to
say "I am documentation, build me and stop." The audit's measured **12.1×
over-compilation** (`.claude/AUTONOMOUS_GOAL.md:39`) is the direct consequence of
a corpus whose membership is a filesystem fact rather than a declaration.

**Corpus-side fix (topology-neutral):** every corpus member carries a declared
role and gate set in its manifest. A gate consumes a *role*, not a directory
listing. This is a prerequisite for anything the topology architect wants to do
about scheduling, and it is cheap.

Two live defects prove the discovery-by-listing model is already failing:

- `examples/39-hub-demo` has no `src/` at its top level (it is a two-app cluster:
  `billing-app/`, `frontend-app/`). Every gate's filter is
  `.filter(|n| root.join("examples").join(n).join("src").is_dir())`
  (`build_run_gate.rs:288`, `coerce_floor_gate.rs:466`), so it is silently
  dropped from **all six gates** and from `coerce_floor.golden`. Nothing in the
  repo builds it. Its `run-demo.sh:31` still instructs `cabal install exe:sky`.
- `sky-bundled/console` — **11 modules, 5,746 lines, statically linked into every
  user binary the compiler produces** — is not under `examples/`, therefore not
  in any gate corpus, and `grep -rn 'regenerate-console\|console_app' .github/`
  returns nothing. The single largest Sky application the project ships is
  verified by no automated path at all.

---

## 2. Inventory of the 58 examples

Verified against `git ls-files` on this branch. `examples/` contains 56 numbered
directories (`00`–`55`; there is no 56/57) plus `simple/` and `test_pkg/`.
(`58-persist-relational-only` / `59-persist-live` exist only on `feat/bluedb`;
`Std.Persist` has **zero** coverage on this line.)

### 2.1 Classification

**A. Compiler regression fixtures (13).** Self-declared — each opens with a
header naming the bug class, and several name the exact fixed function.

| Example | Pins | Cited |
|---|---|---|
| `40-generic-adt-field` | generic record-alias field in ADT variant, narrowed at concrete type | "audit #8" (`src/Main.sky:5`) |
| `41-nested-curry` | lambda-spine collapse vs `sky_ty_to_go` N-ary flatten | "audit #7b" (`:3`) |
| `42-poly-record` | mixed poly/concrete record literal → `lower_record` | `:9` |
| `44-record-update` | top-level record update yields FULL record | "(D2: …)" `:9-10` |
| `45-record-update-anon` | canonical anon-struct field order | `:11-15` |
| `46-row-poly-result` | row var param→result preserves extra field | `:11` |
| `47-func-field-record` | all-`any` record with FUNC fields → `rt.Coerce` | `:15`, **must run** `:20-22` |
| `48-tuple-unused-binding` | `_ = v_N` for unused tuple temps | `:12-13` |
| `49-xmodule-adt` | cross-module parametric ADT, no `undefined: T1` | **`anzellai/sky#153`** `:1` |
| `50-open-row-closure` | open-row closure param widened to `any` | divergences C001/C002, `:3-22` |
| `51-kernel-variadic-arity` | kernel-alias arity = Sky arrow count | **`anzellai/sky#155`** `:10`; compile-only `:26-28` |
| `53-record-update-map` | record-update over `List.map` carries full record | "DarraghStudio bug #2" `:4` |
| `54-record-fieldset-collision` | same field NAMES, different field TYPES → distinct structs | `goty.rs select_record_candidate` `:10` |

**B. Genuine product demos (~24).** `01`, `02`, `03`, `04`, `05`, `06`, `07`,
`08`, `09`, `10`, `12`, `13`, `15`, `16`, `17`, `18`, `19`, `20`, `21`, `23`,
`26`, `27`, `30`, `31`, `33`, `52`.

**C. Multi-backend / composite demos (5).** `24`, `35`, `36`, `37`, `38`.

**D. Misclassified.** `43-composition` sits in the fixture block but contains no
bug/issue/repro language anywhere — it is a 27-line teaching demo of `>>`/`<<`.
`55-store-partial-update` calls itself "Regression + demo" (`:3`) but is a
*stdlib feature* demo requiring a real Go SQLite driver.

**E. Non-projects.** `simple/`, `test_pkg/` — explicitly filtered out of every
xtask corpus (`build_run_gate.rs:290`, `coerce_floor_gate.rs:467`). Both declare
Go dependencies no source imports.

### 2.2 The overlap matrix — and the findings that surprised me

I expected the duplication story to be "too many similar demos". The measured
picture is different and worse.

**Near-duplicate clusters (real, but small):**

| Cluster | Members | Genuinely distinct axis |
|---|---|---|
| Stopwatch | `11` (Fyne FFI), `21` (Tui raw), `22` (Tui + Std.Ui), `31` (Webview + Std.Ui) | backend only — one model, four renderers |
| Counter | `09` (Live), `10` (Live + child component), `20` (Cli) | component decomposition; backend |
| Todo | `07` (CLI + Db.Store), `23` (Tui) | persistence vs TUI input |
| Console | `25` (prototype), `34` (log tiers), `39` (hub cluster) | all three superseded by `sky-bundled/console` |
| Streaming | `28` (consumer), `30` (producer), `32` (relay) | genuinely three different halves — **keep the axis** |
| Kitchen-sink | `24` (Tui+Live), `26` (Ui catalogue), `37` (Ui perf), `38` (Live+Tui+Webview) | 3-way overlap; `38` strictly subsumes `24` |

That is roughly **10–12 directories of true redundancy** — real, but nowhere near
enough to explain a 12.1× multiplier or the coverage holes. The bigger findings:

**Surprise 1 — 14 examples are referenced only by `coerce_floor.golden`.**
`25`, `34`, `35`, `37`, `38`, `40`, `42`, `43`, `44`, `45`, `46`, `48`, `54`,
`55`. They are compiled by every auto-discovering gate but named by no script,
no workflow, and no doc. They are corpus by accident of location.

**Surprise 2 — the sweep covers 29 of 58.** `scripts/example-sweep.sh:228-288`
lists `01`–`19`, `26`, `27`, `30`, `32`, `33`, `52`, `29`, `31`, `51`, `53`.
Absent: `00`, `20`–`25`, `28`, `34`–`50`, `54`, `55`. Meanwhile `verify-cli.sh`
covers `00`–`04`, `06`, `07`, `14`, `20`–`24`, `11`; `verify-examples.mjs`
covers nothing ≥ 20; `conformance.sh` references **zero** examples.
Four overlapping partial lists, no union, no coverage statement.

**Surprise 3 — `43-composition` is a teaching demo shelved among the fixtures**,
and gated identically to them. Nobody noticed because membership is positional.

**Surprise 4 — `rust/crates/xtask/golden/55-store-partial-update.stdout` is one
byte: a bare newline.** `examples/55-store-partial-update/src/Main.sky:41` prints
`name=… imageUrl=… stock=…` on success and `not found` on the `Nothing` branch —
*no* successful path produces empty stdout. The blessed golden therefore encodes
a run where the Task chain died before any `println` (almost certainly
`Db.connect`/`Modernc.Org.Sqlite` unavailable). The one example in the 40+ block
that genuinely requires running to prove anything currently asserts "prints
nothing", i.e. **it is green on total failure**. `bless_goldens`
(`build_run_gate.rs:1758-1770`) only refuses on rust≠oracle, and the oracle fails
identically in that environment, so the empty capture passed both the
double-capture and the oracle check.

**Surprise 5 — `39-hub-demo` and `sky-bundled/console` are verified by nothing.**
See §1.1.

**Surprise 6 — the LSP corpus is a synthetic single file.** `xtask lsp` shells
`scripts/lsp-test-nvim.sh`, whose project is
`PROJECT_DIR="${LSP_NVIM_PROJECT:-/tmp/lsp-real-test}"` (`:14`) — a generated
one-module `sky.toml` + `src/`. The one harness that points at a real codebase,
`scripts/lsp-test-skyshop.lua`, is wired to nothing. And
`scripts/lsp-fleet-sweep.cjs:7` hardcodes
`const ROOT = "/Users/anzel/works/playground/sky/examples"` — an absolute path to
one developer's machine, so it can never run in CI or a worktree. The surface
where import aliases and cross-module resolution matter most (#164 regressed the
control-plane through exactly that path) is tested against a single file with no
imports.

### 2.3 The coverage that is not there

Measured against the compiler's own authoritative API export
(`sky doc --export`, `api/symbols.json`): **1,744 public symbols across 87
modules; 1,623 lowercase functions/values.**

**Import-aware qualified reference count** across every `.sky` file in
`examples/` + `tests/` + `sky-bundled/` (a name counts only if a file that
imports its module references it qualified, or exposes it):

```
public fn/value symbols        1623
referenced anywhere             949   = 58 %
never referenced                674   = 42 %
```

Worst modules by absolute uncovered count:

```
Std.Ui             83/205      Std.Db.Schema      0/25
Std.Css            94/159      Std.Email          0/18
Std.Html           59/86       Std.Config         0/16
Std.Html.Attrs     33/59       Std.Db.Table       0/13
Std.Db.Store       37/59       Sky.Core.Char       0/8
Std.Db             11/35       Sky.Core.Basics     0/6
Std.Html.Events     8/27       Sky.Http.Middleware 0/5
Std.Live            6/19       Std.Jobs            0/4
Sky.Core.File       1/14       Sky.Core.Path       0/4
```

**15 of 87 modules are imported by no Sky source in the repo** — not one example,
not one test, not the bundled console, not a template, not another stdlib module:

```
Sky.Core.Basics   Sky.Core.Char   Sky.Core.Io   Sky.Core.Path
Sky.Http.Middleware   Std.Config   Std.Db.Migrate   Std.Db.Schema
Std.Db.Table   Std.Email   Std.Jobs   Std.Live.Console
Std.Markdown   Std.Trace   Std.Ui.Events
```

That is 155 public functions with **zero lowering-path coverage**. Several are
recently-shipped headline features: `Std.Db.Schema` (typed dialect-safe DDL),
`Std.Db.Migrate` (file-based migrations — and **no example has a `migrations/`
directory at all**, so `sky db init|gen|migrate|status|seed` is exercised by no
project), `Std.Jobs` (the entire background-jobs product; `18-job-queue`
hand-rolls its own queue and does not import it).

**Assertion census** — where behavioural truth actually lives today:

```
tests/conformance/**            575 assertions   (13 suites)
tests/** (non-conformance)      279 assertions   (29 suites, no CI runner)
examples/** (all 58)            124 assertions
rust/crates/**  #[test]         423 test fns
runtime-go/**   func Test*     1369 test fns
```

The 58-project corpus contributes 124 assertions. The Go runtime — which cannot
by construction catch a *lowering* defect (the v0.19.3 `Json.Decode.int` lesson,
`docs/testing/coverage-hardening-plan.md:20-23`) — contributes 1,369 tests.
**Behavioural verification is concentrated exactly where it cannot see the
compiler.**

---

## 3. The split: three artifacts, three contracts

Examples are doing two jobs with opposite requirements. Documentation wants
**few, clear, idiomatic, hand-written, stable**. Regression wants **many,
adversarial, ugly, generated, churning**. Every compromise between them has cost
us both.

| Artifact | Purpose | Membership | Gate contract | Owner |
|---|---|---|---|---|
| **`docs-samples/`** (renamed `examples/`, 58 → 16) | teach a user one thing | hand-curated, each has a README rationale | *builds clean from a wiped slate*, `sky fmt` idempotent, referenced from a doc page. **No golden, no run-match, no coerce-floor entry.** | docs |
| **`corpus/`** (**Layer 1**) | prove the compiler + stdlib + builtins are correct across combinations | generated + hand-seeded, declared per-family | per-case assertion; batched many cases per module; deterministic; fails on any assertion, any accept/reject flip, any golden drift | compiler |
| **`apps/`** (**Layer 2**) | prove real programs work when surfaces combine | 4 designed apps + 1 topology scenario | build + run + behavioural e2e + security scenario, each with a named falsifying mutation | product/runtime |

### 3.1 The 16 documentation samples — named

One sample per thing a newcomer needs to see once. Everything else is deleted,
graduated to `corpus/`, or absorbed by an `apps/` project.

| Keep | Teaches | Replaces |
|---|---|---|
| `01-hello-world` | the skeleton | — |
| `02-go-stdlib` | calling Go stdlib via FFI | — |
| `03-tea-external` | a third-party Go module | — |
| `04-local-pkg` | local modules, `[source] root` | — |
| `06-json` | encode/decode + `Decode.Pipeline` | — |
| `07-todo-cli` | `Std.Db.Store` + `Std.Codec` from a CLI | — |
| `09-live-counter` | the smallest Live TEA loop | `10` |
| `15-http-server` | a bare route table | `05` |
| `19-skyforum` | canonical multi-module `Std.Ui` Live app | `08`, `12` |
| `20-cli-counter` | `Sky.Cli` | — |
| `23-tui-todo` | `Sky.Tui` | `21`, `22` |
| `26-ui-showcase` | the `Std.Ui` catalogue (+ visual-regression snapshots) | `24`, `37` |
| `27-multi-session-chat` | pub/sub topic fan-out | — |
| `30-sse-server-demo` | SSE production | `28`, `32` |
| `33-websocket-echo` | WebSocket upgrade + origin allowlist | — |
| `52-blog-analytics` | "the batteries wired together" narrative | `16`, `17`, `18` |

`31-webview-stopwatch-ui` and `29-webview-threejs-spike` fold into **Fieldbook**'s
Webview backend; `13-skyshop` becomes **Storefront**; `25`/`34`/`39` are
superseded by `sky-bundled/console` joining `apps/`; `35`/`36`/`38` are absorbed
by **Relay**/**Fieldbook** (with the §7.1(3) sequencing caveat); `11` is deleted
(§7.1(1)); `simple`/`test_pkg` are deleted (already excluded from every gate).

Rules that make the split hold:

1. **A docs sample may never be the sole cover for any surface.** Enforced by a
   gate: for each public symbol, its cover-set must include ≥1 `corpus/` or
   `apps/` case. A docs sample deleted tomorrow must move no ledger row.
2. **A corpus case may be ugly.** It exists to break the compiler; nobody reads
   it for style. This is precisely what `examples/` could never allow.
3. **Every corpus member declares its role and gate set** in its manifest. Gates
   consume roles. No gate calls `read_dir`.
4. **`sky-bundled/console` and `sky-bundled/doc` join `apps/` as members** — they
   are 5,746 lines of Sky shipped inside the compiler and must be gated like any
   other app.

---

## 4. Layer 1 — the combinatorial matrix (the centrepiece)

### 4.1 The defect model

Read the history and one pattern dominates. **Every shipped defect was ordinary
usage in an untried combination.** The simple case compiles and behaves. One axis
changes, and it breaks:

| Issue | Base case (worked) | The one axis that changed |
|---|---|---|
| #164 | `import A.B.C` | ...`as X` where `X` is not a path segment; two modules declaring the same alias name |
| #166 | `{ m \| f = v }` | ...returned **inside a tuple** `( { m \| f = v }, Cmd )` |
| #166 fix A | record update | ...on a record with a `List (Dict String String)` field |
| #170/#172 | tuple/record destructure | ...on an **erased** subject via `List.foldr` / `let` / an `any`-typed value |
| #171 | row-poly record update | ...threaded through `foldl`/`foldr` |
| #173 (a) | `Dict k (List Record)` | ...**annotated** (the unannotated body worked) |
| #173 (b) | `Dict.keys` | ...on `Dict Int _` rather than `Dict String _` |
| #173 (c) | record in a Dict value list | ...built in a lambda that reads **only some** fields |
| fieldset collision | two record aliases | ...sharing field **names** but differing in field **types**; recurrence when values erase to `any` via `fst`/`snd` |
| variadic arity | `Ffi.kernel` alias | ...on a **variadic** Go kernel, consumed by a `Task` combinator |
| `Math.pi` | `Math.pi - x` | ...passed **bare** to a `Float -> Float` function |
| Json int64 | `Decode.int` on small ints | ...at 2^53+1, on the other platform |

The corrective is not "add a fixture per bug" — that is what we did, and it
produced 13 one-off directories that cost 3–4.5 s each and pin exactly one point
in the space. The corrective is: **make the space itself the corpus, and treat
each historical bug as a *coordinate* whose neighbours must also be covered.**

### 4.2 The axes

Enumerated from the compiler's own AST (`rust/crates/syntax/src/ast.rs`), so the
axis lists cannot silently drift from the language:

| Axis | Domain | Size | Source |
|---|---|---|---|
| **Expr form** | Literal, Multiline, Ref, QualRef, Accessor, FieldAccess, List, Tuple, Unit, Record, RecordUpdate, Paren, Negate, Bin, Call, Lambda, If, Let, Case | 19 | `ast.rs:185-205` |
| **Pattern form** | Wildcard, Var, Ctor, CtorQual, List, Cons, Tuple, Unit, Record, Alias, Int, Float, Str, Char, Bool, Paren, Negate | 17 | `ast.rs:343-361` |
| **Type form** | Fun, App, Var, Con, Qual, Record, Tuple, Unit, Paren | 9 | `ast.rs:284-294` |
| **Import shape** | plain · `as Alias` (alias = last segment) · `as Alias` (alias ≠ any segment) · `exposing (..)` · `exposing (x, y)` · qualified-only · two modules aliased to the **same** name · app type name colliding with a stdlib type · same-named ADT with same variant names in two modules | 9 | #164, #6aa92587 |
| **Type shape** | scalar · record (closed) · record (open row) · generic ADT payload · nested `Dict k (List Record)` · `Maybe`/`Result` wrapping each of the above · tuple arity 2/3/4 · function-valued field · `Dict Int _` vs `Dict String _` | ~14 | #173, #47, #48 |
| **Context** | top-level · in a tuple · in a `let` · in a `case` arm · through `List.map` · through `foldl` · through `foldr` · in a lambda arg · as a callback param · behind `fst`/`snd` · in an ADT payload · in a record field | 12 | #166, #171, #170 |
| **Annotation state** | fully annotated · unannotated · partially annotated · annotated too general | 4 | D3, `docs/rust-rewrite/13` |
| **Erasure state** | concrete · `any`-typed subject · open-row widened · through a first-class kernel value | 4 | #172, #036d1c38 |
| **Module structure** | single · two-module · deep chain · diamond · cyclic-reject · cross-module ADT · cross-module alias | 7 | D4, #153 |
| **Stdlib edge case** | per function: empty · single · boundary (int64/2^53) · negative · zero · unicode/surrogate · malformed · timezone/DST · platform width | ~9 per fn | conformance tier |

### 4.3 What "100 % coverage" means here — stated precisely

The user's target is 100 %. Full N-way coverage of the axes above is
~10^7 combinations and is not reachable. I will not quietly redefine the target,
so here is the exact criterion I propose, with the residual named:

**C1 — Unary: 100 %, no exceptions.** Every one of the 1,623 public stdlib
functions/values, every one of the 19 Expr / 17 Pattern / 9 Type forms, and every
kernel builtin has **at least one behavioural assertion** (asserted value, not
"it compiled"). Today: 58 % of stdlib functions have even a *reference*. This is
the single biggest coverage gain available and it is measurable to the symbol.

**C2 — Pairwise: 100 % of all-pairs across the seven structural axes.** Every
(Expr form × Context), (Type shape × Context), (Import shape × Module structure),
(Annotation state × Erasure state) pair etc. appears in ≥1 case. All-pairs over
these domains is ~2,400 cases — which, batched at ~200 cases per generated
module, is **12 compilation units ≈ 60 s**. Pairwise is the level at which every
issue in §4.1's table is *structurally* reachable.

**C3 — Defect neighbourhoods: exhaustive at distance 1.** For every historical
issue, its coordinate is pinned **and** every case reachable by changing exactly
one axis value is generated. #166 (record update × in-tuple) expands to record
update × each of the 12 contexts × each of the 4 annotation states = 48 cases,
all asserted. This is the mandate's "its NEIGHBOURS in the variation space become
cases too", made mechanical.

**C4 — Stdlib edge matrix: 100 % of the ~9 edge classes for every function whose
domain admits them**, hand-authored per module (a generator cannot invent "DST
spring-forward gap" or "Money.allocate residue must sum exactly").

**What is knowingly NOT covered, and why:**

- **3-way and higher interactions not adjacent to a known defect.** Full 3-wise
  is ~40× the pairwise cost. Pairwise + defect-neighbourhoods is the deliberate
  stopping point. *Mitigation:* the well-typed generator (§4.4-F4) samples the
  deep space randomly and deterministically, so higher-order interactions are
  sampled, not enumerated. When a 3-way defect escapes, C3 promotes it and its
  neighbours to permanent cases — the matrix ratchets.
- **Semantics only a real runtime exhibits** — SSE lifecycle, cookie expiry,
  cross-process gob, multi-replica session routing. Structurally out of Layer 1;
  that is exactly Layer 2's charter (§5).
- **Third-party Go FFI surfaces** (Stripe's 76k symbols). Enumerable in principle,
  meaningless in practice; covered by scale, not by matrix (§5, app D).

### 4.4 The engine — four case families

Layer 1 is not one mechanism. It is four, each matched to what it covers, and
each **batching many cases per compilation unit** because of §1.

**F1 — `corpus/stdlib/` — the assertion matrix (hand + table-driven).**
One `.sky` suite per stdlib module, using `Sky.Test`, extending the existing
`tests/conformance/` pattern that already carries 575 assertions. Each is a
single compilation unit holding hundreds of cases. Target: every one of the 1,623
public symbols, plus C4's edge classes.
*Sizing from measurement:* `00-standard-libs` = 131 assertions in one 4.84 s
compile. 1,623 symbols × ~4 edge cases ≈ 6,500 assertions ≈ **~50 modules ≈
4 min** of compile for the entire stdlib behavioural surface. This is the single
best value-per-second in the whole proposal.

**F2 — `corpus/lang/` — the structural matrix (generated, self-checking).**
A generator emits Sky programs by walking the axis product under the C2/C3
criteria. **The critical design choice: every generated program asserts its own
answer.** The generator builds a typed term *and knows its value*, so it emits
`Test.equal "case_0417" expected actual`. That removes the Haskell oracle from
the loop entirely — which matters, because the oracle is unavailable in CI
(`divergences_gate.rs:26`) and is being retired. A generated case that compiles
but computes the wrong value (the #173-c / #171 class: *fields silently zeroed*)
fails **as an assertion**, which is the only way that class is detectable without
a human reading emitted Go.

*Reuse, do not parallel-build:* `rust/crates/xtask/src/welltyped_gate.rs` (1,148
lines) already has the type-directed generator — `enum Ty` (`:111`), `gen_ty`,
`gen_record` (`:627`), `gen_adt` (`:648`), `gen_case_body` (`:668`), fixed
splitmix64 seed. It generates single-module programs and diffs the oracle at the
type-check boundary only. **Layer 1 promotes it** with three changes:
(i) emit expected-value assertions instead of relying on oracle agreement;
(ii) add the axes it lacks — module structure, import shapes, annotation state,
erasure contexts, `Dict`/nesting;
(iii) batch — emit N cases per module, not one program per process.
Its oracle-diff mode stays as a separate, local-only gate. This is an extension
of a proven mechanism, not a second implementation.

**F3 — `corpus/reject/` — the negative matrix.** Keep
`rust/crates/ty/tests/reject/corpus` (63 files) intact — the mandate's
must-NOT-touch list names it. Extend it along the same axes: for each generated
*valid* case in F2, a mutation that must be **rejected** (wrong arg type, arity
error, non-exhaustive case, annotation too general). C1 rejection coverage: every
diagnostic code the checker can emit has ≥1 corpus file. **Do not remove either
face of the double-run** (both `test-ty` and `xtask reject` execute it) without
the topology architect's sign-off — that is their call, not the corpus's.

**F4 — `corpus/seed/` — the randomised deep sampler.** The existing `xtask fuzz`
(robustness/determinism under mutation) plus the promoted well-typed generator in
unbounded-seed mode. Nightly, not per-push. Its job is finding the 3-way
interactions C2 deliberately does not enumerate; every find is promoted into F2
as a permanent coordinate with its C3 neighbourhood.

### 4.5 How the 13 fixtures graduate

They are not leftovers to prune — they are the **seed coordinates**. Each becomes
a *case family* in F2, not a directory:

| Fixture (3.26–4.53 s each as a project) | Becomes | Neighbourhood generated |
|---|---|---|
| `44-record-update` | `lang/record_update` | × 12 contexts × 4 annotation states = 48 |
| `53-record-update-map` | same family, `context=List.map` | already inside the 48 — **plus** `foldl`/`foldr` (#171) |
| `45-record-update-anon` | same family, `shape=anon` | × field-order permutations |
| `40-generic-adt-field` | `lang/adt_payload` | × 14 type shapes × 4 erasure states |
| `42-poly-record` | `lang/record_literal` | × mixed poly/concrete field permutations |
| `46-row-poly-result` | `lang/row_poly` | × param→result × through each context |
| `50-open-row-closure` | same family, `context=closure param` | × callback arity |
| `47-func-field-record` | `lang/func_field` | × arity 0–3 × called/uncalled |
| `48-tuple-unused-binding` | `lang/tuple_destructure` | × arity 2/3/4 × used/unused × erased subject (#170/#172) |
| `41-nested-curry` | `lang/currying` | × partial-application depth 1–4 × over-application |
| `49-xmodule-adt` | `lang/module_graph` | × 7 module structures × 9 import shapes |
| `54-record-fieldset-collision` | `lang/fieldset_collision` | × same-names/different-types × erased-via-`fst`/`snd` (the open recurrence) × **imports the real `Std.Live` graph** (non-negotiable — a stubbed stdlib cannot reproduce it) |
| `51-kernel-variadic-arity` | `lang/kernel_alias` | × variadic/non-variadic × 0–3 pipe stages × Task-combinator consumer |

Net: **13 directories (~48 s of compile, ~0 assertions) → ~5 generated modules
(~25 s, ~600 asserted cases).** That is the whole thesis in one row.

`43-composition` does not graduate — it is a docs sample and moves to
`docs-samples/`. `55-store-partial-update` is a `Std.Db.Store` *behavioural* case:
its assertions move to F1 (`corpus/stdlib/Db_Store.sky`) where they run against a
real driver, and its broken 1-byte golden is deleted rather than re-blessed.

### 4.6 The ratchet — every issue becomes permanent, with its neighbours

Process, not tooling, but it belongs in the corpus contract:

1. A defect is reproduced as a **failing** F2/F1 case first (the repo already
   does this well — `64b4ce13` "test(rt): FAILING — …").
2. Its axis coordinate is recorded in `corpus/coordinates.toml`: which axis
   values, which issue, which fix commit.
3. The generator reads `coordinates.toml` and **expands the C3 distance-1
   neighbourhood automatically**. Adding one coordinate adds tens of cases with
   no hand authoring.
4. A gate asserts every entry in `coordinates.toml` maps to a live, passing,
   *asserting* case. A coordinate whose case stops asserting (the 1-byte-golden
   failure mode) fails the gate.

**Falsifying mutation for the whole of Layer 1** (mandate constraint #4): revert
`lower.rs`'s row-close fix and assert the `record_update × in-tuple × ADT-field`
case fails; delete one `coordinates.toml` entry's case and assert the ledger gate
fails; flip one expected value in a generated module and assert the run fails
non-zero. All three run in seconds and are cheap to keep honest.

---

## 5. Layer 2 — the real-world projects

Layer 1 structurally cannot see: session/SSE lifecycle, cookie expiry against
wall-clock, cross-process gob, multi-replica routing, reverse-proxy topology,
a real SQL engine's behaviour, or a browser's DOM. Everything in §5 exists for
that class and nothing else. Sized deliberately small so it cannot crowd out §4.

**Four apps + one topology scenario.** Each is a coherent product a user would
actually build, and each is chosen so its *surface combination* covers a cluster
of real incidents.

---

### App A — **Ledger** (Sky.Live, the full-stack flagship)

*Product:* a small multi-tenant double-entry bookkeeping app. Sign-up/sign-in,
accounts, journal entries, a monthly close job, CSV export, a live balance view.

*Surfaces in combination:* Sky.Live (TEA, routing, `sky-nav`, sessions, CSRF,
SSE) × `Std.Ui` (forms, tables, `Ui.Keyed`, `Ui.Lazy`, `Ui.Responsive`) ×
`Std.Auth` (password, session TTL) × `Std.Db` + `Std.Db.Store` + `Std.Codec` ×
**`Std.Db.Schema` + `Std.Db.Migrate` with a real committed `migrations/`
directory** × `Std.Money`/`Std.Decimal` × `Std.Jobs` (monthly close) ×
`Std.Csv` × `Std.Cmd`/`Sub` pub-sub × `Std.Log`. **Runs against SQLite *and*
Postgres from identical source, driver from env.**

*Incidents it would have caught:*

| Incident | Mechanism in Ledger |
|---|---|
| **Session hijack** (v0.19.13, `64b4ce13`/`b263b71b`) | two-client scenario: client A's cookie + client B's session id in the `/_sky/event` body must be refused |
| **CSRF-idle strand** (`915faf21`, "the darraghstudio incident") | `SKY_LIVE_TTL` short + hold idle past it + interact; assert no 403/offline banner |
| **`liveInto` silent-stale on SQL** (`20a0bee6`, branch) | reactive binding on the **Postgres** arm must either deliver or fail loudly — assert the *verdict*, never merely "no crash" |
| **`[live] store="postgres"` silent memory fallback** (`ab13572a`) | boot with an unreachable DSN; must fail loud, not degrade |
| **#166 record-update field drop** | `Model` carries a `Dict String String` **and** a parametric-ADT field; `update` returns `( { m \| f = v }, Cmd )` both annotated and unannotated |
| **`Store.orderAsc` multi-column reversal** (`0f5b7e0e`) | journal ordered by (date, id); assert the order |
| **`Ui.button` defaults to `type=submit`** (`c4a9ea25`) | a Cancel button inside a form must not save |
| **cross-process gob** (`432feec6`) | restart the binary mid-session; session must survive |
| **`Money.allocate` residue** | split an invoice 100 → 34/33/33; assert exact sum |

*Cost:* build ~10 s (modelled from `19-skyforum` at 6.79 s + Db/Auth surface);
SQLite e2e ~90 s; Postgres arm ~60 s (container); security scenarios ~30 s.
**~3 min for the SQLite arm, ~5 min for both.**

---

### App B — **Relay** (Sky.Http.Server, headless)

*Product:* a JSON API + event gateway. Token-authenticated REST, an SSE feed, a
WebSocket channel, an outbound HTTP fetch pipeline, rate limiting, CORS, a
CSV bulk-import endpoint, config from env.

*Surfaces:* `Sky.Http.Server` × `Sky.Http.Server.Stream` (SSE) ×
`Sky.Http.Server.WebSocket` × **`Sky.Http.Middleware` (0/5 covered today)** ×
`Sky.Http.RateLimit` × `Sky.Core.Jwt` + `Std.Auth` × `Sky.Core.Http` (outbound) ×
`Std.Cache` × `Std.PubSub` × `Std.Csv` × **`Std.Config` (0/16)** ×
**`Std.Trace` (0/3)** × `Std.Log`.

*Incidents it would have caught:*

- **Kernel-alias variadic arity** (`135f5176`, v0.18.9) — needs a real
  `Http.defaultRequest |> withHeader |> Http.request |> Task.andThen` chain and
  an error path reaching `Error.toString`. A compile-only fixture cannot see it;
  `36-composite-server`'s non-variadic `withCors` is the counter-example that
  keeps the fix honest, so Relay must carry **both** shapes.
- **CORS + BasicAuth middleware** — zero tests today
  (`coverage-hardening-plan.md:55`).
- **Console loopback-auth behind a reverse proxy** (`ac9f4fef`) — Relay runs one
  scenario behind a proxy; every request is loopback, and the console must still
  authenticate.
- Subsumes `05-mux-server`, `15-http-server`, `30-sse-server-demo`, `32-sse-relay`,
  `33-websocket-echo`, `36-composite-server`, `34-multi-tier-console`.

*Cost:* build ~6 s; e2e ~60 s. **~1.5 min.**

---

### App C — **Fieldbook** (one `Std.Ui` view, four backends)

*Product:* a field-notes app — list, detail, edit, search — with **one** view
function rendered by Sky.Live, Sky.Tui, Sky.Webview, and a Sky.Cli one-shot
export.

*Surfaces:* `Std.Ui` + every submodule (`Background`, `Border`, `Font`, `Grid`,
`Input`, `Chart`, `Animation`, `Transition`, `Transform`, `Region`, `Keyed`,
`Lazy`, `Responsive`, **`Events` — 0/3 today**) × `Std.Live` × `Std.Tui` ×
`Std.Webview` × `Std.Cli` × `Std.Live.Head`.

*Why it matters:* cross-backend `Std.Ui` divergence has no gate today.
`26-ui-showcase` renders one backend; `38-composite-ui-multibackend` renders three
but asserts nothing beyond building. Fieldbook renders the **same** view on all
backends and diffs the structural output, so a `Std.Ui` change that renders
correctly on Live and wrongly on Tui fails.

*Incidents:* the `24`/`26`/`37`/`38` cluster collapses into it; `Std.Ui`'s 122
uncovered functions get a rendering path.

*Cost:* build ~8 s (4 backends from one source); Live e2e + Tui snapshot +
Webview smoke ~75 s. **~1.5 min.** (Webview needs cgo — the topology architect
owns whether that is a macOS-only lane.)

---

### App D — **Storefront** (Go FFI at scale)

*Product:* the successor to `13-skyshop`. A commerce front end with a
third-party payments SDK, a third-party datastore SDK, an external Sky package
dependency, static assets, OAuth, and i18n.

*Surfaces:* Go FFI at 76k-symbol scale × `sky add`/`install`/`.skydeps` ×
external Sky package deps × Sky.Live `withGuard` route guards × OAuth ×
static assets.

*Why it stays:* nothing else in the corpus exercises the FFI *scale* path —
`safePkgName` aliasing, the typed-FFI cache, `sky-ffi-inspect` memory behaviour.
`13-skyshop` already carries `infer_gate.rs:21` FFI-blocked status and is the
`lsp-test-skyshop.lua` target. Storefront inherits that role and **becomes the
LSP real-codebase corpus** (§7), which is the gap #164 fell through.

*Cost:* build 60–120 s cold with FFI (dominated by `sky add` + Go module
resolution, network-dependent). **Nightly/pre-release only** — this is the one
app whose cost genuinely earns a slower tier.

---

### Scenario E — **Fleet** (deployment topology, not a fifth app)

Not new source. **Ledger, run as a topology:** two replicas + Redis broker +
sticky sessions + a central `sky console serve` hub + `ENV=production`.

*Covers:* cross-instance pub/sub, sticky-session routing, tenant-isolated console
hub (what `39-hub-demo` was for, but actually executed), production gate
(`ENV=production` locking the console/banner/metrics), `SKY_CONSOLE_AUTH`.

*Why a scenario, not an app:* `39-hub-demo` is two bespoke Live apps that exist
only to push telemetry, and nothing builds them. Running the *real* app in a
multi-replica topology tests the same thing and cannot rot into a mock.

*Cost:* ~3 min. Nightly.

---

### 5.1 Where I disagree with the repo's own proposal

`docs/rust-rewrite/13-change-verification-and-edge-cases.md:137-158` proposes a
single hand-written **kitchen-sink app** combining same-named aliases, non-segment
import aliases, `Dict`/ADT record fields, `List (Dict String String)` through
`List.head`, and a user `type alias Event` colliding with a stdlib type —
"building it clean = most of the matrix in one gate."

**I think that is the wrong shape, and I would not build it.** Three reasons:

1. A hand-written hostile app covers exactly the combinations its author thought
   of on the day. The whole lesson of §4.1 is that the *unthought* combination is
   the one that ships broken.
2. It is one compilation unit whose failure mode is "it broke, somewhere in 30
   modules" — poor localisation, exactly when you most need a minimal repro.
3. It rots. Every future combination requires a human to edit a large adversarial
   app without breaking the others.

**Instead: generate it.** The `lang/module_graph` family (§4.5) produces hostile
module graphs by construction — N modules, same-named aliases across them,
non-segment import aliases, stdlib-colliding type names, cross-module ADT
payloads — as *many small graphs* under the C2/C3 criteria, each localising its
own failure. The matrix subsumes the kitchen-sink app and keeps growing without
an author. This is the one place I actively depart from the repo's written plan,
and I would want it grilled.

---

## 6. Compiler fixtures — where they should live

**Not in `examples/`, and mostly not as project directories at all.**

| Fixture kind | Home | Rationale |
|---|---|---|
| Structural language cases (the 13 in §4.5) | `corpus/lang/*.sky`, **generated, batched** | 3.26–4.53 s per project → ~0.5 ms per case |
| Reject cases | `rust/crates/ty/tests/reject/corpus` (**keep as-is**, extend) | already in-process; the mandate's must-NOT-touch list |
| Type-inference shape pins (#166 class) | `rust/crates/ty/tests/` (`inferred_sig_snapshot.rs` pattern) | in-process, no `go build`, milliseconds |
| Emitted-Go shape pins | `rust/crates/lower/tests/` (`goty_erasure.rs` pattern) | asserts nominal `_R` vs `any` without building |
| Cases that require **running** to prove anything (`47-func-field-record`, `53-record-update-map`, `55`) | `corpus/lang/` with a `Sky.Test` assertion | a golden-stdout project is the mechanism that produced the 1-byte-golden failure (§2.2) |
| Cases requiring the **real stdlib import graph** (`54-record-fieldset-collision`) | `corpus/lang/`, importing `Std.Live` for real | a stubbed stdlib cannot reproduce it |
| FFI/ABI arity pins (`51`) | `corpus/lang/` + the existing `abi_guard` unit test | keep both faces |

**Rule:** a fixture becomes a project directory only if it needs a `sky.toml`, a
DB, an FFI dependency, or a real `go build` to demonstrate the behaviour.
By that rule, **1 of the 13 qualifies** (`51`, and only for its FFI half).

Corollary: `coerce_floor.golden` must be re-keyed from example names to corpus
roles. Today it holds 52 rows named after directories, and a new emitting example
absent from it **fails the gate** (`coerce_floor_gate.rs:404-412`) — so adding a
corpus member is currently a cross-cutting edit to six gate corpora plus a
golden. That is why nobody adds one.

---

## 7. Coverage ledger

Verdicts: **↑ stronger · = equal · ↓ WEAKER (explicitly called out)**.

| Surface | Covered today by | Covered under this proposal | Verdict |
|---|---|---|---|
| **Stdlib behaviour — 1,623 public fns** | 949 referenced (58 %); 575 conformance assertions over 13 modules | F1: assertion per symbol + C4 edge classes; C1 gate fails on any uncovered symbol | **↑↑** — the largest single gain |
| **15 zero-coverage modules** (`Std.Jobs`, `Std.Db.Schema`, `Std.Db.Migrate`, `Std.Markdown`, `Std.Email`, `Std.Config`, `Std.Trace`, `Sky.Http.Middleware`, `Sky.Core.{Basics,Char,Io,Path}`, `Std.Db.Table`, `Std.Live.Console`, `Std.Ui.Events`) | **nothing** | F1 suites + Ledger (`Jobs`/`Schema`/`Migrate`) + Relay (`Middleware`/`Config`/`Trace`) + Fieldbook (`Ui.Events`) | **↑↑** from zero |
| **File-based migrations** (`sky db init\|gen\|migrate\|status\|seed`) | **no example has a `migrations/` dir**; `rust/crates/sky/tests/db_flow.rs` only | Ledger commits real migrations + runs the verbs on both dialects | **↑** |
| **Language constructs** (19 Expr / 17 Pat / 9 Type) | incidental — whatever examples happened to write | C1 unary + C2 pairwise, enumerated from `ast.rs` so it cannot drift | **↑↑** |
| **Record/row/erasure defect classes** (#166/#170/#171/#172/#173) | 13 one-off fixtures = 13 points | C3 neighbourhoods ≈ 600 asserted cases | **↑↑** |
| **Accept/reject parity** | 63-file reject corpus | same corpus, extended per-diagnostic; **both faces preserved** | **=** (extension only) |
| **Byte-determinism (`repro`)** | full corpus × 2 builds | same gate, corpus roles instead of listing; **both platforms preserved** | **=** |
| **rt.Coerce floor** | 52-row golden keyed on example names | re-keyed to corpus roles; **fail-on-increase preserved** | **=** |
| **Oracle differential** | `divergences`, `welltyped` (local-only) | unchanged; F2's self-assertions **add** an oracle-independent path | **↑** |
| **Emitted-Go behaviour ("compiles clean, behaves wrong")** | 24 `xtask/golden/*.stdout` files, one of them empty and green-on-failure | F2 self-asserting cases + F1 + Layer 2 goldens; empty-golden gate | **↑** |
| **Sky.Live session/SSE lifecycle** | Playwright scripts, several proven non-failing (`495d5367`, `ee88707f`) | Ledger scenarios with named falsifying mutations | **↑** |
| **Session-hijack / CSRF class** | one Go test (`64b4ce13`) | Ledger two-client + idle-past-TTL scenarios | **↑** |
| **Postgres** (app data + Live store) | **nothing** — `postgres` appears in examples only as a *monitored target string* in `17-skymon/src/Lib/Metrics.sky:129` | Ledger's Postgres arm; `liveInto` verdict assertion | **↑↑** from zero |
| **Multi-replica / broker / sticky sessions** | nothing that executes (`39-hub-demo` unbuilt) | Fleet scenario | **↑** |
| **`sky-bundled/console` (5,746 lines, ships in every binary)** | **nothing** | joins `apps/` | **↑↑** from zero |
| **LSP** | 14 in-process test files + a **synthetic single-file** nvim project; the real-codebase harness is wired to nothing; the fleet sweep hardcodes an absolute developer path (`lsp-fleet-sweep.cjs:7`) | Storefront + Ledger as the LSP corpora (multi-module, aliased imports, FFI) | **↑↑** |
| **Std.Ui cross-backend parity** | none — each backend rendered by a different example, nothing compared | Fieldbook renders one view on four backends and diffs | **↑** |
| **Go FFI at scale** | `13-skyshop` | Storefront (same role, nightly) | **=** |
| **Fyne / native GUI FFI** (`11-fyne-stopwatch`) | skipped on Linux CI, unbuildable on macOS → verified **nowhere** (`example-sweep.sh:315-333`) | **DELETED** | **↓ nominally, = actually** — see below |
| **Documentation samples** | 58 dirs, 29 in the sweep, 14 referenced only by a golden | 16 dirs, all referenced from a doc page, all gated on build | **↑** for docs; **↓ for raw compiled-project count** — see below |
| **Well-typed fuzz / robustness** | `xtask fuzz` + `welltyped` | unchanged + promoted generator | **=** |
| **Conformance (platform/int-width class)** | 13 suites, both platforms | folded into F1, both platforms | **=** |

### 7.1 Coverage I would knowingly give up — stated plainly

1. **`11-fyne-stopwatch` deleted.** It is the only example with a hand-written Go
   helper driving a native desktop toolkit. Nominally that is unique coverage.
   Actually it is **verified nowhere today** — declared `blocked` in the sweep,
   skipped on Linux CI, unbuildable on macOS. Deleting it changes real coverage by
   zero and removes a permanently-yellow row. *If the user wants native-GUI FFI
   covered, it needs a working lane, not a skipped directory* — that is a
   decision, not a corpus mechanic.

2. **Raw compiled-project count drops 58 → ~21** (16 docs samples + 4 apps +
   1 scenario). If a defect can only be triggered by "many distinct top-level
   `sky.toml` projects existing", we lose it. I judge that class empty — the
   per-project surface (manifest parse, module discovery, `sky-out` layout) is
   the *same code path* on every project, and it is covered by
   `rust/crates/sky/tests/*_flow.rs` plus 21 live projects. **But it is a real
   reduction and I am not going to pretend otherwise.** Mitigation: the C2 module-
   structure axis generates manifest/layout variants (`[source] root`, missing
   `entry`, `[dependencies]`, `[database]`, `[live]` variants), which is stricter
   than the accidental variation across today's 58.

3. **Composite examples `35`/`36`/`37`/`38` deleted as directories.** `35`'s
   golden stdout is load-bearing — it caught the #173 zeroed-aggregation bug
   (`7425fd00` re-blessed it after the fix). Its pipeline assertions **must** move
   into F1/F2 as asserted cases before the directory goes. If that migration is
   not done, this row is a genuine loss. **Sequencing requirement: delete only
   after the replacement case asserts, and prove it by mutation.**

4. **`52-blog-analytics` demoted to a docs sample.** It is the best "batteries
   wired together" narrative in the repo (`README.md:12-13`) and its `[analytics]`
   + `Std.Analytics` + bcrypt combination is not otherwise covered. Ledger must
   pick up `Std.Analytics` explicitly or this is a loss.

5. **What I am *not* touching**, per the mandate's carry-in: `repro` + `golden` on
   both platforms, `conformance` on both, the reject corpus (never remove both
   faces), `fuzz`, the sweep's clean-slate wipe + forced `sky install`, and
   `verify-all-web`/`verify-cli`'s "click is a no-op" coverage. Every one of those
   keeps a gate; only its *corpus* is re-keyed.

---

## 8. Cost

Modelled from §1's measurements. Wall-clock, sequential, warm Go build cache;
the topology architect owns parallelism and tiering.

**Today (one clean-slate pass over the corpus):**

```
~20 small fixtures/CLI    × ~3.3 s   ≈  66 s
~24 Live/http/tui apps    × ~7.0 s   ≈ 168 s
13-skyshop (FFI, 76k syms)           ≈  60-120 s
                                     ─────────
one pass                             ≈ 5-6 min
× 12.1 corpus compiles per push      ≈ 60-70 min of compile
  (audit-measured; CI wall-clock 31-34 min with parallelism)
assertions bought                    124
```

**Proposed:**

| Tier | Contents | Compile units | Est. wall-clock | Assertions |
|---|---|---|---|---|
| **L1-fast** (per push) | F1 stdlib suites (~50 modules) + F2 lang matrix (~12 modules) + F3 reject (in-process) | ~62 | **~5 min** | ~7,000 |
| **L1-deep** (nightly) | F4 seed sampler, unbounded | streaming | budgeted | — |
| **L2-fast** (per push) | Ledger (SQLite arm) + Relay build+run | 2 | **~4.5 min** | scenario-level |
| **L2-full** (nightly) | + Ledger Postgres arm, Fieldbook 4 backends, Fleet topology | 4 | ~10 min | scenario-level |
| **L2-scale** (pre-release) | Storefront (FFI cold) | 1 | ~2 min | — |
| **Docs** (per push) | 16 samples, build-only | 16 | **~1.7 min** | 0 (by design) |

**Per-push total ≈ 11 min of compile for ~7,000 assertions**, versus today's
~60–70 min for 124. The gain is not from cutting corners; it is from stopping the
practice of paying 4 seconds of process startup for one assertion.

Two costs I am **adding** on purpose: a Postgres container in L2-full, and the
`sky-bundled/console` build. Both cover surfaces that are at zero today.

---

## 9. Open decisions for the user

1. **Native-GUI FFI (Fyne).** Delete, or invest in a working lane? It is verified
   nowhere today; keeping the directory is the one option that is strictly worse
   than both.
2. **The kitchen-sink app.** `docs/rust-rewrite/13:137-158` proposes hand-writing
   one; I propose generating it (§5.1). This is a genuine architectural fork and
   should be grilled before either is built.
3. **`43-composition` and `55-store-partial-update`** are mislabelled today.
   Confirm they move to docs / F1 respectively.
4. **`examples/` rename.** `docs-samples/` makes the contract explicit, but it
   breaks every doc link and external reference. Worth it or not is the user's
   call; the *contract* matters more than the directory name.
5. **`rust/crates/xtask/golden/55-store-partial-update.stdout` (1 byte)** is a
   live false-green today, independent of this proposal. It should be fixed on
   `main` now, not at the end of a corpus redesign.
