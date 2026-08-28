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
//!     {name collides / distinct}`. Every COLLIDE case is `Expect::KnownOpen` (a
//!     probe of the open class; its pass/fail is data, never a gate failure);
//!     every CONTROL is `Expect::MustPass`. Widening showed the class manifests
//!     for `Live.Route` in all six positions (including a bare list literal — so
//!     it does not even need a `map`). When the class is fixed and every probe
//!     passes, they are promoted to `MustPass` to guard the fix.

use super::{Case, Expect};

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
    cases.extend(value_shape_matrix());
    cases
}

// ─────────────────────────────────────────────────────────────────────────────
// The value-shape matrix (compound value × erased position), breadth check
// ─────────────────────────────────────────────────────────────────────────────

/// A plain (non-kernel) compound value and how to reduce it to an `Int`. These
/// exercise the codegen narrow-back for a function / record / ADT that has been
/// erased through a polymorphic map — the fn arm is the 7a0e5efc class, in the
/// positions its regression test never covered (Dict / nested). All MustPass: a
/// failure is a NEW bug (an incomplete earlier fix, or a third root-cause class).
struct Shape {
    id: &'static str,
    /// type declarations + a top-level `<shape> -> Int` helper named `extract`.
    decls: &'static str,
    /// an expression of the shape's type.
    value: &'static str,
    /// the name of the shape→Int helper declared in `decls`.
    extract: &'static str,
}

fn shapes() -> Vec<Shape> {
    vec![
        Shape {
            id: "fn",
            decls: "extract : (Int -> Int) -> Int\nextract f =\n    f 5\n",
            value: "(\\x -> x + 10)", // extract → 15
            extract: "extract",
        },
        Shape {
            id: "record",
            decls: "type alias Rec =\n    { n : Int }\n\n\nextract : Rec -> Int\nextract r =\n    r.n\n",
            value: "{ n = 7 }", // extract → 7
            extract: "extract",
        },
        Shape {
            id: "adt",
            decls: "type Box\n    = Box Int\n\n\nextract : Box -> Int\nextract b =\n    case b of\n        Box n ->\n            n\n",
            value: "(Box 9)", // extract → 9
            extract: "extract",
        },
    ]
}

/// The positions a plain value is erased through, then reduced to an `Int`.
#[derive(Clone, Copy)]
enum VPos {
    ListMap,
    DictMap,
    NestedMaybeList,
}

impl VPos {
    fn all() -> [VPos; 3] {
        [VPos::ListMap, VPos::DictMap, VPos::NestedMaybeList]
    }
    fn id(self) -> &'static str {
        match self {
            VPos::ListMap => "listmap",
            VPos::DictMap => "dictmap",
            VPos::NestedMaybeList => "nested",
        }
    }
    fn imports(self) -> &'static [&'static str] {
        match self {
            VPos::ListMap => &["import Sky.Core.List as List"],
            VPos::DictMap => &["import Sky.Core.List as List", "import Sky.Core.Dict as Dict"],
            VPos::NestedMaybeList => {
                &["import Sky.Core.List as List", "import Sky.Core.Maybe as Maybe"]
            }
        }
    }
    /// An `Int` = the sum of `extract` applied to `value` after erasure.
    fn observe(self, extract: &str, value: &str) -> String {
        match self {
            VPos::ListMap => format!("List.foldl (\\a b -> a + b) 0 (List.map {extract} [ {value} ])"),
            VPos::DictMap => format!(
                "List.foldl (\\a b -> a + b) 0 (Dict.values (Dict.map (\\_ v -> {extract} v) (Dict.fromList [ ( \"k\", {value} ) ])))"
            ),
            VPos::NestedMaybeList => format!(
                "Maybe.withDefault 0 (Maybe.map (\\xs -> List.foldl (\\a b -> a + b) 0 (List.map {extract} xs)) (Just [ {value} ]))"
            ),
        }
    }
}

fn value_shape_matrix() -> Vec<Case> {
    let mut out = Vec::new();
    for s in shapes() {
        for pos in VPos::all() {
            let mut imports = String::from(BASE);
            for imp in pos.imports() {
                imports.push_str(imp);
                imports.push('\n');
            }
            let body = format!(
                "module Main exposing (main)\n\n{imports}\n{decls}\n\n\
                 main =\n    println (String.fromInt ({obs}))\n",
                decls = s.decls,
                obs = pos.observe(s.extract, s.value),
            );
            out.push(Case {
                id: format!("V_{}__{}", s.id, pos.id()),
                files: vec![("Main.sky".into(), body)],
                expect: Expect::MustPass,
                note: "compound value erased through a poly map, materialised to Int (must pass)",
            });
        }
    }
    out
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
    /// `true` once this kernel's cross-module collision is FIXED (its type is a
    /// DECLARED opaque nominal, not a bare kernel-implicit name) — its collision
    /// probes are then MustPass regression guards. `false` keeps them KnownOpen.
    fixed: bool,
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
            fixed: true,
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
            fixed: true,
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
            fixed: true,
        },
        // Std.Decimal.Decimal — a numeric struct type.
        Kernel {
            module: "Std.Decimal",
            alias: "Dec",
            ty: "Decimal",
            ty_args: "",
            extra_imports: &[],
            construct: |i| format!("Dec.fromInt {i}"),
            fixed: true,
        },
        // Std.Ui.Attribute — another VNode-family kernel type, parameterised.
        Kernel {
            module: "Std.Ui",
            alias: "Ui",
            ty: "Attribute",
            ty_args: " msg",
            extra_imports: &[],
            construct: |i| format!("Ui.class \"c{i}\""),
            fixed: true,
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
    /// A list LITERAL of kernel values — the erasure is at element insertion, not
    /// through a polymorphic `map`. Tests whether the collision needs the map or
    /// just the container's `any` slot.
    ContainerLit,
    /// `Maybe (List kernel)` — the value crosses TWO erased boundaries.
    NestedMaybeList,
}

impl Pos {
    fn all() -> [Pos; 6] {
        [
            Pos::ListMap,
            Pos::MaybeMap,
            Pos::ResultMap,
            Pos::DictMap,
            Pos::ContainerLit,
            Pos::NestedMaybeList,
        ]
    }
    fn id(self) -> &'static str {
        match self {
            Pos::ListMap => "listmap",
            Pos::MaybeMap => "maybemap",
            Pos::ResultMap => "resultmap",
            Pos::DictMap => "dictmap",
            Pos::ContainerLit => "containerlit",
            Pos::NestedMaybeList => "nested",
        }
    }
    /// The qualified-module imports the observation needs.
    fn imports(self) -> &'static [&'static str] {
        match self {
            Pos::ListMap => &["import Sky.Core.List as List"],
            Pos::MaybeMap => &["import Sky.Core.Maybe as Maybe"],
            Pos::ResultMap => &["import Sky.Core.Result as Result"],
            Pos::DictMap => &["import Sky.Core.Dict as Dict"],
            Pos::ContainerLit => &["import Sky.Core.List as List"],
            Pos::NestedMaybeList => {
                &["import Sky.Core.List as List", "import Sky.Core.Maybe as Maybe"]
            }
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
            Pos::ContainerLit => format!("List.length [ {to_k} {v0}, {to_k} {v1} ]"),
            Pos::NestedMaybeList => format!(
                "Maybe.withDefault 0 (Maybe.map List.length (Just (List.map {to_k} [ {v0}, {v1} ])))"
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
                for imp in pos.imports() {
                    imports.push_str(imp);
                    imports.push('\n');
                }
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
                // Proven blast radius: the Live.Route collision panics through the
                // PROMOTED (Fix 1, Std.Live now DECLARES Route): every collision
                // probe passes, so they are MustPass regression guards. A kernel
                // whose collision is NOT yet fixed carries `fixed = false`, keeping
                // its collide probes KnownOpen (tracked, not gate-failing) until it
                // is declared too. A regression of a fixed collision, or a newly
                // added undeclared kernel that collides, turns the gate red.
                out.push(Case {
                    id: format!(
                        "K_{}_{}__{}__{}",
                        k.alias,
                        k.ty,
                        pos.id(),
                        if collide { "collide" } else { "control" }
                    ),
                    files: vec![("Main.sky".into(), body)],
                    expect: if collide && !k.fixed {
                        Expect::KnownOpen
                    } else {
                        Expect::MustPass
                    },
                    note: if collide {
                        "collision probe: local name shadows a kernel type (must not collide)"
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
            expect: Expect::MustPass,
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
            expect: Expect::MustPass,
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
            expect: Expect::MustPass,
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
        expect: Expect::MustPass,
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
        expect: Expect::MustPass,
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
        expect: Expect::MustPass,
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
        expect: Expect::MustPass,
        note: "control: same name, plain-Sky rep mismatch (ADT vs record) — safe",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_proven_collision_coordinate_is_a_regression_guard() {
        // After Fix 1 (Std.Live declares Route) the proven Live.Route × List.map
        // collision must be a MustPass guard — a regression re-opens it and turns
        // the gate red. If this ever reverts to KnownOpen, the fix stopped being
        // guarded.
        let cases = generate_cases();
        let g = cases
            .iter()
            .find(|c| c.id == "K_Live_Route__listmap__collide")
            .expect("the proven kernel-collision coordinate must stay in the matrix");
        assert!(
            g.expect == Expect::MustPass,
            "the fixed Live.Route collision must be MustPass so it guards the fix"
        );
    }

    #[test]
    fn a_fixed_kernels_collision_probes_are_must_pass() {
        // The seeding rule: a collide probe of a `fixed = true` kernel is a
        // MustPass guard; a collide probe of an unfixed kernel stays KnownOpen
        // (tracked); every control + fixed-class seed is MustPass. All current
        // kernels are fixed, so every collide probe here is MustPass.
        for c in generate_cases() {
            if c.id.contains("__collide") {
                // every kernel in the matrix is currently `fixed = true`
                assert!(c.expect == Expect::MustPass, "{} (fixed collide) must be MustPass", c.id);
            } else {
                assert!(c.expect == Expect::MustPass, "{} must be MustPass", c.id);
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
