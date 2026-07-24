//! Phase B — Go-FFI surface resolution in the LSP.
//!
//! The BUILD path's FFI surface (`project::load_ffi_surface`) is loaded into the
//! engine, so a `Uuid.newString` reference hovers its pinned HM signature
//! (never `?`) and `Uuid.` completion enumerates the package's symbols — with
//! the `skyType` as detail. Reuses the same `extdeps` fixture (real
//! github.com/google/uuid surface) as Phase A.

mod common;

use common::*;
use sky_lsp::Analysis;
use tower_lsp::lsp_types::HoverContents;

fn analysis_for(root: &std::path::Path) -> Analysis {
    ensure_stdlib_env();
    let mut a = Analysis::new();
    a.ensure_project_for(&main_path(root));
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
fn ffi_ref_hovers_pinned_sky_type() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    // `Uuid.newString` — a Go-FFI symbol pinned as `() -> Result Error String`.
    let h = hover(&a, &root, "Uuid.newString", 6);
    assert!(
        h.contains("Result Error String") && !h.contains(": ?"),
        "a Go-FFI ref must hover its pinned skyType (never `?`), got: {h:?}"
    );
}

#[test]
fn ffi_member_completion_enumerates_symbols() {
    let root = build_fixture(true);
    let a = analysis_for(&root);
    // Completion after `Uuid.` should enumerate the package's pinned symbols.
    let text = main_text();
    let pos = pos_in(&text, "Uuid.newString", 5); // just after the dot
    let items = a.completion(&main_url(&root), pos);
    let labels: Vec<String> = items.iter().map(|i| i.label.clone()).collect();
    assert!(
        labels.iter().any(|l| l == "Uuid.newString"),
        "FFI member completion must enumerate `Uuid.newString`, got {} items: {labels:?}",
        labels.len()
    );
    assert!(
        labels.len() > 20,
        "FFI member completion must list the full symbol set, got only {}",
        labels.len()
    );
    // The pinned skyType rides along as `detail`.
    let ns = items.iter().find(|i| i.label == "Uuid.newString").unwrap();
    assert_eq!(
        ns.detail.as_deref(),
        Some("() -> Result Error String"),
        "FFI completion detail must carry the pinned skyType"
    );
    // Insert text is the BARE name (no doubled `Uuid.` after the dot).
    assert_eq!(ns.insert_text.as_deref(), Some("newString"));
}

#[test]
fn ffi_empty_sky_type_falls_back_not_blank() {
    // `emptyTypeProbe` is a pinned symbol with an OMITTED skyType (defaults to
    // ""). The is_empty guard must make hover fall back to `?` rather than
    // rendering an empty type — and completion must OMIT its detail (not `""`).
    let root = build_fixture(true);
    let a = analysis_for(&root);

    // Completion detail for the empty-skyType symbol is None, not Some("").
    let text = main_text();
    let pos = pos_in(&text, "Uuid.newString", 5);
    let items = a.completion(&main_url(&root), pos);
    let probe = items
        .iter()
        .find(|i| i.label == "Uuid.emptyTypeProbe")
        .expect("empty-skyType symbol must still be enumerated in completion");
    assert_eq!(
        probe.detail, None,
        "an empty skyType must yield NO detail (never a blank string)"
    );
}

#[test]
fn ffi_hover_matches_build_surface_verbatim() {
    // The rendered signature must be the pinned skyType VERBATIM (no @package
    // stripping / reformatting) — i.e. exactly what the build path's FfiRegistry
    // holds. Assert the hover body contains the pinned string as-is.
    let root = build_fixture(true);
    let a = analysis_for(&root);
    let reg = project::load_ffi_surface(&root);
    let pinned = reg
        .resolve("Github.Com.Google.Uuid")
        .and_then(|p| p.functions.get("newString"))
        .map(|f| f.sky_type.clone())
        .expect("fixture surface must pin newString");
    let h = hover(&a, &root, "Uuid.newString", 6);
    assert!(
        h.contains(&pinned),
        "LSP FFI hover must render the pinned skyType verbatim ({pinned:?}); got {h:?}"
    );
}
