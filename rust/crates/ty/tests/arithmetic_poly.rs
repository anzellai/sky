//! Regression lock for the arithmetic operator model (oracle parity).
//!
//! The oracle types `+ - *` as fully polymorphic `a -> a -> a` and `/` as
//! `Float -> Float -> Float`. Rust previously modelled all of `+ - * / ^` as a
//! shared `FlexSuper(Number)` and concretized numeric helpers to `Int`, which
//! FALSE-REJECTED valid Float callers of unannotated helpers:
//!
//!   * `add x y = x + y ; add 1.0 2.0`  — oracle ACC, Rust REJ (`Int vs Float`).
//!   * `divh x y = x / y ; divh 1.0 2.0` — oracle ACC, Rust REJ.
//!
//! After the fix `+ - *` unify their operands with no super (result = operand
//! type; poly helpers lower via rt.Add/Sub/Mul) and `/` forces Float. This test
//! locks both the fixed accepts AND that genuine misuse still clashes.
//!
//! v0.29.2: on the CHECK path `+ - *` carry the `number` bound again, and an
//! unannotated FUNCTION keeps it as a quantifier (`number -> number -> number`)
//! instead of reading it back as `Int`, so the Float caller still checks and a
//! String caller is refused. The lowering path is unchanged.

use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        let skip = p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        });
        if skip {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn load_stdlib(root: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        out.push((name, parse));
    }
    out
}

/// Build stdlib + a single `Main` module from `src`, return B's type-error count.
fn type_errors(root: &Path, src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

const HDR: &str = "module Main exposing (b)\nimport Sky.Core.Prelude exposing (..)\n\n";

#[test]
fn add_helper_at_float_accepts() {
    // The headline defect: an unannotated `+` helper applied to Floats.
    let src = format!("{HDR}add x y =\n    x + y\n\nb =\n    add 1.0 2.0\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "FALSE-REJECT — `add x y = x + y ; add 1.0 2.0` must type-check (oracle ACC)"
    );
}

#[test]
fn div_helper_at_float_accepts() {
    let src = format!("{HDR}divh x y =\n    x / y\n\nb =\n    divh 1.0 2.0\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "FALSE-REJECT — `divh x y = x / y ; divh 1.0 2.0` must type-check (`/` is Float→Float→Float)"
    );
}

#[test]
fn add_helper_over_string_rejects() {
    // v0.29.2: `+` needs a number (Elm's `number -> number -> number`). The
    // oracle's unconstrained `a -> a -> a` let `add "s" "t"` compile and then
    // panic in `rt.AsInt` at run time, so that parity was unsound.
    let src = format!("{HDR}add x y =\n    x + y\n\nb =\n    add \"s\" \"t\"\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "SOUNDNESS — `add \"s\" \"t\"` must be refused (`+` needs a number)"
    );
}

#[test]
fn add_helper_mixed_string_int_rejects() {
    // Genuine misuse: the two operands must share a type.
    let src = format!("{HDR}add x y =\n    x + y\n\nb =\n    add \"s\" 1\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "SOUNDNESS — `add \"s\" 1` (mixed String/Int) must clash at unify"
    );
}

#[test]
fn div_helper_at_string_rejects() {
    // `/` forces Float, so a String operand clashes.
    let src = format!("{HDR}divh x y =\n    x / y\n\nb =\n    divh \"a\" \"b\"\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "SOUNDNESS — `divh \"a\" \"b\"` must clash (`/` operands are Float)"
    );
}

#[test]
fn annotated_local_number_literals_instantiate_at_int_and_float() {
    let src=format!("{HDR}b =\n    let\n        delta : number -> number\n        delta _ = (9007199254740993 + 1) - 9007199254740993\n    in\n        (delta 1, delta 1.0)\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "an annotated local numeric literal must retain its number quantifier"
    );
}

#[test]
fn annotated_local_number_still_rejects_string_calls() {
    let src=format!("{HDR}b =\n    let\n        delta : number -> number\n        delta _ = 1\n    in\n        delta \"nope\"\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "generalising a literal must retain the number bound"
    );
}

#[test]
fn annotated_local_number_cannot_generalise_a_concrete_int_result() {
    let src=format!("{HDR}import Sky.Core.List as List\n\nb =\n    let\n        fixed : number -> number\n        fixed _ = List.length []\n    in\n        fixed 1.0\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "only literal evidence can stand for number, not a monomorphic Int result"
    );
}

#[test]
fn numeric_metadata_keeps_instantiation_and_literal_identity() {
    let src =
        format!("{HDR}big : number -> number\nbig _ = 9007199254740993\n\nb = (big 1, big 1.0)\n");
    let mut db = SourceDb::new();
    for (name, parse) in load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let module = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    let world = ty::World::build(&db);
    let resolved = hir::SkyDb::resolve(&db, module);
    let get = |name: &str| {
        resolved
            .top_defs
            .iter()
            .find(|d| d.name.as_str() == name)
            .unwrap()
            .def
    };
    let big = ty::numeric::compute_numeric_elaboration(&world, &db, module, get("big"));
    assert!(big.errors.is_empty(), "{:?}", big.errors);
    assert_eq!(big.top_params, 1);
    assert!(big.expressions.values().any(|s|matches!(s,ty::numeric::NumericShape::Number(p) if p.binder==ty::numeric::NumericBinder::Top)));
    let caller = ty::numeric::compute_numeric_elaboration(&world, &db, module, get("b"));
    assert!(caller.errors.is_empty(), "{:?}", caller.errors);
    assert!(caller
        .references
        .values()
        .any(|args| args == &[ty::numeric::NumberEvidence::Int]));
    assert!(caller
        .references
        .values()
        .any(|args| args == &[ty::numeric::NumberEvidence::Float]));
}

#[test]
fn numeric_metadata_keeps_local_quantifier_before_instances_collapse() {
    let src=format!("{HDR}b =\n    let\n        local : number -> number\n        local _ = 9007199254740993\n    in\n        (local 1, local 1.0)\n");
    let mut db = SourceDb::new();
    for (name, parse) in load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let module = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    let world = ty::World::build(&db);
    let resolved = hir::SkyDb::resolve(&db, module);
    let def = resolved
        .top_defs
        .iter()
        .find(|d| d.name.as_str() == "b")
        .unwrap()
        .def;
    let info = ty::numeric::compute_numeric_elaboration(&world, &db, module, def);
    assert!(info.errors.is_empty(), "{:?}", info.errors);
    assert_eq!(
        info.local_params.values().copied().collect::<Vec<_>>(),
        vec![1]
    );
    assert!(info.expressions.values().any(|s|matches!(s,ty::numeric::NumericShape::Number(p) if matches!(p.binder,ty::numeric::NumericBinder::Local(_)))));
    assert!(info
        .references
        .values()
        .any(|args| args == &[ty::numeric::NumberEvidence::Int]));
    assert!(info
        .references
        .values()
        .any(|args| args == &[ty::numeric::NumberEvidence::Float]));
}

#[test]
fn unannotated_recursive_int_parameter_rejects_a_float_self_call() {
    let src = format!(
        "{HDR}loop n x =\n    if n <= 0 then x + 1 else loop (n - 1) 1.0\n\nb = loop 1 1\n"
    );
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "a monomorphic recursive call must share its definition's Int parameter"
    );
}

#[test]
fn unannotated_recursive_numeric_function_generalises_after_its_body() {
    let src=format!("{HDR}loop n x =\n    if n <= 0 then x + x else loop (n - 1) x\n\nb = (loop 2 1, loop 2 1.0)\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "monomorphic recursion must still generalise the finished function for independent callers"
    );
}

#[test]
fn unit_parameter_keeps_numeric_bounds_at_call_sites() {
    let src = format!("{HDR}through () x = x + x\n\nb = through () \"invalid\"\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "a Unit parameter must not hide the numeric bound from callers"
    );
}

#[test]
fn unit_parameter_numeric_function_instantiates_for_int_and_float() {
    let src = format!("{HDR}through () x = x + x\n\nb = (through () 1, through () 1.25)\n");
    assert_eq!(type_errors(&repo_root(), &src), 0);
}

#[test]
fn mutually_recursive_arguments_cannot_form_an_infinite_list_type() {
    let src = format!("{HDR}first x = second [ x ]\n\nsecond y = first [ y ]\n\nb = first 1\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "unannotated recursive peers must share monomorphic argument variables"
    );
}

#[test]
fn local_helper_cannot_generalise_a_recursive_peers_argument() {
    let src = format!("{HDR}first x =\n    let\n        call y = second y\n    in\n    (call 1, call \"invalid\")\n\nsecond y =\n    let\n        _ = first y\n    in\n    y\n\nb = first 0\n");
    assert!(type_errors(&repo_root(), &src) > 0);
}

#[test]
fn independent_local_helper_in_a_recursive_group_still_generalises() {
    let src = format!("{HDR}first n x =\n    let\n        copy y = y\n        _ = copy \"ok\"\n        _ = copy 1\n    in\n    if n <= 0 then x else second (n - 1) x\n\nsecond n x =\n    if n <= 0 then x + x else first (n - 1) x\n\nb = (first 2 1.25, second 2 1)\n");
    assert_eq!(type_errors(&repo_root(), &src), 0);
}
