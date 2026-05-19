# Known limitations (v0.14.x)

Active limitations users still hit. Each entry explains the gap, why,
and the workaround. Closed entries live in the git log +
`docs/compiler/journey.md`.

## Language

1. **No anonymous records in function signatures.** Inline `{ field : Type }`
   in annotations isn't supported — typed codegen needs a named struct.
   Workaround: define a `type alias` for any record you mention in a
   signature.

2. **No higher-kinded types.** No `Functor` / `Monad` / etc. Use
   concrete types (Hindley-Milner only — intentional).

3. **No `where` clauses.** Use `let…in` instead (intentional).

4. **No custom operators.** Only built-in operators (`|>`, `<|`, `++`,
   `::`, etc.) — intentional.

5. **Negative literal arguments need parentheses.** `f -1` parses as
   `f - 1` (subtraction). Write `f (-1)`.

6. **`exposing (Type(..))` doesn't expose ADT constructors for
   user-defined modules** (kernel modules work). Workaround:
   `exposing (..)` or qualify constructors as
   `MyModule.MyConstructor`.

7. **`let` bindings don't support forward references.** Helpers in a
   `let` block must be defined before their consumers in source
   order. Workaround: reorder so dependencies come first.

## Standard library

8. **Non-tail-recursive list operations are O(N) on Go stack.** The
   following functions recurse with work after the recursion (so
   compile-time auto-TCO doesn't help): `List.{map, filter, foldr,
   length, concat, concatMap, take, append, range, zip, indexedMap}`,
   `Maybe.combine`, `Result.combine`. Fine for typical UI lists
   (Go's default goroutine stack grows to 1 GB). For very large
   inputs (1 M+ elements) prefer the tail-recursive accumulator
   pattern (`foldl` + final `reverse`). Auto-TCO covers `foldl`,
   `find`, `any`, `all`, `member`, `drop`, `reverseHelp`,
   `indexedMapHelp` — those compile to constant-stack `for { …
   continue }` loops.

9. **`Dict.toList` returns string keys.** Sky's `Dict` uses
   `map[string]any` internally, so `Dict.toList` returns string keys
   even for `Dict Int v`. Arithmetic on these silently produces 0.
   Workaround: iterate over known key ranges with `Dict.get`.

10. **Zero-arg FFI functions take `()`, but zero-arity kernel
    bindings do not.**
    * FFI (any auto-bound Go pkg): `Uuid.newString ()` /
      `FyneApp.new ()`. The inspector emits a `() -> R` signature
      for every zero-param Go function.
    * Sky-side kernel zero-arity bindings: `Uuid.v4` / `Time.now`
      — called WITHOUT `()`. Kernel-registered as bare values.

11. **Zero-arg `Css.*` keyword constants require `()`.**
    `Css.zero ()`, `Css.auto ()`, `Css.none ()`, etc. Kernel
    bindings exposed as `() -> String` to sidestep zero-arity
    memoisation. Without `()` the function pointer gets serialised
    into the stylesheet.

12. **Zero-arity functions reading env vars are memoised at Go
    `init()` time** — before `.env` is loaded. Workaround: add a
    dummy `_` parameter (`getConfig _ = System.getenv "KEY"`).

## Compiler

13. **`sky check` does not fully model Go interface satisfaction.**
    Opaque FFI types unify with each other, but the checker cannot
    verify that a concrete Go type (e.g. `Label`) satisfies a
    named Go interface (e.g. `CanvasObject`). Calls like
    `Fyne.windowSetContent window label` may fail `sky check` but
    compile and run correctly.

14. **`import X as Alias` leaks the alias into codegen for exposed
    record / ADT types.** `import Lib.Db as Chat` causes
    `Message` to emit as `Chat_Message_R` instead of
    `Lib_Db_Message_R`. Workaround: use `import Lib.Db exposing
    (Message, …)` or qualify without an alias.

15. **Let bindings with parameters after a multi-line case** —
    `let mark j = expr` after `case … of` in the same `let` block
    confuses the parser into treating it as a new top-level
    declaration. Workaround: use lambdas (`\j -> expr`) or extract
    to a top-level function.

16. **HM type-checker heap exhaustion on Std.Ui-heavy modules**
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

## Deferred (roadmap, not active bugs)

* **Install-time Go-binding generation.** `sky install` currently
  emits the full `.skycache/go/<pkg>_bindings.go` (Stripe: 326k
  lines). Could be deferred to `sky build` time on the reachable
  subset only — Stripe install would drop from ~8 min to ~10 s.
* **Sub-app Sky-side API.** `MountSubApp` is currently Go-side
  (`rt.MountSubApp` in generated `main.go`). A Sky-side `Live.app
  { subApps = [...] }` API is on the v0.15 list.
* **Lambda-typed OUTPUT for ALL call sites.** Typed routing for
  `List.map` / `Maybe.map` etc. uses `rt.List_mapT[A, any]` —
  input typed, output `any`. Forcing `B` to concrete would need
  per-call-site monomorphisation that doesn't conflict with Sky's
  curry shape.
