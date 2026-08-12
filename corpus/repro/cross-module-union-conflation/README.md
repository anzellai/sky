# PINNED REPRO — two same-named unions in two modules are ONE type to the checker

Found 2026-08-12 while extending `[E1012]` to the type namespace. **Still open.**

`sky check` passes. `go build` passes. The program panics at runtime with a
`CompilerBug`:

    panic: sky.Unreachable(case): sky: codegen reached an arm the
           exhaustiveness checker said was impossible

That is a direct violation of the repo's non-negotiable *"no runtime panic from
well-typed Sky code"*.

## Reproducing

    cd corpus/repro/cross-module-union-conflation
    sky check src/Main.sky     # "No errors found."
    sky run src/Main.sky       # CompilerBug panic, ref varies

## The mechanism

`Conflate.Alpha.Shape` (`= Circle Int`) and `Conflate.Beta.Shape` (`= Square Int`)
are two different unions with **disjoint** constructor names.

* **The type checker cannot distinguish them.** `ty::sig::rewrite_alias_refs`
  rewrites a type reference to a module-qualified key `"<module>.<name>"` only
  when the reference resolves to a `DefKind::TypeAlias` — that is the #164 fix,
  and it was scoped to aliases. Every *union* falls through to
  `Ty::App(<bare final segment>)`, and `unify.rs` compares `Ty::App` names as
  plain strings. So `App("Shape")` from Alpha unifies with `App("Shape")` from
  Beta, program-wide.
* **Lowering CAN distinguish them**, and does: it emits `Conflate_Alpha_Shape`
  and `Conflate_Beta_Shape` as separate Go interfaces.
* The two views are bridged by
  `rt.Coerce[Conflate_Beta_Shape](Conflate_Alpha_Shape_Circle(3))`. Both Go types
  are interfaces with the same method set, so the assertion **succeeds** — and
  hands `betaTag` a value none of its `case` arms match.

`lower` already knows the name is ambiguous. `lower.rs` builds an
`ambiguous_names` set of "type names declared in MORE THAN ONE module" and uses
it to decline sealing an ADT (falling back to the untyped `rt.SkyADT` bag). It
emits **no diagnostic**, and the fallback is described in its own comment as
"a correctness floor, not a soundness one".

## Why `[E1012]` does not cover it

Every reference in `src/Main.sky` is **fully qualified**. Nothing is ambiguously
imported, so there is no use site for a use-site ambiguity rule to fire at. The
type-namespace extension of `[E1012]` closes the case where a program *writes* a
doubly-bound type name; this file is the case that needs no ambiguous import at
all.

The two defects share a root — type identity is a bare string once resolution
ends — but they need different fixes. `[E1012]` fixes the resolver's choice;
this needs the *checker* to carry a module-qualified identity for unions, the
way it already does for aliases.

## Why the obvious fix is not a one-liner

Dropping the `loc.kind == DefKind::TypeAlias` gate so unions are module-qualified
too changes the checker's entire nominal namespace:

* `record_union` builds a union's own result type as bare `Ty::App(tname)`, so
  declarations and references would have to move together or nothing unifies.
* `ctor_union` (ctor name → union name) and `union_ctors` are keyed on the bare
  name, and exhaustiveness reads them.
* `World::aliases` is documented as a bare-name table kept specifically "for the
  emission path (`expand_ty` from `lower`, which carries no module context)".
* Kernel and builtin type names (`Dict`, `Set`, `Task`, `Element`, `Cmd`, …) are
  matched as bare strings in `goty.rs`'s primitive table and must NOT be
  qualified. `goty.rs` already carries a hand-written band-aid for exactly this
  collision class — `("Decoder", _) => GoTy::Any` and `("Value", _) => GoTy::Any`,
  because "`Decoder` is declared in multiple modules … so a flat nominal lookup
  would coerce a real decoder to an unrelated module's phantom enum and panic at
  runtime".

Same-named unions across modules are also **idiomatic**, not exotic:
`examples/10-live-component` declares `Msg` in both `Main` and `Counter` (and
`Main.Msg` has a variant that *wraps* `Counter.Msg`), and `19-skyforum` and
`39-hub-demo` each declare `Msg`/`Page` twice. Any fix has to keep every one of
those compiling, which is precisely the #164 blast radius.

## Status

Open. Recorded in `docs/KNOWN_LIMITATIONS.md`. If this directory ever starts
rejecting — or `sky run` here ever prints `beta-square` instead of panicking —
update this README with which of those two happened and why.
