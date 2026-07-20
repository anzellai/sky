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
fn add_helper_polymorphic_over_string_accepts() {
    // The oracle's `+` is unconstrained `a -> a -> a`; keep parity (don't
    // re-introduce a Number gate that would reject this).
    let src = format!("{HDR}add x y =\n    x + y\n\nb =\n    add \"s\" \"t\"\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "DIVERGENCE — `add \"s\" \"t\"` must accept (oracle's `+` is a→a→a)"
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
