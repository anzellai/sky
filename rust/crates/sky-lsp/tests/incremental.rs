//! LSP incrementality proof (doc 10 §"Incremental for free", the salsa payoff).
//!
//! The old LSP rebuilt a `SourceDb` per request — every keystroke re-ran
//! `hir::resolve` and `ty::World::build` over *every* module (stdlib + project)
//! from scratch. The engine now holds ONE persistent `skydb::SkyDatabase`; an
//! edit `set_source_text`s only the changed file's input, so salsa recomputes
//! only the dirty sub-DAG and an untouched module's memoised `resolve`/world
//! stands.
//!
//! These tests drive the *same* `sky_lsp::Analysis` entry points the `tower-lsp`
//! server calls (`set_document` on edit; `document_symbols` / `hover` on request)
//! and assert — via the db's salsa-event sink (`Analysis::with_event_log`) — WHICH
//! queries re-execute:
//!
//! * A `WillExecute … resolve_query …` line means that query RE-RAN.
//! * A `DidValidateMemoizedValue … resolve_query …` line means it was served from
//!   memo (checked-still-valid, not recomputed).
//!
//! The proof: editing an UNRELATED module does not re-execute a dependent's
//! `resolve` (it validates from memo) while the edited module's own `resolve` +
//! `parse` do re-execute — and a second same-revision request re-executes NOTHING
//! (impossible under a per-request rebuild). Mirrors the pattern of
//! `skydb/tests/incremental.rs`, but through the LSP request path.

use std::sync::{Arc, Mutex};

use sky_lsp::Analysis;
use tower_lsp::lsp_types::{Position, Url};

// Lib (module 0) exports `greeting`; App (1) imports it; Other (2) is unrelated —
// it neither imports nor is imported by Lib/App, so an edit to it must not
// re-resolve App.
const LIB: &str = "module Lib exposing (greeting)\n\ngreeting = \"hi\"\n";
const APP: &str =
    "module App exposing (main)\n\nimport Lib exposing (greeting)\n\nmain =\n    let msg = greeting\n    in msg\n";
const OTHER_V1: &str = "module Other exposing (x)\n\nx = 1\n";
// Body-only edit: exports unchanged (`x` still the only export), only its value
// moves 1 → 2. App does not depend on Other at all.
const OTHER_V2: &str = "module Other exposing (x)\n\nx = 2\n";

fn url(name: &str) -> Url {
    Url::from_file_path(format!("/tmp/lsp-rust-incremental/{name}.sky")).unwrap()
}

/// An engine with an event sink + Lib/App/Other loaded in that order (so their
/// `ModuleId`s are 0/1/2, matching the eager backend and the skydb harness).
fn engine() -> (Analysis, Arc<Mutex<Vec<String>>>) {
    let log = Arc::new(Mutex::new(Vec::new()));
    let mut a = Analysis::with_event_log(log.clone());
    a.set_document(url("Lib"), LIB.to_string());
    a.set_document(url("App"), APP.to_string());
    a.set_document(url("Other"), OTHER_V1.to_string());
    (a, log)
}

fn clear(log: &Arc<Mutex<Vec<String>>>) {
    log.lock().unwrap().clear();
}

fn take(log: &Arc<Mutex<Vec<String>>>) -> Vec<String> {
    std::mem::take(&mut *log.lock().unwrap())
}

/// A `WillExecute` line naming `query` appeared — i.e. `query` re-ran.
fn executed(logs: &[String], query: &str) -> bool {
    logs.iter()
        .any(|l| l.starts_with("WillExecute") && l.contains(query))
}

/// A `DidValidateMemoizedValue` line naming `query` appeared — served from memo.
fn validated(logs: &[String], query: &str) -> bool {
    logs.iter()
        .any(|l| l.starts_with("DidValidateMemoizedValue") && l.contains(query))
}

/// THE headline proof. Editing the UNRELATED `Other` module must not re-execute
/// `App`'s `resolve` (App imports only Lib) — it validates from memo — while
/// `Other`'s own `resolve` + `parse` do re-execute. `document_symbols` is the
/// probe because it exercises exactly `resolve(module)` and nothing heavier
/// (no world / no per-def infer), isolating the resolve edge.
#[test]
fn unrelated_edit_does_not_re_resolve_dependent() {
    let (mut a, log) = engine();

    // Cold: warm every module's resolve memo (App+Lib via App's imports; Other
    // directly). A per-request-rebuild LSP would redo all of this every call.
    let _ = a.document_symbols(&url("App"));
    let _ = a.document_symbols(&url("Other"));
    assert!(
        !a.document_symbols(&url("App")).is_empty(),
        "App must expose its `main` symbol (sanity: the feature works)"
    );

    // Edit ONLY Other — the sole mutation is its `SourceFile` input.
    a.set_document(url("Other"), OTHER_V2.to_string());

    // Window 1 — demand App. App does not depend on Other, so its resolve must be
    // served from memo: validated, NOT re-executed.
    clear(&log);
    let app_syms = a.document_symbols(&url("App"));
    let l1 = take(&log);
    assert!(
        !executed(&l1, "resolve_query"),
        "App.resolve MUST NOT re-execute after an unrelated Other edit; log={l1:?}"
    );
    assert!(
        validated(&l1, "resolve_query"),
        "App.resolve MUST validate from memo (proves checked-not-rebuilt); log={l1:?}"
    );
    assert!(
        !app_syms.is_empty(),
        "App.document_symbols must still return `main`"
    );

    // Window 2 — demand Other. Its own input changed → resolve + parse re-execute.
    clear(&log);
    let _ = a.document_symbols(&url("Other"));
    let l2 = take(&log);
    assert!(
        executed(&l2, "resolve_query"),
        "Other.resolve MUST re-execute after its own edit; log={l2:?}"
    );
    assert!(
        executed(&l2, "parse"),
        "Other.parse MUST re-execute after its own edit; log={l2:?}"
    );
}

/// Corroborating proof that the per-request rebuild is truly gone: two identical
/// requests in the SAME revision (no edit between them) re-execute NOTHING — the
/// second is served entirely from memo. Under the old per-request `SourceDb`
/// rebuild the second `hover` would re-run `resolve` + `World::build` over every
/// module again. `hover` is the probe here because it exercises both
/// `resolve_query` AND `type_world_query` (the expensive whole-program world).
#[test]
fn same_revision_request_reuses_memo() {
    let (a, log) = engine();
    let pos = Position {
        line: 6,
        character: 7,
    }; // `msg` in `    in msg`

    // First request populates the resolve + world memos.
    let first = a.hover(&url("App"), pos);
    assert!(
        first.is_some(),
        "hover on the `msg` local must resolve (sanity: the feature works)"
    );

    // Second identical request in the same revision: pure memo hits.
    clear(&log);
    let second = a.hover(&url("App"), pos);
    let l = take(&log);
    assert!(second.is_some(), "second hover must still answer");
    assert!(
        !executed(&l, "resolve_query"),
        "a same-revision re-request MUST NOT re-execute resolve (per-request rebuild is gone); log={l:?}"
    );
    assert!(
        !executed(&l, "type_world_query"),
        "a same-revision re-request MUST NOT rebuild the world; log={l:?}"
    );
}
