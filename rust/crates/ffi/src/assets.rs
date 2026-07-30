//! Embedded compiler assets — the stdlib, Go runtime, and FFI inspector source
//! trees baked into the `sky` binary so it runs standalone, outside the repo
//! tree (doc 09 §E).
//!
//! The trees are staged (filtered) by `build.rs` into `$OUT_DIR/embedded-assets`
//! and embedded here via `include_dir!`. [`extract_assets_root`] materialises
//! them once into a content-hashed cache dir laid out **identically to the repo
//! root** (`sky-stdlib/`, `runtime-go/`, `tools/sky-ffi-inspect/`, `templates/`),
//! so every consumer that already takes a `repo_root: &Path` — the build driver,
//! `sky doc`, [`crate::ensure_inspector`] — works against the extracted copy
//! with no change. The repo-tree path (dev) is preferred by the caller
//! ([`project::assets_root_for`]); this is the fallback for a shipped binary.
//!
//! Determinism (L4): the embedded tree is walked in sorted path order for the
//! cache key, and `include_dir!` payloads are stable for a given build.

use include_dir::{include_dir, Dir};
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

/// Content fingerprint of the embedded tree, emitted by `build.rs` as a
/// `rustc-env`. Referencing `env!("SKY_EMBED_FINGERPRINT")` bakes the value into
/// this crate's compile command, so a changed fingerprint (any edit to an
/// embedded stdlib/runtime/template file) forces recompilation — and thus a fresh
/// `include_dir!` below — instead of leaving a re-staged tree embedded stale until
/// `cargo clean -p ffi` (`include_dir!` does not register its files as cargo deps).
pub fn embed_fingerprint() -> &'static str {
    env!("SKY_EMBED_FINGERPRINT")
}

/// The staged asset trees, embedded at compile time. Root entries are
/// `sky-stdlib/`, `runtime-go/`, `tools/`, `templates/` (see `build.rs`).
static EMBEDDED: Dir<'static> = include_dir!("$OUT_DIR/embedded-assets");

/// The whole embedded asset tree (doc 09 §G — `embedded_runtime`/`embedded_stdlib`
/// are views into this).
pub fn embedded_assets() -> &'static Dir<'static> {
    &EMBEDDED
}

/// The embedded `sky-stdlib/` subtree, if present.
pub fn embedded_stdlib() -> Option<&'static Dir<'static>> {
    EMBEDDED.get_dir("sky-stdlib")
}

/// The embedded `runtime-go/` subtree, if present.
pub fn embedded_runtime() -> Option<&'static Dir<'static>> {
    EMBEDDED.get_dir("runtime-go")
}

/// The embedded `sky-bundled/` subtree (the `console` + `doc` bundled Sky apps
/// `sky console` / `sky doc --serve` / `sky doc --tui` build + spawn), if present.
pub fn embedded_bundled() -> Option<&'static Dir<'static>> {
    EMBEDDED.get_dir("sky-bundled")
}

/// Materialise the embedded asset trees into a content-hashed cache dir and
/// return it — a synthetic "repo root" carrying `sky-stdlib/`, `runtime-go/`,
/// `tools/sky-ffi-inspect/`, and `templates/` in the same layout the repo tree
/// has. Extract-once: a completed extraction is detected by a marker file and
/// reused (O(stat)); the content hash keys the dir so a `sky upgrade` that
/// changes the embedded bytes lands in a fresh dir automatically.
///
/// Mirrors [`crate::ensure_inspector`]'s content-hashed cache discipline; the
/// cache root is the same `xdg_cache_sky()` base (`$XDG_CACHE_HOME/sky`, else
/// `~/.cache/sky`).
pub fn extract_assets_root() -> Result<PathBuf, String> {
    let hash = assets_hash();
    let root = crate::inspect::xdg_cache_sky().join("assets").join(hash);
    let marker = root.join(MARKER);
    if marker.is_file() {
        return Ok(root);
    }
    if let Some(parent) = root.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("mkdir {}: {e}", parent.display()))?;
    }
    // Extract into a unique sibling temp dir, then atomically rename into place —
    // so a concurrent `sky` invocation never observes a half-written root.
    let tmp = root.with_file_name(format!("{}.tmp-{}-{}", hash, std::process::id(), nanos()));
    let _ = std::fs::remove_dir_all(&tmp);
    std::fs::create_dir_all(&tmp).map_err(|e| format!("mkdir {}: {e}", tmp.display()))?;
    EMBEDDED
        .extract(&tmp)
        .map_err(|e| format!("extract embedded assets to {}: {e}", tmp.display()))?;
    std::fs::write(tmp.join(MARKER), hash)
        .map_err(|e| format!("write marker in {}: {e}", tmp.display()))?;
    match std::fs::rename(&tmp, &root) {
        Ok(()) => Ok(root),
        Err(e) => {
            // Lost a race (another process finished first) → reuse its copy.
            let _ = std::fs::remove_dir_all(&tmp);
            if root.join(MARKER).is_file() {
                Ok(root)
            } else {
                Err(format!("finalise assets dir {}: {e}", root.display()))
            }
        }
    }
}

const MARKER: &str = ".sky-assets-complete";

/// Stable content hash (FNV-1a, 64-bit) over the embedded tree walked in sorted
/// path order — the cache key. Computed once and memoised. Std-only: the key
/// only needs to change when the embedded bytes change and be reproducible
/// across runs, which FNV-1a satisfies (matches `inspect::content_hash`).
fn assets_hash() -> &'static str {
    static H: OnceLock<String> = OnceLock::new();
    H.get_or_init(|| {
        let mut files: Vec<(String, &'static [u8])> = Vec::new();
        collect(&EMBEDDED, &mut files);
        files.sort_by(|a, b| a.0.cmp(&b.0));
        const OFFSET: u64 = 0xcbf2_9ce4_8422_2325;
        const PRIME: u64 = 0x0000_0100_0000_01b3;
        let mut h = OFFSET;
        let mut mix = |bytes: &[u8]| {
            for &b in bytes {
                h ^= b as u64;
                h = h.wrapping_mul(PRIME);
            }
        };
        for (path, bytes) in &files {
            mix(path.as_bytes());
            mix(&[0]);
            mix(bytes);
            mix(&[0]);
        }
        format!("{h:016x}")
    })
}

/// Recursively collect `(path, bytes)` for every embedded file.
fn collect(dir: &'static Dir<'static>, out: &mut Vec<(String, &'static [u8])>) {
    for f in dir.files() {
        out.push((f.path().to_string_lossy().into_owned(), f.contents()));
    }
    for d in dir.dirs() {
        collect(d, out);
    }
}

fn nanos() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0)
}

/// Materialise just the embedded `templates/` tree's `CLAUDE.md` into `dst`, if
/// present. Returns whether a file was written. Used by `sky init` so the
/// project scaffold ships a CLAUDE.md even outside the repo tree.
pub fn extract_template(name: &str, dst: &Path) -> bool {
    let Some(dir) = EMBEDDED.get_dir("templates") else {
        return false;
    };
    let Some(file) = dir.get_file(format!("templates/{name}")) else {
        return false;
    };
    std::fs::write(dst, file.contents()).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn embedded_trees_present() {
        assert!(embedded_stdlib().is_some(), "sky-stdlib must be embedded");
        assert!(embedded_runtime().is_some(), "runtime-go must be embedded");
        assert!(
            EMBEDDED.get_dir("tools/sky-ffi-inspect").is_some(),
            "inspector source must be embedded"
        );
    }

    #[test]
    fn stdlib_has_basics() {
        let std = embedded_stdlib().expect("stdlib embedded");
        // A canonical stdlib module must be present + non-empty.
        let basics = std
            .get_file("sky-stdlib/Sky/Core/Basics.sky")
            .expect("Basics.sky embedded");
        assert!(!basics.contents().is_empty());
    }

    #[test]
    fn runtime_has_gomod_and_rt() {
        let rt = embedded_runtime().expect("runtime embedded");
        assert!(
            rt.get_file("runtime-go/go.mod").is_some(),
            "go.mod embedded"
        );
        assert!(rt.get_dir("runtime-go/rt").is_some(), "rt/ embedded");
    }

    #[test]
    fn runtime_has_sky_hub_cmd_and_console_app() {
        // `sky console-serve` builds `./cmd/sky-hub` against the extracted
        // runtime-go tree; `rt/hub` blank-imports `rt/console_app`. Both must be
        // present for the standalone hub build to succeed.
        let rt = embedded_runtime().expect("runtime embedded");
        assert!(
            rt.get_file("runtime-go/cmd/sky-hub/main.go").is_some(),
            "cmd/sky-hub/main.go embedded"
        );
        assert!(
            rt.get_dir("runtime-go/rt/console_app").is_some(),
            "rt/console_app embedded (rt/hub blank-imports it)"
        );
        assert!(
            rt.get_dir("runtime-go/rt/hub").is_some(),
            "rt/hub embedded (sky-hub imports it)"
        );
    }

    #[test]
    fn bundled_apps_embedded_source_only() {
        let b = embedded_bundled().expect("sky-bundled embedded");
        // Both bundled apps' entry source + sky.toml present.
        for name in ["console", "doc"] {
            assert!(
                b.get_file(format!("sky-bundled/{name}/src/Main.sky"))
                    .is_some(),
                "sky-bundled/{name}/src/Main.sky embedded"
            );
            assert!(
                b.get_file(format!("sky-bundled/{name}/sky.toml")).is_some(),
                "sky-bundled/{name}/sky.toml embedded"
            );
        }
        // The committed build artefacts must NOT leak into the embed.
        let mut files = Vec::new();
        collect(&EMBEDDED, &mut files);
        for (p, _) in &files {
            assert!(
                !p.contains("sky-bundled/")
                    || (!p.contains("/sky-out/") && !p.contains("/.skycache/")),
                "bundled build artefact leaked into embed: {p}"
            );
        }
    }

    #[test]
    fn no_test_files_or_prebuilt_binary_embedded() {
        let mut files = Vec::new();
        collect(&EMBEDDED, &mut files);
        for (p, _) in &files {
            assert!(!p.ends_with("_test.go"), "test file leaked into embed: {p}");
            assert!(
                !p.ends_with("sky-ffi-inspect"),
                "prebuilt inspector binary leaked into embed: {p}"
            );
        }
    }

    #[test]
    fn assets_hash_is_stable() {
        assert_eq!(assets_hash(), assets_hash());
        assert_eq!(assets_hash().len(), 16);
    }
}
