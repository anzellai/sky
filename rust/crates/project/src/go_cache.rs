// go_cache.rs — Sky-managed Go build cache (compile-speed Phase 1).
//
// `go build` stores compiled package objects in its build cache (GOCACHE). By
// default that is the shared machine-global cache (~/Library/Caches/go-build on
// macOS, ~/.cache/go-build on Linux). Two problems for a Sky user:
//   * it grows without bound — the whole 157K-LOC `rt` package plus its heavy
//     external deps, once per GOOS/GOARCH, across every project — and can reach
//     tens of gigabytes, eating the user's disk;
//   * `sky upgrade` embeds a NEW `rt`, so the previously cached objects become
//     dead weight. (Correctness is never at risk — the cache is content-addressed,
//     so stale objects are simply never reused — but the disk is not reclaimed.)
//
// So Sky ISOLATES its build cache to `~/.sky/go-build`. That single decision lets
// Sky safely (a) CLEAN the cache when the embedded runtime fingerprint changes on
// an upgrade, and (b) BOUND its size — WITHOUT ever touching the build cache the
// user relies on for their other, non-Sky Go projects. When the user has set an
// explicit `GOCACHE`, Sky honours it and manages nothing (their cache, their
// rules).
//
// Everything here is BEST-EFFORT and FAIL-SAFE: any error (an unwritable dir, a
// missing `go`, a failed clean) leaves Go's normal behaviour intact and never
// fails a build. That is the property that keeps this from regressing any app.

use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::Duration;

const GOCACHE: &str = "GOCACHE";
const FP_STAMP: &str = ".sky-embed-fp";
const CAP_MARKER: &str = ".sky-cap-checked";
const DEFAULT_CAP_GB: u64 = 10;
/// Throttle the (directory-walking) size check to at most once per this window,
/// so an ordinary build never pays for a full cache walk.
const CAP_CHECK_EVERY: Duration = Duration::from_secs(600);

/// Sky's own build-cache dir, and the signal that SKY OWNS it (owns ⇒ Sky may
/// clean/cap it). `None` means either the user set an explicit `GOCACHE` (Sky
/// uses theirs and manages nothing) or no writable Sky dir is available (Sky sets
/// nothing and Go uses its default) — both are the "manage nothing" case.
fn owned_cache_dir() -> Option<PathBuf> {
    // An explicit, non-empty user GOCACHE is the escape hatch.
    if let Ok(v) = std::env::var(GOCACHE) {
        if !v.trim().is_empty() {
            return None;
        }
    }
    sky_cache_dir()
}

/// Resolve Sky's build-cache dir: `~/.sky/go-build`, falling back to a temp dir
/// when HOME is missing/unwritable. `None` only if nothing is writable.
fn sky_cache_dir() -> Option<PathBuf> {
    if let Ok(home) = std::env::var("HOME") {
        if !home.is_empty() {
            let d = PathBuf::from(&home).join(".sky").join("go-build");
            if std::fs::create_dir_all(&d).is_ok() {
                return Some(d);
            }
        }
    }
    let d = std::env::temp_dir().join("sky").join("go-build");
    if std::fs::create_dir_all(&d).is_ok() {
        return Some(d);
    }
    None
}

/// Apply Sky's Go build-cache env to a `go build` command: point `GOCACHE` at
/// Sky's isolated dir (unless the user set their own), plus keep the existing
/// constrained-HOME module-cache fallback (`GOPATH`). Best-effort — with no
/// writable Sky dir this sets nothing and Go uses its default cache.
pub(crate) fn apply(cmd: &mut Command) {
    if let Some(dir) = owned_cache_dir() {
        cmd.env(GOCACHE, &dir);
    }
    // Preserve the unwritable-HOME GOPATH/module-cache redirect; Sky's own
    // GOCACHE (above) takes precedence over that helper's GOCACHE entry.
    for (k, v) in ffi::inspect::go_env_for_constrained_home() {
        if k == GOCACHE {
            continue;
        }
        cmd.env(k, v);
    }
}

/// Maintain Sky's build cache before a build: clean it when the embedded runtime
/// fingerprint has changed since the last build (a `sky upgrade` or first run),
/// then bound its size. Sky-owned-only and best-effort — a user `GOCACHE` and a
/// non-writable environment are both left entirely alone.
pub(crate) fn maintain(embed_fingerprint: &str) {
    let Some(dir) = owned_cache_dir() else {
        return;
    };
    invalidate_if_stale(&dir, embed_fingerprint);
    enforce_cap(&dir);
}

/// Clean the Sky cache when the compiler's embedded fingerprint has changed. The
/// stamp records the fingerprint the cache was last populated for; a mismatch
/// means a new compiler (its `rt`/deps differ), so the old objects are dead
/// weight worth reclaiming. Correctness never depends on this — it is disk
/// hygiene + a clean slate for the new runtime.
fn invalidate_if_stale(dir: &Path, fp: &str) {
    let stamp = dir.join(FP_STAMP);
    if std::fs::read_to_string(&stamp).unwrap_or_default().trim() == fp {
        return;
    }
    clean(dir);
    let _ = std::fs::create_dir_all(dir);
    let _ = std::fs::write(&stamp, fp);
}

/// Bound the Sky cache size. The directory walk is throttled (CAP_CHECK_EVERY) so
/// an ordinary build never pays for it. Over the cap ⇒ clean; the fingerprint
/// stamp is preserved across the clean so a size-prune does not also trip a
/// fingerprint re-clean on the next build.
fn enforce_cap(dir: &Path) {
    let marker = dir.join(CAP_MARKER);
    if let Ok(m) = std::fs::metadata(&marker) {
        if let Ok(modified) = m.modified() {
            if modified.elapsed().map(|e| e < CAP_CHECK_EVERY).unwrap_or(false) {
                return;
            }
        }
    }
    let _ = std::fs::write(&marker, b"");

    let cap = cap_gb().saturating_mul(1024 * 1024 * 1024);
    if cap == 0 || dir_size_bytes(dir) <= cap {
        return;
    }
    let fp = std::fs::read_to_string(dir.join(FP_STAMP)).unwrap_or_default();
    clean(dir);
    let _ = std::fs::create_dir_all(dir);
    if !fp.trim().is_empty() {
        let _ = std::fs::write(dir.join(FP_STAMP), fp.trim());
    }
}

fn cap_gb() -> u64 {
    std::env::var("SKY_GO_CACHE_MAX_GB")
        .ok()
        .and_then(|v| v.trim().parse::<u64>().ok())
        .unwrap_or(DEFAULT_CAP_GB)
}

/// `go clean -cache` against the Sky cache dir only. Best-effort.
fn clean(dir: &Path) {
    let _ = Command::new("go")
        .args(["clean", "-cache"])
        .env(GOCACHE, dir)
        .status();
}

/// Sum of regular-file sizes under `dir`. Symlinks are not followed and errors
/// are skipped, so a permission hiccup or a loop cannot hang or panic. Bounded in
/// practice because the cache it walks is itself capped.
fn dir_size_bytes(dir: &Path) -> u64 {
    let mut total: u64 = 0;
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&d) else {
            continue;
        };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else {
                continue;
            };
            if ft.is_symlink() {
                continue;
            }
            if ft.is_dir() {
                stack.push(entry.path());
            } else if let Ok(m) = entry.metadata() {
                total = total.saturating_add(m.len());
            }
        }
    }
    total
}

/// Warm Sky's build cache by compiling the runtime `rt` (and its heavy external
/// deps) for BOTH native and js/wasm, so the first real build after a `sky
/// upgrade` — or on a fresh machine — is warm instead of a multi-minute cold
/// compile. Returns a human-readable note; never fails hard.
///
/// Note: this warms the BASE `rt` package and the shared external deps (pgx,
/// sqlite, otel, the Go stdlib for both targets). A project that adds FFI
/// bindings into `rt` gets a distinct `rt` content hash, so its `rt` object is
/// still compiled on first build — the deps + stdlib are warm regardless. Moving
/// FFI bindings out of `package rt` (a later phase) makes `rt` itself reusable.
pub fn prime() -> String {
    let root = match ffi::assets::extract_assets_root() {
        Ok(r) => r,
        Err(e) => return format!("Go cache prime skipped: {e}"),
    };
    let runtime_dir = root.join("runtime-go");
    if !runtime_dir.join("go.mod").exists() {
        return "Go cache prime skipped: embedded runtime has no go.mod".to_string();
    }
    // Record the fingerprint up front so the first real build does not re-clean
    // the cache we are about to warm.
    if let Some(dir) = owned_cache_dir() {
        let _ = std::fs::create_dir_all(&dir);
        let _ = std::fs::write(dir.join(FP_STAMP), ffi::assets::embed_fingerprint());
    }
    let targets: [(&str, Option<&str>, Option<&str>); 2] =
        [("native", None, None), ("wasm", Some("js"), Some("wasm"))];
    let mut notes = Vec::new();
    for (label, goos, goarch) in targets {
        let mut cmd = Command::new("go");
        cmd.args(["build", "./rt/..."]).current_dir(&runtime_dir);
        apply(&mut cmd);
        if let Some(os) = goos {
            cmd.env("GOOS", os);
        }
        if let Some(arch) = goarch {
            cmd.env("GOARCH", arch);
        }
        cmd.env("CGO_ENABLED", "0");
        cmd.env("GOFLAGS", "-mod=mod -buildvcs=false");
        match cmd.status() {
            Ok(s) if s.success() => notes.push(format!("{label} ✓")),
            Ok(_) => notes.push(format!("{label} (skipped: build tags)")),
            Err(e) => notes.push(format!("{label} error: {e}")),
        }
    }
    format!("Go cache primed for native + wasm ({})", notes.join(", "))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cap_gb_defaults_and_overrides() {
        // Default when unset (this test process does not set it).
        std::env::remove_var("SKY_GO_CACHE_MAX_GB");
        assert_eq!(cap_gb(), DEFAULT_CAP_GB);
    }

    #[test]
    fn dir_size_sums_files_and_skips_errors() {
        let d = std::env::temp_dir().join(format!("sky-gocache-size-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(d.join("sub")).unwrap();
        std::fs::write(d.join("a"), b"12345").unwrap();
        std::fs::write(d.join("sub").join("b"), b"678").unwrap();
        assert_eq!(dir_size_bytes(&d), 8);
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn owned_cache_dir_is_none_when_user_set_gocache() {
        let prev = std::env::var(GOCACHE).ok();
        std::env::set_var(GOCACHE, "/tmp/user-gocache");
        assert!(owned_cache_dir().is_none());
        match prev {
            Some(v) => std::env::set_var(GOCACHE, v),
            None => std::env::remove_var(GOCACHE),
        }
    }
}
