//! Phase A — external Sky dependency resolution in the LSP.
//!
//! Modules fetched under `.skydeps/` load into the salsa db (via `set_document`)
//! so refs into a dependency resolve exactly as the build path resolves them —
//! no `?` hover, no spurious `[E1001]`. Plus the load-order override rule, the
//! decoy-`Main` drop, and the unfetched-dependency hint.

mod common;

use common::*;
use sky_lsp::Analysis;
use tower_lsp::lsp_types::HoverContents;

fn analysis_for(root: &std::path::Path) -> Analysis {
    ensure_stdlib_env();
    let mut a = Analysis::new();
    a.ensure_project_for(&main_path(root));
    // The editor "opens" Main — register its current text (idempotent with the
    // project load, but mirrors the real didOpen path).
    a.set_document(main_url(root), main_text());
    a
}

fn hover(a: &Analysis, root: &std::path::Path, needle: &str, plus: u32) -> String {
    let text = main_text();
    let pos = pos_in(&text, needle, plus);
    match a.hover(&main_url(root), pos) {
        Some(h) => match h.contents {
            HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    }
}

#[test]
fn ext_sky_annotated_ref_hovers_type() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    // `bar` (used at `greeting =\n bar`) is Foo's ANNOTATED export `: String`.
    let h = hover(&a, &root, "\n            bar", 13);
    assert!(
        h.contains("String") && !h.contains(": ?"),
        "annotated external-Sky ref must hover `String`, got: {h:?}"
    );
}

#[test]
fn ext_sky_unannotated_ref_hovers_inferred() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    // `Foo.baz` is UNANNOTATED (`baz n = n + 1`) — inferred `Int -> Int`.
    let h = hover(&a, &root, "Foo.baz", 4);
    assert!(
        h.contains("Int") && !h.contains(": ?"),
        "unannotated external-Sky ref must hover its INFERRED `Int -> Int` (never `?`), got: {h:?}"
    );
}

#[test]
fn ext_sky_qualified_completion_enumerates_exports() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    // Completion right after `Foo.` should list the dep's exports (bar, baz).
    let text = main_text();
    // Position just after the `.` in `Foo.baz`.
    let pos = pos_in(&text, "Foo.baz", 4);
    let items = a.completion(&main_url(&root), pos);
    let labels: Vec<String> = items.iter().map(|i| i.label.clone()).collect();
    assert!(
        labels.iter().any(|l| l == "Foo.bar") && labels.iter().any(|l| l == "Foo.baz"),
        "qualified completion on an external-Sky dep must enumerate its exports, got: {labels:?}"
    );
}

#[test]
fn ext_sky_no_spurious_diagnostics() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    let diags = a.diagnostics(&main_url(&root));
    assert!(
        diags.is_empty(),
        "a project with VALID external refs (Sky dep + Go-FFI alias) must have EMPTY \
         diagnostics — matching `sky build` acceptance; got: {diags:#?}"
    );
}

#[test]
fn skydeps_decoy_main_is_dropped() {
    // The dep ships `DepMain.sky` (`module Main`). It must NOT shadow the
    // project's own `Main` — hovering `main` in the project's Main resolves the
    // project entry, and diagnostics stay clean (no duplicate-module collision).
    let root = build_fixture(true);
    let a = analysis_for(&root);
    let diags = a.diagnostics(&main_url(&root));
    assert!(
        diags.is_empty(),
        "the dep's decoy `module Main` must be dropped, not collide with the project Main; got: {diags:#?}"
    );
    // And the project's Main still resolves its own body (a real hover, not `?`).
    let h = hover(&a, &root, "main =", 0);
    assert!(
        !h.is_empty(),
        "project `main` must still resolve after the decoy drop"
    );
}

#[test]
fn src_overrides_dep_same_name() {
    // Load order: `.skydeps/` FIRST, then `src/`. A project `src/Foo.sky` must
    // WIN over the dep's `Foo.sky` (in-place `set_document` update keeps the
    // index; src loads last → overrides).
    let root = build_fixture(true);
    // Add a project-local Foo that shadows the dep, exporting a DIFFERENT `bar`.
    std::fs::write(
        root.join("src/Foo.sky"),
        "module Foo exposing (bar, baz)\n\nbar : Int\nbar = 99\n\nbaz n = n + 1\n",
    )
    .unwrap();
    let a = analysis_for(&root);
    // `bar` now resolves to the PROJECT Foo (`: Int`), not the dep's (`: String`).
    let h = hover(&a, &root, "\n            bar", 13);
    assert!(
        h.contains("Int") && !h.contains("String"),
        "project src/Foo.sky must OVERRIDE the same-named dep module (load order), got: {h:?}"
    );
}

#[test]
fn unfetched_dep_emits_hint_not_error() {
    // `foo` is declared in sky.toml [dependencies] but NOT fetched (no
    // `.skydeps/foo`). The import of `Foo` must surface an INFO hint pointing at
    // `sky install`, NOT a bare `[E1001]`.
    let root = build_fixture(false); // foo NOT materialised
    let a = analysis_for(&root);
    let diags = a.diagnostics(&main_url(&root));
    let hint = diags.iter().find(|d| {
        d.message.contains("sky install") || d.message.to_lowercase().contains("not fetched")
    });
    assert!(
        hint.is_some(),
        "an unfetched declared Sky dep must emit an actionable hint; diags: {diags:#?}"
    );
    let hint = hint.unwrap();
    assert_eq!(
        hint.severity,
        Some(tower_lsp::lsp_types::DiagnosticSeverity::INFORMATION),
        "the unfetched-dep hint must be INFO severity, not an error"
    );
    // And it must NOT be a hard E1001 import-collision error.
    assert!(
        !diags.iter().any(|d| matches!(
            &d.code,
            Some(tower_lsp::lsp_types::NumberOrString::String(c)) if c == "E1001"
        )),
        "unfetched dep must not raise E1001; diags: {diags:#?}"
    );
}

#[test]
fn invalidation_reload_project_refreshes() {
    // Start with `foo` UNfetched → the import hints "not fetched". Then
    // `sky install` lands the dep on disk; `reload_project` (the explicit
    // invalidation entry point) must rescan so the refs resolve with NO leftover
    // hint and the dep's export hovers its real type.
    let root = build_fixture(false);
    let mut a = analysis_for(&root);
    let before = a.diagnostics(&main_url(&root));
    assert!(
        before.iter().any(|d| d.message.contains("sky install")),
        "precondition: unfetched dep should hint; got {before:#?}"
    );

    materialise_foo(&root); // simulate `sky install`
    a.reload_project(&root);

    let after = a.diagnostics(&main_url(&root));
    assert!(
        !after.iter().any(|d| d.message.contains("sky install")),
        "after the dep is fetched + reload_project, the unfetched hint must clear; got {after:#?}"
    );
    let h = hover(&a, &root, "\n            bar", 13);
    assert!(
        h.contains("String"),
        "after reload, the external-Sky ref must hover its type; got {h:?}"
    );
}

#[test]
fn invalidation_mtime_auto_rescans() {
    // The mtime fallback: materialising the dep advances `.skydeps/`'s newest
    // mtime past the snapshot taken at first load, so a plain
    // `ensure_project_for` (the per-request hook) auto-reloads — no explicit
    // reload_project call needed.
    let root = build_fixture(false);
    let mut a = analysis_for(&root);
    assert!(
        a.diagnostics(&main_url(&root))
            .iter()
            .any(|d| d.message.contains("sky install")),
        "precondition: unfetched dep should hint"
    );

    materialise_foo(&root); // creates .skydeps/ (newer than the load snapshot)
    a.ensure_project_for(&main_path(&root)); // mtime advanced → auto reload

    assert!(
        !a.diagnostics(&main_url(&root))
            .iter()
            .any(|d| d.message.contains("sky install")),
        "the mtime fallback must auto-rescan after `sky install`"
    );
}
