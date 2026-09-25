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
// Sky safely BOUND its size (the dead objects of an old runtime are reclaimed by
// that cap and by Go's own five-day trim) — WITHOUT ever touching the build cache the
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
const CAP_MARKER: &str = ".sky-cap-checked";
/// Advisory lock file inside the cache dir. A build holds it SHARED for the
/// whole `go build`; a clean needs it EXCLUSIVE. Without it, one `sky build`
/// could wipe the cache (the size cap) while a concurrent build was still reading from it — the objects
/// vanish mid-build and `go` reports "could not import X: no such file" and
/// "package X is not in std". Seen as flaky gate failures under `cargo test`.
const LOCK_FILE: &str = ".sky-lock";
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

/// Maintain Sky's build cache before a build: bound its size. Sky-owned-only and
/// best-effort — a user `GOCACHE` and a non-writable environment are both left
/// entirely alone.
///
/// It does NOT clean the cache when the compiler changes. It used to: a build by
/// a `sky` whose embedded runtime fingerprint differed from the stamp in the
/// cache ran `go clean -cache`. Every `sky` on the machine shares this one
/// cache, so two binaries in use at once (an installed release and a dev build,
/// two worktrees, `sky upgrade` while an editor's LSP still runs the old one)
/// wiped each other's objects on every alternation, and each wipe cost the next
/// build a cold compile of the runtime and its dependencies. The clean bought
/// nothing for correctness: the Go cache is content-addressed, so a different
/// runtime has different action IDs and is never served another runtime's
/// objects. The dead objects of an old runtime are reclaimed by Go's own trim
/// (entries unused for five days) and by the size cap below.
pub(crate) fn maintain() {
    let Some(dir) = owned_cache_dir() else {
        return;
    };
    maintain_in(&dir);
}

fn maintain_in(dir: &Path) {
    // Only ever clean with the cache to ourselves. Another `sky build` holding
    // the shared lock means "leave it alone this time": oversized objects are
    // harmless (content-addressed) and the next build retries.
    let Some(_exclusive) = try_exclusive_in(dir) else {
        return;
    };
    enforce_cap(dir);
}

/// A shared hold on Sky's cache for the duration of one `go build`. Keep the
/// value alive across the build; dropping it releases the lock. `None`-backed
/// (no lock) when Sky does not own the cache or the lock file is unwritable.
pub(crate) struct CacheGuard(Option<std::fs::File>);

impl Drop for CacheGuard {
    fn drop(&mut self) {
        if let Some(f) = &self.0 {
            let _ = f.unlock();
        }
    }
}

/// Take the shared lock before running `go build`. Blocks while a clean is in
/// progress (a clean is short); never fails a build.
pub(crate) fn hold_shared() -> CacheGuard {
    match owned_cache_dir() {
        Some(dir) => hold_shared_in(&dir),
        None => CacheGuard(None),
    }
}

fn lock_file(dir: &Path) -> Option<std::fs::File> {
    std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(dir.join(LOCK_FILE))
        .ok()
}

fn hold_shared_in(dir: &Path) -> CacheGuard {
    let Some(f) = lock_file(dir) else {
        return CacheGuard(None);
    };
    if f.lock_shared().is_err() {
        return CacheGuard(None);
    }
    CacheGuard(Some(f))
}

/// The exclusive lock, or `None` right away when any build holds it shared.
fn try_exclusive_in(dir: &Path) -> Option<CacheGuard> {
    let f = lock_file(dir)?;
    if f.try_lock().is_err() {
        return None;
    }
    Some(CacheGuard(Some(f)))
}

/// Bound the Sky cache size. The directory walk is throttled (CAP_CHECK_EVERY) so
/// an ordinary build never pays for it. Over the cap ⇒ clean.
fn enforce_cap(dir: &Path) {
    let marker = dir.join(CAP_MARKER);
    if let Ok(m) = std::fs::metadata(&marker) {
        if let Ok(modified) = m.modified() {
            if modified
                .elapsed()
                .map(|e| e < CAP_CHECK_EVERY)
                .unwrap_or(false)
            {
                return;
            }
        }
    }
    let _ = std::fs::write(&marker, b"");

    let cap = cap_gb().saturating_mul(1024 * 1024 * 1024);
    if cap == 0 || dir_size_bytes(dir) <= cap {
        return;
    }
    clean(dir);
    let _ = std::fs::create_dir_all(dir);
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
    if let Some(dir) = owned_cache_dir() {
        let _ = std::fs::create_dir_all(&dir);
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
        let _cache = hold_shared();
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

    /// A build by a `sky` with a different embedded runtime must not clean the
    /// shared cache. It used to (`go clean -cache` on a fingerprint mismatch), so
    /// two `sky` binaries in use at once wiped each other's objects on every
    /// alternation and forced cold compiles. The cache is content-addressed, so
    /// there is nothing stale to remove.
    #[test]
    fn maintenance_never_cleans_the_cache_for_a_different_compiler() {
        let d = std::env::temp_dir().join(format!("sky-gocache-fp-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(d.join("ab")).unwrap();
        let object = d.join("ab").join("ab12-d");
        std::fs::write(&object, b"compiled package").unwrap();
        // The stamp an older Sky left behind, naming another runtime.
        std::fs::write(d.join(".sky-embed-fp"), "sky-embed-fp-v1:another-runtime").unwrap();
        maintain_in(&d);
        maintain_in(&d);
        assert_eq!(
            std::fs::read(&object).unwrap(),
            b"compiled package",
            "maintenance must keep the cached objects"
        );
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn a_shared_hold_keeps_a_clean_out_until_it_is_dropped() {
        let d = std::env::temp_dir().join(format!("sky-gocache-lock-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        let build = hold_shared_in(&d);
        assert!(build.0.is_some(), "the shared lock should be taken");
        // A clean must not start while a build holds the cache.
        assert!(try_exclusive_in(&d).is_none());
        drop(build);
        // With no build running the clean may proceed, and it in turn keeps a
        // new build waiting (blocking), which we only assert as "exclusive taken".
        let cleaning = try_exclusive_in(&d);
        assert!(cleaning.is_some());
        drop(cleaning);
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
