//! Record row-polymorphism soundness lock (retires the four record-leniency
//! band-aids).
//!
//! The historical checker carried four band-aids that suppressed record
//! field-presence checks so real TEA record threading (model updates / subset
//! field access) would not false-positive. A P0 experiment proved them
//! VESTIGIAL: with record params + expected results seeded CLOSED and the
//! `unify_records` extras rules unconditional, every valid threading shape
//! still ACCEPTS, while genuine field-presence defects (and two shapes the
//! Haskell oracle unsoundly accepts) REJECT.
//!
//! This test locks both directions in one place:
//!   * VALID threading shapes → ZERO type errors (no false-reject regression);
//!   * INVALID field-presence/type shapes → at least one type error (soundness);
//!   * BETTER-THAN-ORACLE update shapes → rejected (rust stricter than the
//!     oracle's update-deferral unsoundness).
//!
//! If a future change re-introduces the record leniency, the VALID asserts stay
//! green but the INVALID/BETTER asserts flip — the lock fires.

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

/// Typecheck a single `Main` module (given source) against the full stdlib and
/// return the number of type errors reported.
fn type_errors(root: &Path, src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    let out = ty::check_modules(&db, &[mid]);
    out.type_errors
}

// ---- VALID threading shapes: MUST accept (no false-reject) ----------------

/// Each entry: (name, source). The full function type is annotated so the
/// annotation gate (`infer_def_against`, where the band-aids lived) is the code
/// path exercised — a record param seeded CLOSED must still thread cleanly.
const VALID: &[(&str, &str)] = &[
    (
        "record_update_existing",
        "module Main exposing (older)\n\
         type alias P = { name : String, age : Int }\n\
         older : P -> P\n\
         older r = { r | age = r.age + 1 }\n",
    ),
    (
        "multi_field_update",
        "module Main exposing (reset)\n\
         type alias M = { count : Int, label : String, active : Bool }\n\
         reset : M -> M\n\
         reset m = { m | count = 0, label = \"\", active = False }\n",
    ),
    (
        "subset_update_of_wider_record",
        "module Main exposing (touch)\n\
         type alias M = { count : Int, label : String, active : Bool }\n\
         touch : M -> M\n\
         touch m = { m | count = m.count + 1 }\n",
    ),
    (
        "read_subset_of_fields",
        "module Main exposing (summary)\n\
         type alias M = { count : Int, label : String, active : Bool }\n\
         summary : M -> String\n\
         summary m = m.label\n",
    ),
    (
        "thread_through_helpers",
        "module Main exposing (run)\n\
         type alias M = { count : Int, step : Int }\n\
         bump : M -> M\n\
         bump m = { m | count = m.count + m.step }\n\
         retune : M -> M\n\
         retune m = { m | step = m.step * 2 }\n\
         run : M -> M\n\
         run m = bump (retune (bump m))\n",
    ),
    (
        "open_annotation_param",
        "module Main exposing (getName)\n\
         getName : { r | name : String } -> String\n\
         getName rec = rec.name\n",
    ),
    (
        "nested_access",
        "module Main exposing (grab)\n\
         type alias Inner = { x : Int }\n\
         type alias Outer = { inner : Inner }\n\
         grab : Outer -> Int\n\
         grab r = r.inner.x\n",
    ),
    (
        "record_literal_exact",
        "module Main exposing (p)\n\
         type alias P = { name : String, age : Int }\n\
         p : P\n\
         p = { name = \"a\", age = 1 }\n",
    ),
];

// ---- INVALID shapes: MUST reject (oracle also rejects — parity) -----------

const INVALID: &[(&str, &str)] = &[
    (
        "literal_missing_field",
        "module Main exposing (p)\n\
         type alias P = { name : String, age : Int }\n\
         p : P\n\
         p = { name = \"a\" }\n",
    ),
    (
        "literal_extra_field",
        "module Main exposing (p)\n\
         type alias P = { name : String, age : Int }\n\
         p : P\n\
         p = { name = \"a\", age = 1, extra = True }\n",
    ),
    (
        "field_type_mismatch_literal",
        "module Main exposing (p)\n\
         type alias P = { name : String, age : Int }\n\
         p : P\n\
         p = { name = \"a\", age = \"old\" }\n",
    ),
    (
        "subset_where_full_required",
        "module Main exposing (main)\n\
         import Std.Log exposing (println)\n\
         type alias P = { name : String, age : Int }\n\
         render : P -> String\n\
         render r = r.name\n\
         main = println (render { name = \"a\" })\n",
    ),
];

// ---- BETTER-THAN-ORACLE shapes: MUST reject (rust stricter) ---------------
// The Haskell oracle ACCEPTS both (its update-deferral never re-checks the
// updated field). Rust rejects — this is the desired improvement.

const BETTER: &[(&str, &str)] = &[
    (
        "update_nonexistent_field",
        "module Main exposing (bad)\n\
         type alias P = { name : String, age : Int }\n\
         bad : P -> P\n\
         bad r = { r | nope = 1 }\n",
    ),
    (
        "update_wrong_type_value",
        "module Main exposing (bad)\n\
         type alias P = { name : String, age : Int }\n\
         bad : P -> P\n\
         bad r = { r | age = \"old\" }\n",
    ),
];

#[test]
fn valid_record_threading_still_accepts() {
    let root = repo_root();
    for (name, src) in VALID {
        let errs = type_errors(&root, src);
        assert_eq!(
            errs, 0,
            "FALSE-REJECT — valid record-threading shape `{name}` reported {errs} type error(s); \
             the record-leniency removal must not false-reject accepted TEA code"
        );
    }
}

#[test]
fn invalid_record_shapes_are_rejected() {
    let root = repo_root();
    for (name, src) in INVALID {
        let errs = type_errors(&root, src);
        assert!(
            errs > 0,
            "SOUNDNESS HOLE — invalid record shape `{name}` was ACCEPTED (0 type errors); \
             the oracle rejects it and so must Rust"
        );
    }
}

#[test]
fn better_than_oracle_update_shapes_are_rejected() {
    let root = repo_root();
    for (name, src) in BETTER {
        let errs = type_errors(&root, src);
        assert!(
            errs > 0,
            "LENIENCY REGRESSION — update shape `{name}` was ACCEPTED (0 type errors); \
             the Rust checker is intentionally stricter than the oracle here — a record \
             leniency band-aid has been re-introduced"
        );
    }
}
