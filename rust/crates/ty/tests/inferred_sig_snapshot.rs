//! T5.3 inference type-equality snapshot — pins the INFERRED top-level
//! signatures of a curated set of #166-shape modules.
//!
//! The `xtask infer` gate is an accept-PARITY gate: it asserts "zero type
//! errors on the non-FFI corpus", i.e. the Rust checker ACCEPTS every program
//! the oracle accepts. That gate is necessarily blind to a change that infers a
//! WRONG-BUT-ACCEPTED type — an over-generalised row var, or an `any` widening
//! where a nominal/precise type was expected (the #166 class: record-update
//! dropping un-updated fields, subset-record-in-`Ok`, Dict-field records,
//! row-var over-unification). Such a program still type-checks clean, so the
//! accept-parity count stays 0 and the regression slides through.
//!
//! The oracle cannot be diffed here: the Haskell oracle has NO CLI command to
//! dump inferred signatures (only build/run/check/…), and its type-printer
//! format differs from Rust's `Ty::render_pretty`, so a cross-printer string
//! diff would be a false-diff machine. Only the Rust side can cleanly dump
//! sigs (`ty::check_modules().def_types` + `Ty::render_pretty`). So this pins
//! the Rust-inferred sigs as a SNAPSHOT: a future over-generalisation /
//! `any`-widening on any of these shapes changes the rendered signature and
//! fails this test.
//!
//! `render_pretty` remaps inference-artefact vars (`t42`, `r7`) to clean
//! first-appearance names (`a`, `b`, …), so the pinned strings are stable
//! regardless of the unifier's internal numbering.
//!
//! Capture mode: `SKY_SNAPSHOT_CAPTURE=1 cargo test -p ty --test
//! inferred_sig_snapshot -- --nocapture` prints the actual inferred sigs for
//! every case (used to author / re-bless the expectations after an INTENTIONAL
//! type change — never to paper over an unintended one).

use hir::{SkyDb, SourceDb};
use std::collections::HashMap;
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

/// Typecheck `stdlib + Main(main_src)` and return the inferred FULL top-level
/// signatures of the `Main` module, as sorted `(name, sig)` pairs rendered with
/// `render_pretty` for stable var names.
///
/// Crucially this pins the FULL arrow spine (parameter types → result), not the
/// body-root result alone that `CheckOutput::def_types[].ty` carries — the #166
/// class is about how PARAMETER rows propagate (a record-param row narrowed to a
/// subset, a Dict-typed field widened to `any`), which only appears in the
/// parameter part of the signature. This uses the same `Typer` tooling path the
/// LSP hover uses (`bt.signature.or(bt.result)`), so a drift here is exactly a
/// drift a user would see on hover.
fn inferred_main_sigs(root: &Path, main_src: &str) -> Vec<(String, String)> {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));

    // Accept-parity guard: the fixture MUST type-check clean, so the snapshot
    // only ever pins the type of an ACCEPTED program (a wrong type on a rejected
    // program is already the accept/reject gates' job).
    let out = ty::check_modules(&db, &[mid]);
    assert_eq!(
        out.type_errors, 0,
        "snapshot fixture must type-check clean, got {} type error(s):\n{}",
        out.type_errors,
        out.diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error)
            .map(|d| format!("  - {}", d.message))
            .collect::<Vec<_>>()
            .join("\n")
    );

    // Full arrow signatures via the tooling path (LSP hover mirror).
    let typer = ty::Typer::new(&db);
    let resolved = db.resolve(mid);
    let names: HashMap<base::DefId, String> = resolved
        .top_defs
        .iter()
        .map(|td| (td.def, td.name.as_str().to_string()))
        .collect();
    let mut sigs: Vec<(String, String)> = Vec::new();
    for (def, body) in &resolved.bodies {
        let Some(name) = names.get(def) else {
            continue;
        };
        let bt = typer.body_types_annotated(*def, body);
        let sig = bt
            .signature
            .or(bt.result)
            .map(|t| t.render_pretty())
            .unwrap_or_else(|| "?".to_string());
        sigs.push((name.clone(), sig));
    }
    sigs.sort();
    sigs
}

/// Assert the inferred Main sigs equal `expected`, or (capture mode) print them.
fn check_snapshot(case: &str, main_src: &str, expected: &[(&str, &str)]) {
    let got = inferred_main_sigs(&repo_root(), main_src);
    if std::env::var("SKY_SNAPSHOT_CAPTURE").is_ok() {
        println!("---- inferred sigs [{case}] ----");
        for (n, s) in &got {
            println!("        (\"{n}\", \"{}\"),", s.replace('"', "\\\""));
        }
        println!("---- end [{case}] ----");
        return;
    }
    let want: Vec<(String, String)> = expected
        .iter()
        .map(|(n, s)| (n.to_string(), s.to_string()))
        .collect();
    assert_eq!(
        got, want,
        "\nINFERENCE DRIFT in [{case}] — a top-level signature changed.\n\
         If this is an INTENTIONAL type-inference change, re-bless via:\n\
           SKY_SNAPSHOT_CAPTURE=1 cargo test -p ty --test inferred_sig_snapshot -- --nocapture\n\
         Otherwise this is a #166-shape regression (over-generalisation / `any`\n\
         widening / dropped record field) that the accept-parity `infer` gate\n\
         cannot see.\n"
    );
}

// ---- #166-shape fixtures --------------------------------------------------

/// (a) record-update-in-a-tuple — the TEA `update` shape. `bump` updates one
/// field and returns the whole record in a tuple; the inferred type MUST keep
/// the open row (all un-updated fields flow through), not narrow to a subset.
const RECORD_UPDATE_IN_TUPLE: &str = "\
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

bump model =
    ( { model | count = model.count + 1 }, model.count )

main =
    println \"ok\"
";

#[test]
fn snapshot_record_update_in_tuple() {
    check_snapshot(
        "record-update-in-tuple",
        RECORD_UPDATE_IN_TUPLE,
        SNAP_RECORD_UPDATE_IN_TUPLE,
    );
}

/// (b) subset-record-in-`Ok` — a function field-accesses a record param, then
/// returns `Ok param` (the WHOLE record). The `Ok` type-arg must keep the full
/// row that flows in, not narrow to just the accessed field(s) (the codegen
/// subset-record bug this shape mirrors on the type side).
const SUBSET_RECORD_IN_OK: &str = "\
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

loadUser rec =
    let
        _ =
            rec.email
    in
    Ok rec

main =
    println \"ok\"
";

#[test]
fn snapshot_subset_record_in_ok() {
    check_snapshot("subset-record-in-Ok", SUBSET_RECORD_IN_OK, SNAP_SUBSET_RECORD_IN_OK);
}

/// (c) Dict-field record — an unannotated helper reading a `Dict`-typed field
/// (the #166 Std.Db `Dict` field shape). The row must carry `Dict String
/// String` precisely, not widen the field to `any`.
const DICT_FIELD_RECORD: &str = "\
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict
import Std.Log exposing (println)

lookupName env =
    Dict.get env.name env.vars

main =
    println \"ok\"
";

#[test]
fn snapshot_dict_field_record() {
    check_snapshot("dict-field-record", DICT_FIELD_RECORD, SNAP_DICT_FIELD_RECORD);
}

/// (d) row-var-sharing / separation — two params each read a `.x` field. The
/// two rows AND the two field value types must stay INDEPENDENT (distinct
/// vars); over-unification would collapse them to one, silently accepting a
/// caller that passes mismatched records.
const ROW_VAR_SHARING: &str = "\
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

combine a b =
    ( a.x, b.x )

selfPair r =
    ( r.x, r.x )

main =
    println \"ok\"
";

#[test]
fn snapshot_row_var_sharing() {
    check_snapshot("row-var-sharing", ROW_VAR_SHARING, SNAP_ROW_VAR_SHARING);
}

// ---- pinned expectations (authored via SKY_SNAPSHOT_CAPTURE) ---------------
// Every pinned type below was audited as SOUND + PRECISE at authoring time
// (2026-08-02): no `any` widening, no subset-record narrowing, no row
// over-unification. A drift from any of these is a #166-shape regression.

/// `bump` keeps the OPEN row `a` through the record update — the un-updated
/// fields flow into the result record. A narrowed result (dropped fields) or a
/// closed `{ count : Int }` would be the #166 record-update field-drop bug.
const SNAP_RECORD_UPDATE_IN_TUPLE: &[(&str, &str)] = &[
    ("bump", "{ a | count : Int } -> ( { a | count : Int }, Int )"),
    ("main", "Task Error ()"),
];

/// `Ok rec` carries the FULL input row `{ a | email : b }` — NOT narrowed to
/// the accessed subset `{ email : b }` (the subset-record-in-Ok bug).
const SNAP_SUBSET_RECORD_IN_OK: &[(&str, &str)] = &[
    ("loadUser", "{ a | email : b } -> Result c { a | email : b }"),
    ("main", "Task Error ()"),
];

/// The `vars` field is precisely `Dict b c` (NOT `any`), and its key type `b`
/// unifies with `env.name`. Any widening of the Dict field to `any` drifts this.
const SNAP_DICT_FIELD_RECORD: &[(&str, &str)] = &[
    ("lookupName", "{ a | name : b, vars : Dict b c } -> Maybe c"),
    ("main", "Task Error ()"),
];

/// `combine`'s two params are INDEPENDENT rows (`a`,`c`) with independent field
/// value types (`b`,`d`); `selfPair` shares one field type `b` across both
/// tuple slots. Over-unification (collapsing the two rows/values) drifts this.
const SNAP_ROW_VAR_SHARING: &[(&str, &str)] = &[
    ("combine", "{ a | x : b } -> { c | x : d } -> ( b, d )"),
    ("main", "Task Error ()"),
    ("selfPair", "{ a | x : b } -> ( b, b )"),
];
