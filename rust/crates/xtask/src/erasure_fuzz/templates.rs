//! The erasure-boundary program templates.
//!
//! Each `Case` is a small program that is well-typed by construction and crosses
//! ONE erasure boundary — a compound value (function, record, ADT, or a
//! same-named cross-module type) placed into an erased position (a container
//! constructor, a polymorphic `List.map`, a cross-module converter) and then
//! materialised back (pattern-match, apply, field access). The known defects are
//! pinned coordinates here; the rest are their distance-1 neighbours.
//!
//! This first cut is TEMPLATE-based (fixed skeletons, a few shape substitutions)
//! rather than a free type-directed generator, because the bug cluster is narrow
//! and precisely characterised — a template that reproduces the archetype is far
//! likelier to land on a real defect than random well-typed noise, and it is
//! self-evidently valid Sky. A type-directed generator over the same axes is the
//! natural next step once the oracle is proven.

use super::Case;

const PRELUDE: &str = "import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.List as List\n\
     import Sky.Core.String as String\n\
     import Std.Log exposing (println)\n";

pub fn generate_cases() -> Vec<Case> {
    let mut cases = Vec::new();
    cases.extend(fn_in_container());
    // The cross-module collision matrix. It pins the EXACT trigger: the defect
    // needs same-unqualified-name AND a foreign KERNEL-represented type. The
    // three passing variants are the controls that isolate each condition.
    cases.push(cross_module_kernel_collision()); // OPEN — same name + kernel rep
    cases.push(cross_module_same_name_same_shape()); // control: same name, same rep → safe
    cases.push(cross_module_same_name()); // control: same name, plain-rep mismatch → safe
    cases.push(cross_module_distinct_name_kernel()); // control: kernel rep, distinct name → safe
    cases.push(record_through_poly_map());
    cases.push(record_update_in_tuple());
    cases
}

/// Template A — a FUNCTION value inside `Maybe` / `List` / `Result`, then
/// applied. This is the `codegen_maybe_of_function_erasure` archetype (7a0e5efc):
/// the container erased the element to `func(any)any` while the concrete slot was
/// `func(string)Msg`. FIXED — so every one of these must now PASS; a regression
/// re-breaks exactly here.
fn fn_in_container() -> Vec<Case> {
    let msg = "type Msg\n    = Go String\n    | NoOp\n";
    vec![
        Case {
            id: "A1_fn_in_maybe".into(),
            files: vec![(
                "Main.sky".into(),
                format!(
                    "module Main exposing (main)\n\n{PRELUDE}\n\n{msg}\n\n\
                     handler : Maybe (String -> Msg)\nhandler =\n    Just Go\n\n\n\
                     applyIt : Maybe (String -> Msg) -> String -> String\n\
                     applyIt mf s =\n    case mf of\n        Just f ->\n\
                     \x20           case f s of\n                Go x ->\n                    x\n\n\
                     \x20               NoOp ->\n                    \"noop\"\n\n\
                     \x20       Nothing ->\n            \"none\"\n\n\n\
                     main =\n    println (applyIt handler \"hello\")\n"
                ),
            )],
            expect_pass: true,
            note: "fn in Maybe, applied (7a0e5efc archetype)",
        },
        Case {
            id: "A2_fn_in_list".into(),
            files: vec![(
                "Main.sky".into(),
                format!(
                    "module Main exposing (main)\n\n{PRELUDE}\n\n{msg}\n\n\
                     handlers : List (String -> Msg)\nhandlers =\n    [ Go, \\_ -> NoOp ]\n\n\n\
                     runFirst : List (String -> Msg) -> String -> String\n\
                     runFirst fs s =\n    case fs of\n        f :: _ ->\n\
                     \x20           case f s of\n                Go x ->\n                    x\n\n\
                     \x20               NoOp ->\n                    \"noop\"\n\n\
                     \x20       [] ->\n            \"empty\"\n\n\n\
                     main =\n    println (runFirst handlers \"hi\")\n"
                ),
            )],
            expect_pass: true,
            note: "fn in List, head applied",
        },
        Case {
            id: "A3_fn_in_result".into(),
            files: vec![(
                "Main.sky".into(),
                format!(
                    "module Main exposing (main)\n\n{PRELUDE}\n\
                     import Sky.Core.Error exposing (Error)\n\n{msg}\n\n\
                     handler : Result Error (String -> Msg)\nhandler =\n    Ok Go\n\n\n\
                     applyIt : Result Error (String -> Msg) -> String -> String\n\
                     applyIt mf s =\n    case mf of\n        Ok f ->\n\
                     \x20           case f s of\n                Go x ->\n                    x\n\n\
                     \x20               NoOp ->\n                    \"noop\"\n\n\
                     \x20       Err _ ->\n            \"err\"\n\n\n\
                     main =\n    println (applyIt handler \"hey\")\n"
                ),
            )],
            expect_pass: true,
            note: "fn in Result, applied",
        },
    ]
}

/// Template B — the OPEN `codegen_samename_crossmodule_type_collision`, faithfully
/// reproduced. A LOCAL `type Route` collides (same unqualified name) with the
/// KERNEL type `Std.Live.Route` (Go `rt.liveRoute`, a bespoke rep — not the plain
/// `rt.SkyADT`/struct a Sky-source type gets). A converter `Route -> Live.Route`
/// is run through a polymorphic `List.map`, which erases the element to `any`;
/// on the way back out codegen narrows it using the unqualified name `Route`,
/// which in this module resolves to the LOCAL ADT, not `Live.Route` — so a
/// `rt.liveRoute` is coerced to the wrong shape and panics. `sky check` passes
/// ("No errors found"), the binary builds, the run dies with `rt.Coerce:
/// expected …, got rt.liveRoute`. Seeded `expect_pass = false`: rediscovering it
/// is the point, until the root-cause fix lands.
///
/// The three sibling controls below isolate the two necessary conditions:
///   * same name + SAME rep (ADT/ADT) → safe (`cross_module_same_name_same_shape`)
///   * same name + different PLAIN rep (ADT/record) → safe (`cross_module_same_name`)
///   * different name + kernel rep → safe (`cross_module_distinct_name_kernel`)
/// so the defect is precisely "same unqualified name AND a kernel-represented
/// foreign type".
fn cross_module_kernel_collision() -> Case {
    Case {
        id: "B_kernel_collision_route".into(),
        files: vec![(
            "Main.sky".into(),
            format!(
                "module Main exposing (main)\n\n{PRELUDE}\
                 import Std.Live as Live\nimport Std.Ui as Ui\n\n\
                 type Route\n    = Home\n    | About\n\n\n\
                 toLive : Route -> Live.Route\ntoLive r =\n    case r of\n\
                 \x20       Home ->\n            Live.route \"/\" (\\_ -> Ui.text \"home\")\n\n\
                 \x20       About ->\n            Live.route \"/about\" (\\_ -> Ui.text \"about\")\n\n\n\
                 main =\n    let\n        routes =\n            List.map toLive [ Home, About ]\n    in\n\
                 \x20   println (String.fromInt (List.length routes))\n"
            ),
        )],
        expect_pass: false,
        note: "local `Route` collides with kernel `Live.Route` through a poly map (OPEN — the real bug)",
    }
}

/// Control — same name, but the foreign type is a plain Sky-source record, not a
/// kernel type. Its Go rep round-trips through `rt.Coerce` fine, so the nominal
/// conflation is harmless: this MUST pass. Passing here while
/// `cross_module_kernel_collision` fails is what proves the KERNEL rep (not the
/// name alone, and not any shape mismatch) is the second necessary condition.
fn cross_module_same_name() -> Case {
    Case {
        id: "B_plain_rep_mismatch".into(),
        files: vec![
            (
                "Sub.sky".into(),
                "module Sub exposing (Route, make, tag)\n\n\
                 type alias Route =\n    { path : String }\n\n\n\
                 make : Int -> Route\nmake n =\n    if n > 0 then\n        { path = \"yes\" }\n\n\
                 \x20   else\n        { path = \"no\" }\n\n\n\
                 tag : Route -> String\ntag r =\n    r.path\n"
                    .into(),
            ),
            (
                "Main.sky".into(),
                format!(
                    "module Main exposing (main)\n\n{PRELUDE}\nimport Sub\n\n\
                     type Route\n    = MA\n    | MB\n\n\n\
                     toSub : Route -> Sub.Route\ntoSub r =\n    case r of\n\
                     \x20       MA ->\n            Sub.make 1\n\n        MB ->\n            Sub.make 0\n\n\n\
                     main =\n    let\n        subs =\n            List.map toSub [ MA, MB ]\n\n\
                     \x20       tags =\n            List.map Sub.tag subs\n    in\n\
                     \x20   println (String.join \",\" tags)\n"
                ),
            ),
        ],
        expect_pass: true,
        note: "control: same name, plain-Sky rep mismatch (ADT vs record) — safe, only KERNEL reps collide",
    }
}

/// Template B0 — the SAME-SHAPE control: two same-named ADTs. Their Go reps agree,
/// so even a nominal conflation is harmless — this documents that same-name alone
/// is not sufficient; the defect needs a representation mismatch (Template B).
fn cross_module_same_name_same_shape() -> Case {
    Case {
        id: "B0_same_name_same_shape".into(),
        files: vec![
            (
                "Sub.sky".into(),
                "module Sub exposing (Route, make, tag)\n\n\
                 type Route\n    = NA\n    | NB\n\n\n\
                 make : Int -> Route\nmake n =\n    if n > 0 then\n        NA\n\n    else\n        NB\n\n\n\
                 tag : Route -> String\ntag r =\n    case r of\n        NA ->\n            \"na\"\n\n\
                 \x20       NB ->\n            \"nb\"\n"
                    .into(),
            ),
            (
                "Main.sky".into(),
                format!(
                    "module Main exposing (main)\n\n{PRELUDE}\nimport Sub\n\n\
                     type Route\n    = MA\n    | MB\n\n\n\
                     toSub : Route -> Sub.Route\ntoSub r =\n    case r of\n\
                     \x20       MA ->\n            Sub.make 1\n\n        MB ->\n            Sub.make 0\n\n\n\
                     main =\n    let\n        subs =\n            List.map toSub [ MA, MB ]\n\n\
                     \x20       tags =\n            List.map Sub.tag subs\n    in\n\
                     \x20   println (String.join \",\" tags)\n"
                ),
            ),
        ],
        expect_pass: true,
        note: "same-named cross-module ADTs, SAME Go rep — harmless (documents the mismatch is required)",
    }
}

/// Control — identical to `cross_module_kernel_collision` (same kernel
/// `Live.Route`, same converter + poly map) EXCEPT the local type is named
/// `Screen`, not `Route`, so there is no unqualified-name collision. It MUST
/// pass; passing here while the collision case fails isolates the defect to the
/// shared NAME — the kernel type alone is fine.
fn cross_module_distinct_name_kernel() -> Case {
    Case {
        id: "B_control_distinct_name_kernel".into(),
        files: vec![(
            "Main.sky".into(),
            format!(
                "module Main exposing (main)\n\n{PRELUDE}\
                 import Std.Live as Live\nimport Std.Ui as Ui\n\n\
                 type Screen\n    = Home\n    | About\n\n\n\
                 toLive : Screen -> Live.Route\ntoLive r =\n    case r of\n\
                 \x20       Home ->\n            Live.route \"/\" (\\_ -> Ui.text \"home\")\n\n\
                 \x20       About ->\n            Live.route \"/about\" (\\_ -> Ui.text \"about\")\n\n\n\
                 main =\n    let\n        routes =\n            List.map toLive [ Home, About ]\n    in\n\
                 \x20   println (String.fromInt (List.length routes))\n"
            ),
        )],
        expect_pass: true,
        note: "control: kernel Live.Route but local type named `Screen` — no name collision, safe",
    }
}

/// Template C — a record threaded through a polymorphic `List.map`, then a field
/// read. The record-erasure family (`codegen_subset_record_in_ok`,
/// `issue166_record_update_field_drop`). Fixed — must pass.
fn record_through_poly_map() -> Case {
    Case {
        id: "C_record_through_map".into(),
        files: vec![(
            "Main.sky".into(),
            format!(
                "module Main exposing (main)\n\n{PRELUDE}\n\n\
                 type alias Item =\n    {{ id : Int, name : String }}\n\n\n\
                 bump : Item -> Item\nbump it =\n    {{ it | id = it.id + 1 }}\n\n\n\
                 main =\n    let\n        items =\n            List.map bump \
                 [ {{ id = 1, name = \"a\" }}, {{ id = 2, name = \"b\" }} ]\n\n\
                 \x20       names =\n            List.map .name items\n    in\n\
                 \x20   println (String.join \",\" names)\n"
            ),
        )],
        expect_pass: true,
        note: "record through poly map + field read",
    }
}

/// Template C2 — a record UPDATE inside a tuple inside an ADT, then extracted.
/// This is the exact `issue166_record_update_field_drop` shape (un-updated fields
/// were dropped → an ADT field became `Unreachable`). Fixed — must pass.
fn record_update_in_tuple() -> Case {
    Case {
        id: "C2_record_update_in_tuple".into(),
        files: vec![(
            "Main.sky".into(),
            format!(
                "module Main exposing (main)\n\n{PRELUDE}\n\n\
                 type alias Item =\n    {{ id : Int, name : String }}\n\n\n\
                 type Wrap\n    = W ( Item, Int )\n\n\n\
                 mk : Item -> Wrap\nmk it =\n    W ( {{ it | id = it.id + 1 }}, 0 )\n\n\n\
                 nameOf : Wrap -> String\nnameOf w =\n    case w of\n        W ( item, _ ) ->\n\
                 \x20           item.name\n\n\n\
                 main =\n    println (nameOf (mk {{ id = 1, name = \"kept\" }}))\n"
            ),
        )],
        expect_pass: true,
        note: "record update in a tuple in an ADT, field read back (#166 shape)",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_known_open_bug_is_still_seeded() {
        // The cross-module kernel collision is the one KNOWN-OPEN coordinate. If
        // it is ever dropped, the gate silently stops watching the regression it
        // exists for. Assert it is present AND still marked open.
        let cases = generate_cases();
        let open = cases
            .iter()
            .find(|c| c.id == "B_kernel_collision_route")
            .expect("the kernel-collision seed must stay in the matrix");
        assert!(
            !open.expect_pass,
            "B_kernel_collision_route is still an OPEN bug; expect_pass must stay false \
             until the root-cause fix lands (then flip it, so it guards the fix)"
        );
    }

    #[test]
    fn every_case_has_an_entry_module() {
        for c in generate_cases() {
            assert!(
                c.files.iter().any(|(rel, _)| rel == "Main.sky"),
                "case {} has no Main.sky entry",
                c.id
            );
        }
    }
}
