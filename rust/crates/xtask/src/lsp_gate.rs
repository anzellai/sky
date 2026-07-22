//! `xtask lsp` — the Neovim editor-parity gate (doc 10's non-negotiable floor).
//!
//! Drives the REAL Neovim LSP client (`scripts/lsp-test-nvim.sh`, 17 tests:
//! hover / completion / goto-def across every user-visible symbol class) against
//! the Rust `sky lsp`. This catches editor-level bugs — label-vs-insertText,
//! filterText, scope handling — that the in-process JSON-RPC tests
//! (`sky-lsp/tests/*.rs`) miss.
//!
//! The gate builds/locates the `sky` binary, puts it on `PATH` (the harness
//! launches `cmd = { "sky", "lsp" }`), and shells the script. If Neovim is not
//! installed it prints a LOUD skip and returns 0 so a contributor without nvim
//! isn't blocked — CI installs nvim explicitly, so the gate is really enforced
//! there (never a silent skip).

use std::path::Path;
use std::process::Command;

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    // nvim present? A missing nvim is a loud skip locally; CI installs it.
    if !tool_available("nvim") {
        println!(
            "LSP GATE: SKIP — `nvim` is not installed. The 17-test editor-parity \
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

    println!("LSP GATE: running 17-test Neovim editor-parity suite against `sky lsp`…");
    let status = Command::new("bash")
        .arg("scripts/lsp-test-nvim.sh")
        .current_dir(repo_root)
        .env("PATH", path)
        .status();

    match status {
        Ok(s) if s.success() => {
            println!("LSP GATE: PASS  (17/17 Neovim editor-parity tests)");
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
