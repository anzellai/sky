//! Regression: `sky test` reported `0 passed`, exit 0, on a file full of tests.
//!
//! A test runner that silently runs nothing and reports success is the same
//! "SKIP counted as pass" class this overhaul exists to kill — worse, because
//! it wears the runner's authority.
//!
//! ROOT CAUSE. `run_test` derived the suite's import name from its FILESYSTEM
//! PATH (`module_name_from_path`, roots hardcoded to `src`/`tests`), while the
//! loader registers every module under the name in its `module` HEADER. Nothing
//! cross-checked the two. When they disagreed, the synthesised entry's
//! `import <derived> as Suite` named a module the db had never heard of — and
//! `HirDb::classify_import` treats an unknown module as a **Go FFI package**
//! (`ImportSource::Foreign`), not an error. `Suite.tests` then lowered to the Go
//! literal `nil` with a warning only, `rt.Coerce[[]any](nil)` yielded the zero
//! slice, and `Test.runMain []` printed "0 passed, 0 failed (0 total)" and
//! exited 0. `run_test` never read `report.warnings`, so the single signal that
//! the suite reference was bogus was dropped on the floor.
//!
//! Two faces, both covered below:
//!   * MODE A — header and path disagree → silently empty, exit 0.
//!   * MODE B — the module name could not be derived at all → the repo's own
//!     `tests/` tree (`tests/sky.toml` is the project root, so the roots tried
//!     were `tests/src` and `tests/tests`) was entirely unrunnable.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-testverb-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

const SUITE_BODY: &str = "import Sky.Core.Prelude exposing (..)\n\
     import Sky.Test as Test exposing (Test)\n\n\
     tests : List Test\n\
     tests =\n    \
     [ Test.suite \"s\"\n        \
     [ Test.test \"a\" (\\_ -> Test.equal 2 (1 + 1))\n        \
     , Test.test \"b\" (\\_ -> Test.equal 3 (1 + 2))\n        \
     , Test.test \"c\" (\\_ -> Test.equal 4 (2 + 2))\n        \
     ]\n    ]\n";

/// Scaffold a project. `suite_rel` is where the suite file goes; `header` is the
/// module name it DECLARES.
fn project(tag: &str, suite_rel: &str, header: &str) -> (PathBuf, PathBuf) {
    let dir = scratch(tag);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"tverb\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println \"hi\"\n",
    )
    .unwrap();
    let suite = dir.join(suite_rel);
    std::fs::create_dir_all(suite.parent().unwrap()).unwrap();
    std::fs::write(&suite, format!("module {header} exposing (tests)\n\n{SUITE_BODY}")).unwrap();
    (dir, suite)
}

/// Run `sky test <suite>` from `dir`, with a JSON report requested so the
/// per-case count is machine-readable. Returns (exit, stdout+stderr, report).
fn run_test(dir: &Path, suite: &Path) -> (i32, String, Option<serde_json::Value>) {
    let report_path = dir.join("report.json");
    let out = Command::new(SKY)
        .arg("test")
        .arg(suite)
        .current_dir(dir)
        .env("SKY_TEST_JSON", &report_path)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky test");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    let report = std::fs::read_to_string(&report_path)
        .ok()
        .and_then(|t| serde_json::from_str(&t).ok());
    (out.status.code().unwrap_or(-1), s, report)
}

/// MODE A — the suite's header (`FooTest`) and its path (`tests/Nested/…`, which
/// derives `Nested.FooTest`) disagree. Before the fix this printed
/// "0 passed, 0 failed (0 total)" and exited 0.
#[test]
fn suite_whose_header_differs_from_its_path_actually_runs() {
    if !have_go() {
        eprintln!("skipping: no `go` on PATH");
        return;
    }
    let (dir, suite) = project("modea", "tests/Nested/FooTest.sky", "FooTest");
    let (code, out, report) = run_test(&dir, &suite);

    assert!(
        !out.contains("0 passed, 0 failed (0 total)"),
        "`sky test` ran NOTHING and said so while exiting {code}. A suite whose \
         module header does not match its path must still be found — or the run \
         must fail loudly. Output:\n{out}"
    );
    let report = report.expect("sky test must write the SKY_TEST_JSON report");
    assert_eq!(
        report["total"].as_i64(),
        Some(3),
        "all 3 declared cases must run; report was {report}"
    );
    assert_eq!(code, 0, "a passing suite exits 0; output:\n{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// MODE B — a dotted header under a matching nested path, with the project root
/// directly above `tests/`. This is the shape of the repo's own suites
/// (`tests/Std/UiMediaQueryTest.sky` declaring `module Std.UiMediaQueryTest`,
/// with `tests/sky.toml` as the project root), every one of which was
/// unrunnable: "must live under src/ or tests/ so its module name can be
/// derived".
#[test]
fn dotted_header_suite_at_the_project_root_is_runnable() {
    if !have_go() {
        eprintln!("skipping: no `go` on PATH");
        return;
    }
    let (dir, suite) = project("modeb", "Std/ThingTest.sky", "Std.ThingTest");
    let (code, out, report) = run_test(&dir, &suite);

    assert!(
        !out.contains("must live under src/ or tests/"),
        "a suite whose dotted header matches its path must be runnable; \
         output:\n{out}"
    );
    let report = report.expect("sky test must write the SKY_TEST_JSON report");
    assert_eq!(
        report["total"].as_i64(),
        Some(3),
        "all 3 declared cases must run; report was {report}"
    );
    assert_eq!(code, 0, "a passing suite exits 0; output:\n{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// The converse: a genuinely failing case must still be reported and exit
/// non-zero, so the fix cannot "pass" by making everything green.
#[test]
fn failing_case_still_fails_the_run() {
    if !have_go() {
        eprintln!("skipping: no `go` on PATH");
        return;
    }
    let dir = scratch("redcase");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"tverb\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\nmain =\n    println \"hi\"\n",
    )
    .unwrap();
    let suite = dir.join("tests").join("RedTest.sky");
    std::fs::write(
        &suite,
        "module RedTest exposing (tests)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\
         tests : List Test\n\
         tests =\n    [ Test.suite \"s\" [ Test.test \"boom\" (\\_ -> Test.equal 1 2) ] ]\n",
    )
    .unwrap();

    let (code, out, report) = run_test(&dir, &suite);
    assert_ne!(code, 0, "a failing case must fail the run; output:\n{out}");
    let report = report.expect("sky test must write the SKY_TEST_JSON report");
    assert_eq!(report["failed"].as_i64(), Some(1), "report was {report}");
    let _ = std::fs::remove_dir_all(&dir);
}
