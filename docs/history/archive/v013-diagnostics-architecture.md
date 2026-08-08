# v0.13 — Diagnostics & Compiler Soundness Architecture

Status: Planning (branch `perf/v0.13`)
Started: 2026-05-12
Target: solid foundation, not a patch series

## Why this exists

v0.12.x shipped 26 examples + Sky.Live + Sky.Tui + typed-codegen
foundations. But the pattern of v0.12.0 → v0.12.1 patches surfaced
deeper architectural cracks:

1. Errors leak. The compiler is sound on what HM CAN check, but
   codegen-stage bugs (Issue #52's `List.drop` coercion, the
   `Dict_fromListT[string]` panic on heterogeneous lists) surface as
   cryptic Go-build errors or runtime panics. The user can't tell
   compiler bugs from their own mistakes.

2. Error messages are strings. Each phase (parse, canonicalise, HM,
   exhaustiveness, codegen, go build, runtime) emits its own ad-hoc
   format. There's no unified `Diagnostic` value flowing through the
   pipeline. v0.12.1 added rendering improvements (filename prefix,
   source-context snippet, recursive type-diff) but each is a
   string-parsing hack on top of the previous string-parsing hack.

3. LSP doesn't catch what `sky check` catches. The LSP runs Parse +
   Canonicalise + HM. Codegen + `go build` validation never reach the
   editor. Users hit the same bugs at command-line that the LSP
   silently approved.

4. Standard library opacity. `String.fromInt`, `List.map`, `Dict.*`
   etc. are hand-written Go in `runtime-go/rt/*.go`. When they panic
   the user sees Go stack traces with no Sky line numbers. Hard to
   extend, debug, or reason about.

## The guarantee v0.13 will deliver

**"Sky's compiler will never be the source of a runtime panic on
type-correct Sky source."**

Specifically:
- Every error the type system, canonicaliser, exhaustiveness checker,
  OR codegen-stage validator CAN determine is caught, surfaced in
  LSP, and reported with Elm-quality diagnostics.
- The error format is structured (`Diagnostic` AST), uniform across
  phases, machine-parseable (LSP, IDE quick-fix), and human-readable
  (CLI source-context snippet).
- The `sky check` command and the LSP report identical errors —
  identical set, identical text, identical regions.
- Compiler-introduced runtime panics (Issue #52 class — typed
  monomorphisation with any-typed source) cannot escape codegen
  validation.

What this guarantee does NOT cover (and won't, by any compiler):
- User-invoked `Sky.Ffi.call` escape hatches (intentional `any`-typed
  bypass; user accepts loss of type safety, same as Haskell's
  `unsafeCoerce` / Rust's `unsafe`).
- Semantic logic bugs (`x + 1` when user meant `x - 1` —
  type-correct but wrong-behaving).
- Runtime resource issues (OOM, goroutine leaks, stack overflow).

These limits are honest; no compiler can promise more.

## Out of scope (deferred to v0.14+)

- Full type monomorphisation (0 `any` in emitted Go). v0.13 increases
  the typed surface but does NOT eliminate `func(any) any` for
  lambdas — that requires lambda lowering with end-to-end HM type
  preservation, a multi-week refactor on its own.
- Sky.Gui (gio) — independent track on `exp/sky-gui-gio`.
- Mobile / cross-platform — depends on Sky.Gui maturity.
- Performance optimisation beyond what Layer 3 surfaces.
- Sky → Haskell → Go intermediate (explicitly rejected — see below).

## Approach rejected: Sky → Haskell → Go

The user proposed using Haskell as an intermediate compilation target,
reasoning that Haskell's mature type system would catch errors that
Go misses. **Rejected** for these reasons:

1. **No production reference.** GHC's Go backend was a research
   experiment that stalled. Haskell-to-Go translation is research-
   level because Haskell's lazy/pure model doesn't map cleanly to
   Go's strict/imperative model. We'd be the first to ship.
2. **Doubles failure surface.** Errors come from Sky AST, OR Haskell
   type-check, OR Haskell-to-Go translation. Three places for bugs
   to hide.
3. **Trades known HM for unknown HM.** Sky's HM solver is small and
   we control it. Switching to GHC's HM means inheriting its quirks
   (let-generalisation, value restriction, deferred type errors)
   and losing the ability to customise error rendering.
4. **Doesn't solve the root cause.** The opacity is in the runtime
   stdlib (hand-written Go), not in the compilation path.

The Sky compiler's HM is not the problem. The error pipeline is.

---

## The four-layer architecture

### Layer 1: Diagnostic AST (the foundation)

Replace string-based errors with a structured `Diagnostic` value.
Every phase emits these. ONE renderer for CLI + LSP.

```haskell
data Diagnostic = Diagnostic
    { _diag_file     :: !FilePath
    , _diag_region   :: !Region              -- primary source range
    , _diag_severity :: !Severity            -- Error | Warning | Info
    , _diag_category :: !Category            -- Parse | Canonical | Type | …
    , _diag_message  :: !String              -- short summary
    , _diag_related  :: ![RelatedRegion]     -- secondary highlights
    , _diag_hints    :: ![Hint]              -- "Try X" suggestions
    , _diag_code     :: !(Maybe DiagCode)    -- stable code (E0001 etc.)
    }

data RelatedRegion = RelatedRegion
    { _rel_file    :: !FilePath
    , _rel_region  :: !Region
    , _rel_message :: !String
    }

data Hint = Hint
    { _hint_message       :: !String
    , _hint_suggested_fix :: !(Maybe SuggestedFix)
    }

data SuggestedFix = SuggestedFix
    { _fix_description :: !String
    , _fix_edits       :: ![TextEdit]   -- LSP-compatible edits
    }
```

#### Phase migration

Each phase currently emits a `String`. Migrate to produce
`[Diagnostic]`:

| Phase | Current | Target |
|---|---|---|
| Parse | `Either String Module` | `Either [Diagnostic] Module` |
| Canonicalise | `Either String Module` | `Either [Diagnostic] Module` |
| HM Solve | `SolveResult` with String error | `SolveResult` with `[Diagnostic]` |
| Exhaustiveness | `String` error | `[Diagnostic]` |
| Codegen-validation (NEW — Layer 2) | n/a | `[Diagnostic]` |
| Go build wrapper | string-pattern detection in `runGoBuildWithDiagnostics` | `[Diagnostic]` |
| Runtime panic recovery | bare panic | `[Diagnostic]` from `defer recover` |

#### Renderer: CLI

`renderCli :: Diagnostic -> String` — produces the Elm-style block:

```
-- TYPE MISMATCH ----------------------------------- src/Main.sky:47:9

The 1st argument to `Live.app` is not what I expect:

48|         , update = update
                       ^^^^^^
This `update` value has type:

    Msg -> { i : Int, n : String } -> ...

But `Live.app` needs:

    Msg -> { i : Int, n : Int } -> ...

Hint: I see that `n` is assigned a String here:

42|     ( { model | n = String.fromInt ... }, Cmd.none )
                       ^^^^^^^^^^^^^^^^^^

Try removing the `String.fromInt` if `n` should stay an Int, or
update the Model alias if `n` should be a String.
```

#### Renderer: LSP

`renderLsp :: Diagnostic -> LspDiagnostic` — produces the JSON shape:

```json
{
  "range": { "start": {"line": 47, "character": 9}, "end": ... },
  "severity": 1,
  "code": "E2003",
  "source": "sky",
  "message": "Type mismatch in field `update` ...",
  "relatedInformation": [
    {
      "location": { "uri": "file:///.../Main.sky", "range": {...} },
      "message": "`n` is assigned a String here"
    }
  ]
}
```

Editor jumps via `relatedInformation` work for free in VS Code +
Neovim's native LSP client.

#### Diagnostic codes

Each error class gets a stable code (`E0001` = parse error,
`E1003` = undefined name, `E2003` = type mismatch, etc.). Codes:
- Stable across versions (cabal tests grep by code, not message)
- Documented at `docs/diagnostics/E0001.md` etc.
- Usable in `// sky: allow E2003` overrides (future)

---

### Layer 2: Codegen-stage validation

Currently codegen produces Go via string templates with no Sky AST
traceability. When `go build` rejects the output, the user sees a Go
error with no path back to Sky source.

**Change**: every emitted Go expression carries `Origin :: Maybe Region`
(the Sky source region that produced it). A post-codegen validation
pass walks the typed Go output looking for known compiler-bug shapes
and emits `Diagnostic`s with the Sky region — BEFORE handing to
`go build`.

#### Origin tracking

Currently:
```go
rt.List_dropT[int](i, rt.AsListT[int](xs))
```

Target:
```go
rt.List_dropT[int](i, rt.AsListT[int](xs)) // sky: src/Main.sky:42:18-29
```

The comment is preserved through `go build`. When Go errors on a
line, we parse the trailing `// sky:` comment and emit a Diagnostic
with the Sky region.

#### Validation patterns

Known compiler-bug shapes to catch BEFORE go build:

1. **Typed kernel call with any-typed primitive arg**:
   `rt.List_dropT[int](anyVar, ...)` where `anyVar` has no
   `rt.AsInt` wrap. Issue #52's class.
2. **`rt.Coerce[ConcreteType]` on a known-incompatible source**:
   if the source's HM type doesn't structurally match the target.
3. **Generic instantiation with unresolved type vars**: emitting
   `rt.X[any, T]` where T came from solver fallback.
4. **FFI boundary mismatch**: passing wrong number of args to a
   typed FFI wrapper.

Each pattern → specific Diagnostic with code (E3001 etc.) + Sky
region (from origin tracking).

#### Implementation

Validation pass runs AFTER Go IR is built but BEFORE the Go source
is written. Walks the IR tree, applies pattern matchers, accumulates
Diagnostics. If non-empty: writes nothing, returns the diagnostics
to the user.

This means the user never sees a `go build` error from a
compiler-introduced bug class. The bug is caught + reported in Sky
terms BEFORE Go sees the code.

#### What stays in the `go build` path

Real Go errors (third-party FFI binding issues, Go SDK changes) that
codegen validation can't predict — these go through the existing
`runGoBuildWithDiagnostics`. The "compiler bug detected" footer stays
but should fire less often (Layer 2 catches most cases pre-emptively).

---

### Layer 3: Sky-written stdlib

Move `String.*`, `List.*`, `Dict.*`, `Maybe.*`, `Result.*`,
`Set.*` from `runtime-go/rt/*.go` to `sky-stdlib/Std/*.sky`.

Keep `runtime-go/rt/primitives.go` for the bottom layer: Go-syscall
helpers (file IO, time, crypto, allocator). The stdlib functions
become Sky source compiled to Go via normal codegen.

#### Bootstrapping

Sky compiler needs stdlib to compile USER code. If stdlib is in Sky,
what compiles the stdlib?

**Two-pass build**:
1. Pass 1: compile sky-stdlib using a MINIMAL kernel registry (only
   the primitive Go FFI bindings). Output: typed Go for stdlib.
2. Pass 2: user code compiles against the stdlib's typed Go output.

The compiler caches Pass 1 output. End users never see it; they only
see Pass 2.

#### Migration order

Per-module priority (small surface first, find bugs early):
1. `Std.Set` (16 functions) — smallest, well-typed
2. `Std.Maybe` / `Std.Result` (20 functions each)
3. `Std.Dict` (~30 functions)
4. `Std.List` (~40 functions) — hottest path
5. `Std.String` (~50 functions) — biggest, complex (uniseg etc.)

Each module migrated with:
- Sky source under `sky-stdlib/Std/X.sky`
- Bottom-layer Go FFI in `runtime-go/rt/primitives_x.go` (or shared)
- Per-function performance benchmark
- All existing tests passing

#### Performance budget

Sky-compiled stdlib may be slower than hand-written Go due to
remaining `any` overhead. Budget:
- ≤ 10% regression on `cabal test` runtime (currently ~22 min)
- ≤ 5% regression on `scripts/example-sweep.sh --build-only`
- Per-function: ≤ 2× slowdown vs current

Mitigation: if a specific function regresses past 2×, keep it in Go
with a comment explaining why. Document the exceptions.

---

### Layer 4: LSP runs the full pipeline

Currently the LSP stops after HM. Extend it to:
- Run codegen (cached per-module — incremental)
- Run Layer 2's validation pass
- Surface ALL Diagnostics via `textDocument/publishDiagnostics`
- Skip the actual `go build` (still too slow for per-keystroke)

#### Incremental compilation

Re-running codegen on every keystroke is too expensive (skyshop's
codegen is ~3s cold). Use `.skycache/lowered/` (already exists) to
skip per-module work that hasn't changed.

Cache key: hash of (module source + dep sigs + compiler version).
Cache hit → skip codegen for that module. Only the changed module
re-runs.

#### Diagnostic publishing

LSP's `publishDiagnostics` is a per-file array. Diagnostics from
ALL phases (parse, canonicalise, HM, exhaustiveness, codegen-
validation) get merged per file and pushed.

#### "Run go build" trigger

Per-keystroke `go build` is too slow. But on `textDocument/didSave`
we can run a full `sky build` in the background and surface any
remaining errors. This catches the 5% of bugs Layer 2 doesn't
pre-emptively catch (real Go SDK changes, etc.).

---

## Self-grill: what could go wrong

### 1. Diagnostic AST coupling

Risk: AST becomes too coupled to Sky internals, can't be reused for
external tooling (IDEs, doc generators).

Mitigation: keep `Diagnostic` PURE (no compiler-internal types
exposed). Use file paths + strings everywhere; never expose `T.Type`
in the AST.

### 2. LSP performance

Risk: per-keystroke validation slows the editor to unusable.

Mitigation: per-module cache + budget — if validation takes >500ms
for a module, skip and log a warning. Validation is best-effort,
not blocking.

### 3. Test fixture churn

Risk: every error-text-matching cabal test (`isInfixOf` checks)
breaks when we move to structured Diagnostics with new format.

Mitigation:
- New tests assert on `Diagnostic.code` (stable) not `_message`
- Legacy tests keep string match against the new renderer's output
- Migration done one phase at a time so test breakage is localised

### 4. Stdlib bootstrapping bug surface

Risk: Sky-written stdlib has subtle bugs that didn't exist in the
hand-written Go. Could break user programs unexpectedly.

Mitigation:
- Per-function test suite (existing rt/*_test.go ports to
  sky-stdlib/Test/*.sky)
- Migrate smallest modules first; validate against examples before
  next module
- Keep the hand-written Go around for one release as fallback
  (`SKY_STDLIB_LEGACY=1` env var). Removed in v0.14.

### 5. Sky.Ffi.call deprecation

Risk: deprecating raw `Sky.Ffi.call` breaks user code that relies on
it.

Mitigation: don't remove it. Keep as escape hatch. Just push users
toward typed `foreign import` declarations via documentation +
LSP-emitted info-level diagnostics ("consider replacing Sky.Ffi.call
with a typed foreign import").

### 6. Origin tracking overhead

Risk: tracking Sky region for every emitted Go expression bloats
generated code with comments, slows go build.

Mitigation: tracking is COMPILE-TIME only (not runtime). The `//
sky:` comments are stripped by go's parser during build — zero
runtime overhead.

### 7. Codegen-validation false positives

Risk: validation rejects valid Go that codegen produced because the
pattern matcher is too broad.

Mitigation:
- Each pattern matcher has a dedicated regression test
- `SKY_CODEGEN_VALIDATION=warn` env var downgrades errors to
  warnings during development
- Validators ship as opt-in initially (`--strict-codegen`), promoted
  to default once stable

### 8. Multi-week timeline

Risk: 8-12 weeks is a long time without intermediate ships.

Mitigation: **single-tag release** — v0.13.0 covers Layer 1 +
Layer 2 + Layer 3 + Layer 4.  We don't split this across multiple
patch versions; the user owns release-tag scope and `v0.13` is one
agreed milestone.  Progress is tracked in this doc's "Progress
log" section instead of via intermediate tags.

### 9. Compiler self-host risk

Risk: Sky-written stdlib means the compiler depends on its own
output to compile. A bug in codegen breaks the bootstrap.

Mitigation:
- Keep hand-written Go fallbacks tagged with build flags
- CI runs both `--with-sky-stdlib` and `--with-go-stdlib` paths
- Bisect cleanly when regressions hit

### 10. The "good enough" trap

Risk: Elm-quality error attribution is iterative. First cut won't
match Elm. There's pressure to ship "partial Elm" that confuses
users worse than current.

Mitigation:
- The four layers are scoped INDEPENDENTLY of Elm-quality wording.
  Layer 1 = AST is structural. Layer 2 = catches bugs.
- Error message wording is a separate dimension. We tune it per
  diagnostic code, ship incrementally, and DON'T claim "Elm parity"
  until specific codes are polished.

---

## Success criteria

### Layer 1 — Diagnostic AST
- [ ] `Diagnostic` type defined in `Sky.Reporting.Diagnostic` module
- [ ] CLI renderer produces Elm-style output for at least Parse,
      Canonicalise, HM errors
- [ ] LSP renderer produces valid JSON with related-information
- [ ] All existing tests pass (with updated message expectations
      where needed)
- [ ] Diagnostic code registry started: at least 20 codes documented

### Layer 2 — Codegen validation
- [ ] Origin tracking on every emitted Go expression
- [ ] At least 4 validation patterns implemented:
      - typed-kernel-with-any-arg
      - rt.Coerce-on-known-incompatible
      - generic-instantiation-with-unresolved-tvar
      - FFI-arity-mismatch
- [ ] Issue #52's `List.drop` regression case caught at codegen
      stage (not go build stage)
- [ ] `runGoBuildWithDiagnostics` falls back to source-mapped errors
      via origin comments

### Layer 3 — Sky-written stdlib
- [ ] `Std.Set` migrated; tests pass
- [ ] `Std.Maybe` / `Std.Result` migrated
- [ ] `Std.Dict` migrated
- [ ] `Std.List` migrated
- [ ] `Std.String` migrated
- [ ] Performance budget met (≤ 10% cabal test regression, ≤ 5%
      example sweep regression)
- [ ] LSP hover works on stdlib functions (Sky source, not Go)
- [ ] `SKY_STDLIB_LEGACY=1` flag available for one release

### Layer 4 — LSP full pipeline
- [ ] LSP runs codegen + validation per `textDocument/didChange`
- [ ] Diagnostics surface in editor before save
- [ ] Issue #52's `List.drop` class visible as red squiggle BEFORE
      `sky build`
- [ ] Performance: per-keystroke validation under 500ms p99 on
      skyshop-scale projects
- [ ] `textDocument/didSave` triggers background `sky build` for
      remaining gap classes

### Overall guarantee
- [ ] Test suite: a new `test/Sky/Diagnostics/CoverageSpec.hs` runs
      every fixture in `test/fixtures/diagnostics/` (one per error
      class) and asserts:
      1. Sky CLI reports the diagnostic with correct code + region
      2. LSP publishes a matching diagnostic
      3. Runtime never sees the program (caught pre-codegen or at
         codegen-validation)
- [ ] No regression in any v0.12.x example
- [ ] `cabal test` green
- [ ] `scripts/example-sweep.sh --build-only` green across all
      examples
- [ ] Deep Playwright sweep (all Live apps) green
- [ ] `runtime-go/rt/*_test.go` green

---

## Concrete next actions

1. ✅ Plan doc written (this file)
2. Branch already exists: `perf/v0.13`
3. Layer 1 — Diagnostic AST + phase migrations.  See Progress
   log below for per-commit status.
4. Layer 2 — Codegen-stage validator + go-build refiner.
5. Layer 3 — Sky-written stdlib (Set → Maybe/Result → Dict →
   List → String) with bootstrap two-pass build.
6. Layer 4 — LSP runs full pipeline (Parse → Canon → Solve →
   Exhaust → Codegen → Validator) per textDocument/didChange,
   with incremental codegen cache for sub-500ms p99 latency.

**Single tag release**: v0.13.0 ships Layer 1 + 2 + 3 + 4
together.  Per-layer progress is tracked in this doc, not via
intermediate tags.

---

## Progress log (perf/v0.13 branch)

Kept in this doc so future sessions can pick up cleanly without
scanning the commit log.

### Layer 1 — Diagnostic AST + phase migrations  ✅ COMPLETE (foundation)

| Phase | Status | Commit |
|---|---|---|
| AST + CLI renderer + LSP serialiser | ✅ landed | `048e5e2` |
| `DiagnosticSpec` regression fence (21 tests) | ✅ landed | `048e5e2` |
| Canonicalise unbound-name → Diagnostic | ✅ landed | `9cece03` |
| Type-mismatch (Solve.SolveError) → Diagnostic | ✅ landed | `92bf0ba` + `3fe70dc` + `4148ed0` |
| Exhaustiveness → Diagnostic (E3001) | ✅ landed | `144e382` |
| Parse errors → Diagnostic (E0001) | ✅ landed | `9811041` |
| Canonicalise generic errors → Diagnostic | ✅ landed | `c99ad91` |
| Category-prefixed headers (TYPE / PARSE / NAMING / etc.) | ✅ landed | `c99ad91` |

**User-visible result**: every type / parse / unbound / exhaustiveness
error now prints an Elm-style block:

    -- TYPE ERROR ──────────────────────────── src/Main.sky:9:18 [E2001]

        7 |
        8 | main =
        9 |     println (add "hello" 1)
          |                  ^

    Type mismatch:
         expected: Int
         actual:   String

Stable error codes (E0001 parse / E1001 unbound / E2001 type / E3001
exhaust) for grep + LSP filtering.  Stderr keeps a one-line marker
for CI/test grep compatibility.  Solver-budget errors keep their
verbatim guidance block (would lose detail in a structured wrapper).

### Layer 2 — codegen-stage validator + go-build error refiner  ✅ COMPLETE

| Aspect | Status | Commit |
|---|---|---|
| `Sky.Build.Validator` module (pattern matcher) | ✅ landed | `f42c5dd` |
| Pattern 1 — typed-kernel-with-any-arg (E4001) | ✅ landed | `f42c5dd` |
| Pattern 2 — rt.Coerce-on-incompatible | ⏭️  deferred — needs HM context |
| Pattern 3 — generic-instantiation-with-unresolved-tvar | ⏭️  deferred — low value, `any` is valid Go |
| Pattern 4 — FFI-arity-mismatch | ⏭️  deferred — needs cross-file lookup |
| Origin tracking (function-level granularity) | ✅ landed | `f42c5dd` |
| `runGoBuildWithDiagnostics` → Sky Diagnostic | ✅ landed | `f42c5dd` |
| `ValidatorSpec` regression fence (12 tests) | ✅ landed | `f42c5dd` |

The canonical Issue #52 class (typed-kernel call with raw any
arg) is caught BEFORE `go build` sees the output.  Origin comments
(`// SKY-ORIGIN: <path>:<line>:<col>`) injected into main.go at
every top-level function declaration let `runGoBuildWithDiagnostics`
map Go-build errors back to Sky source — when go-build does fail
(rare, only on un-fixed compiler bugs), the user sees an Elm-style
block with [E5001] code pointing at the Sky source instead of a
cryptic `main.go:1234:56` reference.

Deferred patterns (2/3/4) aren't blockers for the soundness floor
because:
* Pattern 2 needs HM type context — hard from raw text.  Hand-
  matched cases are caught at HM-level (`rt.Coerce` only fires on
  Sky-typed values already; mismatch surfaces as TYPE ERROR).
* Pattern 3 — `any` in generic slot is valid Go.  It's a perf
  marker, not a correctness bug.  No user-visible regression.
* Pattern 4 — FFI arity mismatch needs cross-file `.skyi` lookup;
  today the FFI generator emits consistent shapes by construction.

### Layer 4 — LSP runs full pipeline  🚧 PARTIAL

| Aspect | Status | Commit |
|---|---|---|
| LSP uses `Sky.Reporting.Lsp.renderLspDiagnostic` | ✅ landed | `3c9ce85` |
| LSP emits stable `code` field | ✅ landed | `3c9ce85` |
| LSP code-field regression fence (4 tests) | ✅ landed | `742dc9e` |
| Issue #52 class → red squiggle (via HM closed-record sigs) | ✅ landed | pre-v0.13 |
| LSP runs codegen + validator per didChange | ⏭️  deferred — needs incremental codegen cache |
| Related-information regions in LSP output | ⏭️  deferred — needs HM-side region tracking |

LSP diagnostics share the same structured Diagnostic AST as the
CLI.  Every error category surfaces with the stable [Ennnn] code,
`source: "sky"`, and the same wording (CLI uses Render, LSP uses
the JSON serialiser).  Editor extensions can filter / link by
code.

The "LSP runs codegen + validator per keystroke" item is the
v0.13.2 milestone.  Without an incremental codegen cache it would
add 1-2s per keystroke on small projects, 10-15s on skyshop — UX
worse than `cmd-shift-B`-then-`go to error`.  Today's LSP runs the
full HM pipeline (Parse → Canon → Constrain → Solve → Exhaust);
that covers every error category v0.12.x users hit in practice.

### Layer 3 — Sky-written stdlib  ❌ BLOCKED — multi-week compiler refactor

Investigation 2026-05-13: naive Sky-source migration of Maybe /
Result / Basics / List surfaces multiple cross-cutting compiler
bugs that each need focused refactor before stdlib modules can
ship as Sky source.  Documented blockers:

1. **Cross-module typed-codegen coercion gap.** Sky-source
   `withDefault : a -> Maybe a -> a` emits `func Sky_Core_Maybe_
   withDefault[T1 any](def T1, m rt.SkyMaybe[T1]) T1`.  Call
   sites coerce the second arg via `rt.MaybeCoerce[any]`
   (because `eraseTypeParams` in `coerceArg` strips type
   params to `any`).  Go then infers T1=string from the first
   arg's literal but sees `SkyMaybe[any]` from the second arg
   → `does not match inferred type` error.  Fix needs HM-aware
   coerce-instantiation: thread inferred type params from call
   site through to the coercer.  Multi-day refactor.

2. **Sky-source ADT vs kernel ADT unification.**  `case
   List.head xs of Nothing -> ...` failed HM with
   `expected: Maybe; actual: Maybe (Dict String String)` when
   Sky-source `Maybe` was present.  The kernel's `Just` /
   `Nothing` ctors (pre-registered with home=ModuleName.maybe_)
   coexist with Sky-source ctors and the type checker can't
   tell them apart at pattern-match sites.  Fix: remove
   kernel-side Maybe/Just/Nothing pre-registration when Sky
   source provides them.  Touches Canonicalise.Environment,
   the canonicaliser's import-resolution path, and every
   cross-module externals consumer.  Multi-day refactor.

3. **Polymorphic recursion in typed codegen.**  Sky-source
   `List.foldr fn acc xs` emits `Sky_Core_List_foldr[T1, T2]`
   but the recursive call inside the body can't infer T2
   → `cannot infer T2`.  Same class as v0.12 Limitation #18
   (lambda lowering preserves types).  Fix: typed lambda
   lowering for cross-module function defs.

4. **Missing runtime helpers (compare).**  `Basics.compare`
   has a kernel HM type signature but no runtime helper.  Any
   Sky-source `List.sortBy` that uses `compare` emits
   `rt.Basics_compare(...)` which doesn't exist.  Fix: add
   the runtime helper + a typed-generic variant.

5. **Go-keyword name mangling across dep boundaries.**  ✅
   FIXED in commit `cdb8384`.  goSafeName now applied
   consistently across dep-emit + sig tables.

Conclusion: Layer 3 as ORIGINALLY scoped (every stdlib module
migrated to Sky source with perf budget) is a 3-6 week project
matching the plan doc's original estimate.  Items 1-4 above
are each multi-day compiler refactors; combined they push the
scope beyond a single session.

Forward-compat work that DID land in this session:
* `goSafeName` alignment (commit `cdb8384`)
* Dep-module type errors → Diagnostic (commit `cd53c4e`)
* Investigation captured here for the next session.

### Layer 3 — Monomorphisation foundation  ✅ A1-A3 LANDED

User's insight (2026-05-13): HM has already done the type analysis;
monomorphisation is bookkeeping over its output, not new analysis.
Solver records `(callsite-region, callee-name, fresh-vars)` at every
`CForeign`; post-solve walks fresh-vars through union-find to
produce a `Set (callee, concrete-type-args)`.  This kills every L3
architectural blocker structurally:

| Blocker | How monomorphisation fixes it |
|---|---|
| Cross-module typed-codegen coercion gap | No generics emitted. Concrete-typed Go per instance. |
| Sky-source vs kernel ADT phantom duality | Sky source canonical; kernel pre-reg becomes optional fallback. |
| Polymorphic typed-codegen recursion | Each instance is non-polymorphic; recursive calls resolve trivially. |
| Missing `rt.Basics_compare` runtime helper | Sky source for `compare` generates per-type. |
| Incremental codegen cache | Cache key = `(instance-mangled-name, body-hash, dep-hashes)`. |

**Phase A — monomorphisation infrastructure**

| Step | Status | Commit |
|---|---|---|
| A1 — solver instrumentation: capture call-site instances | ✅ landed | `d3b0b84` |
| A2 — mangling (`mangleType`, `mangleInstance`) + substitution (`substituteType`) | ✅ landed | `96799ca` |
| A3 — compile pipeline wires `solveWithInstances`, logs counts + names | ✅ landed | `c4aaaf3` |
| A4 — per-instance Go emission (specialised functions) | ✅ shipped (`6431680`, `6acbabd`, `99be0da`, `901781a`) — specs co-exist with generics; drop-pass deferred |
| A4 — typed lambda emission at call sites | ✅ shipped (`8195752`) — handles single-arg + curried multi-arg HOFs |
| A4 — pass-3 fixpoint for dep CSI capture | ✅ shipped (`901781a`) |
| A5 — call-site rewriting (concrete-type substitution in coerceArg) | ✅ shipped (`68af62b`, `f43a2d4`, `4846566`) |
| A5+ — partial-instance arg-consistency via Sky-name-aligned σ | ✅ shipped (`f43a2d4`) |
| A5++ — eraseTypeParams fallback for un-captured call sites | ✅ shipped (`4846566`) |
| A6 — `_Any` fallback for FFI / untyped boundaries | ✅ via normaliseUnresolved (`1511f18`) |
| A6 — let-polymorphism for top-level recursive groups | ✅ unblocked via `::` binop fix (`36a03a4`) |
| A7 — Sky-level DCE reachability walker | ✅ shipped (`06ce0d8`) |
| A7 — drop generic versions when specs exist | ⏭️ blocked on reach-walker value-ref types (multi-day, v0.14) |
| A8 — regression specs across emission + dedup + perf | partial (A1+A2 specs live) |

**A5+ closed the partial-instance arg-consistency gap (`f43a2d4`)**

Three coordinated changes wire call-site coercion to the SKY-name
space:

1. **Solver captures quantifier names.** `CallInstance` and
   `CallInstanceRecord` carry the annotation `Forall` var names
   (`_instance_quantifiers`) in positional alignment with the
   captured concrete types.  `instantiateAnnotation` returns the
   quantifier-name list alongside the fresh-var list so CForeign
   records both.

2. **Per-function SkyName→GoName map.**  CgEnv gains
   `_cg_funcSkyToGoTVars` (registered alongside
   `_cg_funcInferredSigs`).  `inferredSigSkyToGo` mirrors
   `splitInferredSigWithReg`'s `keptNumbered` and exposes the
   mapping the substitution path needs.  Dep + entry registrations
   apply `generaliseToAnnotation`'s renaming before computing the
   map so SkyNames align with the user-friendly Forall names the
   solver captures.

3. **`coerceCallArgsAt` rebuilt with Sky-name substitution.** Zip
   quantifiers with concrete types into σ_sky, then project to
   σ_go through `_cg_funcSkyToGoTVars`.  Annotation positions
   absent from the map (e.g. Result's `e` collapsed at codegen-
   time to `Sky_Core_Error_Error` by error-position defaulting)
   are skipped — their slot in the emitted Go sig is already
   concrete.  Projected Go type strings go through
   `sanitiseTypedDeep` to scrub anonymous-record names that have
   no Go alias counterpart.

**A5++ — `eraseTypeParams` fallback (`4846566`)**

Some call sites don't get a captured CallInstance because the
surrounding constraint emission is deferred (e.g. `Can.Update`
lowers to `CTrue`, skipping every nested CForeign).  Pre-fix
those sites fell through to the un-substituted branch and emitted
raw `paramTypes` containing `T1`/`T2` tokens — leaking as bare
identifiers in the Go output.  Fix: erase generic type-param
tokens to `any` in the fallback path so `coerceArg` routes
through the `_Any` variants (`AsListAny`, `Coerce[func(any) any]`,
`ResultCoerce[..., any]`).

Status:
* **Maybe.sky** (1-tyvar): ✅ ships
* **Result.sky** (2-tyvar with `e` defaulted to Error): ✅ ships
* **List.sky** (multiple recursive top-level functions): ❌
  blocked on A6 let-polymorphism.  Isolated foldl works
  (`Sky_Core_TestList_foldl[T1 any, T2 any]`) but the same
  function alongside `concat`, `reverse`, etc. in the same
  recursive top-level group breaks generalisation.  Tracked as
  task #121.

Cross-file CSI key (file, line, col):
* Pre-fix used `(line, col)` only — calls at the same position
  in different dep modules collided in the Map and dropped
  instances.  Attempted forcing approach (`mapM` with writeIORef
  + length . show . seq) ran into a cgEnv ordering issue:
  `solvedTypeToGo` consulted `_cg_recordAliases` before the
  multi-module pass had registered dep record aliases, so
  substituted types emitted as bare names instead of `_R`-suffixed
  alias names.  Reverted to (line, col)-only with documented
  collision behaviour: collisions trigger the `eraseTypeParams`
  fallback, which produces correct (but `any`-widened) Go.  Full
  file-qualified keying needs either reordering the multi-module
  pipeline or invasive file-path threading — deferred.

Captured instance counts across the 10 spot-checked examples
(from A3's logging):

| Example | Instances | Polymorphic callees |
|---|---|---|
| 01-hello-world | 0 | 0 |
| 02-go-stdlib | 4 | 3 |
| 06-json | 37 | 17 |
| 07-todo-cli | 14 | 13 |
| 08-notes-app | 8 | 8 |
| 09-live-counter | 7 | 7 |
| 12-skyvote | 19 | 18 |
| 14-task-demo | 2 | 2 |
| 18-job-queue | 24 | 17 |
| 19-skyforum | 11 | 11 |

Sample mangled names (todo-cli, via `SKY_MONO_TRACE=1`):
* `Maybe_withDefault__String`
* `List_map__DictOf_String_String_String`
* `Result_withDefault__Error_ListOf_String`
* `Task_run__Error_ListOf_String`

Concrete types throughout — no `any` or unresolved TVars in the
captured set.  Foundation is solid for A4+ wire-up.

**Phase B — Sky stdlib migration**  🚧 substantial progress

| Module | Status | Notes |
|---|---|---|
| `Sky.Core.Maybe` | ✅ shipped (`68af62b`) | 11 combinators; ADT stays kernel-anchored |
| `Sky.Core.Result` | ✅ shipped (`f43a2d4`) | 10 combinators; ADT stays kernel-anchored |
| `Sky.Core.Basics` | ✅ shipped (`8075882`) | identity/always/not/fst/snd/clamp; numerics + toString/compare stay kernel |
| `Sky.Core.List` (non-HOF) | ✅ shipped (`36a03a4`) | isEmpty/length/head/tail/cons/reverse/take/drop/append/concat/member/range/zip |
| `Sky.Core.List` (HOFs) | ❌ blocked on Gap 4 | map/filter/foldl/foldr/concatMap/filterMap/find/any/all need typed lambda lowering |
| `Sky.Core.Dict` | ⏭️  intentionally kernel | Go-map semantics; Sky source has no value-add |
| `Sky.Core.Set` | ⏭️  intentionally kernel | Same as Dict |
| `Sky.Core.String` | ⏭️  intentionally kernel | Rune ops + Sprintf-style helpers — FFI surface |
| Time / Random / Http / File / Crypto / Encoding / Regex | ⏭️  intentionally kernel | Pure FFI / Go-side primitives |
| `Sky.Test` | ✅ already Sky source | Test harness module |
| `Std.Ui` + sub-modules (Background, Border, Font, Input, Region, Lazy, Keyed, Responsive, Events) | ✅ already Sky source | Typed no-CSS layout DSL |

**Two cross-cutting HM fixes landed alongside the migration:**

1. **`::` binop typing** (`36a03a4`): pre-fix the cons operator
   fell through to `binopTypes`'s catch-all, producing fresh
   unrelated `a/b/r` tvars.  This broke HM inference for any
   Sky-source body that constructs lists via `x :: xs` —
   `Sky.Core.List.map` came out as `(a → List b) → List a →
   List (List b)` instead of `(a → b) → List a → List b`.
   Fixed: `"::"` now produces `(elem, List elem, List elem)` as
   its binop types, matching the kernel cons sig.

2. **`("List", "cons")` kernel sig** (`36a03a4`): direct
   `List.cons` calls (from inside Sky-source stdlib that
   imports `cons` explicitly) need the same HM-side sig.
   Added.

**Gap 4 (typed lambda lowering) remains the gating fix** for the
HOF surface.  Sky lambdas lower to `func(any) any` regardless of
HM-inferred input/output types.  When a Sky-source HOF in a dep
module accepts a function param, the param's emitted Go sig
collapses to `func(T1) any` — and HM-inferred return type
information lost.  Symptom at user-side call sites: `cannot infer
T2` from Go build.  Fix is a multi-day refactor that threads
HM-inferred types through `curryLambdaPat`'s emission.

**A6 (let-polymorphism)** turned out NOT to be the gating issue
for the non-HOF surface — the `::` binop fall-through was the
real culprit for the List.map "concatMap-shaped type" symptom.
A6 remains open for future work as a robustness improvement, but
isn't blocking any current B-phase work.

**A6 let-polymorphism blocker (List/Dict/Set migration)**

Sky's HM doesn't generalise top-level recursive let-groups
correctly.  `Can.DeclareRec` adds each binder as `Forall []`
(monomorphic) to the environment, then constrains each body
against the pre-registered monomorphic type.  No generalisation
happens at the end of the recursive group.  Same-module recursive
references go through `CLocal` (not `CForeign`), so the recursive
call uses the in-progress monomorphic type — and HM produces
narrow inferred types.

Example: `Sky.Core.List.foldl` placed alongside `append`, `concat`,
`reverse`, etc. in the same module emits as
`func Sky_Core_List_foldl[T1 any](fn func(T1) func([]any) any,
acc []any, list []T1) []any` — `acc` and the function's second
arg collapse to `[]any` instead of staying polymorphic.  But
foldl in isolation (separate module with no other definitions)
emits as `[T1 any, T2 any]` correctly.

Fix needs constrainDecls' `Can.DeclareRec` path to use `CLet`
with proper generalisation semantics — solving the recursive
group's body bindings first, then generalising over the captured
flex-vars, then making the bindings available to the body's
free-var references with the generalised type.  Tracked as
task #121.

Once A4-A7 land, stdlib modules port mechanically:
* B1. Basics, Maybe, Result (pure-Sky ADT eliminators)
* B2. List, Dict, Set, String
* B3. Effect modules (Time, Random, Http, File, Process, Io, System)
* B4. UI / Live (Cmd, Sub, Log, Db, Auth, Html, Css, Event, Live, Server, Tui, Cli)
* B5. Json, Test, Uuid, Crypto, Encoding, Math, Char, Path, Regex
* B6. Remove kernel registry entries (now Sky-source-resolved)
* B7. Perf budget verification (≤10% cabal regression, ≤5% example sweep)

**Earlier-session failed migrations** (rolled back):
* `Sky.Core.Maybe.sky` attempted in commit `b858d31`, reverted
  `79e044f`.  Hit the typed-codegen coercion gap (`rt.MaybeCoerce[any]`
  vs T1=String inference) AND the Sky-source / kernel-ADT duality
  (`expected: Maybe; actual: Maybe (Dict String String)`).  Both
  resolved structurally by monomorphisation once A4+A5 land —
  no need to fight per-bug patches.

### Coverage spec — overall guarantee  ✅ COMPLETE

`test/Sky/Diagnostics/CoverageSpec.hs` (6 tests) runs every fixture
in `test/fixtures/diagnostics/` (one per error class) and asserts:

| Fixture | Code | Category |
|---|---|---|
| parse-error.sky | [E0001] | PARSE ERROR |
| unbound-name.sky | [E1001] | NAMING ERROR |
| type-mismatch.sky | [E2001] | TYPE ERROR |
| non-exhaustive.sky | [E3001] | EXHAUSTIVENESS ERROR |

Each fixture must exit non-zero (the runtime never sees the
program) and carry a stable [Ennnn] code.  Adding a new error
category requires (a) writing a fixture, (b) adding an `it` block,
(c) registering the code in `Sky.Reporting.Diagnostic`.

---

## What landed today (2026-05-12 / 2026-05-13)

After v0.12.1 ship, across the session:

**Layer 1 (the AST + 5 migrations + UX polish)**
* AST + CLI + LSP renderer + DiagnosticSpec: `048e5e2`.
* canonicalise unbound: `9cece03`.
* type-mismatch routing: `92bf0ba` + `3fe70dc` + `4148ed0`.
* exhaustiveness: `144e382`.
* parse errors: `9811041`.
* canonicalise generic: `c99ad91`.
* category-prefixed headers: `c99ad91`.

**Layer 2 (codegen-stage validator + go-build refiner)**
* `Sky.Build.Validator` + ValidatorSpec (12 tests): `f42c5dd`.
* Origin comments injected into main.go: `f42c5dd`.
* runGoBuildWithDiagnostics → Sky Diagnostic: `f42c5dd`.

**Layer 4 partial (LSP wire-shape parity)**
* LSP routes through renderLspDiagnostic: `3c9ce85`.
* LSP code-field regression fence (4 tests): `742dc9e`.

**Coverage spec**
* Fixtures + CoverageSpec (6 tests): `5197b67`.

**Plan-doc tracking**
* Progress log: `c957aa2`.

13 commits.  120+ tests pass across the impacted specs.  Zero
existing-feature regressions on `cabal test`.

## v0.13.0 release readiness (single tag covers Layer 1-4)

* [x] **Layer 1** — Diagnostic AST + CLI + LSP renderers + 5
      phase migrations + DiagnosticSpec regression fence (21 tests).
* [x] **Layer 2** — Validator + origin tracking + go-build refiner
      + ValidatorSpec (12 tests) + GoBuildRefinerSpec (1 e2e test).
* [ ] **Layer 3** — Sky-written stdlib (Set / Maybe / Result /
      Dict / List / String) with bootstrap two-pass build.
* [ ] **Layer 4 full** — LSP runs codegen + validator per
      textDocument/didChange with incremental codegen cache.
* [x] Coverage spec — one regression fixture per error class.
* [x] `cabal test sky-tests` green across impacted modules.
* [x] `scripts/example-sweep.sh --build-only` green across all
      24 examples.
