//! Build + spawn machinery for the bundled Sky apps that back the `console`,
//! `console-serve`, and `doc --serve` / `doc --tui` verbs.
//!
//! Each bundled verb (except the pure-Go hub) mirrors the Haskell driver's
//! materialise-then-spawn pattern (`app/Main.hs` `runDocServe` / `runConsole`):
//! copy the bundled app's Sky source into a per-version cache dir, build it once
//! with the shared `project` build driver, then foreground the produced binary
//! with the right env. Building into `~/.cache/sky/…` keeps the committed
//! `sky-bundled/*/sky-out` untouched.
//!
//! Asset resolution: the bundled source is read from `<root>/sky-bundled/<name>`
//! where `root` is whatever [`project::assets_root_for`] resolved at the call
//! site — the repo tree in dev (byte-identical bring-up), else the extracted
//! embedded asset root ([`ffi::extract_assets_root`]) when running standalone.
//! `sky-bundled/` is embedded in the binary alongside `sky-stdlib` /
//! `runtime-go` / `tools` / `templates`, so [`bundled_src_dir`] finds the
//! bundled source under the extracted root and these verbs work from any
//! directory. (`console-serve`'s hub builds directly against the same root's
//! embedded `runtime-go/cmd/sky-hub`.)

use std::path::{Path, PathBuf};

use project::{build_example, BuildOptions};

/// The two entry-file basenames a bundled app ships: the Sky.Live/Http server
/// variant and the Sky.Tui variant. Both declare `module Main`, so a build must
/// see exactly one of them — [`materialise`] drops the non-target file.
pub const ENTRY_LIVE: &str = "Main.sky";
pub const ENTRY_TUI: &str = "MainTui.sky";

/// `~/.cache/sky` (or `$XDG_CACHE_HOME/sky`), the per-user cache root the
/// bundled builds materialise into. Mirrors the Haskell `getXdgDirectory
/// XdgCache "sky"` location.
pub fn cache_root() -> PathBuf {
    if let Ok(xdg) = std::env::var("XDG_CACHE_HOME") {
        if !xdg.is_empty() {
            return PathBuf::from(xdg).join("sky");
        }
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".to_string());
    PathBuf::from(home).join(".cache").join("sky")
}

/// The source dir for bundled app `name` (`console` / `doc`) under `repo_root`
/// (the [`project::assets_root_for`]-resolved root — repo tree in dev, extracted
/// embedded root standalone), if it exists with a `src/`. Returns `None` only
/// when the resolved root somehow lacks the bundled source (should not happen
/// once `sky-bundled/` is embedded + extracted).
pub fn bundled_src_dir(repo_root: &Path, name: &str) -> Option<PathBuf> {
    let dir = repo_root.join("sky-bundled").join(name);
    if dir.join("src").is_dir() {
        Some(dir)
    } else {
        None
    }
}

/// Ensure the bundled app `name`'s `keep_entry` variant is built, returning the
/// output dir holding the `app` binary. `variant` disambiguates the cache dir so
/// the Live and Tui builds of the same app coexist (each is its own single-
/// `module Main` project). Idempotent: an existing `app` binary short-circuits
/// the copy + `go build` (matching the Haskell one-build-per-version cache).
pub fn ensure_built(
    repo_root: &Path,
    src_dir: &Path,
    name: &str,
    variant: &str,
    keep_entry: &str,
    version_slug: &str,
) -> Result<PathBuf, String> {
    let app_dir = cache_root().join(format!("{name}-{variant}-{version_slug}"));
    let out_dir = app_dir.join("sky-out");
    let bin = out_dir.join("app");
    if bin.is_file() {
        return Ok(out_dir);
    }

    materialise(src_dir, &app_dir, keep_entry)
        .map_err(|e| format!("sky {name}: could not materialise bundled source: {e}"))?;

    println!(
        "sky {name}: building bundled app (one-time per version, into {})...",
        app_dir.display()
    );
    let opts = BuildOptions {
        repo_root: repo_root.to_path_buf(),
        example_dir: app_dir.clone(),
        out_dir_name: "sky-out".to_string(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        return Err(format!("sky {name}: {}", report.note));
    }
    if !report.go_build_ok {
        return Err(format!(
            "sky {name}: go build failed for the bundled app:\n{}",
            report.go_build_stderr
        ));
    }
    if !bin.is_file() {
        return Err(format!(
            "sky {name}: build reported success but {} is missing",
            bin.display()
        ));
    }
    Ok(out_dir)
}

/// Copy `src_dir`'s `sky.toml` + `src/` tree into `app_dir`, dropping the entry
/// file that is NOT `keep_entry` (the two `module Main` variants can't coexist
/// in one build). Deterministic + additive: a fresh copy each materialise so the
/// cache reflects the current bundled source.
fn materialise(src_dir: &Path, app_dir: &Path, keep_entry: &str) -> std::io::Result<()> {
    // Start clean so a prior partial materialise can't leave a stale entry file.
    let dst_src = app_dir.join("src");
    if dst_src.exists() {
        std::fs::remove_dir_all(&dst_src)?;
    }
    std::fs::create_dir_all(&dst_src)?;

    let drop_entry = if keep_entry == ENTRY_LIVE {
        ENTRY_TUI
    } else {
        ENTRY_LIVE
    };
    copy_src(&src_dir.join("src"), &dst_src, drop_entry)?;

    // Carry sky.toml (the `[live] port`, `[source]`, dep pins the build reads).
    let toml = src_dir.join("sky.toml");
    if toml.is_file() {
        std::fs::copy(&toml, app_dir.join("sky.toml"))?;
    }
    Ok(())
}

/// Recursively copy `*.sky` (and any nested dirs) from `from` to `to`, skipping
/// generated trees and the `drop_entry` file at the source root. `drop_entry` is
/// matched by top-level basename so a nested `MainTui.sky` (there is none today)
/// wouldn't be dropped by accident.
fn copy_src(from: &Path, to: &Path, drop_entry: &str) -> std::io::Result<()> {
    for entry in std::fs::read_dir(from)? {
        let entry = entry?;
        let path = entry.path();
        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        if matches!(
            name_str.as_ref(),
            "sky-out" | "sky-out-rust" | ".skycache" | ".skydeps"
        ) {
            continue;
        }
        if path.is_dir() {
            let sub = to.join(&name);
            std::fs::create_dir_all(&sub)?;
            // Nested dirs never carry a root entry file — pass an empty sentinel.
            copy_src(&path, &sub, "")?;
        } else if name_str == drop_entry {
            continue;
        } else {
            std::fs::copy(&path, to.join(&name))?;
        }
    }
    Ok(())
}
