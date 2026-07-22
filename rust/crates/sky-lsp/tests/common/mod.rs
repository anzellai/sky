//! Shared fixture assembly for the external-dependency LSP tests.
//!
//! `.skydeps/` and `sky-ffi/` are gitignored, so the committed fixture data
//! lives as FLAT files under `tests/fixtures/extdeps/`; each test assembles a
//! real on-disk project layout in a unique temp dir at runtime (which also lets
//! the invalidation test mutate the tree). The FFI surface is the REAL
//! `github.com/google/uuid` kernel.json + wrapper (reused from an example), so
//! the pinned `skyType`s the LSP renders are exactly what `sky build` sees.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU32, Ordering};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn fixtures() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/extdeps")
}

/// Point the LSP's stdlib resolver at the on-disk `sky-stdlib/` so `String`,
/// `println`, the Prelude, … resolve for the temp-dir project (whose
/// `assets_root_for` walk can't reach the repo tree). Every test wants the same
/// value, so the process-global env set is race-free.
pub fn ensure_stdlib_env() {
    let dir = repo_root().join("sky-stdlib");
    std::env::set_var("SKY_STDLIB_DIR", dir);
}

static COUNTER: AtomicU32 = AtomicU32::new(0);

/// A freshly-assembled external-deps project in a unique temp dir. Returns the
/// project root. `with_foo_fetched` controls whether the `foo` Sky dep's
/// `.skydeps/foo/src/` tree is materialised (false → the unfetched-hint case).
pub fn build_fixture(with_foo_fetched: bool) -> PathBuf {
    let n = COUNTER.fetch_add(1, Ordering::SeqCst);
    let root = std::env::temp_dir().join(format!("sky-lsp-extdeps-{}-{n}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let fx = fixtures();

    std::fs::create_dir_all(root.join("src")).unwrap();
    std::fs::copy(fx.join("sky.toml"), root.join("sky.toml")).unwrap();
    std::fs::copy(fx.join("Main.sky"), root.join("src/Main.sky")).unwrap();

    // The Go-FFI surface (real uuid kernel.json + wrapper).
    std::fs::create_dir_all(root.join("sky-ffi/go")).unwrap();
    std::fs::copy(
        fx.join("uuid.kernel.json"),
        root.join("sky-ffi/uuid.kernel.json"),
    )
    .unwrap();
    std::fs::copy(
        fx.join("uuid_bindings.go"),
        root.join("sky-ffi/go/uuid_bindings.go"),
    )
    .unwrap();

    if with_foo_fetched {
        materialise_foo(&root);
    }
    root
}

/// Materialise the fetched `foo` Sky dependency: `Foo.sky` (the real module) +
/// `DepMain.sky` (the decoy `module Main` that must be dropped) under
/// `.skydeps/foo/src/`.
pub fn materialise_foo(root: &Path) {
    let fx = fixtures();
    let src = root.join(".skydeps/foo/src");
    std::fs::create_dir_all(&src).unwrap();
    std::fs::copy(fx.join("Foo.sky"), src.join("Foo.sky")).unwrap();
    std::fs::copy(fx.join("DepMain.sky"), src.join("DepMain.sky")).unwrap();
}

pub fn main_url(root: &Path) -> tower_lsp::lsp_types::Url {
    tower_lsp::lsp_types::Url::from_file_path(root.join("src/Main.sky")).unwrap()
}

pub fn main_path(root: &Path) -> PathBuf {
    root.join("src/Main.sky")
}

pub fn main_text() -> String {
    std::fs::read_to_string(fixtures().join("Main.sky")).unwrap()
}

/// The LSP `Position` (line, UTF-16 char) at byte offset `needle_start + plus`,
/// where `needle` is located inside `text`. Keeps hover targets robust to
/// reformatting of the fixture (no hand-counted columns).
pub fn pos_in(text: &str, needle: &str, plus: u32) -> tower_lsp::lsp_types::Position {
    let byte = text.find(needle).unwrap_or_else(|| panic!("needle {needle:?} not in fixture"))
        + plus as usize;
    let mut line = 0u32;
    let mut col = 0u32;
    for (i, ch) in text.char_indices() {
        if i >= byte {
            break;
        }
        if ch == '\n' {
            line += 1;
            col = 0;
        } else {
            col += ch.len_utf16() as u32;
        }
    }
    tower_lsp::lsp_types::Position { line, character: col }
}
