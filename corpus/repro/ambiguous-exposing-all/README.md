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

    cd corpus/repro/ambiguous-exposing-all/order-beta-last && sky build src/Main.sky && ./sky-out/app   # BETA
    cd corpus/repro/ambiguous-exposing-all/order-alpha-last && sky build src/Main.sky && ./sky-out/app  # ALPHA

## Why it is BLOCKED rather than fixed

The obvious rule — "an unqualified name bound by two imports is an error" —
cannot be applied naively. With `import Sky.Core.Prelude exposing (..)` and
`import Sky.Core.Math exposing (..)` both in scope, `abs` / `min` / `max` /
`sqrt` are already bound twice today, and a strict rule would reject working
programs. The correct rule has to privilege the implicit prelude the way Elm
does, so this is a language decision with app-breaking blast radius:
CLAUDE.md §0.3 rule 2 puts strategic feasibility at the user level, and the
`rust_ty_alias_resolution_164` postmortem is the standing reminder that a
name-resolution heuristic which passes the whole corpus can still regress a real
app.

The generated case `stdlib_import/exposing_all-ambiguous_exposing_all` carries
this as a `Blocked` entry (`corpus/gen.rs::blocked_reason`): it RUNS on every
corpus run, it never contributes PASS, a transition to green is reported, and
after `expires = 2026-09-30` it FAILS the gate outright. A block is a deadline,
not a parking space.
