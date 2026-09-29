//! Let-generalisation (doc 06 §"Let-generalisation").
//!
//! A let-bound FUNCTION, or a let binding whose right-hand side is a syntactic
//! value, is generalised over the type variables it does not share with the
//! enclosing scope, so one local helper can be used at two types — as a
//! top-level def always could. `docs/language/types.md` promised "full HM,
//! including generalisation"; before this, a let-bound function was
//! monomorphic, and `field "n" Decode.int` beside `field "s" Decode.string`
//! was rejected `[E2001] type mismatch: Int vs String`.
//!
//! The value restriction keeps an APPLICATION monomorphic, and a variable the
//! binding shares with the enclosing scope is never generalised: both are
//! pinned below as rejections, so the accept side cannot over-reach.

use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root");
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
        if p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        }) {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn type_errors(root: &Path, src: &str) -> usize {
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut db = SourceDb::new();
    for path in files {
        let Ok(s) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&s, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

const HDR: &str = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Sky.Core.Json.Decode as Decode\n\
                   import Std.Log exposing (println)\n\n";

fn check(body: &str) -> usize {
    type_errors(&repo_root(), &format!("{HDR}{body}"))
}

#[test]
fn let_function_used_at_two_types_accepts() {
    let n = check(
        "main =\n    let\n        twice x =\n            ( x, x )\n    in\n    \
         println (fst (twice \"a\") ++ String.fromInt (fst (twice 2)))\n",
    );
    assert_eq!(
        n, 0,
        "a let-bound function must generalise (used at String and Int)"
    );
}

#[test]
fn let_closure_over_outer_value_used_at_two_types_accepts() {
    // The downstream shape: the helper captures `raw` (monomorphic, from the
    // scope) and is polymorphic in its decoder.
    let n = check(
        "describe : String -> String\n\
         describe raw =\n    let\n        field name decoder =\n            \
         Decode.decodeString (Decode.field name decoder) raw\n    in\n    \
         case ( field \"n\" Decode.int, field \"s\" Decode.string ) of\n        \
         ( Ok n, Ok s ) ->\n            s ++ String.fromInt n\n\n        \
         _ ->\n            \"bad\"\n\n\
         main =\n    println (describe \"{}\")\n",
    );
    assert_eq!(
        n, 0,
        "a let closure over an outer value must generalise its own vars"
    );
}

#[test]
fn let_lambda_binding_and_sibling_uses_accept() {
    // A lambda right-hand side is a syntactic value, and siblings of the same
    // `let` that use the helper at two types are inferred after it (SCC order).
    let n = check(
        "main =\n    let\n        a =\n            wrap 1\n\n        \
         b =\n            wrap \"s\"\n\n        wrap =\n            \\x -> [ x ]\n    in\n    \
         println (String.fromInt (List.length a + List.length b))\n",
    );
    assert_eq!(
        n, 0,
        "a lambda-bound let must generalise; forward siblings see the scheme"
    );
}

#[test]
fn let_value_empty_list_used_at_two_types_accepts() {
    // `[]` is a syntactic value: it generalises under the value restriction.
    let n = check(
        "main =\n    let\n        none =\n            []\n    in\n    \
         println (String.fromInt (List.length (1 :: none) + List.length (\"a\" :: none)))\n",
    );
    assert_eq!(n, 0, "a syntactic-value let (`[]`) must generalise");
}

#[test]
fn let_application_is_not_generalised() {
    // The value restriction: an application stays monomorphic.
    let n = check(
        "main =\n    let\n        none =\n            identity []\n    in\n    \
         println (String.fromInt (List.length (1 :: none) + List.length (\"a\" :: none)))\n",
    );
    assert!(
        n > 0,
        "an application must stay monomorphic under the value restriction"
    );
}

#[test]
fn let_function_sharing_an_outer_variable_is_not_generalised_over_it() {
    // `k` has the type of the outer parameter `x`; that variable belongs to the
    // enclosing scope, so `k` cannot be used at two types.
    let n = check(
        "pair x =\n    let\n        k _ =\n            x\n    in\n    \
         ( String.fromInt (k 1), String.length (k \"s\") )\n\n\
         main =\n    println \"x\"\n",
    );
    assert!(n > 0, "a var shared with the scope must not generalise");
}

#[test]
fn let_function_result_is_still_checked_per_use() {
    // Generalising must not lose the per-instance result type: `twice 2` is
    // `( Int, Int )`, so appending its first element to a String rejects.
    let n = check(
        "main =\n    let\n        twice x =\n            ( x, x )\n    in\n    \
         println (fst (twice 2) ++ \"s\")\n",
    );
    assert!(n > 0, "an instance keeps its own concrete type");
}
