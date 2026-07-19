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

/// True when `dir` is the Sky *compiler* repo root — it carries the unique
/// `sky-compiler.cabal` marker. `sky build` refuses to run here because its
/// output dir (`sky-out/`) would overwrite the compiler binary (task #662,
/// `app/Main.hs:1293`).
pub fn is_compiler_repo_root(dir: &Path) -> bool {
    dir.join("sky-compiler.cabal").is_file()
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
    let mut cmd = Command::new("./app");
    cmd.current_dir(out_dir);
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
