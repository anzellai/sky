//! F1c record reject-lock: cross-module misuse of an UNANNOTATED **record**-typed
//! app def.
//!
//! Companion to `cross_module_app_check.rs` (which covers a `List String` def).
//! This one exercises the record axis that was unlocked when the F1c "cleanly
//! usable" filter dropped its record-free clause (record row-polymorphism became
//! sound + oracle-parity once the leniency valve retired). It builds a 2-module
//! `SourceDb` where module `Geo` defines an unannotated record helper
//! (`mkPt : String -> { px : String, py : String }`) and an unannotated record
//! constant (`origin : { px : String, py : String }`), both admitted into the
//! CHECK-ONLY `app_check_sigs` channel. Module `Main` uses them cross-module.
//!
//! NOTE on field types: numeric literals infer as generalizable flex vars in
//! this checker, so a `{ px = 0 }` record generalizes to `∀a. { px : a }` and is
//! excluded by the monomorphic clause (2) — orthogonal to the record clause.
//! Using `String` fields (string literals infer concretely) yields a genuinely
//! MONOMORPHIC record scheme, which is the shape the lifted clause admits.
//!
//! Asserts, mirroring the oracle (`sky-out/sky check`) on the same sources:
//!   * VALID   (`(mkPt "s").px`, `origin.py`)     — ACCEPTS (0 type errors).
//!   * MISUSE  (`(mkPt "s").pz`)  nonexistent field — REJECTED (record is CLOSED
//!     at instantiation, so the field access clashes rather than extending a row).
//!   * MISUSE  (`(mkPt 5).px`)    wrong arg type    — REJECTED.
//!   * MISUSE  (`origin + 1`)     record as Number   — REJECTED.

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

/// Build stdlib + module `Geo` (record helper + record constant) + module
/// `Main` (source given), then return the number of type errors for `Main`.
fn type_errors_for_main(root: &Path, main_src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let geo_src = "module Geo exposing (mkPt, origin)\n\
                   \n\
                   import Sky.Core.String as String\n\
                   \n\
                   mkPt x =\n\
                   \x20   { px = String.toUpper x, py = \"z\" }\n\
                   \n\
                   origin =\n\
                   \x20   { px = \"a\", py = \"b\" }\n";

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    db.add_module("Geo", syntax::parse(geo_src, base::FileId(0)));
    let main_mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));

    ty::check_modules(&db, &[main_mid]).type_errors
}

const HDR: &str = "module Main exposing (v)\nimport Geo exposing (mkPt, origin)\n\n";

#[test]
fn cross_module_record_valid_use_accepts() {
    let root = repo_root();
    let a = type_errors_for_main(&root, &format!("{HDR}v = (mkPt \"s\").px\n"));
    assert_eq!(a, 0, "OVER-EAGER — valid `(mkPt \"s\").px` was REJECTED ({a} errs)");
    let b = type_errors_for_main(&root, &format!("{HDR}v = origin.py\n"));
    assert_eq!(b, 0, "OVER-EAGER — valid `origin.py` was REJECTED ({b} errs)");
}

#[test]
fn cross_module_record_nonexistent_field_is_rejected() {
    let root = repo_root();
    // `.pz` on a cross-module inferred CLOSED record `{ px, py }` — REJECT.
    let errs = type_errors_for_main(&root, &format!("{HDR}v = (mkPt \"s\").pz\n"));
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — nonexistent-field access `(mkPt \"s\").pz` on a \
         cross-module inferred record was ACCEPTED (record row treated as open)"
    );
}

#[test]
fn cross_module_record_wrong_arg_is_rejected() {
    let root = repo_root();
    // `mkPt 5` — Int arg to a `String -> _` helper — REJECT.
    let errs = type_errors_for_main(&root, &format!("{HDR}v = (mkPt 5).px\n"));
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — wrong-arg `mkPt 5` (helper is String -> _) was ACCEPTED"
    );
}

#[test]
fn cross_module_record_as_number_is_rejected() {
    let root = repo_root();
    // `origin + 1` — a record used as a Number — REJECT.
    let errs = type_errors_for_main(&root, &format!("{HDR}v = origin + 1\n"));
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — record-as-Number `origin + 1` was ACCEPTED"
    );
}
