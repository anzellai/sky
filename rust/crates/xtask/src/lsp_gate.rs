//! `xtask lsp` — the Neovim editor-parity gate (doc 10's non-negotiable floor).
//!
//! Drives the REAL Neovim LSP client (`scripts/lsp-test-nvim.sh`) against the
//! Rust `sky lsp`. This catches editor-level bugs — label-vs-insertText,
//! filterText, scope handling — that the in-process JSON-RPC tests
//! (`sky-lsp/tests/*.rs`) miss.
//!
//! Two suites run under that one script:
//!   * the original 17 single-fixture cases (`lsp-test-nvim.lua`), one symbol
//!     class each; and
//!   * the corpus (`lsp-corpus-nvim.lua`) — cross-module resolution through
//!     every import shape, goto-def into `sky-stdlib`, `[E1012]`/`[E2008]`/
//!     `[E2007]` as an editor renders them, the broken-file usability cases,
//!     and the server driven over a REAL `examples/` project.
//!
//! The case count is NOT asserted here: the script owns it, prints one line per
//! case, and cross-checks each corpus group's parsed line count against the
//! count that group declares. Hardcoding a total in this file would only add a
//! second place to forget to update.
//!
//! The gate builds/locates the `sky` binary, puts it on `PATH` (the harness
//! launches `cmd = { "sky", "lsp" }`), and shells the script. If Neovim is not
//! installed it prints a LOUD skip and returns 0 so a contributor without nvim
//! isn't blocked — CI installs nvim explicitly, so the gate is really enforced
//! there (never a silent skip).

use std::path::Path;
use std::process::Command;

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    // nvim present? A missing nvim is a loud skip LOCALLY and a hard FAILURE in
    // CI.
    //
    // The skip exists so a contributor without Neovim is not blocked, and the
    // note above says "CI installs nvim explicitly, so the gate is really
    // enforced there (never a silent skip)". That was an assumption about a
    // workflow file, enforced by nothing in this process: if the install step is
    // removed, renamed, or fails on a runner image change, this function returns
    // 0 and prints SKIP, and `ci-green` reads a pass. The suite would stop
    // running and no gate would say so — a gate that cannot fail, which is the
    // one thing this cycle set out to remove.
    //
    // `CI` is set to `true` by GitHub Actions (and by every other major CI), so
    // the environment that is supposed to enforce this now proves it did.
    if !tool_available("nvim") {
        let in_ci = std::env::var("CI").is_ok_and(|v| !v.is_empty() && v != "false");
        if in_ci {
            eprintln!(
                "LSP GATE: FAIL — `nvim` is not installed, and CI=1.\n\n\
                 The editor-parity suite (17 symbol-class + 32 corpus cases) did \
                 NOT run. In CI that is a failure, not a skip: the workflow is \
                 responsible for installing Neovim, and a missing binary means \
                 that step is gone or broken. Returning 0 here would report a \
                 green gate for a suite that executed nothing.\n\n\
                 Fix the workflow's Neovim install step; do not silence this."
            );
            return 1;
        }
        println!(
            "LSP GATE: SKIP — `nvim` is not installed. The editor-parity \
             suite did NOT run. CI installs Neovim and enforces it; install nvim \
             locally to run this gate (`brew install neovim` / `apt install neovim`)."
        );
        return 0;
    }

    // Locate the `sky` binary next to this xtask executable (same target dir),
    // else build it. The harness resolves `sky` from PATH.
    let sky = match locate_or_build_sky(repo_root) {
        Ok(p) => p,
        Err(e) => {
            println!("LSP GATE: FAIL — could not obtain a `sky` binary: {e}");
            return 1;
        }
    };
    let sky_dir = sky.parent().unwrap_or(repo_root);

    let path = match std::env::var("PATH") {
        Ok(p) => format!("{}:{p}", sky_dir.display()),
        Err(_) => sky_dir.display().to_string(),
    };

    println!("LSP GATE: running the Neovim editor-parity suite against `sky lsp`…");
    let status = Command::new("bash")
        .arg("scripts/lsp-test-nvim.sh")
        .current_dir(repo_root)
        .env("PATH", path)
        .status();

    match status {
        Ok(s) if s.success() => {
            println!("LSP GATE: PASS  (all Neovim editor-parity cases; see the per-case lines above)");
            0
        }
        Ok(s) => {
            println!("LSP GATE: FAIL  (exit {})", s.code().unwrap_or(-1));
            1
        }
        Err(e) => {
            println!("LSP GATE: FAIL — could not run the suite: {e}");
            1
        }
    }
}

fn tool_available(name: &str) -> bool {
    Command::new(name)
        .arg("--version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// The `sky` binary in this xtask's target dir, or a freshly built one.
fn locate_or_build_sky(repo_root: &Path) -> Result<std::path::PathBuf, String> {
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            let candidate = dir.join(if cfg!(windows) { "sky.exe" } else { "sky" });
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }
    // Build it (debug) into the workspace target dir.
    let status = Command::new("cargo")
        .args(["build", "-p", "sky"])
        .current_dir(repo_root.join("rust"))
        .status()
        .map_err(|e| format!("cargo build -p sky: {e}"))?;
    if !status.success() {
        return Err("cargo build -p sky failed".to_string());
    }
    for rel in ["rust/target/debug/sky", "target/debug/sky"] {
        let p = repo_root.join(rel);
        if p.is_file() {
            return Ok(p);
        }
    }
    Err("built sky but could not find the binary".to_string())
}
