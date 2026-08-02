//! Smoke / regression coverage for `sky run --profile`.
//!
//! The profiling FLAG parsing is unit-tested, but the end-to-end behaviour —
//! `sky run <cli> --profile` actually producing the documented artifacts
//! (`cpu.pprof` / `heap.pprof` / `goroutines.txt` / `REPORT.md`, per CLAUDE.md's
//! `sky run --profile` row) — had no coverage. This drives the real `sky` binary
//! through a build+run of a tiny CLI program under a temp `--profile-dir` and
//! asserts every artifact appears and `REPORT.md` is non-empty + plausible.
//!
//! Needs a `go` toolchain (the run path compiles the emitted Go). When absent it
//! early-returns with a note, matching the example-sweep toolchain gate.

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

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-profile-{}-{}",
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
        "name = \"profile-smoke\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    // A one-shot CLI that prints and exits (main = Task) — the profiler flushes on
    // the normal main-exit path (stopProfiling in LogPanicAndExit).
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Std.Log exposing (println)\n\n\nmain : Task Error ()\nmain =\n    let\n        _ =\n            println \"profiled run\"\n    in\n    Task.succeed ()\n",
    )
    .unwrap();
    dir
}

#[test]
fn run_profile_writes_all_artifacts() {
    if !go_on_path() {
        eprintln!("profile_flow: skipping — needs `go` on PATH");
        return;
    }
    let dir = scratch_project();
    let prof_dir = dir.join("profdir");

    // Bound the run with a --profile-timeout so a hypothetical hang self-dumps and
    // exits rather than wedging the test. The program exits promptly on its own.
    let out = Command::new(SKY)
        .args([
            "run",
            "src/Main.sky",
            "--profile",
            "--profile-dir",
            prof_dir.to_str().unwrap(),
            "--profile-timeout",
            "20s",
        ])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky run --profile");
    let log = {
        let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
        s.push_str(&String::from_utf8_lossy(&out.stderr));
        s
    };
    assert!(out.status.success(), "sky run --profile failed:\n{log}");
    assert!(
        log.contains("Profiling enabled"),
        "expected the profiling banner:\n{log}"
    );

    // Every documented artifact must exist.
    for name in ["cpu.pprof", "heap.pprof", "goroutines.txt", "REPORT.md"] {
        let p = prof_dir.join(name);
        assert!(
            p.is_file(),
            "profile artifact `{name}` missing under {}\nlog:\n{log}",
            prof_dir.display()
        );
    }

    // REPORT.md must be non-empty and look like the Sky-named summary.
    let report = std::fs::read_to_string(prof_dir.join("REPORT.md")).unwrap();
    assert!(!report.trim().is_empty(), "REPORT.md is empty");
    assert!(
        report.contains("Sky app profile"),
        "REPORT.md missing its heading:\n{report}"
    );
    assert!(
        report.contains("Memory") || report.contains("Heap"),
        "REPORT.md missing the memory section:\n{report}"
    );

    cleanup(&dir);
}

fn cleanup(dir: &Path) {
    let _ = std::fs::remove_dir_all(dir);
}
