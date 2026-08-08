# Typed Codegen — Session Resume Brief

**Branch**: `feat/typed-codegen` — latest `472b1e9` (64 commits ahead of main)
**Target**: zero `any` in generated Go sigs across all 20 examples
**Current state**: **44 real anys** across 20 examples (excluding legit polymorphic `[T1 any]` generics); all 20 examples build; all 9 live servers return HTTP 200. 12-skyvote and 18-job-queue now hit zero real-any sigs alongside the primitive examples (01-04, 06, 11, 14, 15).

## Headline numbers (corrected counter strips `[T1 any, T2 any, …]` generics first, then greps for word-boundary `any`)

| Example | real-any sigs | Source |
|---------|---------------|--------|
| 01-hello-world | 0 | ✅ typed |
| 02-go-stdlib | 0 | ✅ typed |
| 03-tea-external | 0 | ✅ typed |
| 04-local-pkg | 0 | ✅ typed |
| 05-mux-server | 1 | genuinely-generic `[T1 any]` wrapper |
| 06-json | 0 | ✅ typed |
| 07-todo-cli | 1 | one Db-opaque helper |
| 08-notes-app | 0 | ✅ typed |
| 09-live-counter | 1 | `viewPage` polymorphic through Layout.page pass-2 fallback |
| 10-live-component | 1 | parentMsg callback param (Go function covariance) |
| 11-fyne-stopwatch | 0 | ✅ typed |
| 12-skyvote | 0 | ✅ typed |
| 13-skyshop | 31 | Firestore/Stripe FFI opaque returns (29 bare `any`) + 2 view helpers stuck in pass-2 fallback |
| 14-task-demo | 0 | ✅ typed |
| 15-http-server | 0 | ✅ typed |
| 16-skychess | 7 | Lib.GameLogic unannotated helpers (HM let-generalises to `forall a b. a -> b -> …`) |
| 17-skymon | 1 | Database.conn fallback |
| 18-job-queue | 0 | ✅ typed |
| simple | 1 | one FFI helper (`expensiveTask`) without Sky.Core.Error in scope |
| test_pkg | 0 | ✅ typed |
| **Total** | **44 real any** across 20 examples | 11/20 examples at zero. |

Cycle drops from the corrected 153 baseline:

1. TRecord→_R (18-job-queue 8→1)
2. List/Dict typed element (16-skychess 51→10, 17-skymon 29→4, 08-notes-app 8→3)
3. Css String-return kernels (1-2 per view-helper module)
4. Error-position TVar defaulting to Sky.Core.Error.Error (13-skyshop 77→61)
5. Ok-slot + Maybe-slot TVar defaulting to rt.SkyValue (13-skyshop 61→34)
6. `Live.app.init` typed as `Dict String v` instead of `forall req. req` (18-job-queue 1→0)
7. TypedDef path prefers HM-solved type + runs the same defaulting
   (12-skyvote 1→0; eliminates annotated-TVar leaks like
   `init : a -> (Model, Cmd Msg)`).

That's 153 → 44 = ~71% reduction this cycle, with 11/20 examples at zero real-any.

## Attempted but reverted this cycle

- **`Db.exec/query/open/connect` kernel sigs** — caused every dep
  module that wraps Db to degrade from partial-any to all-any. Root
  cause: `conn = case Db.open … of Ok c -> c ; Err _ -> identity ""`
  relies on polymorphic fallback unifying with the Ok branch; a typed
  `Db.open : … -> Result Error Db` correctly rejects the String vs Db
  mismatch.
- **`Css.rule / property / margin / …` kernel sigs** — the runtime
  returns opaque `cssRule / cssProp` structs, not String. A "returns
  String" kernel sig caused `rt.Coerce[[]string]` to panic at
  Tailwind's rules-list boundary (caught via HTTP regression test).
- **`Attribute ↔ (String, String)` unifier alias** — would let
  Tailwind's tuple-typed helpers flow into Html.div's `List Attribute`
  slots and kill most pass-2 fallback messages. But the naive
  implementation (unify components without constraint) broke HM
  propagation for `state` vs `"ordered"` comparisons in skyshop's
  Page.Orders — state inferred as T1 but body compares strings,
  Go compiler rejected. Needs a proper alias/TAlias registration
  instead; deferred.

## What landed in the latest resume cycle (2026-04-20)

25. `5c2f5fd` — **TRecord → `_R` in signatures**. HM collapses a named
    record alias to its underlying TRecord after row-poly unification,
    so unannotated `mkJob id name = { id, name, running = True, result = "" }`
    had its sig degrade to `any` even though the body emitted `Job_R{...}`.
    Added `splitInferredSigWithReg` + `typeStrWithAliasesReg` that carry
    a field-set → alias-name registry (the same registry `safeReturnType`
    already consulted). `Rec.buildDepFieldIndex` exported so pass-1 and
    pass-2 construct the registry identically. **18-job-queue: 8 → 1**.
26. `34f273c` — **Typed element/value for List/Dict in HM-inferred sigs**.
    `T.TType _ "List" [elem]` now emits `[]T` when elem resolves to a
    concrete Go type; same for `Dict`. Body `[]any{...}` round-trips
    via `rt.Coerce[[]T]`/`rt.AsListT[T]` (reflect element-walk) at the
    homogeneous-slice boundary. Also documents why `Db.open/connect/
    exec/query/execRaw` stay un-kernelled: user wrappers like
    `conn = case Db.open of Ok c -> c | Err _ -> identity ""` rely on
    polymorphic fallback unifying with the Ok branch. **16-skychess:
    51 → 10; 17-skymon: 29 → 4; 08-notes-app: 8 → 3**.
27. `7ef15ba` — **Propagate typed element/value in annotation paths**.
    Same change applied to `safeReturnType` and `safeReturnTypeWith`
    so annotated functions get the same treatment.
28. `8a214d0` — **Kernel sigs for verified String-returning Css helpers**
    (`rgb/rgba/hsl/hsla/shadow`). Explicit comment documenting why
    `Css.rule / property / margin / etc.` stay un-kernelled — they return
    opaque `cssRule / cssProp` structs, not String. A speculative
    `Css.rule : String -> List a -> String` kernel caused `rt.Coerce[[]string]`
    to panic at Tailwind's rules-list boundary; reverted before commit
    after HTTP regression test.
29. `4265c99` — **Default error-position TVars to `Sky.Core.Error.Error`**.
    HM leaves the Err slot of `Result a b` polymorphic when a function
    never constructs a failing value. New `defaultErrorTVars` pre-pass
    substitutes the concrete `Error` type for every TVar that appears
    ONLY in Result/Task error slots. Guarded by `errorTypeAvailable`.
    **13-skyshop 77→61**.
30. `080a3e8` — **Default ok-only TVars to `rt.SkyValue`**. Extends
    `defaultErrorTVars` to also substitute `rt.SkyValue` for every
    TVar that appears only in Result/Task Ok positions. Matches the
    "opaque FFI pointer" semantic the code actually has and lets
    `rt.SkyResult[Error, any]` collapse to `rt.SkyResult[Error,
    rt.SkyValue]`. `tvarOccurrences` now returns
    `(errorCount, okCount, otherCount)` per TVar. **86→53**.
31. `339bcdc` — **Maybe-only TVars also default to `rt.SkyValue`**.
    `T.TType _ "Maybe" [a]` is treated as `OkSlot` in the walker so
    `Maybe a` with `a` unused elsewhere becomes
    `rt.SkyMaybe[rt.SkyValue]`. **53→51**.
32. `e32b407` — **Type `Live.app.init` as `Dict String v`**. Runtime
    passes `map[string]any{"path": …}` to init, so the kernel sig's
    `forall req. req` was leaking `any` through every `init _ = …`.
    `Dict String v` pins the outer shape while leaving `v` polymorphic
    for user code that reads cookies or other extensions. **51→50;
    18-job-queue hits zero.**
33. `472b1e9` — **Annotation path prefers HM-solved type + runs
    defaulting**. Previously the Go-sig for `Can.TypedDef` ignored
    HM's more-specific inferred type when an annotation was present,
    so `init : a -> (Model, Cmd Msg)` emitted `any` for the `a`
    param even though HM had unified it with `Dict String v` via the
    `Live.app` record field. Now the codegen uses the HM-solved type
    when available and applies `defaultErrorTVars` to it. **50→44;
    12-skyvote hits zero.**

## What landed on this branch earlier (since `95772d8`)

Commits on `feat/typed-codegen`:

1. `efc2fba` — kernel type dictionary expanded (~60 entries for Task/List/Dict/Set/Math/Basics/Slog/Os)
2. `160f0ff` — alias expansion pass in canonicaliser + record-field unification (TType → TAlias; DepInfo._dep_aliasDefs carries cross-module alias bodies)
3. `dd1ad85` — TTuple → `rt.SkyTuple{2,3,N}` in sigs; tuple destructure wraps subject in `any(...)` for both typed/legacy paths
4. `ec7ea6e` — `Live.app` kernel type carries full record shape so TEA functions get Model/Msg inferred
5. `3000d32` — re-export delegation: `foo = Other.foo` inherits callee's typed return (dropped skyshop 1290→200)
6. `b7b7d9d` — opaque runtime aliases (`type SkyDecoder = any`, `SkyValue`, `SkyAttribute`, `SkyHandler`, `SkyMiddleware`, `SkySession`, `SkyStore`) + JsonDec kernel sigs + `opaqueParameterisedGoTy` for `Decoder a`
7. `6235497` — alias expansion walks into VarCtor annotations so `Error Io (mkInfo msg)` unifies
8. `26484db` — TRecord → record-alias `_R` lookup in safeReturnType
9. `37cce54` — pragmatic runtime-compat fixes: `anyTaskInvoke` reflects into typed SkyTask, `errorKindAdt` returns plain int, `GoEnumDef` emits `type X = int` alias, fixed user-code annotation arity bugs
10. `799b980` — typed `[]T` / `map[string]V` + runtime coercers `rt.AsListT[T]`, `rt.AsMapT[V]`, `rt.AsListAny` (uses reflect) to bridge runtime's `[]any` / `map[string]any` with typed boundaries
11. `1b61002` — cross-module HM scaffolding (`constrainModuleWithExternals` + `buildCrossModuleExternals`)
12. `4f960fd` — **enabled** cross-module HM with home fixup: `buildCrossModuleExternalsWithMods` walks all deps to build a global type-name → home map, then `fixupHomes` rewrites empty-home nominal refs in each external annotation (fixes the Chess.Ai-uses-`Model`-without-importing-State pattern). Filter ensures externals only cross for names actually DECLARED in their module (not imported constructors in the solver env).
13. `6acbb93` — pass-2 dep re-solve with externals: deps that pass-1 failed (e.g. Chess.Move) now succeed because imported helpers' concrete types disambiguate their internal calls. -5 any sigs.
14. `fce64cc` — **formatter**: multi-line record types with leading commas at the alias body indent (>1 field always breaks). Fixes sky-stdlib/Sky/Test.sky's `Suite String List Test` (parsed as 3-arg ctor, 2 actual uses) to `Suite String (List Test)`.
15. `9953ff7` — apply the new formatter to all example `.sky` files (State/Model records now flow multi-line), plus fixes two more Result arity typos in `authenticateUser` annotations for notes-app and skyvote.
16. `c05d785` — Css kernel sigs (hex/px/rem/em/pct/stylesheet → String). User helpers wrapping them now type.
17. `466a2b8` — Html.raw/styleNode/render kernel sigs. Pre-fix the catch-all `(Html, _)` → `attrs → children → VNode` mis-typed 1-arg helpers, which cascaded to whole-dep-module solve failures. Drops 65 real-any sigs.
18. `f0a8f94` — TypedDef wraps its body in CLet. Annotated functions were skipping the param-binding registration in the solver's _env, so `CLocal "dir"` in an annotated body hit an empty env and fabricated a fresh unconstrained TVar. Fixes Chess.Move (-12 real-any).
19. `89f331b` — more Html kernel sigs for void elements (meta/link/area/…) and inline-body script/titleNode/doctype; Attr.* catch-all accepts `any` (boolean attrs ignore arg). Drops 67 real-any sigs.
20. `9705ba8` — allow polymorphic externals: generaliseToAnnotation renames solver-internal TVars (`_carg49`, etc.) to user-level names (a, b, c) before quantifying, so previously-rejected polymorphic dep functions flow as `Forall [a, b, …] ty` cross-module.
21. `136bed3` — note why TLambda stays as `any` in safeReturnType (Go lacks return-type covariance for function values).
22. `73d9632` — Db row-accessor kernel sigs (getField/getString/getInt/getBool) + opaque aliases for Stmt/Row/Conn.
23. `683350f` — Os.getenv returns Result Error String (runtime returns Err(ErrNotFound) on miss). Unblocks 5 dep-module solves in skyshop (-17 real-any).
24. `fcd1034` — alpha-rename TypedDef free TVars so `a` in one annotation doesn't alias with `a` in the next via the solver's shared TVar cache. Also fixes skyshop Lib.Db.snapshotToDict to unwrap the Result return from Firestore.documentSnapshotData. -22 real-any.

## Runtime safety: all 9 live servers return HTTP 200

`09-live-counter`, `10-live-component`, `12-skyvote`, `13-skyshop`, `15-http-server`, `16-skychess`, `17-skymon`, `18-job-queue`, `08-notes-app`.

## What's left to close the remaining ~13%

Three structural improvements, in order of expected yield:

### A. Cross-module HM specialisation (~5-8%)

The scaffolding is on `feat/typed-codegen` at `1b61002`. Turning it on requires:

1. Replace `buildCrossModuleExternals`'s `onlyKernelTypes` filter with something subtler — a filter that skips types containing **user ADT constructors that are ALSO defined in the entry module**. That's the actual collision class (e.g. `Msg`, `Page` constructors that happen to share names between importer and importee).
2. Enable externals on both the pass-2 dep re-solve AND the entry module canonicalisation.
3. Regression-guard with `/tmp/count-any.sh` — the current numbers are the floor, any gain must preserve runtime HTTP 200.

Expected yield: +5–8% (most unannotated Lib/View/Db helpers currently typed as polymorphic `[T1 any]` would resolve to their concrete callers' types).

### B. FFI-generated wrappers retain Go types (~2-3%)

The FFI generator (`bin/sky-ffi-gen`) currently emits `func StripeCheckoutSessionCreate(params any) any`. If it propagated the original Go signatures from the reflect scan, skyshop's 199 would drop significantly (most are Stripe/Firebase opaque-struct wrappers).

Approach:
- In `tools/sky-ffi-inspect`, already has the parsed Go sig.
- In `src/Sky/Build/Ffi.hs` (or wherever the wrapper emission lives), use the scanned Go type instead of stripping to `any`.
- Map Go types to Sky opaque-kind via `runtimeTypedMap` (already has scaffolding for Go pointer types).

Expected yield: +2–3%.

### C. Runtime container boxing rewrite (~2%, high effort)

Pure strict typing would require `List a` to be `[]A` at runtime (not `[]any`), produced natively by every constructor (Dict.fromList, Db query rows, JSON decode, etc.). That means touching `rt.go`, `live.go`, `db_auth.go`, `stdlib_extra.go`.

Not worth the effort for the last ~2% unless other invariants also require it (e.g. performance — reflection-based coercers have a cost).

## Infrastructure bits worth knowing

- **`rt.AsListT[T](v any) []T`** walks `[]any`, asserts each to T. Used at record-ctor and call-site boundaries.
- **`rt.AsListAny(v any) []any`** widens typed slices via reflect for any-typed callees.
- **`rt.AsMapT[V](v any) map[string]V`** same pattern for dicts.
- **Alias expansion in canonicaliser** rewrites `TType "Model"` → `TAlias "Model" (Filled (TRecord ...))`. Lives in `Sky.Canonicalise.Module.expandModuleAliases`, called from `canonicaliseWithDeps`. Cross-module alias bodies flow via `DepInfo._dep_aliasDefs`.
- **`globalExternals :: IORef (Map (String, String) T.Annotation)`** in `Sky.Type.Constrain.Expression` — used by the disabled cross-module channel. Set by `constrainModuleWithExternals`.
- **`opaqueParameterisedGoTy :: String -> Maybe String`** — maps `Decoder a` → `rt.SkyDecoder` regardless of the type argument. Extend this for any future opaque-kind parameterised types.

## Commands to resume

```bash
cd /Users/anzel/works/playground/sky
git log --oneline feat/typed-codegen...main | head    # see what's ahead of main

# rebuild compiler
cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky

# rebuild all examples
for d in examples/*/; do
    name=$(basename "$d")
    (cd "$d" && rm -rf sky-out .skycache && ../../sky-out/sky build src/Main.sky \
        && echo "$name: OK" || echo "$name: FAIL")
done

# count any in emitted sigs
bash /tmp/count-any.sh   # OR recreate from this file's script

# runtime smoke test (each server)
for name in 09-live-counter 12-skyvote 16-skychess 17-skymon 18-job-queue \
            15-http-server 08-notes-app 10-live-component 13-skyshop; do
    cd examples/$name
    ./sky-out/app &
    p=$!; sleep 2
    curl -s -o /dev/null -w "$name: %{http_code}\n" http://localhost:8000/
    kill $p 2>/dev/null; wait $p 2>/dev/null
    cd ../..
done
```

## Regression invariants

1. All 20 examples build from clean slate.
2. All 9 server examples return HTTP 200 on `/`.
3. `cabal test` passes (known failing tests at snapshot: 2 of ~77 — RecordFieldOrder has been updated to accept typed param forms; VerifyScenario was flaky on external-port races).
4. Any change that raises the `any` count is a regression unless justified.

## Honest caveat

The remaining 44 real-any sigs are structural and split into distinct classes, each needing a different kind of work:

1. **FFI opaque returns** (29 bare-`any` in skyshop) — Firestore/Stripe/Firebase Go pointers that Sky wraps opaquely. Fix = FFI generator emits typed wrappers using the scanned Go signatures rather than `any`.
2. **Pass-2 dep fallback chains** (Page.Home `viewHome`, Page.Product `viewProduct`, etc.) — the Attribute-vs-(String,String) nominal mismatch makes Ui.Layout's pass-2 fail for skyshop, so downstream dep modules that depend on Layout.page's typed return fall back to pass-1 polymorphic types. Fix = transparent `Attribute` ≡ `(String, String)` unification (attempted as a TAlias; breaks some string-equality flows in Page.Orders; needs a deeper unifier patch).
3. **Within-module let-generalisation** (7 in skychess) — `pawnCaptureLeft sq dir colour board col = …` is called *only* from `pawnCaptures : … -> Colour -> Dict Int Piece -> …` with concrete args, but HM generalises the helper to `forall a b. a -> b -> …` and the caller *instantiates* the scheme instead of constraining it. Fix = monomorphisation pass after HM.
4. **`identity ""` fatal fallbacks** (17-skymon Database.conn) — `conn = case Db.open of Ok c -> c | Err _ -> identity ""` types `conn : String` instead of `Db`. Any typed `Db.open` kernel sig would correctly reject this; the user code predates error-unification. Fix = rewrite the Err branch to return a Result/Task.
5. **Go function covariance** (10-live-component Counter.view's `toMsg`) — a callback parameter `toMsg : CMsg -> Msg` must be assignable to the wider `func(any) any` that Go typed-codegen accepts.

None of (1)–(5) is reachable via another kernel-sig tweak. They need a compiler pass (monomorphisation), a unifier extension (transparent alias), an FFI generator change, or user-code edits.
