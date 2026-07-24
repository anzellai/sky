//! Row-polymorphism completeness lock — row-poly RESULT field access.
//!
//! `bump r = { r | age = r.age + 1 }` has the row-polymorphic type
//! `{ age : Int | ρ } -> { age : Int | ρ }`: the row variable ρ flows from the
//! parameter into the result. Applying `bump` to a WIDER record must preserve
//! the extra field through the call, so a later access of that field
//! type-checks. Before the fix the checker DROPPED the row-carried field when a
//! resolved row-extension var was read back (it closed the record to its literal
//! fields), so `ada.name` rejected with a spurious "record is missing field(s):
//! name". See `UnionFind::normalize_record` + the `read_back_seen` record arm.
//!
//! The oracle (`sky-out/sky`) ACCEPTS these shapes; this locks the ACCEPT side.
//! The REJECT side (mixing DIFFERENT extra fields across uses of one monomorphic
//! `bump` — the oracle rejects it) is covered by the accept-parity corpus.

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
                   import Std.Log exposing (println)\n\n\
                   bump r =\n    { r | age = r.age + 1 }\n\n";

#[test]
fn row_poly_result_access_accepts() {
    // The reduced repro: apply the row-poly `bump` to `{ name, age }`, then read
    // the row-extra field `name` off the result. Must accept (row carries name).
    let src = format!(
        "{HDR}ada =\n    bump {{ name = \"ada\", age = 36 }}\n\n\
         main =\n    println ada.name\n"
    );
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "OVER-REJECT — the row variable carries `name` from the argument into the \
         result, so `ada.name` must type-check (the read-back-drops-ext bug)"
    );
}

#[test]
fn row_poly_result_access_nested_accepts() {
    // Nested: the row-poly result of one `bump` fed into another, then the
    // row-extra field read off the doubly-bumped result.
    let src = format!(
        "{HDR}ada =\n    bump (bump {{ name = \"ada\", age = 36 }})\n\n\
         main =\n    println ada.name\n"
    );
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "OVER-REJECT — nested row-poly application must still preserve the \
         row-carried `name` field through both calls"
    );
}

#[test]
fn row_poly_multi_field_accepts() {
    // Two DISTINCT extra fields carried through the SAME `bump` application — the
    // row absorbs both `name` and `active`, and both are readable off the result.
    let src = format!(
        "{HDR}ada =\n    bump {{ name = \"ada\", age = 36, active = True }}\n\n\
         main =\n    println ada.name\n"
    );
    assert_eq!(
        type_errors(&repo_root(), &src),
        0,
        "OVER-REJECT — a single application must preserve every row-carried field"
    );
}

#[test]
fn accessing_absent_field_still_rejects() {
    // SOUNDNESS guard: the fix must NOT over-accept. The row carries only `name`
    // (the fields present in the argument), so reading a field NEVER introduced
    // anywhere (`ada.height`) must still REJECT — the closed record lacks it.
    let src = format!(
        "{HDR}ada =\n    bump {{ name = \"ada\", age = 36 }}\n\n\
         main =\n    println (String.fromInt ada.height)\n"
    );
    assert!(
        type_errors(&repo_root(), &src) > 0,
        "OVER-ACCEPT — `height` is on neither the argument nor `bump`'s row, so \
         `ada.height` must be rejected"
    );
}
