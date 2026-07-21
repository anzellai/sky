//! F1c reject-lock: cross-module misuse of an UNANNOTATED app def.
//!
//! The single-module reject corpus (`tests/reject.rs`) cannot express a
//! cross-module fixture — it loads stdlib + exactly one target file. This test
//! closes that: it builds a 2-module `SourceDb` where module `A` defines an
//! unannotated `xs = ["a", "b"]` (a `List String`) and module `B` uses it
//! cross-module. It asserts:
//!
//!   * MISUSE (`bad = xs + 1`) — `xs` used as a Number — is now REJECTED (the
//!     F1c `app_check_sigs` channel pins `xs : List String`, so `+` clashes).
//!     Before the fix `xs` resolved to a fresh flex cross-module and the misuse
//!     was silently ACCEPTED (go build caught it downstream).
//!   * CONTROL (`ok = List.length xs`) — a VALID cross-module use — still
//!     ACCEPTS (zero type errors). Proves the tightening is not over-eager.

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

/// Build stdlib + module A (`State`) + module B (`Main`, source given), then
/// return the number of type errors reported for B.
fn type_errors_for_b(root: &Path, b_src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let a_src = "module State exposing (xs)\n\nxs = [ \"a\", \"b\" ]\n";

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    db.add_module("State", syntax::parse(a_src, base::FileId(0)));
    let b_mid = db.add_module("Main", syntax::parse(b_src, base::FileId(0)));

    let out = ty::check_modules(&db, &[b_mid]);
    out.type_errors
}

#[test]
fn cross_module_unannotated_misuse_is_rejected() {
    let root = repo_root();
    // `xs : List String` used as a Number — must be REJECTED.
    let b_src = "module Main exposing (bad)\n\
                 import State exposing (xs)\n\n\
                 bad = xs + 1\n";
    let errs = type_errors_for_b(&root, b_src);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — cross-module misuse `xs + 1` (xs : List String) was ACCEPTED"
    );
}

#[test]
fn cross_module_valid_use_still_accepts() {
    let root = repo_root();
    // A VALID cross-module use of the same def — must still ACCEPT.
    let b_src = "module Main exposing (ok)\n\
                 import State exposing (xs)\n\
                 import Sky.Core.List as List\n\n\
                 ok = List.length xs\n";
    let errs = type_errors_for_b(&root, b_src);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a valid cross-module use `List.length xs` was REJECTED ({errs} errors)"
    );
}
