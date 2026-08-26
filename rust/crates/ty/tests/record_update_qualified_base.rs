//! **A QUALIFIED path as a record-update base** — `{ App.webDefaults | port =
//! 9000 }`.
//!
//! A qualified reference is a valid expression, and a valid expression is a
//! valid record-update base. This exercises the D1 (type-reference resolution)
//! + D4 (module-structure) path: the base must resolve through the *qualifier*
//! (`resolve_qual_var`), not by taking the final segment and resolving it as a
//! local (which would either miss or bind the wrong `webDefaults`).
//!
//! The bug: the parser rejected the form outright (`expected LowerIdent`), and
//! resolution read the base via an UNqualified `first_lower` + `resolve_var`,
//! so even had it parsed, the qualifier was dropped. Both are covered here —
//! the qualified base must type-check, and a bare base must keep working
//! unchanged (an over-eager fix that broke `{ x | f = v }` is a regression).

use hir::SourceDb;
use std::path::PathBuf;
use ty::reject_corpus as rc;

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

/// Check ONE `Main` module against the real stdlib.
fn check(src: &str) -> ty::CheckOutput {
    let mut db = SourceDb::new();
    for (name, parse) in rc::load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
}

fn assert_clean(label: &str, src: &str) {
    let out = check(src);
    let render = |out: &ty::CheckOutput| {
        out.diagnostics
            .iter()
            .map(|d| format!("[{}] {}", d.code.0, d.message))
            .collect::<Vec<_>>()
    };
    // Parse errors surface as `E0001` diagnostics (the pre-fix symptom was
    // `E0001: expected LowerIdent` at the qualified base).
    let parse_errs: Vec<_> = out
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E0001")
        .map(|d| d.message.clone())
        .collect();
    assert!(
        parse_errs.is_empty(),
        "{label}: must have zero parse errors, got {parse_errs:?}"
    );
    assert_eq!(
        out.name_errors,
        0,
        "{label}: qualified base must RESOLVE (zero name errors), got {:?}",
        render(&out)
    );
    assert_eq!(
        out.type_errors,
        0,
        "{label}: must type-check cleanly, got {:?}",
        render(&out)
    );
}

/// THE regression: a qualified path as the update base. `App.webDefaults` is a
/// `WebOpts` record exported by `Std.App`; updating its `port` must resolve the
/// qualified base and type-check.
#[test]
fn qualified_base_resolves_and_type_checks() {
    let src = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.App as App
import Std.Log exposing (println)


opts : App.WebOpts
opts =
    { App.webDefaults | port = 9000 }


main =
    println (String.fromInt opts.port)
";
    assert_clean("qualified update base", src);
}

/// The accept twin: a BARE local base must still resolve and type-check
/// unchanged. Without this, the reject/accept pair above is satisfied by a
/// compiler that regressed every ordinary record update.
#[test]
fn bare_base_still_type_checks() {
    let src = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)


bump : { count : Int } -> { count : Int }
bump r =
    { r | count = r.count + 1 }


main =
    println (String.fromInt (bump { count = 1 }).count)
";
    assert_clean("bare update base", src);
}
