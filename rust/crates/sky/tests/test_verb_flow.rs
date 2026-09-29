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

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

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
    std::fs::write(
        &suite,
        format!("module {header} exposing (tests)\n\n{SUITE_BODY}"),
    )
    .unwrap();
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
    if !required(Need::Go, have_go()) {
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
    if !required(Need::Go, have_go()) {
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
    if !required(Need::Go, have_go()) {
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
    // Exactly 1: the documented "one or more tests failed" status, distinct from
    // the 2 a build failure reports (docs/tooling/testing.md).
    assert_eq!(code, 1, "a failing case must exit 1; output:\n{out}");
    let report = report.expect("sky test must write the SKY_TEST_JSON report");
    assert_eq!(report["failed"].as_i64(), Some(1), "report was {report}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// A suite passed by a RELATIVE path (`sky test tests/FooTest.sky`, the usual
/// spelling) printed every compile error in it TWICE: the declared-name source
/// root (`tests`) and the project's `<abs>/tests` compared unequal, so the tree
/// was loaded twice and the module checked twice. Found while fixing the
/// v0.25.19 dangling-export hole, where each `[E1001]` appeared twice.
#[test]
fn compile_error_in_a_relative_suite_path_is_reported_once() {
    let (dir, _suite) = project("dupdiag", "tests/FooTest.sky", "FooTest");
    std::fs::write(
        dir.join("tests").join("FooTest.sky"),
        "module FooTest exposing (tests)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"a\" (\\_ -> Test.equal 2 (noSuchHelper 1)) ]\n",
    )
    .unwrap();
    let (code, out, _) = run_test(&dir, Path::new("tests/FooTest.sky"));
    assert_ne!(code, 0, "a suite with an undefined name must fail:\n{out}");
    assert_eq!(
        out.matches("Undefined name: noSuchHelper").count(),
        1,
        "each compile error must be reported exactly once:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky test` exit statuses are a contract CI scripts branch on: 0 all passed,
/// 1 a test failed, 2 the suite did not build so no test ran. A build failure
/// used to exit 1 — indistinguishable from a failing test — because the runner
/// left no exit code and the CLI mapped "none" to `ExitCode::FAILURE`.
#[test]
fn build_failure_exits_2_not_1() {
    // A type error: the suite never compiles, so no test can run.
    let (dir, _suite) = project("buildfail", "tests/FooTest.sky", "FooTest");
    std::fs::write(
        dir.join("tests").join("FooTest.sky"),
        "module FooTest exposing (tests)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"a\" (\\_ -> Test.equal 2 (1 + \"one\")) ]\n",
    )
    .unwrap();
    let (code, out, report) = run_test(&dir, Path::new("tests/FooTest.sky"));
    assert_eq!(
        code, 2,
        "a suite that does not build must exit 2 (nothing ran), not 1; output:\n{out}"
    );
    assert!(
        report.is_none(),
        "no test ran, so no per-case report may exist; got {report:?}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky test <file>` builds the named suite and the modules it imports, never
/// every suite under `tests/`. A downstream project ran `sky test
/// tests/GoodTest.sky` and got `BadTest`'s type error and exit 2, because the
/// whole `tests/` tree was type-checked with the suite.
#[test]
fn a_named_suite_is_built_without_the_other_suites() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let (dir, _) = project("scoped", "tests/GoodTest.sky", "GoodTest");
    let tests = dir.join("tests");
    // GoodTest imports a helper under tests/: the imports ARE built.
    std::fs::write(
        tests.join("Helpers.sky"),
        "module Helpers exposing (three)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\n\
         three : Int\n\
         three =\n    3\n",
    )
    .unwrap();
    std::fs::write(
        tests.join("GoodTest.sky"),
        "module GoodTest exposing (tests)\n\n\
         import Helpers exposing (three)\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"passes\" (\\_ -> Test.equal 3 three)\n    \
         , Test.test \"fails\" (\\_ -> Test.equal 2 three)\n    \
         ]\n",
    )
    .unwrap();
    std::fs::write(
        tests.join("BadTest.sky"),
        "module BadTest exposing (tests)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"bad\" (\\_ -> Test.equal 2 \"x\") ]\n",
    )
    .unwrap();

    let (code, out, report) = run_test(&dir, Path::new("tests/GoodTest.sky"));
    assert_eq!(code, 1, "one failing case → exit 1:\n{out}");
    assert!(
        !out.contains("BadTest"),
        "another suite is not built:\n{out}"
    );
    assert!(out.contains("1 passed, 1 failed"), "{out}");
    assert!(report.is_some(), "the suite ran:\n{out}");

    // The same suite through `--format json`: its cases and exit code 1.
    let json = Command::new(SKY)
        .args(["test", "--format", "json", "tests/GoodTest.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky test --format json");
    assert_eq!(json.status.code(), Some(1));
    let stdout = String::from_utf8_lossy(&json.stdout);
    let summary: serde_json::Value =
        serde_json::from_str(stdout.lines().last().unwrap_or("{}")).unwrap();
    assert_eq!(summary["kind"], "summary", "{stdout}");
    assert_eq!(summary["passed"], 1, "{stdout}");
    assert_eq!(summary["failed"], 1, "{stdout}");
    assert_eq!(summary["errors"], 0, "{stdout}");
    assert!(!stdout.contains("BadTest"), "{stdout}");

    // The broken suite itself still fails to build: exit 2.
    let _ = std::fs::remove_file(dir.join("report.json"));
    let (code, out, report) = run_test(&dir, Path::new("tests/BadTest.sky"));
    assert_eq!(code, 2, "{out}");
    assert!(out.contains("type mismatch"), "{out}");
    assert!(report.is_none());

    // `sky test` with no file names the usage and runs nothing: exit 2.
    let bare = Command::new(SKY)
        .arg("test")
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky test");
    assert_eq!(bare.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&bare.stderr).contains("usage: sky test"));
    let _ = std::fs::remove_dir_all(&dir);
}

fn run_verify(dir: &Path) -> (i32, String) {
    let out = Command::new(SKY)
        .arg("verify")
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky verify");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

/// `sky verify` on a LIBRARY package (no `entry`, no `Main`; a downstream
/// project reported it): it failed with "no entry module named Main" and
/// skipped the tests. A library is checked module by module, its tests run,
/// and only the entry binary is skipped. A type error in a module that nothing
/// imports still fails the check.
#[test]
fn verify_checks_a_library_package_and_runs_its_tests() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("verifylib");
    std::fs::create_dir_all(dir.join("src/Lib")).unwrap();
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"lib\"\nversion = \"0.1.0\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Lib/Double.sky"),
        "module Lib.Double exposing (double)\n\nimport Sky.Core.Prelude exposing (..)\n\n\n\
         double : Int -> Int\ndouble n =\n    n * 2\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("tests/DoubleTest.sky"),
        "module DoubleTest exposing (tests)\n\nimport Lib.Double exposing (double)\n\
         import Sky.Core.Prelude exposing (..)\nimport Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\ntests =\n    [ Test.test \"double\" (\\_ -> Test.equal 4 (double 2)) ]\n",
    )
    .unwrap();

    let (code, out) = run_verify(&dir);
    assert_eq!(code, 0, "a library package verifies:\n{out}");
    assert!(out.contains("library: 1 module(s)"), "{out}");
    assert!(out.contains("1 suite(s) passed"), "{out}");
    assert!(!out.contains("no entry module named Main"), "{out}");
    assert!(
        !dir.join("sky-out").exists(),
        "the library check builds in scratch, not in the project"
    );

    // A failing test fails the gate at the test phase.
    std::fs::write(
        dir.join("tests/DoubleTest.sky"),
        "module DoubleTest exposing (tests)\n\nimport Lib.Double exposing (double)\n\
         import Sky.Core.Prelude exposing (..)\nimport Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\ntests =\n    [ Test.test \"double\" (\\_ -> Test.equal 5 (double 2)) ]\n",
    )
    .unwrap();
    let (code, out) = run_verify(&dir);
    assert_ne!(code, 0, "{out}");
    assert!(out.contains("verify failed: tests"), "{out}");

    // A type error in a module nothing imports fails the check phase.
    std::fs::write(
        dir.join("src/Lib/Broken.sky"),
        "module Lib.Broken exposing (bad)\n\nimport Sky.Core.Prelude exposing (..)\n\n\n\
         bad : Int\nbad =\n    \"not an int\"\n",
    )
    .unwrap();
    let (code, out) = run_verify(&dir);
    assert_ne!(code, 0, "{out}");
    assert!(out.contains("✗ check"), "{out}");
    assert!(
        out.contains("Broken.sky"),
        "the error names the module's file:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky test` printed a failing `Test.equal`'s values as Go structs
/// (`expected {0 a <nil>} but got {0 b <nil>}`; a downstream project reported
/// it). They print as Sky values, with the printer `Debug.toString` uses. A
/// union whose constructors take no arguments prints its constructor names,
/// also inside a Maybe, a List and a tuple (it used to print its index).
#[test]
fn a_failing_equal_prints_sky_values() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("skyvalues");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"tvals\"\nversion = \"0.1.0\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Shapes.sky"),
        "module Shapes exposing (Shape(..), User, Color(..))\n\nimport Sky.Core.Prelude exposing (..)\n\n\n\
         type Color\n    = Red\n    | Green\n    | Blue\n\n\n\
         type Shape\n    = Circle Float\n    | Rect { w : Int, h : Int }\n    | Empty\n\n\n\
         type alias User =\n    { name : String, age : Int, tags : List String }\n",
    )
    .unwrap();
    let suite = dir.join("tests/ValuesTest.sky");
    std::fs::write(
        &suite,
        "module ValuesTest exposing (tests)\n\n\
         import Sky.Core.Error as Error\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Shapes exposing (Color(..), Shape(..), User)\n\
         import Sky.Test as Test exposing (Test)\n\n\n\
         user : User\nuser =\n    { name = \"Ada\", age = 40, tags = [ \"a\", \"b\" ] }\n\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"result\" (\\_ -> Test.equal (Ok \"a\") (Ok \"b\"))\n    \
         , Test.test \"error\" (\\_ -> Test.equal (Err (Error.io \"x\")) (Ok 1))\n    \
         , Test.test \"record\" (\\_ -> Test.equal user { user | age = 41 })\n    \
         , Test.test \"adt\" (\\_ -> Test.equal [ Circle 1.5, Empty ] [ Rect { w = 1, h = 2 } ])\n    \
         , Test.test \"maybe\" (\\_ -> Test.equal (Just ( 1, 'c' )) Nothing)\n    \
         , Test.test \"nullary\" (\\_ -> Test.equal Red Green)\n    \
         , Test.test \"nullary nested\" (\\_ -> Test.equal (Just [ ( Red, 1 ) ]) (Just [ ( Blue, 2 ) ]))\n    \
         ]\n",
    )
    .unwrap();
    let (code, out, _) = run_test(&dir, &suite);
    assert_eq!(code, 1, "{out}");
    for want in [
        "expected Ok \"a\" but got Ok \"b\"",
        "expected Err (Io \"x\") but got Ok 1",
        "expected { age = 40, name = \"Ada\", tags = [\"a\", \"b\"] } but got { age = 41, name = \"Ada\", tags = [\"a\", \"b\"] }",
        "expected [Circle 1.5, Empty] but got [Rect { h = 2, w = 1 }]",
        "expected Just (1, 'c') but got Nothing",
        "expected Red but got Green",
        "expected Just [(Red, 1)] but got Just [(Blue, 2)]",
    ] {
        assert!(out.contains(want), "missing `{want}` in:\n{out}");
    }
    assert!(!out.contains("<nil>"), "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// A module with a helper that sorts BEFORE the broken one (`Aaa`, `Aab`), so
/// the broken module's load-order ordinal differs from its module id once
/// `sky test` drops what the suite does not import.
fn module_src(name: &str, value: &str) -> String {
    format!(
        "module {name} exposing (v)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\n\
         v : Int\n\
         v =\n    {value}\n"
    )
}

fn suite_src(name: &str, import: &str) -> String {
    format!(
        "module {name} exposing (tests)\n\n\
         import {import}\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Test as Test exposing (Test)\n\n\n\
         tests : List Test\n\
         tests =\n    \
         [ Test.test \"v\" (\\_ -> Test.equal 2 {import}.v) ]\n"
    )
}

/// `sky test` reported an imported module's parse error at the right offset in
/// ANOTHER file: `tests/LibTest.sky:5:31` with that suite's line under the
/// caret, for an extra `)` on line 8 of `src/Lib.sky`. A parse diagnostic's
/// span carried the file's load-order ordinal while the CLI keyed paths and
/// source text by module id; `sky test` drops the modules its suite does not
/// import, which is when the two differ. Every module kind is covered: `src/`,
/// a helper under `tests/`, the suite itself, and a Sky path dependency (whose
/// modules were not parse-gated or type-checked at all).
#[test]
fn a_parse_error_is_reported_in_its_own_file_for_every_module_kind() {
    let root = scratch("parsefile");
    let dir = root.join("app");
    let dep = root.join("dep");
    for d in [dir.join("src"), dir.join("tests"), dep.join("src")] {
        std::fs::create_dir_all(d).unwrap();
    }
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"parsefile\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n\n\
         [dependencies]\n\"widgets\" = { path = \"../dep\" }\n",
    )
    .unwrap();
    std::fs::write(
        dep.join("sky.toml"),
        "name = \"widgets\"\nversion = \"0.1.0\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println \"hi\"\n",
    )
    .unwrap();
    let w = |p: PathBuf, s: String| std::fs::write(p, s).unwrap();
    w(dir.join("src/Aaa.sky"), module_src("Aaa", "1"));
    w(dir.join("src/Lib.sky"), module_src("Lib", "(1 * 2))"));
    w(dir.join("tests/Aab.sky"), module_src("Aab", "1"));
    w(dir.join("tests/Helper.sky"), module_src("Helper", "[ 2 ]]"));
    w(dep.join("src/Aac.sky"), module_src("Aac", "1"));
    w(dep.join("src/Widget.sky"), module_src("Widget", "(1 + 1))"));
    w(dir.join("tests/LibTest.sky"), suite_src("LibTest", "Lib"));
    w(
        dir.join("tests/HelperTest.sky"),
        suite_src("HelperTest", "Helper"),
    );
    w(
        dir.join("tests/WidgetTest.sky"),
        suite_src("WidgetTest", "Widget"),
    );
    w(
        dir.join("tests/SelfTest.sky"),
        suite_src("SelfTest", "Aaa").replace("Aaa.v) ]", "Aaa.v) ]]"),
    );

    // (suite, the file:line:col the error must name, that line's text)
    let cases = [
        ("tests/LibTest.sky", "src/Lib.sky:8:12", "8 |     (1 * 2))"),
        (
            "tests/HelperTest.sky",
            "tests/Helper.sky:8:10",
            "8 |     [ 2 ]]",
        ),
        (
            "tests/WidgetTest.sky",
            "../dep/src/Widget.sky:8:12",
            "8 |     (1 + 1))",
        ),
        (
            "tests/SelfTest.sky",
            "tests/SelfTest.sky:10:49",
            "10 |     [ Test.test \"v\" (\\_ -> Test.equal 2 Aaa.v) ]]",
        ),
    ];
    for (suite, at, line) in cases {
        let (code, out, _) = run_test(&dir, Path::new(suite));
        assert_eq!(
            code, 2,
            "{suite}: a parse error must stop the build:\n{out}"
        );
        assert!(
            out.contains(&format!("{at} [E0001]")),
            "{suite}: the parse error must be reported at {at}:\n{out}"
        );
        assert!(
            out.contains(line),
            "{suite}: the excerpt must be the broken file's line `{line}`:\n{out}"
        );
        assert_eq!(out.matches("[E0001]").count(), 1, "{suite}: once:\n{out}");
    }

    // A type error in the path dependency is checked and named there too
    // (it used to pass `sky check` and panic at run time).
    w(
        dep.join("src/Widget.sky"),
        "module Widget exposing (v)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\n\
         v : Int\n\
         v =\n    1 ++ \"x\"\n"
            .to_string(),
    );
    let (code, out, _) = run_test(&dir, Path::new("tests/WidgetTest.sky"));
    assert_eq!(
        code, 2,
        "a type error in a path dependency must stop the build:\n{out}"
    );
    assert!(
        out.contains("../dep/src/Widget.sky:8:5 [E2001]"),
        "the path dependency's type error must name its file:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&root);
}
