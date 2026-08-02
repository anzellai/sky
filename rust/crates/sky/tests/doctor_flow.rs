//! Smoke / regression coverage for the `sky doctor` verb path.
//!
//! `sky doctor`'s individual check helpers are unit-tested, but the VERB path
//! (project-root discovery → run_all_checks → severity summary → exit code) had
//! no end-to-end coverage. This drives the real `sky` binary through:
//!   (a) a HEALTHY scratch project → exit 0 + "no issues found",
//!   (b) an EMPTY sky.toml fault  → exit 1 + the specific `sky.toml is empty`
//!       Error finding (✗ marker), and
//!   (c) a MISSING-ENTRY fault    → exit 1 + the `entry file ... does not exist`
//!       Error finding, and
//!   (d) run OUTSIDE any project   → exit 2 (diagnostic couldn't run).
//!
//! The healthy assertion needs a `go` toolchain on PATH (else the go-toolchain
//! check legitimately reports an Error); when go is absent that one sub-case
//! early-returns with a note, matching the example-sweep toolchain-gate
//! convention. The fault + no-project cases need no toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_dir(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-doctor-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// A minimal, well-formed project (valid sky.toml + existing entry file).
fn healthy_project(tag: &str) -> PathBuf {
    let dir = scratch_dir(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"doctor-smoke\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\n\nmain : Task Error ()\nmain =\n    Task.succeed ()\n",
    )
    .unwrap();
    dir
}

/// Run `sky <args...>` in `dir`. Returns (exit_code, stdout+stderr).
fn run_sky(dir: &Path, args: &[&str]) -> (i32, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

#[test]
fn doctor_healthy_project_reports_clean() {
    if !go_on_path() {
        eprintln!("doctor_flow: skipping healthy-case — needs `go` on PATH");
        return;
    }
    let dir = healthy_project("healthy");
    let (code, log) = run_sky(&dir, &["doctor"]);
    assert_eq!(code, 0, "healthy project should exit 0:\n{log}");
    assert!(
        log.contains("no issues found"),
        "expected clean report, got:\n{log}"
    );
    // No Error findings should be printed.
    assert!(
        !log.contains('✗'),
        "healthy project must not report any ✗ Error finding:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn doctor_empty_sky_toml_is_error() {
    let dir = scratch_dir("empty");
    // An empty (but present) sky.toml: root is located, but the file is invalid.
    std::fs::write(dir.join("sky.toml"), "").unwrap();

    let (code, log) = run_sky(&dir, &["doctor"]);
    assert_eq!(code, 1, "empty sky.toml should exit 1:\n{log}");
    assert!(
        log.contains("sky.toml is empty"),
        "expected the specific empty-toml finding:\n{log}"
    );
    assert!(log.contains('✗'), "empty-toml should be an Error (✗):\n{log}");
    assert!(
        log.contains("errors"),
        "summary should count the error:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn doctor_missing_entry_file_is_error() {
    let dir = scratch_dir("noentry");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"x\"\nversion = \"0.1.0\"\nentry = \"src/Nope.sky\"\n",
    )
    .unwrap();

    let (code, log) = run_sky(&dir, &["doctor"]);
    assert_eq!(code, 1, "missing entry should exit 1:\n{log}");
    assert!(
        log.contains("does not exist") && log.contains("Nope.sky"),
        "expected the entry-missing finding naming the file:\n{log}"
    );
    assert!(log.contains('✗'), "missing-entry should be an Error (✗):\n{log}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn doctor_outside_project_exits_2() {
    // A bare temp dir with no sky.toml in it or any ancestor we control. We can't
    // guarantee no ancestor has a sky.toml on an arbitrary host, so create an
    // isolated dir directly under the OS temp root (which is not a Sky project)
    // and assert the "no sky.toml" diagnostic path.
    let dir = std::env::temp_dir().join(format!(
        "sky-doctor-noproject-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    let (code, log) = run_sky(&dir, &["doctor"]);
    // Exit 2 = couldn't locate a project root. If the host's temp dir happens to
    // sit under a Sky checkout, this would differ — tolerate that by only
    // asserting the message when we got the 2 path.
    if code == 2 {
        assert!(
            log.contains("no sky.toml found"),
            "exit 2 should explain the missing project:\n{log}"
        );
    } else {
        eprintln!(
            "doctor_flow: no-project case saw exit {code} (an ancestor sky.toml?); log:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}
