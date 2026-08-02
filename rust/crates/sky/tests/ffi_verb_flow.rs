//! Smoke coverage for the `sky add` / `sky remove` verb orchestration.
//!
//! The `ffi` crate byte-tests binding GENERATION; this test covers the VERB
//! orchestration around it — that `sky add <go-module>`:
//!   * records the module under `["go.dependencies"]` in sky.toml, and
//!   * writes the `sky-ffi/<pkg>.{kernel.json,skyi}` + `sky-ffi/go/<pkg>_bindings.go`
//!     artifacts,
//! and that `sky remove <go-module>` reverts all of that.
//!
//! This exercises a real Go-module resolve (`go get` + package introspection),
//! so it needs BOTH a `go` toolchain AND network access. When `go` is absent, or
//! the resolve fails for a network reason, the test early-returns with a note
//! rather than failing (matching the toolchain-gate convention) — a genuine
//! orchestration bug still fails because the resolve itself succeeds first.
//!
//! Package under test: `rsc.io/quote` — a tiny, stable, pure-Go module with no
//! heavy transitive deps, the canonical Go-modules smoke package.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const PKG: &str = "rsc.io/quote";

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-ffi-verb-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-verb\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\n\nmain : Task Error ()\nmain =\n    Task.succeed ()\n",
    )
    .unwrap();
    dir
}

fn run_sky(dir: &Path, args: &[&str]) -> (bool, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

/// Heuristic: did `sky add` fail for a NETWORK reason (offline CI)? Such a
/// failure is a skip, not an orchestration bug.
fn looks_like_network_failure(log: &str) -> bool {
    let l = log.to_lowercase();
    l.contains("dial tcp")
        || l.contains("no such host")
        || l.contains("timeout")
        || l.contains("network is unreachable")
        || l.contains("connection refused")
        || l.contains("could not resolve")
        || l.contains("temporary failure in name resolution")
        || l.contains("proxy.golang.org")
        || l.contains("i/o timeout")
}

#[test]
fn add_then_remove_roundtrips_sky_toml_and_bindings() {
    if !go_on_path() {
        eprintln!("ffi_verb_flow: skipping — needs `go` on PATH");
        return;
    }
    let dir = scratch_project();

    // ---- sky add -------------------------------------------------------
    let (ok, log) = run_sky(&dir, &["add", PKG]);
    if !ok {
        if looks_like_network_failure(&log) {
            eprintln!("ffi_verb_flow: skipping — `sky add {PKG}` failed (no network):\n{log}");
            let _ = std::fs::remove_dir_all(&dir);
            return;
        }
        let _ = std::fs::remove_dir_all(&dir);
        panic!("sky add {PKG} failed (not a network error):\n{log}");
    }

    let toml_after_add = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    assert!(
        toml_after_add.contains("go.dependencies"),
        "sky add should add a [go.dependencies] table:\n{toml_after_add}"
    );
    assert!(
        toml_after_add.contains(PKG),
        "sky add should record `{PKG}` in sky.toml:\n{toml_after_add}"
    );

    // Bindings artifacts (pkg last segment = "quote").
    let ffi = dir.join("sky-ffi");
    assert!(
        ffi.join("quote.kernel.json").is_file(),
        "sky add should write sky-ffi/quote.kernel.json"
    );
    assert!(
        ffi.join("quote.skyi").is_file(),
        "sky add should write sky-ffi/quote.skyi"
    );
    assert!(
        ffi.join("go").join("quote_bindings.go").is_file(),
        "sky add should write sky-ffi/go/quote_bindings.go"
    );

    // ---- sky remove ----------------------------------------------------
    let (ok, log) = run_sky(&dir, &["remove", PKG]);
    assert!(ok, "sky remove {PKG} failed:\n{log}");

    let toml_after_remove = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    assert!(
        !toml_after_remove.contains(PKG),
        "sky remove should drop `{PKG}` from sky.toml:\n{toml_after_remove}"
    );
    assert!(
        !ffi.join("quote.kernel.json").is_file(),
        "sky remove should delete sky-ffi/quote.kernel.json"
    );
    assert!(
        !ffi.join("quote.skyi").is_file(),
        "sky remove should delete sky-ffi/quote.skyi"
    );
    assert!(
        !ffi.join("go").join("quote_bindings.go").is_file(),
        "sky remove should delete sky-ffi/go/quote_bindings.go"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
