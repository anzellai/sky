//! `xtask lsp` — the Neovim editor-parity gate (doc 10's non-negotiable floor).
//!
//! Drives the REAL Neovim LSP client (`scripts/lsp-test-nvim.sh`) against the
//! Rust `sky lsp`. This catches editor-level bugs — label-vs-insertText,
//! filterText, scope handling — that the in-process JSON-RPC tests
//! (`sky-lsp/tests/*.rs`) miss.
//!
//! Three suites run under that one script:
//!   * the original 17 single-fixture cases (`lsp-test-nvim.lua`), one symbol
//!     class each; and
//!   * the corpus (`lsp-corpus-nvim.lua`) — cross-module resolution through
//!     every import shape, goto-def into `sky-stdlib`, `[E1012]`/`[E2008]`/
//!     `[E2007]` as an editor renders them, the broken-file usability cases,
//!     and the server driven over a REAL `examples/` project; and
//!   * the `realffi` group (`lsp-test-skyshop.lua`) — hover and completion on
//!     `examples/13-skyshop`'s generated Stripe SDK surface.
//!
//! The script owns the per-case reporting and cross-checks each corpus group's
//! parsed line count against the count that group declares. This gate adds the
//! ANTI-SHRINK floor on top: it reads the script's JSON report and fails if
//! fewer cases ran than `harness::bodies::LSP_EXPECTED`.
//!
//! That floor already existed, but only in the harness body, which runs under
//! `harness --tier t1` — a RELEASE-workflow invocation. Per-push CI calls
//! `xtask lsp` directly, so until now the corpus could shrink on every push for
//! a whole cycle and only be caught on tag day. A passing suite proves the cases
//! that RAN are correct; it says nothing about cases someone deleted, and
//! deleting a case is the cheapest way to make a gate greener.
//!
//! Growth is deliberately not an error — a gate that fails when you ADD a case
//! is a gate that stops people adding cases.
//!
//! The gate builds/locates the `sky` binary, puts it on `PATH` (the harness
//! launches `cmd = { "sky", "lsp" }`), and shells the script. If Neovim is not
//! installed the gate FAILS naming what to install, like every other gate whose
//! prerequisite is missing. `SKY_LIVE_TESTS=skip` is the one opt-out, said out
//! loud (the same variable `scripts/lib/require-tool.sh` and
//! `rust/crates/sky/src/live_gate.rs` read).

use std::path::Path;
use std::process::Command;

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    // nvim present? A missing nvim FAILS. It used to be a loud skip locally
    // (and a failure only when `CI` was set), which made the gate the one
    // prerequisite-gated check in the repository that passed on a machine
    // without its prerequisite. The rule everywhere else is AGENTS.md's: a
    // gate whose prerequisite is missing fails, naming what to install, and
    // `SKY_LIVE_TESTS=skip` is the only way to say "I cannot run this".
    if !tool_available("nvim") {
        return match missing_nvim_verdict(std::env::var("SKY_LIVE_TESTS").ok().as_deref()) {
            Ok(()) => {
                println!(
                    "LSP GATE: SKIP — `nvim` is not installed and SKY_LIVE_TESTS=skip was set. \
                     The editor-parity suite did NOT run, so nothing it proves is proven by \
                     this run."
                );
                0
            }
            Err(msg) => {
                eprintln!("{msg}");
                1
            }
        };
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
    // Ask for the JSON report as well as the exit status. Passing cases is only
    // half the property: the suite must still be the SIZE it claims. Without
    // this, deleting cases from the corpus leaves the gate green here, and the
    // anti-shrink ratchet (`harness::bodies::LSP_EXPECTED`) only runs at
    // RELEASE — so a corpus could shrink per-push for an entire cycle and be
    // caught on tag day, which is the worst possible moment to discover it.
    let json = std::env::temp_dir().join(format!("sky-lsp-report-{}.json", std::process::id()));
    let _ = std::fs::remove_file(&json);
    let status = Command::new("bash")
        .arg("scripts/lsp-test-nvim.sh")
        .args(["--json", &json.display().to_string()])
        .current_dir(repo_root)
        .env("PATH", path)
        .status();

    match status {
        Ok(s) if s.success() => {
            if let Some(code) = shrink_check(&json) {
                return code;
            }
            println!(
                "LSP GATE: PASS  (all Neovim editor-parity cases; see the per-case lines above)"
            );
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

/// What a missing `nvim` means, given `SKY_LIVE_TESTS`: `Ok(())` is the one
/// declared skip, every other value is a failure message.
fn missing_nvim_verdict(live_tests: Option<&str>) -> Result<(), String> {
    match live_tests {
        Some("skip") => Ok(()),
        None | Some("require") => Err(
            "LSP GATE: FAIL — this gate needs `nvim` and it is not on PATH.\n  \
             Install Neovim (`brew install neovim` / `apt-get install neovim`).\n  \
             The editor-parity suite did NOT run. If you genuinely cannot install \
             it, say so out loud: SKY_LIVE_TESTS=skip cargo run -p xtask -- lsp"
                .to_string(),
        ),
        Some(other) => Err(format!(
            "LSP GATE: FAIL — SKY_LIVE_TESTS='{other}' is not a value I know. Use \
             'require' (the default) or 'skip'."
        )),
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

/// The suite must still be the size it claims.
///
/// Returns `Some(exit_code)` when the corpus has SHRUNK, `None` when it is fine.
///
/// A passing suite proves the cases that ran are correct. It says nothing about
/// cases that were deleted — and deleting a case is the cheapest way to make a
/// gate greener. The count was already pinned by
/// `harness::bodies::LSP_EXPECTED`, but that body only executes under
/// `harness --tier t1`, which the RELEASE workflow runs; per-push CI invokes
/// `xtask lsp` directly. So the corpus could shrink on every push for a whole
/// cycle and be caught on tag day.
///
/// GROWTH is fine and is not reported here — the corpus is meant to grow, and
/// making an addition fail the build is how people stop adding cases. Only a
/// shrink is an error, and it names the constant to update if the removal was
/// deliberate.
fn shrink_check(json: &std::path::Path) -> Option<i32> {
    let expected = crate::harness::bodies::LSP_EXPECTED;
    let body = match std::fs::read_to_string(json) {
        Ok(b) => b,
        Err(e) => {
            eprintln!(
                "LSP GATE: FAIL — the suite passed but wrote no JSON report to {} ({e}).\n\
                 The case count could not be checked, and an unverifiable count is \
                 not a verified one.",
                json.display()
            );
            return Some(1);
        }
    };
    let total = body
        .split("\"total\":")
        .nth(1)
        .and_then(|rest| {
            let digits: String = rest
                .trim_start()
                .chars()
                .take_while(|c| c.is_ascii_digit())
                .collect();
            digits.parse::<u64>().ok()
        })
        .unwrap_or(0);

    if total == 0 {
        eprintln!(
            "LSP GATE: FAIL — the JSON report declares 0 cases. A suite that ran \
             nothing cannot pass; this is the vacuity the report exists to expose."
        );
        return Some(1);
    }
    if total < expected {
        eprintln!(
            "LSP GATE: FAIL — the editor-parity corpus SHRANK: {total} cases ran, \
             {expected} expected.\n\
             Cases were removed. If that was deliberate, lower \
             `harness::bodies::LSP_EXPECTED` in the same commit and say why; \
             otherwise restore them. Deleting a case is the cheapest way to make \
             a gate greener, which is why this direction is an error and growth \
             is not."
        );
        return Some(1);
    }
    if total > expected {
        println!(
            "LSP GATE: note — {total} cases ran, {expected} expected. The corpus \
             GREW, which is fine; raise `LSP_EXPECTED` to {total} to keep the \
             anti-shrink floor meaningful."
        );
    }
    None
}

#[cfg(test)]
mod tests {
    use super::missing_nvim_verdict;

    /// A missing `nvim` used to return 0 with a SKIP line whenever `CI` was
    /// unset. It now fails unless the one opt-out is set.
    #[test]
    fn a_missing_nvim_fails_unless_skip_is_declared() {
        assert!(missing_nvim_verdict(None).is_err());
        assert!(missing_nvim_verdict(Some("require")).is_err());
        assert!(missing_nvim_verdict(Some("1")).is_err());
        assert!(missing_nvim_verdict(Some("skip")).is_ok());
    }
}
