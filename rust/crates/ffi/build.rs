//! Build script for the `ffi` crate — stages the compiler asset trees into
//! `$OUT_DIR/embedded-assets/` so `src/assets.rs` can embed them with
//! `include_dir!` and the shipped `sky` binary is standalone (doc 09 §E).
//!
//! Why a filtered copy instead of `include_dir!` straight at the repo trees:
//!   * `tools/sky-ffi-inspect/` carries a committed 6.7 MB prebuilt binary — an
//!     *output*, not a source. Embedding it would bloat every `sky` binary and,
//!     because the binary changes per rebuild, break determinism. The filter
//!     drops it (mirrors `EmbeddedInspector.collectToolSources`).
//!   * `runtime-go/rt/` carries `*_test.go` + `testdata/`; stripping those at
//!     stage time keeps the embedded payload lean (doc 09 §E.3). `console_app/`
//!     IS embedded (unlike the normal user-app materialise, which skips it):
//!     `rt/hub` — built by `sky console-serve` via the embedded `cmd/sky-hub` —
//!     blank-imports `sky-app/rt/console_app`, so the standalone hub build needs
//!     it on disk.
//!   * `sky-bundled/` (the `console` + `doc` bundled Sky apps) is embedded
//!     source-only: its committed `sky-out/` / `.skycache/` / `.skydeps/` build
//!     artefacts are dropped so only `src/` + `sky.toml` ship.
//!
//! The `cargo:rerun-if-changed` lines below make Cargo re-stage whenever any
//! source tree changes — new files included by construction. This is the direct
//! replacement for the Haskell compiler's 89 hand-written "re-embed marker"
//! comments (doc 09 §E.2): embedding staleness becomes structurally impossible.

use std::path::{Path, PathBuf};

fn main() {
    let manifest = PathBuf::from(env("CARGO_MANIFEST_DIR"));
    // rust/crates/ffi -> rust/crates -> rust -> <repo root>
    let repo = manifest
        .ancestors()
        .nth(3)
        .expect("ffi crate must live at <repo>/rust/crates/ffi")
        .to_path_buf();
    let dest = PathBuf::from(env("OUT_DIR")).join("embedded-assets");

    // Fresh staging every run keyed off rerun-if-changed; a stale file left from
    // a since-deleted source must not survive into the embed.
    let _ = std::fs::remove_dir_all(&dest);
    std::fs::create_dir_all(&dest).expect("mkdir embedded-assets");

    // sky-stdlib/ — the .sky stdlib (read by the build driver + `sky doc`).
    stage(&repo.join("sky-stdlib"), &dest.join("sky-stdlib"));
    // runtime-go/ — go.mod + go.sum + the rt/ tree (materialised beside main.go)
    // + cmd/sky-hub (the pure-Go console hub `sky console-serve` builds).
    stage_runtime(&repo.join("runtime-go"), &dest.join("runtime-go"));
    // tools/sky-ffi-inspect/ — the Go introspector source (ensure_inspector).
    stage(
        &repo.join("tools").join("sky-ffi-inspect"),
        &dest.join("tools").join("sky-ffi-inspect"),
    );
    // templates/ — CLAUDE.md et al. (copied by `sky init`).
    stage(&repo.join("templates"), &dest.join("templates"));
    // sky-bundled/ — the console + doc bundled Sky apps `sky console` /
    // `sky doc --serve` / `sky doc --tui` build + spawn. Source only: the
    // committed `sky-out/` / `.skycache/` / `.skydeps/` build artefacts are
    // dropped by `skip_dir` so the embed carries just `src/` + `sky.toml`.
    stage(&repo.join("sky-bundled"), &dest.join("sky-bundled"));

    // Re-stage when any source tree changes (new files included).
    rerun(&repo.join("sky-stdlib"));
    rerun(&repo.join("runtime-go"));
    rerun(&repo.join("tools").join("sky-ffi-inspect"));
    rerun(&repo.join("templates"));
    rerun(&repo.join("sky-bundled"));
    // And when this script itself changes.
    rerun(&manifest.join("build.rs"));
}

fn env(k: &str) -> String {
    std::env::var(k).unwrap_or_else(|_| panic!("missing env var {k}"))
}

/// Emit `cargo:rerun-if-changed` for `p` AND, when it's a directory, every file
/// beneath it — recursively. A directory-only watch does NOT fire when a file's
/// CONTENT changes in place (Cargo compares the directory's own mtime, which an
/// in-place edit leaves untouched), so an edit to e.g. `runtime-go/rt/live.go`
/// would not re-stage the embedded runtime and the change would silently miss
/// the next `sky` binary. Watching each file closes that gap; over-watching a
/// staged-but-filtered file only costs a redundant re-stage, never a stale embed.
fn rerun(p: &Path) {
    println!("cargo:rerun-if-changed={}", p.display());
    if p.is_dir() {
        if let Ok(entries) = std::fs::read_dir(p) {
            for e in entries.flatten() {
                rerun(&e.path());
            }
        }
    }
}

/// Recursively copy `src` → `dst`, applying the shared non-embeddable filter
/// (mirrors `EmbedDirTH.isEmbeddableRuntimeFile/Dir`).
fn stage(src: &Path, dst: &Path) {
    if !src.is_dir() {
        return;
    }
    let mut entries: Vec<PathBuf> = std::fs::read_dir(src)
        .unwrap_or_else(|e| panic!("read {}: {e}", src.display()))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .collect();
    entries.sort();
    for path in entries {
        let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if path.is_dir() {
            if skip_dir(name) {
                continue;
            }
            stage(&path, &dst.join(name));
        } else if !skip_file(name) {
            copy_file(&path, &dst.join(name));
        }
    }
}

/// Stage `runtime-go/` selectively: `go.mod`, `go.sum`, the `rt/` tree (read by
/// the driver's write_out + ensure_go_mod), and `cmd/sky-hub/` (built directly
/// by `sky console-serve` — `go build ./cmd/sky-hub` against this tree). The
/// `rt/` stage includes `rt/console_app/` because `rt/hub` (which sky-hub
/// imports) blank-imports it, so a standalone hub build needs it present.
fn stage_runtime(src: &Path, dst: &Path) {
    std::fs::create_dir_all(dst).unwrap_or_else(|e| panic!("mkdir {}: {e}", dst.display()));
    for f in ["go.mod", "go.sum"] {
        let p = src.join(f);
        if p.is_file() {
            copy_file(&p, &dst.join(f));
        }
    }
    stage(&src.join("rt"), &dst.join("rt"));
    // cmd/ — the `sky-hub` daemon main package (`sky console-serve`).
    stage(&src.join("cmd"), &dst.join("cmd"));
}

fn copy_file(src: &Path, dst: &Path) {
    if let Some(parent) = dst.parent() {
        std::fs::create_dir_all(parent)
            .unwrap_or_else(|e| panic!("mkdir {}: {e}", parent.display()));
    }
    std::fs::copy(src, dst)
        .unwrap_or_else(|e| panic!("copy {} -> {}: {e}", src.display(), dst.display()));
}

/// Non-embeddable files: Go test files, the committed inspector binary, OS +
/// editor junk. Matches `EmbedDirTH.isEmbeddableRuntimeFile`.
fn skip_file(name: &str) -> bool {
    name.ends_with("_test.go")
        || name == "sky-ffi-inspect"
        || name == "sky-ffi-inspect.exe"
        || name == ".DS_Store"
        || name.ends_with(".bak")
        || name.ends_with(".swp")
        || name.ends_with('~')
}

/// Non-embeddable dirs: test fixtures, per-project caches, and committed build
/// outputs (`sky-out/` / `.skydeps/` under `sky-bundled/*`). Matches
/// `EmbedDirTH.isEmbeddableRuntimeDir`. NOTE: `console_app` is intentionally NOT
/// skipped — `rt/hub` (built by `sky console-serve` via `cmd/sky-hub`) blank-
/// imports `sky-app/rt/console_app`, so the standalone hub build needs it. The
/// normal user-app build re-materialises `rt/` beside `main.go` and skips
/// `console_app` there (see `project::build::materialise_rt`), so it never
/// bloats a user's `sky-out/`.
fn skip_dir(name: &str) -> bool {
    matches!(
        name,
        ".skycache" | ".skydeps" | "sky-out" | "testdata" | "node_modules" | ".git"
    )
}
