//! F8 reject-lock: element/type precision for UNANNOTATED stdlib combinators
//! outside the audit-#3 List-HOF set.
//!
//! Before F8, an unannotated stdlib combinator NOT in the `check_sigs` /
//! `check_kernel_sigs` channel (e.g. `List.reverse`, `Maybe.withDefault`, `fst`)
//! fell to a fresh flex at its call site (`infer_res`), so the accept/reject
//! checker could not catch element/type misuse — the ill-typed program was
//! silently ACCEPTED and only `go build` (or a runtime `rt.Coerce` panic) caught
//! it downstream. F8 extends `World::seed_check_sigs` with precise CHECK-ONLY
//! schemes for the `Sky.Core.List` core ops, `Sky.Core.Basics` tuple projections
//! (`fst`/`snd`), and `Sky.Core.Maybe` core combinators.
//!
//! This test builds `stdlib + Main` and asserts, per combinator:
//!   * MISUSE — element/type clash — is now REJECTED (`type_errors > 0`).
//!   * VALID  — a well-typed use — still ACCEPTS (`type_errors == 0`), proving
//!     the tightening is not over-eager (the accept-parity guard).
//!
//! The three CONFIRMED gaps from the F8 mandate are locked first
//! (`List.reverse` element, `Dict.get` value via `Maybe.withDefault`, `fst`
//! type), followed by representative locks for the rest of the added set.

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
    let Ok(rd) = std::fs::read_dir(dir) else { return };
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
        let Ok(src) = std::fs::read_to_string(&path) else { continue };
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

/// Build stdlib + a single `Main` module and return its type-error count.
fn type_errors_for_main(root: &Path, main_src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

const PRELUDE: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.String as String\n\
    import Sky.Core.List as List\n\
    import Sky.Core.Dict as Dict\n\
    import Sky.Core.Maybe as Maybe\n\
    import Std.Log exposing (println)\n";

fn misuse(body: &str) -> String {
    format!("{PRELUDE}main =\n    {body}\n")
}

fn assert_rejected(name: &str, body: &str) {
    let src = misuse(body);
    let errs = type_errors_for_main(&repo_root(), &src);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE ({name}) — misuse `{body}` was ACCEPTED (0 type errors)"
    );
}

fn assert_accepted(name: &str, body: &str) {
    let src = misuse(body);
    let errs = type_errors_for_main(&repo_root(), &src);
    assert_eq!(
        errs, 0,
        "OVER-EAGER ({name}) — valid use `{body}` was REJECTED ({errs} type errors)"
    );
}

// ---- the three CONFIRMED F8 gaps ----

#[test]
fn confirmed_list_reverse_element_misuse_rejected() {
    // List.reverse [1,2,3] : List Int, fed to String.join's List String.
    assert_rejected("List.reverse", "println (String.join \",\" (List.reverse [ 1, 2, 3 ]))");
}

#[test]
fn confirmed_dictget_value_misuse_rejected() {
    // Dict.get "k" strDict : Maybe String, default 0 : Int via Maybe.withDefault.
    assert_rejected(
        "Maybe.withDefault/Dict.get",
        "println (String.fromInt (Maybe.withDefault 0 (Dict.get \"k\" (Dict.fromList [ ( \"k\", \"str\" ) ]))))",
    );
}

#[test]
fn confirmed_fst_type_misuse_rejected() {
    // fst ("a", 1) : String, fed to String.fromInt : Int -> String.
    assert_rejected("fst", "println (String.fromInt (fst ( \"a\", 1 )))");
}

// ---- the three CONFIRMED valid controls still accept ----

#[test]
fn confirmed_valid_controls_accept() {
    assert_accepted("List.reverse", "println (String.join \",\" (List.reverse [ \"a\", \"b\" ]))");
    assert_accepted("snd", "println (String.fromInt (snd ( \"a\", 1 )))");
}

// ---- representative locks for the rest of the added set ----

#[test]
fn list_core_ops_misuse_rejected() {
    assert_rejected("List.take", "println (String.join \",\" (List.take 2 [ 1, 2, 3 ]))");
    assert_rejected("List.drop", "println (String.join \",\" (List.drop 1 [ 1, 2, 3 ]))");
    assert_rejected("List.append", "println (String.join \",\" (List.append [ \"a\" ] [ 1 ]))");
    assert_rejected("List.concat", "println (String.join \",\" (List.concat [ [ 1 ], [ 2 ] ]))");
    assert_rejected("List.member", "println (String.join \",\" (List.member 1 [ 1, 2 ]))");
    assert_rejected("List.length", "println (String.fromInt (String.length (List.length [ 1, 2 ])))");
    assert_rejected("List.isEmpty", "println (String.join \",\" (List.isEmpty [ 1 ]))");
    assert_rejected("List.head", "println (String.fromInt (Maybe.withDefault 0 (List.head [ \"a\" ])))");
    assert_rejected("List.tail", "println (String.join \",\" (Maybe.withDefault [] (List.tail [ 1, 2 ])))");
    assert_rejected("List.cons", "println (String.join \",\" (List.cons 1 [ \"a\" ]))");
    assert_rejected("List.zip", "println (String.join \",\" (List.zip [ 1 ] [ 2 ]))");
}

#[test]
fn list_core_ops_valid_accepts() {
    assert_accepted("List.take", "println (String.join \",\" (List.take 2 [ \"a\", \"b\" ]))");
    assert_accepted("List.append", "println (String.join \",\" (List.append [ \"a\" ] [ \"b\" ]))");
    assert_accepted("List.concat", "println (String.join \",\" (List.concat [ [ \"a\" ], [ \"b\" ] ]))");
    assert_accepted("List.length", "println (String.fromInt (List.length [ 1, 2 ]))");
    assert_accepted("List.head", "println (String.fromInt (Maybe.withDefault 0 (List.head [ 1, 2 ])))");
    assert_accepted("List.cons", "println (String.join \",\" (List.cons \"z\" [ \"a\" ]))");
}

#[test]
fn maybe_combinators_misuse_rejected() {
    assert_rejected(
        "Maybe.map",
        "println (String.fromInt (Maybe.withDefault 0 (Maybe.map String.length (Just 5))))",
    );
    assert_rejected(
        "Maybe.andThen",
        "println (String.fromInt (Maybe.withDefault 0 (Maybe.andThen String.toInt (Just 5))))",
    );
}

#[test]
fn maybe_combinators_valid_accepts() {
    assert_accepted(
        "Maybe.map",
        "println (String.fromInt (Maybe.withDefault 0 (Maybe.map String.length (Just \"ab\"))))",
    );
    assert_accepted(
        "Maybe.andThen",
        "println (String.fromInt (Maybe.withDefault 0 (Maybe.andThen String.toInt (Just \"5\"))))",
    );
}
