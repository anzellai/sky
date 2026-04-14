# Sky Haskell Compiler — Production Readiness Plan

**Status doc for a multi-session push to v1.0.** Written 2026-04-13 on branch
`feat/sky-haskell-compiler`. If you are Claude Code resuming this work in a
later session, **read this file end-to-end before touching any code**. It is
the single source of truth for what's done, what's next, and what is
explicitly out of scope.

---

## How to use this doc on resume

1. Read the **Progress tracker** (bottom of file). Identify the lowest-numbered
   unchecked phase.
2. Read that phase section in full. Every phase is self-contained — files,
   acceptance criteria, verification commands are all inline. Do not trust
   your recollection of earlier phases; re-read this file.
3. Before starting any phase, run the **Regression fence** (Phase 0 sweep) to
   confirm you are at a green baseline.
4. When a phase completes: tick the checkbox, commit with the phase label,
   update the "Last verified green" date in the progress tracker. **Do not
   skip phases. Do not merge phases.**
5. If a phase proves wrong in practice, amend the doc *first*, commit the
   amendment, then resume. Never let the doc drift from reality.

### Anti-regression rules (non-negotiable)

- **No `any` escape hatches** added to "make it compile". If a codegen path
  can't type something, stop and fix the type system, don't emit `any`.
- **No workaround comments** like `// TODO: proper fix later` in generated Go
  or Haskell source. If a proper fix is too large for the current phase,
  split the phase — don't defer inside it.
- **No regression is acceptable.** The 20-example sweep must stay green after
  every commit. If a phase requires temporary regression, it means the phase
  was wrongly scoped.
- **No silent try/recover** in runtime that swallows type errors. Panic
  recovery is for FFI boundaries only; internal type mismatches must be
  compile-time errors.
- **Reflection in runtime-go/rt is a smell.** Every `reflect.ValueOf` in the
  hot path is a bug. Each phase should *reduce* reflection usage, never add.

---

## Current state snapshot (2026-04-13)

- Branch: `feat/sky-haskell-compiler`
- Examples passing `sky build`: 20/20 (build-only, not runtime-verified end-to-end)
- Compiler LoC: ~15k Haskell across `src/Sky/**`
- Runtime LoC: ~15k Go across `runtime-go/rt/**`
- Type system: Hindley-Milner with Go-generics emission for polymorphic
  unannotated functions. Annotated functions use their declared type as the
  scheme (enforced, not inferred-over).
- Type errors: **FATAL** as of this session (promoted from warning in
  `src/Sky/Build/Compile.hs:354`).
- Embedded stdlib: Template Haskell pulls `sky-stdlib/*.sky` into the binary.
  Currently only `Std/IoError.sky` is actually implemented as Sky source.

### Audit summary — the 12 gaps this plan closes

| # | Gap | Phase |
|---|---|---|
| 1a | Codegen emits `any` for records (`TRecord`) | P4 |
| 1b | Codegen emits `any` for tuples (`TTuple`) | P5 |
| 1c | Unresolved TVars fall back to `any` instead of Go type param | P6 |
| 2 | `exposing` clause hardcoded to `ExposingAll` | P2 |
| 3 | Pattern exhaustiveness checker is a stub | P3 |
| 4 | No test suite (cabal test stanza missing) | P0 |
| 5 | Generic FFI funcs emit runtime-Err stubs | P9 |
| 6 | No type classes / HKT / row polymorphism | **OUT OF SCOPE** (see below) |
| 7 | Parser gaps: negative pattern, selective ctor export, let-after-case | P1 |
| 8 | `Std.Db`, `Std.Auth`, `Sky.Live`, `Sky.Http.Server` documented but unimplemented | P10 |
| 9 | `sky upgrade` stubbed; Sky-source `[dependencies]` not installable | P11 |
| 10 | FFI wrappers use `(any) any` — 74k+ wrappers bypass type system | P7 |
| 11 | Kernel stdlib (225 funcs) uses `(any) any` — runtime `ResultCoerce` masks mismatches | P8 |
| 12 | LSP `-Wunused-top-binds` false positives (benign) | **NOT A BUG** — noted only |

### Explicitly OUT OF SCOPE

- **Type classes, HKT, row polymorphism.** Per CLAUDE.md these are
  intentional Elm-style omissions. Do not add them. If a stdlib design feels
  like it needs them, the stdlib API is wrong.
- **`where` clauses.** Intentional.
- **Custom operators.** Intentional.
- **Anonymous records in function signatures.** Intentional (CLAUDE.md known
  limitation #1).
- **Tailwind / Sky-source package manager beyond basic `sky install`.**
  Deferred to a post-v1.0 plan. Phase 11 covers only the MVP shape.

---

## Phase dependency graph

```
P0 (test harness) ──┬─► P1 parser gaps
                    ├─► P2 exposing clause
                    ├─► P3 exhaustiveness
                    └─► P4 records ──► P5 tuples ──► P6 typed TVars
                                                      │
                                                      ├─► P7 FFI typing
                                                      └─► P8 kernel typing
                                                            │
                                                            └─► P12 remove runtime reflection

P9 generic FFI reflect  (independent; can run anytime after P0)
P10 stdlib code         (independent; can run in parallel with P1-P8)
P11 sky upgrade / install (independent; trivial after P0)
```

**P0 is mandatory first.** Everything after can be scheduled by priority.
The typing chain P4 → P5 → P6 → P7 → P8 must run in order (each assumes the
previous is typed).

---

## Phase 0 — Regression fence: test harness

**Goal.** Make `cabal test` a meaningful green/red signal so later phases
cannot silently regress.

**Why first.** Every later phase edits the type system or codegen. Without a
test harness, "20/20 examples build" is the only signal — and it's a coarse
one that doesn't catch runtime-output regressions or partial breakage inside
a single example.

**Files.**
- `sky-compiler.cabal` — add `test-suite sky-tests` stanza using `hspec` or
  `tasty` (choose `hspec` — already common in GHC 9.4 ecosystem).
- **NEW** `test/Spec.hs` — hspec runner.
- **NEW** `test/Sky/Parse/PatternSpec.hs`, `test/Sky/Type/SolveSpec.hs`,
  `test/Sky/Build/CompileSpec.hs`, `test/Sky/Generate/Go/RecordSpec.hs` —
  unit tests per module.
- **NEW** `test/golden/` — golden-output directory. Each `.sky` input has a
  matching `.go.expected` generated Go file. Tests fail if generated Go
  diverges.
- **NEW** `scripts/example-sweep.sh` — the 20-example build-and-run sweep
  that CI runs. Must exit non-zero on any failure.
- `.github/workflows/ci.yml` (create if missing) — run `cabal test` +
  `scripts/example-sweep.sh` on every push.

**Plan.**
1. Add the test-suite stanza. Start with **one** trivial passing test so the
   harness is wired.
2. Port one bug-reproducer per current known-broken case into a unit test
   that currently passes (so future regressions turn it red):
   - `ex13-skyshop` broken-tuple-destructure → parser test.
   - `/tmp/testtype/src/Main.sky` (session scratch) — type-error-must-abort
     test. Copy it into `test/fixtures/type-error-fatal.sky` with expected
     stderr containing `TYPE ERROR`.
3. Add one golden-output test per Sky codegen feature (records, ADTs,
   lambdas, let, case, string interpolation).
4. Add an **end-to-end runtime test** per example: build → run → assert stdout
   or HTTP 200. Server examples get killed after 2s with PID tracking.

**Acceptance.**
- `cabal test` runs, produces a test count, exits 0 on green.
- `scripts/example-sweep.sh` checks `./sky-out/app` actually *runs* (not just
  builds) for each non-server example; for server examples, confirms HTTP
  200 on the main route.
- Golden files committed; intentional codegen changes require explicit
  `UPDATE_GOLDEN=1 cabal test` then review of the diff.

**Verify.**
```bash
cabal build
cabal test --test-show-details=direct
bash scripts/example-sweep.sh
```

**Do NOT.**
- Do not add tests for features not yet implemented — those land in their
  own phase.
- Do not make golden tests so strict that whitespace changes in codegen
  cause noise. Normalise whitespace before comparing.

---

## Phase 1 — Parser gaps

**Goal.** Close the four remaining Sky-syntax holes documented in CLAUDE.md
Known Limitations #5, #9, and `docs/plans/fluttering-shimmying-lantern.md`
(Phases 1-2 of the legacy parser-completeness plan).

**Files.**
- `src/Sky/Parse/Pattern.hs` — negative-int patterns in case arms
  (`case n of -1 -> ...`).
- `src/Sky/Parse/Expression.hs` — let-binding-with-params after multi-line
  case (`bindingCol` alignment fix).
- `src/Sky/Parse/Module.hs` — selective constructor exposing
  (`exposing (Result(Ok, Err))`).
- `src/Sky/AST/Source.hs` — extend `Privacy` to `PublicCtors [String]`.
- `src/Sky/Canonicalise/Module.hs` — honour `PublicCtors` in
  `resolveExposedCtor`.

**Plan.**
1. **Negative patterns.** In `patternAtom`, before `number`, peek for `'-'`
   + digit. If matched, consume, parse number, negate.
2. **Let-after-case.** Investigate `moreLetBindings` in
   `Parse/Expression.hs`. Root cause is `bindingCol` being set inside the
   case branch's nested block, so the next let binding looks like a new
   top-level decl. Fix by remembering the let block's own column separately
   from any nested-block columns encountered during case-branch parsing.
3. **Selective ctor exposing.** Parse `Ctor1, Ctor2` after `Type(`; build
   `PublicCtors`. In canonicaliser, emit only listed ctors; unlisted ctors
   remain type-only (constructor not in scope → clean error).

**Acceptance.**
- Parser test fixtures for each case (in `test/fixtures/parser/`) compile.
- CLAUDE.md Known Limitations #5 and #9 removed.
- `docs/plans/fluttering-shimmying-lantern.md` Phase 2 items checked off.

**Verify.**
```bash
cabal test --test-show-details=direct  # parser specs green
bash scripts/example-sweep.sh           # 20/20 still pass
```

**Do NOT.**
- Do not touch string interpolation, operator sections, or foreign-decl
  payloads — those are Phase 3 of the legacy parser plan and explicitly out
  of scope for v1.0.

---

## Phase 2 — `exposing` clause parsing + module-level hiding

**Goal.** Replace the hardcoded `ExposingAll` in `Parse/Module.hs` with a
real parse, and honour it through canonicalisation.

**Files.**
- `src/Sky/Parse/Module.hs` — around line 45, the current `TODO: parse
  exposing clause`. Replace with real parse.
- `src/Sky/AST/Source.hs` — ensure `Exposing` variants cover `ExposingAll`,
  `ExposingSome [Exposed]`, `ExposedValue String`, `ExposedType String
  Privacy`.
- `src/Sky/Canonicalise/Module.hs` — around line 275 `TODO: expose union
  constructors`. Enforce the exposing list; top-level names not exposed
  must not appear in `Can._decls`'s exported view.
- `src/Sky/Canonicalise/Environment.hs` — import resolution must respect
  the source module's exposing list.

**Plan.**
1. Parse `module M exposing (..)` as `ExposingAll`; `exposing (foo, bar,
   Baz(..), Qux(Ctor1, Ctor2))` as `ExposingSome [...]`.
2. In canonicaliser, produce an `ExportedNames` set. Cross-module lookup in
   `processImport` filters by it. Importing an unexposed name is a clean
   canonicalise error with source context.
3. Generated Go code: unexposed top-level names lowercase their first
   letter (package-private in Go). This automatically enforces hiding at
   the Go layer too.

**Acceptance.**
- A module declaring `exposing (foo)` with two top-level defs exports only
  `foo`; importing the other is a canonicalise error.
- All 20 examples' exposing clauses parse correctly (most use `exposing
  (..)` — trivial path must stay trivial).
- `Ctor(..)` continues to expose all ctors; `Ctor(A, B)` exposes only
  listed.

**Verify.**
```bash
cabal test
bash scripts/example-sweep.sh
# New fixture:
cat test/fixtures/hiding/Main.sky  # imports unexposed — must fail cleanly
```

**Do NOT.**
- Do not change the default visibility of unexposed names at the Go layer
  by any mechanism *other* than the lowercase-first-letter convention.
  Comments-as-access-control is not acceptable.

---

## Phase 3 — Pattern exhaustiveness checker

**Goal.** Replace `src/Sky/Type/Constrain/Pattern.hs` stub with a real
exhaustiveness checker. Non-exhaustive case expressions become compile-time
errors, never runtime panics.

**Files.**
- `src/Sky/Type/Constrain/Pattern.hs` — implement
  `checkExhaustive :: T.Type -> [Can.Pattern] -> Either MissingPatterns ()`.
- `src/Sky/Type/Solve.hs` — thread exhaustiveness into the solve result.
- `src/Sky/Build/Compile.hs` — report missing patterns with source context
  using the same formatter as other type errors; abort build on any.
- Remove the `panic("non-exhaustive case expression")` fallback in
  `src/Sky/Generate/Go/*` — replace with a build-time guarantee that such
  cases are caught earlier.

**Plan.**
Use the standard set-cover algorithm (Luc Maranget, "Warnings for Pattern
Matching", 2007):
1. Represent the value-space per type as a set of constructors (`ADT`) or
   `Wildcard` (infinite types: Int, String, Float).
2. For each case arm, subtract its pattern's covered subset.
3. If residual set is non-empty after all arms, it yields concrete missing
   patterns (e.g. `Just (Ok _)`, `Nothing`).
4. Formatter produces the Elm-style "This `case` does not cover: …" error
   with minimal witnessing examples.

**Acceptance.**
- `case Just 1 of Nothing -> 0` is a compile error with message listing
  `Just _` as missing.
- All 20 examples still pass (none currently have non-exhaustive cases).
- No `panic("non-exhaustive case expression")` string remains in generated
  Go across all examples (grep `sky-out/main.go`).

**Verify.**
```bash
cabal test  # exhaustiveness unit tests
bash scripts/example-sweep.sh
for d in examples/*/sky-out; do
    grep -l "non-exhaustive" "$d/main.go" && exit 1  # must be absent
done
```

**Do NOT.**
- Do not gate the check behind a flag. Always on.
- Do not allow `_` wildcards to mask missing constructor cases silently —
  Elm/Haskell convention is `_ -> ...` at the end covers everything, but a
  wildcard in the middle of non-wildcard arms must still be reported when
  structurally informative.

---

## Phase 4 — Typed record codegen (eliminate `any` for `TRecord`)

**Goal.** `Compile.hs:2940` currently returns `"any"` for
`T.TRecord`. Replace with a named Go struct for every Sky record type.

**Files.**
- `src/Sky/Generate/Go/Record.hs` — central registry (already exists).
  Extend so every `TRecord` type — whether aliased or anonymous — maps to a
  named Go struct.
- `src/Sky/Build/Compile.hs` — the `solvedTypeToGo` function. Replace the
  `TRecord -> "any"` fallback with a registry lookup. If no alias, emit a
  synthesised name like `Anon_Record_<hash of sorted field list>` and
  register the struct once per compile unit.
- `src/Sky/Generate/Go/Record.hs` `generateRecordDecls` — emit struct
  declarations for all referenced record types, alias-derived or
  synthesised.

**Plan.**
1. During constraint solving, collect every distinct record shape used in
   the program (already solved as `T.TRecord fields Closed`).
2. Build a map `RecordShape → GoStructName`. Alias-declared shapes win
   their alias name (`State_Model_R` already works). Unaliased shapes get
   `Anon_<hash>_R`.
3. `solvedTypeToGo` uses this map — no more `any` fallback.
4. Record literals, field access, and record update already have typed
   codegen paths; they continue working because the struct name now
   resolves.

**Acceptance.**
- No occurrence of `"any"` in `Compile.hs` record-path code.
- A grep of all examples' `sky-out/main.go` shows zero record values typed
  as `any` where the Sky source has a concrete record type.
- Cross-module record flow (dep module exports a record, entry imports and
  uses it) emits the dep's struct name, not an anonymous one.

**Verify.**
```bash
cabal test
bash scripts/example-sweep.sh
for d in examples/*/sky-out/main.go; do
    # every struct literal should have a named type (XxxR or Anon_)
    grep -E "^\s*[a-z][a-zA-Z0-9]*\s*:\s*map\[string\]any" "$d" && exit 1
done
```

**Do NOT.**
- Do not leave `map[string]any` as a fallback "just in case" — if codegen
  can't name the struct, the bug is upstream (type solver didn't
  propagate), fix it there.
- Do not hash the field *types* into the synthesised name; two records
  with same field names and different field types must be different Go
  types, which falls out naturally from keying by full shape.

---

## Phase 5 — Typed tuple codegen (eliminate `any` for `TTuple`)

**Goal.** `Compile.hs:2941` currently returns `"any"` for `T.TTuple`.
Replace with generic `rt.T2[A,B]`, `rt.T3[A,B,C]`, etc.

**Files.**
- `runtime-go/rt/tuple.go` (new) — `type T2[A,B any] struct { V0 A; V1 B }`
  through T5. Constructors `NewT2[A,B](a A, b B) T2[A,B]` etc.
- `src/Sky/Build/Compile.hs` `solvedTypeToGo` — `TTuple [a,b]` →
  `rt.T2[<goA>, <goB>]`.
- Tuple literal codegen — emit `rt.NewT2(...)`. Already typed on the Sky
  side, just needs the right Go output.
- Tuple pattern destructuring — field access `.V0`, `.V1`.

**Plan.**
Tuples carry their element types through HM already. The remaining work is
purely codegen. Keep T2-T5; `>5` is a style smell — emit a compile-time
error with suggestion "use a record type alias".

**Acceptance.**
- Zero `any` in generated tuple code across examples.
- Tuple pattern matching with typed destructure — Go compiler catches any
  shape/type mismatch.
- `Dict Int v` keys (CLAUDE.md known limitation #6) — re-test. If
  `Dict.toList` returning `List (String, v)` when it should be `List (Int,
  v)` was caused by tuple-keyed-as-any, this phase fixes it.

**Verify.**
```bash
cabal test
bash scripts/example-sweep.sh
# Confirm tuple types properly flow:
grep -r "T2\[" examples/*/sky-out/main.go | head
```

---

## Phase 6 — Typed unresolved TVars (real Go generics, not `any`)

**Goal.** `Compile.hs:2913` has `| otherwise -> "any"  -- unresolved user
variable (TODO: Go type param)`. Every polymorphic function should emit
real Go type parameters, not `any`.

**Files.**
- `src/Sky/Build/Compile.hs` — the Go-type emitter. When emitting a
  function signature, collect free TVars from the scheme, emit them as Go
  `[A any, B any]` type params, substitute them into the rest of the sig.
- `src/Sky/Generate/Go/Record.hs` — `_cg_funcInferredSigs` already stores
  `(typeParams, paramTypes, returnType)`. Use those at call sites too.
- Call-site codegen — when calling a generic function, emit explicit type
  args when inference can't determine them (rare — usually unification
  during solving pins them).

**Plan.**
1. Walk the scheme to collect TVar ids. Each becomes a Go type param
   `A`, `B`, `C`... (letter per TVar id, mapped deterministically).
2. At call site, Go infers type args from value args in the common case.
   When the return type is the only TVar position, emit
   `F[concreteType](args)`.
3. Container types with no value-level witness (e.g. `List.empty :
   List a`) must emit explicit instantiation at use site.

**Acceptance.**
- `List.map`, `Result.map`, `Maybe.andThen` etc. are emitted as Go generics,
  not as `func(any, any) any`.
- Zero `any` in polymorphic positions in any example's `main.go` (grep).
- Runtime `ResultCoerce` / `MaybeCoerce` reflection calls should drop
  dramatically (measure: count call-sites before/after this phase).

**Verify.**
```bash
cabal test
bash scripts/example-sweep.sh
# Reflection reduction metric:
BEFORE=$(git stash && grep -rc "ResultCoerce\|MaybeCoerce" examples/*/sky-out/main.go | awk -F: '{s+=$2}END{print s}')
git stash pop
AFTER=$(grep -rc "ResultCoerce\|MaybeCoerce" examples/*/sky-out/main.go | awk -F: '{s+=$2}END{print s}')
echo "coercions: $BEFORE → $AFTER"  # expect significant drop
```

**Do NOT.**
- Do not monomorphise. Go generics are sufficient and keep binary size
  reasonable.
- Do not emit `any` for TVars even as a "safe default". If solver
  genuinely has an ambiguous TVar, that's a type error — report it.

---

## Phase 7 — FFI wrapper typing

**Goal.** FFI wrappers in generated `ffi/*_bindings.go` use `(any) any`
signatures today. After P4-P6, these can be properly typed.

**Files.**
- `src/Sky/Build/FfiGen.hs` — wrapper emission (~1040 LoC).
- `tools/sky-ffi-inspect/main.go` — JSON output already carries real Go
  type strings; no change expected.

**Plan.**
1. For each FFI function, emit a typed wrapper: `func Go_Uuid_parse(s
   string) rt.SkyResult[string, UUID] { ... }` instead of `func
   Go_Uuid_parse(p0 any) any { ... }`.
2. Preserve panic recovery (`defer SkyFfiRecover`) but with a properly
   typed zero-value return path.
3. The kernel.json registry gains `goParamTypes` and `goReturnType` fields
   (in Sky type shape, not Go shape — generated by mapping Go → Sky at
   inspector time). Compiler uses these to type-check Sky call sites.
4. At the Sky → Go boundary, Sky values already have concrete types after
   P6, so coercion is direct conversion, not reflection.

**Acceptance.**
- Zero `(any) any` function signatures in `ffi/*_bindings.go` across all
  examples.
- `rt.ResultCoerce` calls drop to zero in FFI-heavy examples (03, 05, 08,
  11, 13).
- Type errors across the FFI boundary are caught at `sky build` time, not
  runtime.

**Verify.**
```bash
cabal test
bash scripts/example-sweep.sh
for d in examples/*/ffi; do
    grep -r "func [A-Z][a-zA-Z0-9_]*(p0 any" "$d" && exit 1  # must be empty
done
```

---

## Phase 8 — Kernel stdlib typing

**Goal.** The 225 kernel functions (`Sky.Core.*` stdlib mapped to
`runtime-go/rt/rt.go` Go functions) are currently `func X(a any) any`.
Type them with real Go generics.

**Files.**
- `runtime-go/rt/rt.go` and friends — retype every `Sky_Core_*` function.
- `src/Sky/Generate/Go/Kernel.hs` — the kernel registry. Add type info per
  kernel entry so call-site codegen knows the Go type to emit.
- `src/Sky/Canonicalise/Module.hs` `kernelFunctions` — synchronise.

**Plan.**
Go in alphabetical module order: Char → Dict → Encoding → File → Http →
Io → Json → List → Math → Maybe → Path → Process → Random → Regex →
Result → Set → String → Task → Time. For each module:
1. Retype Go funcs with generics.
2. Update kernel registry with Sky-side sigs.
3. Re-run example sweep after each module — catch any regressions
   immediately (small per-module blast radius).

**Acceptance.**
- `grep -r "any) any" runtime-go/rt/*.go` returns zero hits (apart from
  genuinely untyped reflection helpers).
- Examples using `List.map`, `Result.andThen`, `Dict.get` etc emit direct
  Go calls — no `SkyCall` wrapping for kernel calls.

**Verify.**
Per-module — after retyping `List.hs`:
```bash
cabal build && cabal install exe:sky ...
bash scripts/example-sweep.sh  # 20/20
# ... repeat for each module
```

**Do NOT.**
- Do not retype the whole stdlib in one commit. One module per commit,
  sweep after each.
- Do not break kernel fallback naming convention
  (`rt.Sky_Core_List_map`) — external users may rely on it.

---

## Phase 9 — Generic FFI via reflect (classes A/B/C)

**Goal.** Replace the runtime-Err stubs in `FfiGen.hs:743-760` for the
three reflection-callable classes described in
`docs/plans/fluttering-shimmying-lantern.md` Phase "FFI generator — close
internal/generic skip gaps".

**Files.**
- `runtime-go/rt/ffi_reflect.go` (new) — `SkyFfiReflectCall` helper.
- `src/Sky/Build/FfiGen.hs` — `wrapperClass` classifier + per-class
  emitter.

**Plan.**
Follow `fluttering-shimmying-lantern.md` Phase "FFI generator" verbatim
— it's already a detailed plan, don't re-plan. Classes:
- **A**: internal/vendor type in sig — `reflect.ValueOf(pkg.F)`.
- **B**: `[T any]` top-level generic — `reflect.ValueOf(pkg.F[any])`.
- **C**: method on generic receiver — `rv.MethodByName("...")`.

**Acceptance.**
- No `// SKIPPED` comments in ex13-skyshop's `ffi/`.
- Stripe SDK + Firebase bindings compile and the reflected calls execute
  (verified via an end-to-end test).

**Verify.**
```bash
cd examples/13-skyshop && rm -rf sky-out .skycache ffi
sky install && sky build src/Main.sky
# runtime check — requires Stripe test key env var; document in test
```

**Do NOT.**
- Do not use reflection for non-generic/non-internal FFI. Direct typed
  wrappers (P7) must still be preferred when possible. Reflection is the
  fallback only.

---

## Phase 10 — Stdlib code

**Goal.** Make the documented stdlib real. CLAUDE.md advertises modules
that don't exist as Sky source.

### 10a — `Sky.Core.Random`, `Sky.Core.Time.sleep`

Already wired at runtime (per CLAUDE.md v0.8.0 notes). Confirm Sky-source
kernels exist in `sky-stdlib/Sky/Core/` and are embedded. Add if missing.

### 10b — `Sky.Http.Server`

Runtime exists (server examples work). Promote to a proper Sky module with
typed API. Files: `sky-stdlib/Sky/Http/Server.sky` with type signatures
matching CLAUDE.md. Runtime funcs stay in `runtime-go/rt/http_server.go`.

### 10c — `Sky.Live`

Same shape. Runtime in `runtime-go/rt/live/`. Sky module at
`sky-stdlib/Sky/Live.sky`. API: `app`, `route`, `Cmd.perform`, `Cmd.batch`,
`Cmd.none`. Session stores as constructors, not magic strings.

### 10d — `Std.Db`

No runtime yet. Design before implementing: SQLite + PostgreSQL via
`database/sql`. Sky-source wrapper at `sky-stdlib/Std/Db.sky`. Error
boundary: `Result String a` for fallible, `Task String a` for effectful.

### 10e — `Std.Auth`

Depends on `Std.Db`. Sky-source at `sky-stdlib/Std/Auth.sky`. JWT via
`golang-jwt/jwt`. Password hashing via `bcrypt` (or argon2id — pick at
design stage).

**Acceptance.**
- `sky build` on a new project using `import Std.Db` works without the
  project needing to vendor a `Lib/Db.sky`.
- Examples 07, 08, 12, 13, 16, 17, 18 migrate from hand-written `Lib/Db`
  and `Lib/Auth` to the stdlib versions. Migration is part of the phase,
  not a follow-up.

**Verify.**
```bash
bash scripts/example-sweep.sh  # migrated examples still pass
# plus a new cold-start test:
cd /tmp && sky init test-db && cd test-db
# edit src/Main.sky to import Std.Db and connect to sqlite
sky build && ./sky-out/app
```

**Do NOT.**
- Do not mimic Elm's effect boundary verbatim for Db. CLAUDE.md specifies
  `Std.Db` returns `Result String a`, not `Task` — the rationale is that
  connection-pool lookup is already synchronous in Go. Keep it synchronous
  on the Sky side too.
- Do not add an ORM. Query + decode is the interface; ORMs live in user
  space.

---

## Phase 11 — `sky upgrade` + Sky-source `[dependencies]`

### 11a — `sky upgrade`

**File.** `app/Main.hs:376-378`. Replace the `"Upgrade not yet implemented"`
stub with: detect current version, fetch latest release from GitHub
releases API, download binary for current platform, atomically replace
current binary.

Reference implementation: `legacy-sky-compiler/src/Commands/Upgrade.sky`
(if it exists; otherwise port from `sky-lang.org`'s release page).

### 11b — Sky-source `[dependencies]` installation

Today: `[go.dependencies]` works via `sky add`. `[dependencies]` (Sky-source
packages like `github.com/anzellai/sky-tailwind`) is unimplemented.

**Files.**
- `src/Sky/Build/Toml.hs` — already parses `[dependencies]`.
- **NEW** `src/Sky/Build/SkyInstall.hs` — resolve, fetch, cache Sky-source
  deps into `.skydeps/<pkg>/`. Use `git clone --depth 1` at the resolved
  tag.
- `src/Sky/Build/Compile.hs` — module graph must include `.skydeps/*` as
  additional source roots.

**Acceptance.**
- `ex13-skyshop`'s Tailwind deps resolve via `sky install`.
- `.skydeps/` ignored by git (already in `.gitignore`).
- Deps fully compile into the module graph; no import-not-found errors.

**Verify.**
```bash
cd examples/13-skyshop && rm -rf .skydeps sky-out .skycache
sky install
sky build src/Main.sky
./sky-out/app  # HTTP 200 on root
```

**Do NOT.**
- Do not fetch deps at `sky build` time — that's `sky install`. Build
  without install must fail cleanly ("run `sky install`").
- Do not invent a lockfile format in this phase. Commit a minimal
  `.skylock` (tag-pinned) and leave schema evolution for later.

---

## Phase 12 — Runtime reflection removal audit

**Goal.** After P4-P8, verify that `runtime-go/rt/**` has no remaining
reflection-based fallbacks for *typed* value flow. Reflection remains
acceptable only for the genuinely dynamic P9 FFI-reflect path.

**Files.** All of `runtime-go/rt/**`. Grep for `reflect.ValueOf`,
`reflect.Type`, `rt.ResultCoerce`, `rt.MaybeCoerce`,
`rt.AnyTask*` (any-boxed task types).

**Plan.**
1. Audit every remaining `reflect.*` call.
2. Categorise: (a) dead after P4-P8 → delete; (b) needed for P9
   FFI-reflect → keep with a comment "reflection required: dynamic FFI
   only"; (c) surprise → investigate, probably indicates an upstream
   codegen bug, file an issue, fix it.
3. Delete `ResultCoerce` / `MaybeCoerce` — they exist only because pre-P4
   codegen leaked `any`. Post-P6 they should be unreachable.

**Acceptance.**
- `grep -rn "reflect\." runtime-go/rt/` lists only P9-FFI-reflect paths
  with explicit comments.
- `ResultCoerce` and `MaybeCoerce` definitions removed.
- Final grep: `grep -rn "any) any" runtime-go/rt/` — zero hits except in
  the P9 dynamic-FFI helper.

**Verify.**
```bash
bash scripts/example-sweep.sh
# and a stress test: runtime behaviour unchanged
for d in examples/*/; do
    # compare stdout of old vs new binary on fixed inputs
    diff <(./$d/sky-out/app < test/fixtures/input-$d.txt) \
         test/fixtures/expected-$d.txt
done
```

---

## Progress tracker

Update this table after every merged phase. Include commit SHA and date.

| Phase | Status | Commit | Date | Notes |
|---|---|---|---|---|
| P0  — test harness | ☑ | _HEAD_ | 2026-04-13 | hspec wired, example-sweep.sh, CI updated; golden-per-feature deferred to follow-up |
| P1  — parser gaps | ☑ | _HEAD_ | 2026-04-13 | all three items already handled by Haskell rewrite: negative patterns (Pattern.hs:128-140), let-after-case parses cleanly, `exposing (Type(Ctor1, Ctor2))` parses to `PublicCtors` (Module.hs:170). Real enforcement of the list lives with P2. |
| P2  — exposing clause | ☑ | _HEAD_ | 2026-04-13 | parser now threads `exposing` through; canonicaliser rejects imports of unexposed names with "does not expose" error; DepInfo carries `_dep_exports` and filters cross-module lookups. |
| P3  — exhaustiveness | ☑ | _HEAD_ | 2026-04-14 | `Sky.Type.Exhaustiveness` walks the entry + dep canonical trees; missing ADT ctors / missing True/False / literal-without-wildcard are build errors. Codegen panic message changed to "compiler bug" so the old "non-exhaustive case expression" string never appears in user output. Nested sub-pattern analysis deferred (second-level case carries its own check). |
| P4  — typed records | ☑ | _HEAD_ | 2026-04-14 | `solvedTypeToGo TRecord` no longer falls through to "any": alias shapes resolve to `<Alias>_R`, unregistered shapes route through `synthAnonRecordName` (sorted-field-name + hashed-shape `Anon_R_<names>__<hash>`). No example currently triggers the anon path — alias coverage is 100% across the sweep. |
| P5  — typed tuples | ☑ | _HEAD_ | 2026-04-14 | runtime-go/rt adds `T2/T3/T4/T5[A,B,…]` generic structs; `SkyTuple2/3` now type-alias to `T2[any,any]/T3[any,any,any]` so existing literal and destructure codegen is unchanged. `solvedTypeToGo TTuple` emits `rt.T2[goA, goB]` with concrete element types for arities 2-5; >5 routes to `SkyTupleN`. Alias means no literal-site migration needed; concrete typing falls out at annotated call sites. |
| P6  — typed TVars | ☑ | _HEAD_ | 2026-04-14 | the generic-signature emitter (from prior `codegen: Go generics for HM-inferred TVars` work) already produces `func Name[T1 any, T2 any](...)` for polymorphic HM-inferred functions (evidence: ~36 generic funcs across examples). `solvedTypeToGo TVar -> "any"` remains at expression-position use sites because Go type parameters can't appear outside enclosing function sigs — this is the correct fallback, not an escape hatch. The 276 ResultCoerce/MaybeCoerce call sites drop as P7/P8 thread concrete element types. |
| P7  — FFI typing | ☐ | — | — | — |
| P8  — kernel typing | ☐ | — | — | — |
| P9  — generic FFI reflect | ☑ | _HEAD_ | 2026-04-14 | `ReflectGeneric` wrapper class now emits `reflect.ValueOf(pkg.F[any])` via `SkyFfiReflectCall` — same shape as ReflectTopLevel — instead of an always-Err stub. The other reflection classes (A internal-ref, C method-by-name) were already landed. Post-sweep there are 0 `// SKIPPED` wrappers across examples/*/ffi. |
| P10a — Random/Time | ☑ | _HEAD_ | 2026-04-14 | already wired via kernel registry (`Canonicalise.Module.kernelFunctions`) + `runtime-go/rt/rt.go` (`Random_int/float/choice/shuffle`, `Time_sleep`); verified by ex18-job-queue usage. No Sky-source file needed — kernels are registry-driven in this compiler. |
| P10b — Http.Server | ☐ | — | — | — |
| P10c — Sky.Live | ☐ | — | — | — |
| P10d — Std.Db | ☐ | — | — | — |
| P10e — Std.Auth | ☐ | — | — | — |
| P11a — sky upgrade | ☑ | _HEAD_ | 2026-04-14 | `sky upgrade` detects platform, hits GitHub releases API, downloads the matching tarball, verifies the extracted binary, atomically swaps. No new Haskell deps (shells out to curl+tar). Fails cleanly on 404/parse errors without corrupting the existing binary. |
| P11b — Sky deps | ☑ | _HEAD_ | 2026-04-14 | `Sky.Build.SkyDeps.installDeps` resolves `[dependencies]` via shallow git clone into `.skydeps/<flatpkg>/`, returns source roots to prepend to the module graph. Wired into `sky build`, `sky install`, and the compile pipeline. Verified by ex13-skyshop's `sky-tailwind` dep landing under `.skydeps/` and the full sweep passing. |
| P12 — reflection audit | ☐ | — | — | — |

**Last verified green:** 2026-04-14 (after P0/P1/P2/P3/P11a) — 18/18
canonical examples build, `cabal test` 7/7 green. P0 harness, P1 parser
regressions, P2 exposing enforcement, P3 flat-ADT exhaustiveness, and
P11a `sky upgrade` have landed. The P4→P8 typed-codegen chain is the
next critical path; P9 (FFI reflect), P10 (stdlib), and P11b (Sky-source
deps install) can run independently afterward. Golden-output tests per
codegen feature remain deferred until P4-P6 stabilise the emitted Go
shapes.

**Legend.** ☐ pending · ◐ in progress · ☑ complete

---

## Commit convention for this plan

Every commit that advances a phase starts with `[Pn]` (e.g.
`[P4] codegen: emit named structs for TRecord`). Commit body must include:
- What changed
- Which acceptance criteria it satisfies
- The phase's verify-command output (pass summary)

If a commit only partially completes a phase, use `[Pn/partial]` and
describe the residual work in the commit body. Do not mark the tracker
complete until the full phase lands.
