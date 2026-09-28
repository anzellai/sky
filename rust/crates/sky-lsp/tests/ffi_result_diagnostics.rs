//! The editor reports the same Go-FFI `Result` misuse `sky check` rejects, and
//! reads the Go-FFI surface of the project that owns the edited file.
//!
//! The checker types a Go-FFI reference from the salsa db's FFI surface
//! (`SkyDb::ffi_fn`). The LSP used to MERGE every loaded project's registry
//! into one map (`extend`), so a package pinned by one project leaked into
//! another's analysis. It now keys registries by project root and installs the
//! edited project's surface on the db before analysing it.

mod common;

use common::*;
use sky_lsp::Analysis;
use tower_lsp::lsp_types::{NumberOrString, Url};

const MISUSE: &str = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Github.Com.Google.Uuid as Uuid


probe : String
probe =
    Uuid.newString ()


main =
    println probe
";

fn has_code(a: &Analysis, url: &Url, code: &str) -> bool {
    a.diagnostics(url)
        .iter()
        .any(|d| d.code == Some(NumberOrString::String(code.to_string())))
}

/// A project with NO Go-FFI surface (no `sky-ffi/`), holding the same program.
fn bare_project() -> std::path::PathBuf {
    let root = std::env::temp_dir().join(format!(
        "sky-lsp-ffi-bare-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(root.join("src")).unwrap();
    std::fs::write(
        root.join("sky.toml"),
        "name = \"bare\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(root.join("src/Main.sky"), MISUSE).unwrap();
    root
}

#[test]
fn ffi_result_misuse_is_an_editor_diagnostic_per_project() {
    ensure_stdlib_env();
    let with_surface = build_fixture(true);
    std::fs::write(with_surface.join("src/Main.sky"), MISUSE).unwrap();
    let mut a = Analysis::new();

    // Project A pins `Uuid.newString : () -> Result Error String`.
    a.ensure_project_for(&main_path(&with_surface));
    a.set_document(main_url(&with_surface), MISUSE.to_string());
    assert!(
        has_code(&a, &main_url(&with_surface), "E2001"),
        "the pinned Result must be enforced in the editor: {:?}",
        a.diagnostics(&main_url(&with_surface))
    );

    // Project B has no surface for the package: its analysis must NOT borrow
    // project A's pinned signature.
    let bare = bare_project();
    let bare_url = Url::from_file_path(bare.join("src/Main.sky")).unwrap();
    a.ensure_project_for(&bare.join("src/Main.sky"));
    a.set_document(bare_url.clone(), MISUSE.to_string());
    assert!(
        !has_code(&a, &bare_url, "E2001"),
        "project B must not see project A's FFI surface: {:?}",
        a.diagnostics(&bare_url)
    );

    // Back to A: its surface is re-installed.
    a.activate_ffi_for(&main_path(&with_surface));
    a.set_document(main_url(&with_surface), MISUSE.to_string());
    assert!(
        has_code(&a, &main_url(&with_surface), "E2001"),
        "switching back must re-install project A's surface"
    );
    let _ = std::fs::remove_dir_all(&bare);
    let _ = std::fs::remove_dir_all(&with_surface);
}
