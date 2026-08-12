# PINNED REPRO — two same-named unions in two modules are ONE type to the checker

Found 2026-08-12 while extending `[E1012]` to the type namespace. **FIXED 2026-08-12** — see "Status" at the bottom for which of the two possible outcomes happened.

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

**CLOSED 2026-08-12 — it REJECTS.** Of the two outcomes this file asked about,
the first happened, and it is the right one: `Conflate.Alpha.Shape` and
`Conflate.Beta.Shape` are two different types, so `B.betaTag (A.Circle 3)` is a
type error, not a program that should have printed `beta-square`.

    $ sky check src/Main.sky
    -- TYPE ERROR ------------------- src/Main.sky:40:13 [E2001]

    40 |     println (B.betaTag (A.Circle 3))
       |             ^^^^^^^^^^^^^^^^^^^^^^^^

    [main] type mismatch: `Conflate.Beta.Shape` vs `Conflate.Alpha.Shape`

### Why it rejects now

Union identity became module-qualified, the way alias identity already was.
`ty::sig` builds a `union_keys` set of `"<module>.<name>"` in pass 1a (beside
`alias_keys`); `record_union` stamps that key on the union's own result type, and
`rewrite_alias_refs` rewrites a reference to the same key — both sides read the
one set, so a declaration and its references cannot disagree. `unify` then asks
`ty::nominal::same` instead of comparing strings.

The safety rule that keeps the #164 blast radius closed is in `ty::nominal`: a
**bare** name means "declaring module unknown" and still matches anything with
its base name. Two types are different ONLY when both sides resolved with
certainty to two DIFFERENT modules. Builtins and kernel-implicit types intern
into the `BUILTIN_MOD` sentinel rather than a real module, so they are never
qualified and `Dict`/`Task`/`Cmd`/`Decoder`/`Value` keep matching as bare
strings everywhere downstream.

Same-named `Msg`/`Model`/`Page` across modules keep working — they are now
genuinely distinct types instead of accidentally-compatible ones, and correct
code never mixes them. `examples/10-live-component`'s `Main.Msg` variant that
WRAPS `Counter.Msg` is pinned as a test.

### Where the lock lives

`rust/crates/ty/tests/cross_module_union_identity.rs` — this reproduction plus
seven accepted twins (same-named unions used correctly, `Msg`-wrapping, a union
and an alias sharing a name, one union reached by two paths, qualified-vs-bare
references, a kernel-implicit `Decoder` across modules, an alias of a
cross-module union). The single-file reject corpus cannot express a two-module
fixture, which is why the lock is there and not in `tests/reject/corpus/`.

This directory stays checked in: if `sky check` here ever prints
"No errors found." again, the hole is back.
