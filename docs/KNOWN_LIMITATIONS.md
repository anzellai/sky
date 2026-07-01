# Known limitations (v0.17.0)

Active limitations users still hit at HEAD. Each entry explains the gap,
why it exists, and the workaround. Closures across the v0.15 / v0.16 /
v0.17 cycle (parametric record aliases, polymorphic re-instantiation,
wildcard-`any` soundness, panic-class hardening, head-alias unfolding,
auto-TCO for all list ops, negative literal args, multi-line signatures,
zero-arg call shapes, Css keyword constants, FFI interface satisfaction,
Dict typed-key inference, Sky.Live init request shape, URL-driven route
Navigate Msg) are recorded in `CHANGELOG.md` and the per-version archives
under `docs/archive/`. This file lists ONLY what's still active at HEAD.

## Language (design floor — by intent)

1. **No higher-kinded types.** No `Functor` / `Monad` / `Applicative`
   classes. Sky's type system is Hindley-Milner (intentional). Use
   concrete types and explicit `andThen` / `map` per ADT.

2. **No `where` clauses.** Use `let…in` instead. Intentional —
   one bindings construct, two would be a needless surface.

3. **No custom operators.** Only built-in operators (`|>`, `<|`, `++`,
   `::`, arithmetic, comparison). Intentional — reading other people's
   code stays predictable.

4. **No row-polymorphic annotation syntax.** Sky doesn't parse
   `{ r | field : T }` in annotations. Use a closed record alias for
   the function's input. (Row-poly inference does work at the solver
   level; only the surface syntax is restricted.)

## Compiler (defensive bounds)

5. **HM type-checker heap budget on monolithic Std.Ui-heavy modules.**
   For very large monolithic view files (~25+ polymorphic `Element Msg`
   helpers + many nested calls) the constraint solver can grow O(N²) in
   heap. The compiler defensively caps solver invocations at
   `SKY_SOLVER_BUDGET` steps (default `max(5,000,000, constraint_count
   × 200)`). On hitting the cap, the compiler aborts with a clear
   `TYPE ERROR: constraint solver exceeded budget` rather than OOMing
   the host.

   **Workaround**: split heavy view modules across multiple files
   (per `examples/19-skyforum`'s 8-module pattern — `State.sky` holds
   types only, `Update.sky` / `View/Common.sky` / one View module per
   page / `Main.sky` dispatcher).

## rt.Coerce residual surface (documented sound — not a soundness gap)

6. **`rt.Coerce`-family narrowing calls remain at typed boundaries**
   (sealed-iface ctor narrowing, parametric record alias, typed list,
   container, primitive, tuple, map/dict, generic-param erasure). 214
   call sites on the canonical `examples/26-ui-showcase` benchmark.
   **All sites are documented sound** with explicit per-class
   soundness proofs in `docs/v0.17/rt-coerce-residual-surface.md`. The
   synchronous-panic gate (`defer rt.LogPanicAndExit()`) catches any
   panic that does fire and routes it to an `Err`-classified clean
   exit. Sealed-interface ADT emission (#677) would drop ~476 of these
   sites further; deferred to v0.17.x / v0.18.0 per the
   v0.17.0 release plan.

## Sky.Live + Std.Ui (active items tracked for v0.17.x)

7. **`SKY_LIVE_BASE_PATH` mounted sub-apps share session-store
   namespace.** When two Sky.Live apps mount under the same parent,
   they currently share the parent's session ID space (the `sky_sid`
   cookie). For multi-tenant deployments use separate cookie names
   per sub-app via the `[live]` cfg.

## Roadmap (not active bugs, just deferred)

* **Install-time Go-binding generation deferral.** `sky install`
  currently emits the full `.skycache/go/<pkg>_bindings.go` (Stripe:
  76k FFI symbols, ~330k lines). A future build-time, reachable-only
  generation pass would drop Stripe install from ~8 min to ~10 s.

* **Sub-app Sky-side API.** `MountSubApp` is currently Go-side
  (`rt.MountSubApp` in generated `main.go`). A Sky-side `Live.app {
  subApps = [...] }` API is on the v0.17.x list.

* **Lambda-typed OUTPUT for ALL call sites.** Typed routing for
  `List.map` / `Maybe.map` etc. uses `rt.List_mapT[A, any]` — input
  typed, output `any`. Forcing `B` to concrete would need per-call-site
  monomorphisation that doesn't conflict with Sky's curry shape.

* **Sealed-interface ADT emission (#677).** Would drop the
  rt.Coerce/AsListT floor by ~75 % on UI-heavy examples. Multi-session
  work per CLAUDE.md §0.2 N-strikes circuit-breaker (3 prior swap
  attempts produced regressions — requires re-classification before a
  4th attempt). Deferred to v0.17.x patches or v0.18.0.

* **`scopeStateRef` full IORef deletion.** The IORef carries a
  machine-verified bracket-scoped + monotonic-accumulating contract
  (Compile.hs:496-595, audited by `Sky.Build.ScopeStateRefAuditSpec`),
  so it's sound at HEAD. Full deletion via Reader-monad threading
  through every emission helper is multi-session structural work,
  deferred to v0.18.0.

## What was closed in v0.17.0

(Reference for users upgrading from v0.16.x.)

- Negative literal arguments (`f -1` parses correctly as `f (-1)`)
- Multi-line function signatures (both `: T` and `-> T` continuation)
- Zero-arg call shape arity gate (StrictHmArityGate, code `[E2007]`)
- `Css.*` keyword constants are bare values (`Css.zero` not
  `Css.zero ()`)
- `Dict.toList` typed-key inference works inline AND let-bound
- `sky check` empirically validates Go interface satisfaction
- Non-tail-recursive list operations now CPS / accumulator-rewritten
  (13/13 List/Maybe/Result list ops on constant Go stack)
- 3-tuple literals at top-level parse correctly
- Sky.Live `init` receives full `Request` (path / query / method /
  headers / cookies)
- URL-driven route matches fire `Navigate` Msg

Full history in `CHANGELOG.md` + `docs/archive/v0.17-design-notes/`.
