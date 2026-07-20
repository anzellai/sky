//! Numeric-super concretize reject-lock: cross-module misuse of an UNANNOTATED
//! numeric app def.
//!
//! Companion to `cross_module_app_check.rs`. An unresolved `Number` super (from
//! `5`, `n + 1`) is read back into the checker's `app_check_sigs` scheme. Before
//! the fix that super-constraint was DROPPED to a fresh plain quantifier, so an
//! unannotated numeric helper (`mkBadge n = { txt = "b", lvl = n + 1 }`, morally
//! `Int -> {…}`) over-generalised to `∀a. a -> {…}` and ESCAPED the F1c
//! `app_check_sigs` monomorphism filter (`!scheme.vars.is_empty()`). A
//! cross-module misuse (`mkBadge "s"`, `x = 5; String.toUpper x`) then fell to
//! fresh-flex leniency and was ACCEPTED — while the oracle REJECTS.
//!
//! The fix makes the checker's scheme read-back oracle-faithful
//! (`Sky/Type/Solve.hs:1457`): an unresolved `Number` super DEFAULTS TO CONCRETE
//! `Int` on the `app_check_sigs` channel only. So `mkBadge : Int -> {…}` is
//! monomorphic and the misuse rejects, matching the oracle.
//!
//! Asserts:
//!   * `mkBadge "s"`  (String where Int)                 — REJECTED
//!   * `x = 5; String.toUpper x` (cross-module Number)   — REJECTED
//!   * `mkBadge 2`    (Int literal; super_matches(Number, Int))
//!                                                        — ACCEPTED (positive:
//!     proves the concrete-Int default is not over-monomorphic — a valid `Int`
//!     use still type-checks).

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

/// Build stdlib + module A (`Badge`, source given) + module B (`Main`, source
/// given), then return the number of type errors reported for B.
fn type_errors(root: &Path, a_src: &str, b_src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    db.add_module("Badge", syntax::parse(a_src, base::FileId(0)));
    let b_mid = db.add_module("Main", syntax::parse(b_src, base::FileId(0)));

    let out = ty::check_modules(&db, &[b_mid]);
    out.type_errors
}

const BADGE: &str = "module Badge exposing (mkBadge)\n\n\
                     mkBadge n =\n    { txt = \"b\", lvl = n + 1 }\n";

#[test]
fn cross_module_numeric_helper_string_arg_is_rejected() {
    let root = repo_root();
    // `mkBadge : Int -> {…}` applied to a String — must be REJECTED.
    let b_src = "module Main exposing (bad)\n\
                 import Badge exposing (mkBadge)\n\n\
                 bad = mkBadge \"s\"\n";
    let errs = type_errors(&root, BADGE, b_src);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — cross-module misuse `mkBadge \"s\"` (mkBadge : Int -> {{…}}) \
         was ACCEPTED; the Number super over-generalised past the monomorphism filter"
    );
}

#[test]
fn cross_module_numeric_binding_used_as_string_is_rejected() {
    let root = repo_root();
    // `n5 : Int` (from `5`) passed to String.toUpper cross-module — REJECTED.
    let a_src = "module Badge exposing (n5)\n\nn5 =\n    5\n";
    let b_src = "module Main exposing (bad)\n\
                 import Badge exposing (n5)\n\
                 import Sky.Core.String as String\n\n\
                 bad = String.toUpper n5\n";
    let errs = type_errors(&root, a_src, b_src);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — cross-module `String.toUpper n5` (n5 : Int) was ACCEPTED; \
         the Number super over-generalised to a plain quantifier"
    );
}

#[test]
fn cross_module_numeric_helper_int_arg_still_accepts() {
    let root = repo_root();
    // A VALID Int application of the concretised helper — must still ACCEPT.
    // Proves the concrete-Int default is not over-monomorphic: super_matches
    // (Number, Int) admits the literal `2`.
    let b_src = "module Main exposing (ok)\n\
                 import Badge exposing (mkBadge)\n\n\
                 ok = .txt (mkBadge 2)\n";
    let errs = type_errors(&root, BADGE, b_src);
    assert_eq!(
        errs, 0,
        "OVER-MONOMORPHIZED — a valid Int use `mkBadge 2` was REJECTED ({errs} errors)"
    );
}
