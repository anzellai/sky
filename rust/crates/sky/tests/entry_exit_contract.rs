//! Regression: a `main : Task Error a` that FAILS must not exit 0 silently.
//!
//! `lower_main_body` emitted the entry as `_ = rt.AnyTaskRun(<main>)` — the
//! entry Task's `Result` went straight into the blank identifier. A program
//! whose `main` returned `Err` therefore ran to completion, printed nothing
//! about the failure, and exited 0.
//!
//! That is not merely a wrong exit code: **every gate keyed on exit status is
//! blind to app-level failure.** It is how a golden file came to hold one byte
//! encoding a dead `Db.connect` and stayed green — the app "succeeded".
//!
//! The assertion below is deliberately split so it is worth something on a
//! runner with no Go toolchain (see the doctrine in `cli_verb_flow.rs`: a test
//! that skips and reports green is the defect this suite exists to kill):
//!
//!   * the EMITTED-GO leg always runs — `sky build` writes `sky-out/main.go`
//!     before it ever invokes `go build`, so the entry's shape is checkable
//!     with no toolchain at all;
//!   * the BEHAVIOURAL leg runs only when `go` is on `PATH`, and asserts the
//!     built binary actually exits non-zero and says why.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-entryexit-{tag}-{}-{}",
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

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// Scaffold a project whose `main` is the given expression body.
fn project(tag: &str, main_src: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"entryexit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

fn build(dir: &Path) -> String {
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    s
}

const FAILING_MAIN: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.Task as Task\n\
     import Sky.Core.Error as Error\n\n\
     main : Task Error ()\n\
     main =\n    Task.fail (Error.unexpected \"deliberate entry failure\")\n";

const OK_MAIN: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Std.Log exposing (println)\n\n\
     main =\n    println \"fine\"\n";

/// Emission leg — toolchain-free. The entry must INSPECT the run's result, not
/// discard it. `_ = rt.AnyTaskRun(...)` as the whole entry statement is the
/// defect verbatim.
#[test]
fn entry_task_result_is_not_discarded_in_emitted_go() {
    let dir = project("emit", FAILING_MAIN);
    let log = build(&dir);
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (build log: {log})"
    );
    let src = std::fs::read_to_string(&main_go).unwrap();
    let entry = src
        .split("func main()")
        .nth(1)
        .expect("emitted Go must contain func main()");

    assert!(
        entry.contains("rt.ResultTag("),
        "the entry must check the run's result tag — otherwise a failing \
         `main : Task Error ()` exits 0 silently. Emitted entry:\n{entry}"
    );
    assert!(
        entry.contains("rt.System_exit("),
        "the entry must exit non-zero when the entry Task failed. \
         Emitted entry:\n{entry}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Behavioural leg — needs a Go toolchain. A failing entry exits non-zero AND
/// says something about the failure.
#[test]
fn failing_entry_task_exits_nonzero_and_reports() {
    if !have_go() {
        eprintln!("skipping behavioural leg: no `go` on PATH (emission leg still asserts)");
        return;
    }
    let dir = project("run", FAILING_MAIN);
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log: {log})");

    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));

    assert_ne!(
        out.status.code(),
        Some(0),
        "a `main : Task Error ()` that fails must NOT exit 0 — every gate keyed \
         on exit status is blind otherwise. Output was:\n{combined}"
    );
    assert!(
        combined.contains("deliberate entry failure"),
        "the failure must be reported, not swallowed. Output was:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// The other half of the contract: a SUCCEEDING entry still exits 0 and is not
/// made noisy. Without this the fix could "pass" by failing everything.
#[test]
fn succeeding_entry_still_exits_zero() {
    if !have_go() {
        eprintln!("skipping behavioural leg: no `go` on PATH");
        return;
    }
    let dir = project("ok", OK_MAIN);
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log: {log})");

    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let combined = String::from_utf8_lossy(&out.stdout).into_owned();
    assert_eq!(
        out.status.code(),
        Some(0),
        "a succeeding entry must still exit 0; output:\n{combined}"
    );
    assert!(
        combined.contains("fine"),
        "a succeeding entry must still produce its output; got:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
