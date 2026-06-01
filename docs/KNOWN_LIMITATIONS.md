# Known limitations (v0.15.x)

Active limitations users still hit at HEAD. Each entry explains the
gap, why, and the workaround. Anything not on this list was either
never a limitation or has been fixed; the repo `CHANGELOG.md` records
the per-version closures if you want history.

## Language

1. **Anonymous records in function signatures (parser-side).** The
   parser accepts `f : { name : String, age : Int } -> String`
   cleanly; what previously broke was a separate multi-line-sig
   issue (see #8 below). For sharing a record shape across several
   functions, a `type alias` is still preferable.

2. **No higher-kinded types.** No `Functor` / `Monad` / etc. Use
   concrete types (Hindley-Milner only — intentional).

3. **No `where` clauses.** Use `let…in` instead (intentional).

4. **No custom operators.** Only built-in operators (`|>`, `<|`,
   `++`, `::`, etc.) — intentional.

5. **No row-polymorphic annotation syntax.** Sky doesn't parse
   `{ r | field : T }` in annotations. Use a closed record alias for
   the function's input.

6. **Negative literal arguments need parentheses.** `f -1` parses
   as `f - 1` (subtraction). Write `f (-1)`.

7. **Zero-arg call shapes follow the binding's declared type.** Bare
   `Uuid.v4` works because its stdlib sig is `v4 : String`. `Time.now
   ()` / `Time.unixMillis ()` / `FyneApp.new ()` are all needed
   because their sigs are `() -> Task Error a`. Calling a `: String`
   binding with `()` triggers a known codegen bug for arity-0 kernels
   — stick to the declared shape.

8. **Multi-line function signatures with continuation INSIDE the
   type body.** `name\n    : T` (the `:` on a continuation line)
   parses cleanly. Continuation *inside* the type body
   (`T1\n    -> T2`) is not supported — extract a `type alias` for
   the whole arrow type.

## Standard library

9. **Non-tail-recursive list operations are O(N) on Go stack.** The
   following functions recurse with work after the recursion (so
   auto-TCO doesn't help): `List.{map, filter, foldr, length, concat,
   concatMap, take, append, range, zip, indexedMap}`, `Maybe.combine`,
   `Result.combine`. Fine for typical UI lists (Go's default goroutine
   stack grows to 1 GB). For very large inputs (200k+ elements) prefer
   the tail-recursive accumulator pattern (`foldl` + final `reverse`).
   Auto-TCO covers `foldl`, `find`, `any`, `all`, `member`, `drop`,
   `reverseHelp`, `indexedMapHelp` — those compile to constant-stack
   `for { … continue }` loops.

10. **`Dict.toList` returns string keys.** Sky's `Dict` uses
    `map[string]any` internally, so `Dict.toList` returns string keys
    even for `Dict Int v`. Arithmetic on these silently produces 0.
    Workaround: iterate over known key ranges with `Dict.get`.

11. **Zero-arg `Css.*` keyword constants require `()`.** `Css.zero
    ()`, `Css.auto ()`, `Css.none ()`, etc. — kernel bindings exposed
    as `() -> String`. The bare form is now a clean type error (no
    longer the silent function-pointer leak it used to be), but the
    `()` is still required. Pattern: any `Css.X` that names a literal
    CSS keyword takes `()`; value constructors like `px`, `rem`, `em`,
    `hex`, `rgba` take their arguments directly.

## Compiler

12. **`sky check` does not fully model Go interface satisfaction.**
    Opaque FFI types unify with each other, but the checker cannot
    verify that a concrete Go type (e.g. `Label`) satisfies a named
    Go interface (e.g. `CanvasObject`). Calls like
    `Fyne.windowSetContent window label` may fail `sky check` but
    compile and run correctly.

13. **HM type-checker heap exhaustion on Std.Ui-heavy modules**
    (defensive bound). For very large monolithic view files
    (~25+ polymorphic `Element Msg` helpers + many nested calls)
    the constraint solver can grow O(N²) in heap. The compiler
    defensively caps solver invocations at `SKY_SOLVER_BUDGET`
    steps (default `max(5,000,000, constraint_count × 200)`). On
    hitting the cap, the compiler aborts with a clear `TYPE
    ERROR: constraint solver exceeded budget` rather than OOMing
    the host.

    **Workaround**: split heavy view modules across multiple files
    (per `examples/19-skyforum`'s 8-module pattern — `State.sky`
    (types) / `Update.sky` / `View/Common.sky` / one View module
    per page / `Main.sky` dispatcher).

## Closed in v0.15 (for grep)

The v0.15 type-directed lowering pass and Go generics on parametric
record aliases closed a cluster of long-standing limitations:

- ~~Parametric record alias bugs (Surfaces 1, 2, 3)~~ — fields on
  `Cfg msg`-typed function parameters, inline lambdas at record-field
  slots, cross-alias call without the alias-chain workaround all
  shipped.
- ~~Same-module polymorphic call pinned by first instantiation~~ —
  sibling refs to polymorphic annotated TypedDefs now alpha-rename
  per call site.
- ~~`exposing (Type(..))` for user-module ADT constructors~~ — user
  `type Color = Red | Green` exporting `Color(..)` and imported
  `exposing (Color(..))` now exposes unqualified constructors.
- ~~`let` bindings don't support forward references~~ — `let a = b +
  1; b = 5 in a` now compiles and evaluates correctly.
- ~~`import X as Alias` leaks the alias into codegen~~ — `import
  Lib.Db as Chat` now emits `Lib_Db_Message_R` (source module name),
  not `Chat_Message_R`.
- ~~Let bindings with parameters after a multi-line case~~ — `let
  mark j = …` after a `case … of` arm now parses cleanly.
- ~~Zero-arity functions reading env vars memoised at init()~~ —
  `apiKey = System.getenvOr "K" "def"` now reads the runtime
  environment.
- ~~Unknown qualified name silently passes canonicaliser~~ —
  v0.15.42 (audit §3.1). `NotARealModule.foo` is now flagged at
  canonicalisation with a Did-you-mean suggestion, not as a
  cryptic `undefined: NotARealModule_foo` from `go build`.
- ~~"Compilation successful" prints before `go build` runs~~ —
  v0.15.42 (audit §3.4). Sky lowering prints "Sky lowering
  succeeded"; "Compilation successful" only fires after Go
  returns 0. Failure path is labelled "Sky lowering succeeded
  but `go build` failed:" so log readers can disambiguate.
- ~~User ADTs silently shadow Prelude-exposed types and
  constructors~~ — v0.15.42 (audit §3.2). `type Result a = Just a
  | Nothing` is now a hard canonicaliser error citing the stdlib
  origin (e.g. `Sky.Core.Result`), eliminating the refactor
  regression class where downstream code silently bound to the
  user's ADT instead of stdlib Maybe / Result.
- ~~Point-free top-level alias of a polymorphic / N-ary function
  ships a 0-arity Go thunk wrapper~~ — v0.15.52 (#398). `tickle =
  String.toUpper` (and any `name = fn` whose RHS has greater arrow
  arity than its syntactic param count) now eta-expands at the
  codegen entry point via `etaExpandPointFree` in `Sky.Build.Compile`.
  The emitted Go is a normal N-ary function with synthetic
  `_skyEta_pN` parameters, so `tickle "hi"` compiles and runs.
  Applied at both the entry-module path (`generateDef`) AND the
  dep-module path (`generateDeclsForDep.mkDef`) — the latter uses
  the per-module-scoped `Solve.withCurrentModule` lookup so the
  arity check matches the dep's own HM ledger.
- ~~Synchronous Sky main crashes with a Go stack dump on `1 // 0`,
  bad numeric cast, or comparison-type-mismatch~~ — v0.15.43
  (audit §3.5 + §9). Codegen now injects `defer rt.LogPanicAndExit()`
  as the first statement of every emitted `func main()`. The
  recover catches each "reachable from valid Sky" panic site
  (`rt.IntDiv` / `rt.Rem` / `rt.Div`, `rt.AsInt` / `AsFloat` /
  `AsBool`, `rt.cmp`, `rt.Coerce`, `rt.skyCallDirect`, plus Go-
  runtime `index out of range` / nil-deref) and emits a structured
  `Sky panic: <Kind> (ref <errId>) — <hint>` log line + exit 1
  instead. Compiler-bug panics (`Unreachable`, `Ffi.kernel`,
  `coerceInner`) are classified as `CompilerBug` and prompt the
  user to file a report. Full site-by-site audit at
  `docs/v0.15.x-hardening/audits/CYCLE-06-PC-panic-site-audit.md`.

## Deferred (roadmap, not active bugs)

* **Install-time Go-binding generation.** `sky install` currently
  emits the full `.skycache/go/<pkg>_bindings.go` (Stripe: 326k
  lines). Could be deferred to `sky build` time on the reachable
  subset only — Stripe install would drop from ~8 min to ~10 s.
* **Sub-app Sky-side API.** `MountSubApp` is currently Go-side
  (`rt.MountSubApp` in generated `main.go`). A Sky-side `Live.app
  { subApps = [...] }` API is on the v0.15.x list.
* **Lambda-typed OUTPUT for ALL call sites.** Typed routing for
  `List.map` / `Maybe.map` etc. uses `rt.List_mapT[A, any]` —
  input typed, output `any`. Forcing `B` to concrete would need
  per-call-site monomorphisation that doesn't conflict with Sky's
  curry shape.
