//! **The stdlib passes the same annotation gate as app code.**
//!
//! `ty::check_modules` runs the annotation gate (`Infer::infer_def_against`:
//! params seeded from the signature, result-position quantifiers skolemised
//! RIGID) only over the modules it is asked to check. `sky check` asks for the
//! project's modules, never the stdlib, so a stdlib body that contradicts its
//! own signature compiled clean and failed at run time. The concrete case:
//!
//! ```elm
//! onKeyDown : msg -> Attribute msg
//! onKeyDown msg =
//!     AttrEvent (Event.onKeyDown msg)   -- Event.onKeyDown : (String -> msg) -> …
//! ```
//!
//! The `AttrEvent any` carrier hid the mismatch from the lowering path, and
//! every view that used `Ui.onKeyDown` panicked on render (`rt.Coerce: expected
//! func(string) interface {}, got main.Main_Msg_KeyPressed_V`) on both Sky.Live
//! and Sky.Spa. The same shape in user code is rejected `[E2001]` ("rigid type
//! variable `msg` cannot unify …").
//!
//! This gate type-checks EVERY stdlib module with the app-code checker and
//! fails on any type error, so a stdlib signature can no longer lie.

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
        let src = std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| panic!("{} has no module header", path.display()));
        out.push((name, parse));
    }
    out
}

/// Type-check `stdlib` (every module) with the app-code checker and return
/// every type error as `Module: [code] message`.
fn stdlib_type_errors(extra: &[(&str, &str)]) -> Vec<String> {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    assert!(
        stdlib.len() > 50,
        "stdlib failed to load ({} modules)",
        stdlib.len()
    );
    let mut db = SourceDb::new();
    let mut ids = Vec::new();
    for (n, parse) in &stdlib {
        if extra.iter().any(|(en, _)| en == n) {
            continue;
        }
        ids.push((n.clone(), db.add_module(n, parse.clone())));
    }
    for (n, src) in extra {
        ids.push((
            n.to_string(),
            db.add_module(n, syntax::parse(src, base::FileId(0))),
        ));
    }
    // One world, every module checked against it (a per-module call would
    // rebuild the world each time). A module with errors is re-checked alone to
    // attribute them — errors are rare, so that costs nothing on a green run.
    let all: Vec<_> = ids.iter().map(|(_, id)| *id).collect();
    let is_type_err = |d: &diagnostics::Diagnostic| {
        d.severity == diagnostics::Severity::Error && d.code.0.starts_with("E2")
    };
    let whole = ty::check_modules(&db, &all);
    if !whole.diagnostics.iter().any(is_type_err) {
        return Vec::new();
    }
    let mut errs = Vec::new();
    for (name, id) in &ids {
        let out = ty::check_modules(&db, &[*id]);
        for d in out.diagnostics.iter().filter(|d| is_type_err(d)) {
            errs.push(format!("{name}: [{}] {}", d.code.0, d.message));
        }
    }
    errs
}

#[test]
fn every_stdlib_body_checks_against_its_annotation() {
    let errs = stdlib_type_errors(&[]);
    assert!(
        errs.is_empty(),
        "{} stdlib definition(s) do not type-check against their own signature \
         (the app-code annotation gate). Each is a program that compiles and then \
         fails at run time:\n{}",
        errs.len(),
        errs.join("\n")
    );
}

/// Falsifier: the gate must go red on the exact shape that shipped (UF-1). A
/// stdlib module whose `onKeyDown : msg -> Attribute msg` hands a bare `msg` to
/// a `(String -> msg)` parameter must be reported.
#[test]
fn gate_rejects_the_ui_onkeydown_shape() {
    let bad = r#"module Std.Ui.GateProbe exposing (onKeyDownBad)

import Std.Html exposing (Attribute)
import Std.Html.Events as Event


onKeyDownBad : msg -> Attribute msg
onKeyDownBad msg =
    Event.onKeyDown msg
"#;
    let errs = stdlib_type_errors(&[("Std.Ui.GateProbe", bad)]);
    assert!(
        errs.iter()
            .any(|e| e.starts_with("Std.Ui.GateProbe:") && e.contains("onKeyDownBad")),
        "the gate must reject a stdlib def whose body contradicts its signature, got: {errs:#?}"
    );
}
