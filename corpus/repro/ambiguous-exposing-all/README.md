# PINNED REPRO — ambiguous unqualified name across two `exposing (..)` imports

Found by Family S's `stdlib_import` stratum, 2026-08-11.

Two modules both `exposing (..)` the SAME name at the SAME type. `Main`
references that name unqualified. **The program compiles clean, and the value it
computes depends on the ORDER of the two import lines.**

    import Ambig.Alpha exposing (..)   -- label = "ALPHA"
    import Ambig.Beta exposing (..)    -- label = "BETA"
    main = println label               -- prints BETA

    import Ambig.Beta exposing (..)
    import Ambig.Alpha exposing (..)
    main = println label               -- prints ALPHA

Both were verified against `rust/target/release/sky` at
`f3d322c2` — no diagnostic, no warning, exit 0, and two different answers.

Elm rejects this ("This usage of `label` is ambiguous"). Sky silently takes the
last import. That makes it the defect class this corpus exists for — *compiles
clean, behaves wrong* — and it is the #164 family: whole-program name resolution
turning a difference that should not matter into a different program. Reordering
imports is something a formatter, a merge, or an added import does routinely.

## Reproducing

    cd corpus/repro/ambiguous-exposing-all/order-beta-last && sky build src/Main.sky   # rejected [E1012]
    cd corpus/repro/ambiguous-exposing-all/order-alpha-last && sky build src/Main.sky  # rejected [E1012]

## FIXED — 2026-08-11

Both directories are now REJECTED, which is what the pair was always asking for:
the two programs differ only in import order, so either they mean the same thing
or the compiler must refuse to guess.

The rule is a **precedence lattice** over unqualified bindings
(`hir::resolve::BindLayer`, doc `rust-rewrite/05-name-resolution.md` §6b):

    ambient (0) < open (1) < explicit (2) < local (3)

A name bound in several layers resolves to the highest, deterministically and
independently of import order. Only a tie *inside* the winning layer is
ambiguous, and it is reported at the **use site** as `[E1012] AMBIGUOUS NAME`.

That is what made the fix non-breaking. The naive rule — "an unqualified name
bound by two imports is an error" — rejects working programs: with `import
Sky.Core.Prelude exposing (..)` and `import Sky.Core.Math exposing (..)` both in
scope, `abs` / `min` / `max` / `sqrt` are bound twice and real examples rely on
it. `Sky.Core.Prelude` is autoloaded, so it sits in the ambient layer and any
explicit import shadows it silently. And because the error fires only where a
name is actually *read*, the ubiquitous `Std.Html exposing (..)` +
`Std.Html.Attributes exposing (..)` pairing — which genuinely overlaps — keeps
compiling.

These two directories stay checked in as the regression: if either ever builds
again, "swapping two import lines changes the answer" is back.
