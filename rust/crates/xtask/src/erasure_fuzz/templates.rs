//! The erasure-boundary program generator.
//!
//! Each `Case` is a small program that is well-typed by construction and crosses
//! ONE erasure boundary — a compound value (function, record, ADT, or a
//! same-named cross-module KERNEL type) placed into an erased position (a
//! polymorphic `map`, a container) and materialised back. The known defects are
//! pinned coordinates; the rest are their distance-1 neighbours.
//!
//! It has two parts:
//!   * a fixed set of hand-written seeds for the fn-in-container and
//!     record-erasure classes (the fixed bugs — regression guards), and
//!   * a COMBINATORIAL matrix for the cross-module kernel collision — the class
//!     with a live OPEN bug — crossing `{kernel type} × {erased position} ×
//!     {name collides / distinct}`. Only the one PROVEN coordinate
//!     (`Live.Route × List.map × collide`) is seeded `expect_pass=false`; every
//!     other kernel/position collision is `expect_pass=true`, so a failure there
//!     is reported as a NEW bug, which is the point of widening the matrix.

use super::Case;

/// Imports every generated program shares (Prelude auto-loads Maybe/Result/etc.
/// as VALUES; the qualified module aliases are added per program as needed).
const BASE: &str = "import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Std.Log exposing (println)\n";

pub fn generate_cases() -> Vec<Case> {
    let mut cases = Vec::new();
    cases.extend(fn_in_container());
    cases.push(record_through_poly_map());
    cases.push(record_update_in_tuple());
    cases.push(cross_module_same_name_same_shape());
    cases.push(cross_module_plain_rep_mismatch());
    cases.extend(kernel_collision_matrix());
    cases
}

// ─────────────────────────────────────────────────────────────────────────────
// The cross-module kernel-collision matrix (kernel × position × collide)
// ─────────────────────────────────────────────────────────────────────────────

/// A KERNEL stdlib type: opaque, backed by an `Ffi.kernel`, with a bespoke Go
/// representation (NOT the plain `rt.SkyADT`/struct a Sky-source type gets). A
/// local type sharing its unqualified NAME is what the collision needs.
struct Kernel {
    /// stdlib module to import + the alias to bind it to.
    module: &'static str,
    alias: &'static str,
    /// the unqualified type name — the collision target (a local `type <ty>`).
    ty: &'static str,
    /// type arguments the kernel type takes, e.g. " msg" for `Element msg`.
    ty_args: &'static str,
    /// extra imports the constructor needs beyond the kernel module itself.
    extra_imports: &'static [&'static str],
    /// an expression constructing a value of the kernel type; `i` disambiguates.
    construct: fn(u8) -> String,
}

fn kernels() -> Vec<Kernel> {
    vec![
        // The PROVEN one: App.Route vs Live.Route (`rt.liveRoute`).
        Kernel {
            module: "Std.Live",
            alias: "Live",
            ty: "Route",
            ty_args: "",
            extra_imports: &["import Std.Ui as Ui"],
            construct: |i| format!("Live.route \"/p{i}\" (\\_ -> Ui.text \"x\")"),
        },
        // Std.Ui.Element — VNode-backed, a different kernel rep. Unknown whether
        // it collides; expect_pass=true, so a break here is a NEW discovery.
        Kernel {
            module: "Std.Ui",
            alias: "Ui",
            ty: "Element",
            ty_args: " msg",
            extra_imports: &[],
            construct: |i| format!("Ui.text \"e{i}\""),
        },
        // Sky.Core.Secret — `rt.Secret`, a redacting struct. A user `type Secret`
        // colliding with the security type is a plausible real-world shadow.
        Kernel {
            module: "Sky.Core.Secret",
            alias: "Secret",
            ty: "Secret",
            ty_args: "",
            extra_imports: &[],
            construct: |i| format!("Secret.unsafeFromString \"s{i}\""),
        },
        // Std.Decimal.Decimal — a numeric struct type.
        Kernel {
            module: "Std.Decimal",
            alias: "Dec",
            ty: "Decimal",
            ty_args: "",
            extra_imports: &[],
            construct: |i| format!("Dec.fromInt {i}"),
        },
        // Std.Ui.Attribute — another VNode-family kernel type, parameterised.
        Kernel {
            module: "Std.Ui",
            alias: "Ui",
            ty: "Attribute",
            ty_args: " msg",
            extra_imports: &[],
            construct: |i| format!("Ui.class \"c{i}\""),
        },
    ]
}

/// The erased position the converter's result flows through. Each erases its
/// element to `any` inside a polymorphic `map`, then observes structurally
/// (without consuming the kernel value's internals) to an `Int`.
#[derive(Clone, Copy)]
enum Pos {
    ListMap,
    MaybeMap,
    ResultMap,
    DictMap,
}

impl Pos {
    fn all() -> [Pos; 4] {
        [Pos::ListMap, Pos::MaybeMap, Pos::ResultMap, Pos::DictMap]
    }
    fn id(self) -> &'static str {
        match self {
            Pos::ListMap => "listmap",
            Pos::MaybeMap => "maybemap",
            Pos::ResultMap => "resultmap",
            Pos::DictMap => "dictmap",
        }
    }
    /// The qualified-module import the observation needs.
    fn import(self) -> &'static str {
        match self {
            Pos::ListMap => "import Sky.Core.List as List",
            Pos::MaybeMap => "import Sky.Core.Maybe as Maybe",
            Pos::ResultMap => "import Sky.Core.Result as Result",
            Pos::DictMap => "import Sky.Core.Dict as Dict",
        }
    }
    /// An `Int`-valued expression that runs `to_k` over `v0`/`v1` through this
    /// erased position and observes the result structurally.
    fn observe(self, to_k: &str, v0: &str, v1: &str) -> String {
        match self {
            Pos::ListMap => format!("List.length (List.map {to_k} [ {v0}, {v1} ])"),
            Pos::MaybeMap => format!(
                "Maybe.withDefault 0 (Maybe.map (\\_ -> 1) (Maybe.map {to_k} (Just {v0})))"
            ),
            Pos::ResultMap => format!(
                "Result.withDefault 0 (Result.map (\\_ -> 1) (Result.map {to_k} (Ok {v0})))"
            ),
            Pos::DictMap => format!(
                "Dict.size (Dict.map (\\_ v -> {to_k} v) (Dict.fromList [ ( \"k\", {v0} ) ]))"
            ),
        }
    }
}

fn kernel_collision_matrix() -> Vec<Case> {
    let mut out = Vec::new();
    for k in kernels() {
        for pos in Pos::all() {
            for collide in [true, false] {
                // Collide → the local type shares the kernel's unqualified name;
                // otherwise a fresh name (`Screen`) that cannot collide.
                let local = if collide { k.ty } else { "Screen" };
                let result_ty = format!("{}.{}{}", k.alias, k.ty, k.ty_args);

                let mut imports = String::from(BASE);
                imports.push_str(pos.import());
                imports.push('\n');
                imports.push_str(&format!("import {} as {}\n", k.module, k.alias));
                for extra in k.extra_imports {
                    imports.push_str(extra);
                    imports.push('\n');
                }

                let body = format!(
                    "module Main exposing (main)\n\n{imports}\n\
                     type {local}\n    = V0\n    | V1\n\n\n\
                     toK : {local} -> {result_ty}\ntoK r =\n    case r of\n\
                     \x20       V0 ->\n            {c0}\n\n        V1 ->\n            {c1}\n\n\n\
                     main =\n    println (String.fromInt ({obs}))\n",
                    c0 = (k.construct)(0),
                    c1 = (k.construct)(1),
                    obs = pos.observe("toK", "V0", "V1"),
                );

                // The proven-open BLAST RADIUS: the `Live.Route` collision panics
                // through EVERY polymorphic-map position (List / Maybe / Result —
                // widening the matrix discovered the last two). They are one
                // defect at three coordinates, so all three are seeded open; when
                // the root-cause fix lands, all three flip to guard it. Every
                // OTHER collision candidate (other kernels) stays expect_pass=true,
                // so a failure there is a genuinely NEW discovery.
                // Proven blast radius: the Live.Route collision panics through
                // ALL FOUR poly-map positions (List/Maybe/Result/Dict — each
                // discovered by widening the matrix). One defect, four coordinates.
                let known_open = collide && k.alias == "Live" && k.ty == "Route";

                out.push(Case {
                    id: format!(
                        "K_{}_{}__{}__{}",
                        k.alias,
                        k.ty,
                        pos.id(),
                        if collide { "collide" } else { "control" }
                    ),
                    files: vec![("Main.sky".into(), body)],
                    expect_pass: !known_open,
                    note: if known_open {
                        "OPEN — proven: local `Route` collides with kernel `Live.Route` (all poly-map positions)"
                    } else if collide {
                        "collision candidate: local name shadows a kernel type (new bug if it fails)"
                    } else {
                        "control: distinct local name, no collision (must pass)"
                    },
                });
            }
        }
    }
    out
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixed-class seeds (regression guards for the already-fixed bugs)
// ─────────────────────────────────────────────────────────────────────────────

const PRELUDE: &str = "import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.List as List\n\
     import Sky.Core.String as String\n\
     import Std.Log exposing (println)\n";

/// Template A — a FUNCTION value inside `Maybe` / `List` / `Result`, then
/// applied (the `codegen_maybe_of_function_erasure` archetype, 7a0e5efc). FIXED —
/// each must PASS; a regression re-breaks exactly here.
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
                    "module Main exposing (main)\n\n{PRELUDE}\
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

/// Template C — a record threaded through a polymorphic `List.map`, then a field
/// read (the `codegen_subset_record_in_ok` / `issue166` family). Fixed — passes.
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

/// Template C2 — a record UPDATE inside a tuple inside an ADT, then extracted
/// (the exact `issue166_record_update_field_drop` shape). Fixed — passes.
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

/// Control — same unqualified name across modules but both PLAIN Sky ADTs. Their
/// Go reps agree, so a nominal conflation is harmless: documents that same-name
/// alone is insufficient — the defect also needs a kernel rep.
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
        note: "control: same-named cross-module ADTs, SAME plain rep — safe",
    }
}

/// Control — same name, foreign type is a plain Sky RECORD (not a kernel). Its
/// rep round-trips through `rt.Coerce`, so the conflation is harmless. Passing
/// here while the kernel matrix's collide cases fail proves the KERNEL rep (not
/// the name alone, nor any shape mismatch) is the second necessary condition.
fn cross_module_plain_rep_mismatch() -> Case {
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
        note: "control: same name, plain-Sky rep mismatch (ADT vs record) — safe",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_known_open_bug_is_still_seeded() {
        // The proven cross-module kernel collision is the one KNOWN-OPEN
        // coordinate. If it is ever dropped, the gate silently stops watching the
        // regression it exists for. Assert it is present AND still marked open.
        let cases = generate_cases();
        let open = cases
            .iter()
            .find(|c| c.id == "K_Live_Route__listmap__collide")
            .expect("the proven kernel-collision coordinate must stay in the matrix");
        assert!(
            !open.expect_pass,
            "the proven collision is still an OPEN bug; expect_pass must stay false \
             until the root-cause fix lands (then flip it, so it guards the fix)"
        );
    }

    #[test]
    fn only_the_proven_blast_radius_is_seeded_open() {
        // The known-open set is EXACTLY the three `Live.Route` collision
        // coordinates (List/Maybe/Result map). Every other case must be
        // expect_pass=true, so a failure anywhere else is a NEW discovery, never
        // silently tolerated.
        let open: std::collections::BTreeSet<&str> = [
            "K_Live_Route__listmap__collide",
            "K_Live_Route__maybemap__collide",
            "K_Live_Route__resultmap__collide",
            "K_Live_Route__dictmap__collide",
        ]
        .into_iter()
        .collect();
        for c in generate_cases() {
            if open.contains(c.id.as_str()) {
                assert!(!c.expect_pass, "{} is a known-open coordinate", c.id);
            } else {
                assert!(c.expect_pass, "{} must be expect_pass=true (discovery mode)", c.id);
            }
        }
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
