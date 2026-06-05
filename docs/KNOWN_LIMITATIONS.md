# Known limitations (v0.16.x)

Active limitations users still hit at HEAD. Each entry explains the
gap, why it exists, and the workaround. Anything not on this list was
either never a limitation or has been fixed; closures are recorded in
the git history and in `CHANGELOG.md`.

## Language

1. **No higher-kinded types.** No `Functor` / `Monad` / `Applicative`
   classes. Sky's type system is Hindley-Milner (intentional). Use
   concrete types and explicit `andThen` / `map` per ADT.

2. **No `where` clauses.** Use `let…in` instead. Intentional —
   single bindings construct, two is a needless surface.

3. **No custom operators.** Only built-in operators (`|>`, `<|`,
   `++`, `::`, arithmetic, comparison). Intentional — reading other
   people's code stays predictable.

4. **No row-polymorphic annotation syntax.** Sky doesn't parse
   `{ r | field : T }` in annotations. Use a closed record alias
   for the function's input. (Row-poly inference does work at the
   solver level; only the surface syntax is restricted.)

5. **Negative literal arguments need parentheses.** `f -1` parses
   as `f - 1` (subtraction). Write `f (-1)`.

6. **Zero-arg call shapes follow the binding's declared type.**
   Bare `Uuid.v4` works because its stdlib sig is `v4 : String`.
   `Time.now ()` / `Time.unixMillis ()` need `()` because their
   sigs are `() -> Task Error a`. Calling a `: String` binding with
   `()` triggers a codegen bug for arity-0 kernels — stick to the
   declared shape.

   **v0.15.50+ mitigation — `Sky.Core.Pure`.** New code can use
   the uniform `() -> Task Error a` shape via `Pure.uuidV4 ()` /
   `Pure.timeNow ()` / `Pure.systemArgs ()` etc. — additive
   companions over the canonical kernels, no `any` widening.

7. **Multi-line function signatures with continuation INSIDE the
   type body.** `name\n    : T` (the `:` on a continuation line)
   parses cleanly. Continuation *inside* the type body
   (`T1\n    -> T2`) is not supported — extract a `type alias` for
   the whole arrow type.

8. **3-tuple literals (`(a, b, c)`) at top-level expression
   position.** Parser rejects 3-tuple literals as a standalone
   top-level expression. Pairs `(a, b)` work everywhere. Wrap in
   a typed record alias if you need a 3-field shape.

## Standard library

9. **Non-tail-recursive list operations are O(N) on Go stack.**
   The following recurse with work after the recursive call (so
   auto-TCO doesn't help): `List.{map, filter, foldr, length,
   concat, concatMap, take, append, range, zip, indexedMap}`,
   `Maybe.combine`, `Result.combine`. Fine for typical UI lists
   (Go's default goroutine stack grows to 1 GB). For 200k+
   elements, prefer the tail-recursive accumulator pattern
   (`foldl` + final `reverse`). Auto-TCO covers `foldl`, `find`,
   `any`, `all`, `member`, `drop`, `reverseHelp`, `indexedMapHelp`
   — those compile to constant-stack `for { … continue }` loops.

10. **`Dict.toList` typed-key inference is inline-only.**
    `Dict.toList (Dict.fromList [(1, "a")])` chained inline returns
    real `Int` keys (v0.15.45+ closed the soundness hole for that
    shape). For let-bound intermediates — `let d = Dict.fromList […]
    in Dict.toList d` — the solver doesn't expose `d`'s typed shape
    at the use-site region, so the routing falls back to the legacy
    String-key path. Workaround: inline the chain
    (`d |> Dict.toList` works because pipe preserves the inline
    region).

11. **`Css.*` keyword constants require `()`.** `Css.zero ()`,
    `Css.auto ()`, `Css.none ()` — kernel bindings exposed as `()
    -> String`. The bare form is a clean type error. Value
    constructors like `px`, `rem`, `em`, `hex`, `rgba` take their
    args directly.

## Compiler

12. **`sky check` does not fully model Go interface satisfaction.**
    Opaque FFI types unify with each other, but the checker cannot
    verify a concrete Go type satisfies a named Go interface
    (e.g. `Label` satisfies `CanvasObject`). Calls like
    `Fyne.windowSetContent window label` may report `sky check`
    errors but compile + run correctly (`go build` does the final
    interface check at codegen time).

13. **HM type-checker heap exhaustion on monolithic Std.Ui-heavy
    modules** (defensive bound). For very large monolithic view
    files (~25+ polymorphic `Element Msg` helpers + many nested
    calls) the constraint solver can grow O(N²) in heap. The
    compiler defensively caps solver invocations at
    `SKY_SOLVER_BUDGET` steps (default `max(5,000,000,
    constraint_count × 200)`). On hitting the cap, the compiler
    aborts with a clear `TYPE ERROR: constraint solver exceeded
    budget` rather than OOMing the host.

    **Workaround**: split heavy view modules across multiple files
    (per `examples/19-skyforum`'s 8-module pattern — `State.sky`
    holds types only, `Update.sky` / `View/Common.sky` /
    one View module per page / `Main.sky` dispatcher).

## Sky.Live

14. **`init` receives only path + query.** The `Request` value
    passed to `init` exposes `path`, `query` (Dict String String),
    and the route-captured params — but not cookies, headers, or
    HTTP method. Move cookie / header reads to per-request
    handlers (`Sky.Http.Server` handlers do see the full
    `Request`).

15. **Sky.Live URL params only flow to multi-arg page
    constructors.** `route "/apps/:slug" AppDetailPage` works
    when `AppDetailPage` is `String -> Page` (the constructor is
    function-typed). A no-arg page constructor that wants to read
    the slug from a Dict needs a different shape — pin the param
    in the Page ADT.

16. **No `Navigate` Msg dispatch on URL-driven route matches.**
    The runtime sets `model.page` from a URL match, but does not
    fire a `Navigate <newPage>` Msg. Side effects that should
    happen on route change (e.g. fetch data for the new page) need
    to live in `init` (for first-load) and in the explicit
    `Navigate` Msg arm for client-driven navigation. Tracking for
    v0.17+.

## Deferred (roadmap, not active bugs)

* **Install-time Go-binding generation deferral.** `sky install`
  currently emits the full `.skycache/go/<pkg>_bindings.go`
  (Stripe: 326k lines). A future build-time, reachable-only
  generation pass would drop Stripe install from ~8 min to ~10 s.
* **Sub-app Sky-side API.** `MountSubApp` is currently
  Go-side (`rt.MountSubApp` in generated `main.go`). A Sky-side
  `Live.app { subApps = [...] }` API is on the v0.17 list.
* **Lambda-typed OUTPUT for ALL call sites.** Typed routing for
  `List.map` / `Maybe.map` etc. uses `rt.List_mapT[A, any]` —
  input typed, output `any`. Forcing `B` to concrete would need
  per-call-site monomorphisation that doesn't conflict with
  Sky's curry shape.

For the history of closures (parametric record aliases, polymorphic
re-instantiation, wildcard-`any` soundness, panic-class hardening, head-
alias unfolding, etc.) see `git log -- docs/KNOWN_LIMITATIONS.md` and
the per-version release notes.
