//! CLI/testrunner driver glue over [`crate::build_example`] (doc 10 §"The full
//! CLI verb surface"). Path resolution + process exec that the `sky` binary and
//! the `testrunner` crate share. Purely additive over the directory-oriented
//! [`crate::build_example`] engine — the xtask gates call that same function and
//! are unaffected.

use std::path::{Path, PathBuf};
use std::process::{Command, ExitStatus};

/// Walk up from `start` (a file or dir) to the compiler repo root — the nearest
/// ancestor holding both `sky-stdlib/` and `runtime-go/` (the stdlib + Go
/// runtime the emit reads). Returns `None` if neither is found up to the fs
/// root. In the bring-up these assets live on disk under the repo; a shipped
/// binary would embed them (doc 09) and this resolver would be replaced by an
/// embedded-asset lookup.
pub fn repo_root_for(start: &Path) -> Option<PathBuf> {
    let start = start.canonicalize().unwrap_or_else(|_| start.to_path_buf());
    let mut dir: Option<&Path> = if start.is_dir() {
        Some(start.as_path())
    } else {
        start.parent()
    };
    while let Some(d) = dir {
        if d.join("sky-stdlib").is_dir() && d.join("runtime-go").is_dir() {
            return Some(d.to_path_buf());
        }
        dir = d.parent();
    }
    None
}

/// Resolve the compiler-asset root for a build/doc/ffi invocation starting at
/// `start` (a file or dir). Two-tier, dev-first (doc 09 §E):
///
/// 1. **Repo tree** — if [`repo_root_for`] finds `sky-stdlib/` + `runtime-go/`
///    above `start`, use it. This keeps the in-repo dev path (the xtask gates,
///    `examples/*`) byte-identical: assets read straight off disk, no extraction.
/// 2. **Embedded** — otherwise the binary is running standalone; materialise the
///    trees baked in at compile time ([`ffi::extract_assets_root`]) into a
///    content-hashed cache dir laid out identically to the repo root, and use
///    that. Extract-once, reused across invocations.
///
/// The returned path always carries `sky-stdlib/`, `runtime-go/`, and
/// `tools/sky-ffi-inspect/` in repo layout, so every downstream consumer that
/// takes a `repo_root: &Path` (the build driver, `sky doc`,
/// [`ffi::ensure_inspector`]) works against it unchanged.
pub fn assets_root_for(start: &Path) -> Option<PathBuf> {
    if let Some(root) = repo_root_for(start) {
        return Some(root);
    }
    match ffi::extract_assets_root() {
        Ok(root) => Some(root),
        Err(e) => {
            eprintln!("sky: could not materialise embedded compiler assets: {e}");
            None
        }
    }
}

/// Resolve the project directory owning `file` (an entry `.sky` under `src/`):
/// the nearest ancestor containing `sky.toml`, else the grandparent when the
/// file sits directly under a `src/` or `tests/` root, else the file's parent.
/// This is the directory [`crate::build_example`] treats as `example_dir`.
pub fn project_dir_for(file: &Path) -> PathBuf {
    let abs = file.canonicalize().unwrap_or_else(|_| file.to_path_buf());
    // nearest ancestor with a sky.toml
    let mut dir = abs.parent();
    while let Some(d) = dir {
        if d.join("sky.toml").is_file() {
            return d.to_path_buf();
        }
        dir = d.parent();
    }
    // no sky.toml — fall back to the src/tests-root convention.
    if let Some(parent) = abs.parent() {
        let is_root_dir = parent
            .file_name()
            .and_then(|n| n.to_str())
            .map(|n| n == "src" || n == "tests")
            .unwrap_or(false);
        if is_root_dir {
            if let Some(gp) = parent.parent() {
                return gp.to_path_buf();
            }
        }
        return parent.to_path_buf();
    }
    abs
}

/// True when `dir` is the Sky *compiler* repo root. Identified by the Rust
/// workspace plus the embedded stdlib/runtime source trees — a combination that
/// never occurs in a user project. `sky build` refuses to run here because its
/// output dir (`sky-out/`) would overwrite the oracle binary kept there.
pub fn is_compiler_repo_root(dir: &Path) -> bool {
    dir.join("rust").join("Cargo.toml").is_file()
        && dir.join("sky-stdlib").is_dir()
        && dir.join("runtime-go").is_dir()
}

/// Derive a Sky module name from a source path relative to one of `roots`
/// (`["src", "tests"]`), capitalising each segment's first letter. Port of
/// `moduleNameFromPathWithRoots` (`app/Main.hs:591`). `base` is the project dir
/// the roots are relative to. Returns `None` when the path is not a `.sky` file
/// under any root.
pub fn module_name_from_path(base: &Path, roots: &[&str], file: &Path) -> Option<String> {
    if file.extension().and_then(|e| e.to_str()) != Some("sky") {
        return None;
    }
    let abs = file.canonicalize().unwrap_or_else(|_| file.to_path_buf());
    let base = base.canonicalize().unwrap_or_else(|_| base.to_path_buf());
    for root in roots {
        let root_dir = if *root == "." || root.is_empty() {
            base.clone()
        } else {
            base.join(root)
        };
        let Ok(rel) = abs.strip_prefix(&root_dir) else {
            continue;
        };
        let stem = rel.with_extension("");
        let mut parts = Vec::new();
        for comp in stem.components() {
            let seg = comp.as_os_str().to_string_lossy();
            parts.push(cap_first(&seg));
        }
        if parts.is_empty() {
            return None;
        }
        return Some(parts.join("."));
    }
    None
}

/// The module name a `.sky` file DECLARES in its `module … exposing (…)` header.
///
/// This is the name the loader registers the module under (`build::module_name`),
/// so it — not the file's path — is the module's identity. Anything that needs to
/// `import` a file it was handed must use this, because a path-derived guess that
/// disagrees does not fail: `HirDb::classify_import` treats an unknown module as a
/// Go FFI package, and the reference silently lowers to `nil`.
///
/// Returns `None` for a non-`.sky` file, an unreadable file, or a file with no
/// (or an empty) module header.
pub fn declared_module_name(file: &Path) -> Option<String> {
    if file.extension().and_then(|e| e.to_str()) != Some("sky") {
        return None;
    }
    let src = std::fs::read_to_string(file).ok()?;
    let parse = syntax::parse(&src, base::FileId(0));
    let name = parse.tree().module_header()?.name()?.text();
    (!name.is_empty()).then_some(name)
}

/// The source ROOT a file must be loaded from for its declared module name to
/// resolve — the file's directory with one level stripped per dotted segment
/// beyond the last.
///
/// `tests/Std/UiMediaQueryTest.sky` declaring `Std.UiMediaQueryTest` has two
/// segments, so one directory (`Std`) belongs to the module name and the root is
/// `tests/`. A flat `FooTest` in `tests/Nested/` has one segment, so the root is
/// `tests/Nested/` itself. Returns `None` if the path is too shallow for the
/// declared name (a malformed pairing).
pub fn source_root_for_declared(file: &Path, declared: &str) -> Option<PathBuf> {
    let mut dir = file.parent()?.to_path_buf();
    for _ in 0..declared.split('.').count().saturating_sub(1) {
        dir = dir.parent()?.to_path_buf();
    }
    Some(dir)
}

fn cap_first(s: &str) -> String {
    let mut chars = s.chars();
    match chars.next() {
        Some(c) if c.is_ascii_lowercase() => c.to_ascii_uppercase().to_string() + chars.as_str(),
        Some(c) => c.to_string() + chars.as_str(),
        None => String::new(),
    }
}

/// Run the built binary at `<out_dir>/app` with inherited stdio, forwarding the
/// given env vars, and return its exit status (`sky run` / `sky test` / `sky db`
/// share this). The child inherits stdin/stdout/stderr so interactive CLIs and
/// test output surface directly; the exit code propagates for CI.
pub fn run_app(out_dir: &Path, envs: &[(String, String)]) -> std::io::Result<ExitStatus> {
    // The project root is the out dir's parent (`<project>/sky-out` ->
    // `<project>`) — the directory that holds THIS app's `sky.toml`. `resolve()`
    // walks up from the entry file to the NEAREST sky.toml, so a sub-app with its
    // own sky.toml (deeper than any monorepo root) roots at ITS OWN dir, while a
    // sub-app without one shares its parent's root. Either way this is exactly
    // where the app's relative paths (.env, data/, the sqlite store, static dirs)
    // are meant to resolve, and it is independent of where `sky run` was invoked.
    let project_dir = out_dir.parent().filter(|p| !p.as_os_str().is_empty());
    let bin_name = project_dir
        .map(crate::build::configured_bin_name)
        .unwrap_or_else(|| "app".to_string());
    // Absolute path to the built binary, so program resolution never depends on
    // the child's working directory. A relative program path combined with a
    // `current_dir` is platform-specific + unstable (see the std::process docs),
    // which is exactly the combination the old `Command::new("./app")` +
    // `current_dir(out_dir)` relied on.
    let bin_abs =
        std::fs::canonicalize(out_dir.join(&bin_name)).unwrap_or_else(|_| out_dir.join(&bin_name));
    let mut cmd = Command::new(&bin_abs);
    // Run from the PROJECT ROOT (the sky.toml dir), NOT sky-out/. The old
    // `current_dir(out_dir)` ran the app inside sky-out/, so every relative path
    // the app uses (.env, data/, the sqlite store path, static dirs) resolved
    // against sky-out/ and silently broke — e.g. the sqlite store reported
    // "unable to open database file" and fell back to the memory store. Canonical
    // absolute cwd so it's stable regardless of the invocation dir. Falls back to
    // the invocation dir, then out_dir, if the project dir can't be resolved.
    let cwd = project_dir
        .and_then(|p| std::fs::canonicalize(p).ok())
        .or_else(|| std::env::current_dir().ok())
        .unwrap_or_else(|| out_dir.to_path_buf());
    cmd.current_dir(&cwd);
    for (k, v) in envs {
        cmd.env(k, v);
    }
    cmd.status()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cap_first_capitalises() {
        assert_eq!(cap_first("core"), "Core");
        assert_eq!(cap_first("Main"), "Main");
        assert_eq!(cap_first(""), "");
    }
}
