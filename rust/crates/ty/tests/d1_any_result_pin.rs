//! D1 regression lock — wildcard-`any` result-position pinning.
//!
//! The MISUSE (reject) side is locked by `tests/reject/corpus/d1_*.sky`; this
//! test locks the ACCEPT side — the pin must NOT over-reject. It is check-only
//! (codegen-neutral, proven by the repro gate), applies only to non-polymorphic
//! annotated defs whose declared result contains `any` AND whose body is fully
//! monomorphic, so a polymorphic body (the shape every stdlib `-> any` helper
//! has) is left unpinned and its call sites keep accepting.

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

const HDR: &str = "module Main exposing (b)\nimport Sky.Core.Prelude exposing (..)\n\n";

#[test]
fn any_result_misuse_rejects() {
    // `f : Int -> any; f x = x` returns Int; List.length needs a List → clash.
    let src = format!("{HDR}f : Int -> any\nf x =\n    x\n\nb =\n    List.length (f 5)\n");
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "SOUNDNESS — wildcard-any result must be pinned to the body type (Int), so \
         `List.length (f 5)` clashes"
    );
}

#[test]
fn any_result_mono_body_correct_use_accepts() {
    // Same def, but the result IS used as Int — must accept (pin = Int -> Int).
    let src = format!("{HDR}f : Int -> any\nf x =\n    x\n\nb =\n    (f 5) + 1\n");
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "OVER-REJECT — a correct Int use of the pinned result must accept"
    );
}

#[test]
fn any_result_poly_body_stays_lenient() {
    // `f : Int -> List any; f x = []` has a POLYMORPHIC body (List t) → not pinned,
    // so the wildcard stays lenient and this accepts (matching the oracle).
    let src = format!(
        "{HDR}f : Int -> List any\nf x =\n    []\n\nb =\n    List.map String.toUpper (f 5)\n"
    );
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "OVER-REJECT — a polymorphic-body any-result def must stay unpinned (oracle parity)"
    );
}
