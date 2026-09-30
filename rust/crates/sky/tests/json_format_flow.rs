//! `sky check` / `sky build` / `sky test` / `sky fmt --check` with
//! `--format json`, through the real binary.
//!
//! The contract (`docs/tooling/cli.md`, "Machine-readable output"): stdout is
//! NDJSON and nothing else, every line carries `"schema": 1`, the LAST line is
//! the one `summary`, human text goes to stderr, and the exit code is the text
//! mode's. Each test below parses EVERY stdout line as JSON, so a stray
//! progress line on stdout fails it.
//!
//! The text and json modes render the same structured diagnostics
//! (`BuildReport::diagnostics`), so one test counts the text mode's error
//! blocks against the json mode's error lines on the same program.

use serde_json::Value;
use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-json-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// A project with `src/Main.sky` = `main_src` and an optional extra sky.toml tail.
fn project(tag: &str, main_src: &str, toml_tail: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        format!(
            "name = \"json-{tag}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n{toml_tail}"
        ),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), main_src).unwrap();
    dir
}

struct Out {
    code: i32,
    lines: Vec<Value>,
    stderr: String,
}

fn sky(dir: &Path, args: &[&str]) -> Out {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    let stderr = String::from_utf8_lossy(&out.stderr).into_owned();
    let lines: Vec<Value> = stdout
        .lines()
        .map(|l| {
            serde_json::from_str(l).unwrap_or_else(|e| {
                panic!("stdout line is not JSON ({e}): {l:?}\n--- stdout ---\n{stdout}\n--- stderr ---\n{stderr}")
            })
        })
        .collect();
    Out {
        code: out.status.code().unwrap_or(-1),
        lines,
        stderr,
    }
}

/// The stream invariants every json run must hold. Returns the summary.
fn check_stream(o: &Out) -> &Value {
    assert!(
        !o.lines.is_empty(),
        "no NDJSON at all; stderr:\n{}",
        o.stderr
    );
    for l in &o.lines {
        assert_eq!(l["schema"], 1, "every line carries schema 1: {l}");
    }
    let summaries: Vec<&Value> = o.lines.iter().filter(|l| l["kind"] == "summary").collect();
    assert_eq!(summaries.len(), 1, "exactly one summary: {:?}", o.lines);
    let last = o.lines.last().unwrap();
    assert_eq!(last["kind"], "summary", "the summary is the last line");
    assert_eq!(
        last["ok"].as_bool().unwrap(),
        o.code == 0,
        "ok mirrors the exit code ({}): {last}",
        o.code
    );
    let errors = o
        .lines
        .iter()
        .filter(|l| l["kind"] == "diagnostic" && l["severity"] == "error")
        .count();
    let warnings = o
        .lines
        .iter()
        .filter(|l| l["kind"] == "diagnostic" && l["severity"] == "warning")
        .count();
    assert_eq!(last["errors"].as_u64().unwrap() as usize, errors);
    assert_eq!(last["warnings"].as_u64().unwrap() as usize, warnings);
    assert!(last["durationMs"].is_u64());
    last
}

fn diags(o: &Out) -> Vec<&Value> {
    o.lines
        .iter()
        .filter(|l| l["kind"] == "diagnostic")
        .collect()
}

const CLEAN: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println \"hi\"\n";

/// A type error on a known line (0-based line 8).
const TYPE_ERR: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
x : Int\n\
x =\n    \"nope\"\n\n\n\
main =\n    println \"hi\"\n";

#[test]
fn clean_project_is_one_ok_summary() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("clean", CLEAN, "");
    for verb in ["check", "build"] {
        let o = sky(&dir, &[verb, "--format", "json", "src/Main.sky"]);
        assert_eq!(o.code, 0, "{verb}: {}", o.stderr);
        let s = check_stream(&o);
        assert_eq!(o.lines.len(), 1, "{verb}: only the summary: {:?}", o.lines);
        assert_eq!(s["command"], verb);
        assert_eq!(s["errors"], 0);
        let root = PathBuf::from(s["root"].as_str().unwrap());
        assert_eq!(
            root.canonicalize().unwrap(),
            dir.canonicalize().unwrap(),
            "root names the project directory"
        );
    }
    // The human progress text went to stderr, not stdout.
    let o = sky(&dir, &["check", "--format=json", "src/Main.sky"]);
    assert!(o.stderr.contains("No errors found."), "{}", o.stderr);
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn type_error_lines_are_lsp_shaped_and_golden() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("type", TYPE_ERR, "");
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    let s = check_stream(&o);
    assert!(s["errors"].as_u64().unwrap() >= 1);
    let ds = diags(&o);
    for d in &ds {
        assert_eq!(d["source"], "sky");
        assert_eq!(d["file"], "src/Main.sky", "project-relative path: {d}");
        assert!(d["code"].as_str().unwrap().starts_with("E2"), "{d}");
        assert!(d["range"]["start"]["line"].is_u64());
        assert!(d["range"]["end"]["character"].is_u64());
    }
    // Golden: the `x = "nope"` error, byte for byte.
    let raw = String::from_utf8_lossy(
        &Command::new(SKY)
            .args(["check", "--format", "json", "src/Main.sky"])
            .current_dir(&dir)
            .output()
            .unwrap()
            .stdout,
    )
    .into_owned();
    let first = raw.lines().next().unwrap();
    assert_eq!(
        first,
        r#"{"kind":"diagnostic","schema":1,"file":"src/Main.sky","range":{"start":{"line":8,"character":4},"end":{"line":8,"character":10}},"severity":"error","code":"E2001","message":"[x] type mismatch: `String` vs `Int`","source":"sky"}"#
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn text_and_json_report_the_same_number_of_errors() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let src = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
x : Int\n\
x =\n    \"nope\"\n\n\n\
main =\n    println (1 + \"a\")\n";
    let dir = project("count", src, "");
    let text = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    let text_err = String::from_utf8_lossy(&text.stderr);
    let blocks = text_err.matches("-- TYPE ERROR ").count();
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    let s = check_stream(&o);
    assert!(blocks >= 2, "fixture has several errors: {text_err}");
    assert_eq!(
        s["errors"].as_u64().unwrap() as usize,
        blocks,
        "text printed {blocks} error blocks; json must report as many:\n{text_err}\n{:?}",
        o.lines
    );
    assert_eq!(text.status.code(), Some(o.code), "same exit code");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn parse_error_is_e0001_with_a_range() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let src = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println (\"unclosed\"\n";
    let dir = project("parse", src, "");
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    assert!(
        ds.iter()
            .any(|d| d["code"] == "E0001" && d["file"] == "src/Main.sky" && d["range"].is_object()),
        "a parse error diagnostic with a location: {ds:?}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_warning_does_not_fail_the_build() {
    if !required(Need::Go, have_go()) {
        return;
    }
    // A key no runtime section honours is reported as a warning, not dropped.
    let dir = project("warn", CLEAN, "\n[live]\nnot_a_real_key = 1\n");
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    let s = check_stream(&o);
    assert!(s["warnings"].as_u64().unwrap() >= 1, "{:?}", o.lines);
    let w = diags(&o)
        .into_iter()
        .find(|d| d["severity"] == "warning")
        .unwrap();
    assert!(
        w["message"].as_str().unwrap().contains("not_a_real_key"),
        "{w}"
    );
    assert!(w["file"].is_null() && w["range"].is_null(), "no span: {w}");
    // The text mode prints the same warning.
    let text = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert!(String::from_utf8_lossy(&text.stderr).contains("warning: "));
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_go_build_failure_is_reported_against_the_go_file() {
    if !required(Need::Go, have_go()) {
        return;
    }
    // A local Go module (a path dependency) that compiles when added, then is
    // broken: the Sky side still type-checks, and `go build` fails in the
    // dependency's own file.
    let base = scratch("gofail");
    let greet = base.join("greet");
    std::fs::create_dir_all(&greet).unwrap();
    std::fs::write(
        greet.join("go.mod"),
        "module example.com/greet\n\ngo 1.22\n",
    )
    .unwrap();
    std::fs::write(
        greet.join("greet.go"),
        "package greet\n\nfunc Hello(name string) string { return \"hi \" + name }\n",
    )
    .unwrap();
    let app = base.join("app");
    std::fs::create_dir_all(app.join("src")).unwrap();
    std::fs::write(
        app.join("sky.toml"),
        "name = \"gofail\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        app.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
import Example.Com.Greet as Greet\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Result as Result\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println (Greet.hello \"x\" |> Result.withDefault \"err\")\n",
    )
    .unwrap();
    let add = Command::new(SKY)
        .args(["add", "../greet"])
        .current_dir(&app)
        .output()
        .unwrap();
    assert!(
        add.status.success(),
        "{}",
        String::from_utf8_lossy(&add.stdout)
    );
    std::fs::write(
        greet.join("greet.go"),
        "package greet\n\nfunc Hello(name string) string { return 42 }\n",
    )
    .unwrap();
    let o = sky(&app, &["build", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let go: Vec<&Value> = diags(&o)
        .into_iter()
        .filter(|d| d["source"] == "go")
        .collect();
    assert!(!go.is_empty(), "a go-sourced diagnostic: {:?}", o.lines);
    let d = go[0];
    assert_eq!(d["severity"], "error");
    assert!(
        d["file"].as_str().unwrap().ends_with("greet/greet.go"),
        "points at the Go file that failed: {d}"
    );
    assert_eq!(
        d["range"]["start"]["line"], 2,
        "0-based line of `return 42`: {d}"
    );
    let _ = std::fs::remove_dir_all(&base);
}

const SUITE: &str = "module AppTest exposing (tests)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Test as Test exposing (Test)\n\n\n\
tests : List Test\n\
tests =\n    \
[ Test.test \"top\" (\\_ -> Test.equal 2 (1 + 1))\n    \
, Test.suite \"s\"\n        \
[ Test.test \"a\" (\\_ -> Test.equal 2 (1 + 1))\n        \
, Test.test \"b\" (\\_ -> Test.equal BAD (1 + 1))\n        \
]\n    \
]\n";

#[test]
fn sky_test_json_has_one_line_per_case() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("test", CLEAN, "");
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    let suite = dir.join("tests/AppTest.sky");

    // All pass → exit 0, three pass lines.
    std::fs::write(&suite, SUITE.replace("BAD", "2")).unwrap();
    let o = sky(&dir, &["test", "--format", "json", "tests/AppTest.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    let s = check_stream(&o).clone();
    let tests: Vec<&Value> = o.lines.iter().filter(|l| l["kind"] == "test").collect();
    assert_eq!(tests.len(), 3, "{:?}", o.lines);
    assert!(tests.iter().all(|t| t["status"] == "pass"));
    assert_eq!(s["total"], 3);
    assert_eq!(s["passed"], 3);
    assert_eq!(s["failed"], 0);
    assert_eq!(s["exitCode"], 0);
    let b = tests.iter().find(|t| t["fullName"] == "s > b").unwrap();
    assert_eq!(b["suite"], "s");
    assert_eq!(b["name"], "b");
    let top = tests.iter().find(|t| t["fullName"] == "top").unwrap();
    assert_eq!(
        top["suite"], "AppTest",
        "a top-level case belongs to the module"
    );
    // The suite's human `ok` lines went to stderr.
    assert!(o.stderr.contains("ok    top"), "{}", o.stderr);

    // One fails → exit 1, the failing line carries the message.
    std::fs::write(&suite, SUITE.replace("BAD", "3")).unwrap();
    let o = sky(&dir, &["test", "--format", "json", "tests/AppTest.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    let s = check_stream(&o);
    assert_eq!(s["failed"], 1);
    assert_eq!(s["passed"], 2);
    let failed: Vec<&Value> = o
        .lines
        .iter()
        .filter(|l| l["kind"] == "test" && l["status"] == "fail")
        .collect();
    assert_eq!(failed.len(), 1);
    assert!(failed[0]["message"].as_str().unwrap().contains('3'));

    // It does not build → exit 2 (nothing ran), the type error as a diagnostic.
    std::fs::write(&suite, SUITE.replace("BAD", "\"two\"")).unwrap();
    let o = sky(&dir, &["test", "--format", "json", "tests/AppTest.sky"]);
    assert_eq!(o.code, 2, "{}", o.stderr);
    let s = check_stream(&o);
    assert_eq!(s["total"], 0);
    assert!(
        diags(&o)
            .iter()
            .any(|d| d["code"] == "E2001" && d["file"] == "tests/AppTest.sky"),
        "{:?}",
        o.lines
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// F-12: `SKY_TEST_JSON=<path>` writes the per-case report in `--format
/// json` mode too, as in text mode. It used to be ignored there.
#[test]
fn sky_test_json_mode_still_writes_the_sky_test_json_report() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("testreport", CLEAN, "");
    std::fs::create_dir_all(dir.join("tests")).unwrap();
    std::fs::write(dir.join("tests/AppTest.sky"), SUITE.replace("BAD", "2")).unwrap();
    let report = dir.join("report.json");
    let out = Command::new(SKY)
        .args(["test", "--format", "json", "tests/AppTest.sky"])
        .env("SKY_TEST_JSON", &report)
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let text = std::fs::read_to_string(&report).expect("SKY_TEST_JSON report written");
    let v: Value = serde_json::from_str(&text).expect("the report is JSON");
    assert_eq!(v["cases"].as_array().map(Vec::len), Some(3), "{text}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn fmt_check_json_names_the_unformatted_file() {
    let dir = project("fmt", CLEAN, "");
    std::fs::write(
        dir.join("src/Ugly.sky"),
        "module Ugly exposing (x)\nimport Sky.Core.Prelude exposing (..)\nx = 1\n",
    )
    .unwrap();
    let o = sky(
        &dir,
        &[
            "fmt",
            "--check",
            "--format",
            "json",
            "src/Main.sky",
            "src/Ugly.sky",
        ],
    );
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    assert_eq!(ds.len(), 1, "{ds:?}");
    assert_eq!(ds[0]["file"], "src/Ugly.sky");
    // Without --check, json is refused (it would rewrite files silently).
    let o = Command::new(SKY)
        .args(["fmt", "--format", "json", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert_eq!(o.status.code(), Some(2));
    let _ = std::fs::remove_dir_all(&dir);
}

/// F-8: `sky fmt` on a file that does not parse is an error in every mode:
/// `--check` exits 1 with an `[E0001]` diagnostic (json: one error, `ok:
/// false`), write mode exits non-zero and leaves the file alone, and `--stdin`
/// exits non-zero. It used to pass as "already formatted".
#[test]
fn fmt_refuses_a_file_that_does_not_parse() {
    let broken = format!("{CLEAN}\nbroken = (\n");
    let dir = project("fmtparse", &broken, "");
    let o = sky(
        &dir,
        &["fmt", "--check", "--format", "json", "src/Main.sky"],
    );
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    assert!(!ds.is_empty(), "a parse error diagnostic: {:?}", o.lines);
    assert!(
        ds.iter()
            .all(|d| d["severity"] == "error" && d["code"] == "E0001"),
        "{ds:?}"
    );
    assert_eq!(ds[0]["file"], "src/Main.sky");
    let text = Command::new(SKY)
        .args(["fmt", "--check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert_eq!(text.status.code(), Some(1));
    let stderr = String::from_utf8_lossy(&text.stderr);
    assert!(
        stderr.contains("Fix: correct the syntax error")
            && stderr.contains("docs/migration/v0.27.md#fmt-refuses-a-file-that-does-not-parse"),
        "{stderr}"
    );
    let write = Command::new(SKY)
        .args(["fmt", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert!(
        !write.status.success(),
        "write mode must fail on a parse error"
    );
    assert_eq!(
        std::fs::read_to_string(dir.join("src/Main.sky")).unwrap(),
        broken,
        "the file is left as it was"
    );
    let mut child = Command::new(SKY)
        .args(["fmt", "--stdin"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    {
        use std::io::Write;
        child
            .stdin
            .take()
            .unwrap()
            .write_all(broken.as_bytes())
            .unwrap();
    }
    let out = child.wait_with_output().unwrap();
    assert!(!out.status.success(), "--stdin must fail on a parse error");
    let _ = std::fs::remove_dir_all(&dir);
}

/// F-15: a failure the command reports only on stderr (a missing entry, an
/// unknown target) reaches the json stream with its real message, and a
/// source file that is not UTF-8 is named rather than reported as "no .sky".
#[test]
fn json_failures_carry_the_real_message() {
    let dir = project("realmsg", CLEAN, "");
    let o = sky(&dir, &["check", "--format", "json", "src/Nope.sky"]);
    assert_ne!(o.code, 0);
    check_stream(&o);
    let msg = diags(&o)[0]["message"].as_str().unwrap().to_string();
    assert!(msg.contains("src/Nope.sky"), "the real message: {msg}");
    let o = sky(
        &dir,
        &[
            "check",
            "--format",
            "json",
            "--target",
            "nope:nope",
            "src/Main.sky",
        ],
    );
    assert_ne!(o.code, 0);
    check_stream(&o);
    let msg = diags(&o)[0]["message"].as_str().unwrap().to_string();
    assert!(msg.contains("nope"), "the real message: {msg}");
    std::fs::write(
        dir.join("src/Main.sky"),
        b"module Main exposing (main)\n\xff\xfe\n",
    )
    .unwrap();
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_ne!(o.code, 0);
    check_stream(&o);
    let msg = diags(&o)[0]["message"].as_str().unwrap().to_string();
    assert!(
        msg.contains("src/Main.sky") && msg.contains("not valid UTF-8"),
        "the file is named: {msg}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn an_unknown_format_is_a_usage_error() {
    let dir = project("badfmt", CLEAN, "");
    let o = Command::new(SKY)
        .args(["check", "--format", "xml", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert_eq!(o.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&o.stderr).contains("expected `text` or `json`"));
    let _ = std::fs::remove_dir_all(&dir);
}

/// Copy a checked-in fixture project to a fresh temp dir.
fn copy_fixture(name: &str, tag: &str) -> PathBuf {
    let src = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures")
        .join(name);
    let dst = scratch(tag);
    let _ = std::fs::remove_dir_all(&dst);
    let ok = Command::new("cp")
        .arg("-R")
        .arg(&src)
        .arg(&dst)
        .status()
        .expect("cp -R")
        .success();
    assert!(ok, "copy {}", src.display());
    dst
}

/// A dispatched `Std.App` entry is checked through a derived child `sky check`.
/// In json mode the child runs with `--format json` and its lines are relayed;
/// the `HasFallback vs NoFallback` phantom from generated code becomes the one
/// actionable diagnostic, exactly as the text mode remaps it.
#[test]
fn std_app_check_relays_the_child_diagnostics() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let good = copy_fixture("std-app-dispatch", "stdapp-ok");
    let o = sky(&good, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    let s = check_stream(&o);
    assert_eq!(s["errors"], 0, "{:?}", o.lines);

    // No `withNotFound`, checked for the default `web` target.
    let bad = copy_fixture("std-app-terminal", "stdapp-fallback");
    let o = sky(&bad, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    assert_eq!(ds.len(), 1, "one remapped diagnostic, no phantom: {ds:?}");
    let m = ds[0]["message"].as_str().unwrap();
    assert!(
        m.contains("requires a fallback page") && !m.contains("HasFallback"),
        "{m}"
    );
    let _ = std::fs::remove_dir_all(&good);
    let _ = std::fs::remove_dir_all(&bad);
}

/// A lowering WARNING carries the span of the node being lowered: the
/// memoised-CAF lint points at the definition's name.
#[test]
fn a_lowering_warning_has_a_file_and_range() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let src = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Result as Result\n\
import Sky.Core.Task as Task\n\
import Sky.Core.Uuid as Uuid\n\
import Std.Log exposing (println)\n\n\n\
stamp : String\n\
stamp =\n    Task.run Uuid.v4 |> Result.withDefault \"\"\n\n\n\
main =\n    println stamp\n";
    let dir = project("lowerwarn", src, "");
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    check_stream(&o);
    let w = diags(&o)
        .into_iter()
        .find(|d| {
            d["severity"] == "warning"
                && d["message"]
                    .as_str()
                    .unwrap()
                    .contains("memoised to a SINGLE value")
        })
        .unwrap_or_else(|| panic!("the CAF lint: {:?}", o.lines));
    assert_eq!(w["file"], "src/Main.sky", "{w}");
    // 0-based line 10 is `stamp =`, the definition's name.
    assert_eq!(w["range"]["start"]["line"], 10, "{w}");
    assert_eq!(w["range"]["start"]["character"], 0, "{w}");
    // The text mode prints the same location.
    let text = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    assert!(
        String::from_utf8_lossy(&text.stderr)
            .contains("warning: src/Main.sky:11:1: top-level `stamp`"),
        "{}",
        String::from_utf8_lossy(&text.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A lowering ERROR carries the span of the expression being lowered: a call
/// into a Go package with no generated FFI surface points at the call.
#[test]
fn a_lowering_error_has_a_file_and_range() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let src = "module Main exposing (main)\n\n\
import Github.Com.Nope.Pkg as P\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println (P.thing 1)\n";
    let dir = project("lowererr", src, "");
    let o = sky(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let e = diags(&o)
        .into_iter()
        .find(|d| d["severity"] == "error")
        .unwrap();
    assert!(
        e["message"]
            .as_str()
            .unwrap()
            .contains("no generated FFI surface"),
        "{e}"
    );
    assert_eq!(e["file"], "src/Main.sky", "{e}");
    // 0-based line 8, the parenthesised `(P.thing 1)` call inside `println`:
    // it starts at character 12, the `(` (the node's leading space is not
    // part of the range).
    assert_eq!(e["range"]["start"]["line"], 8, "{e}");
    assert_eq!(e["range"]["start"]["character"], 12, "{e}");
    assert_eq!(e["range"]["end"]["character"], 23, "{e}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky build --format json` of a Sky.Spa entry (auto-split into a wasm
/// frontend and a native backend): a clean build is one ok summary; an error
/// in the app's own source is reported against `src/…` (checked before the
/// split); a leg's diagnostic is relayed with its `half`, against the app's own
/// file when the leg compiled a byte-identical copy of it.
#[test]
fn spa_build_json_reports_every_diagnostic() {
    if !required(Need::Go, have_go()) {
        return;
    }
    // Clean.
    let clean = copy_fixture("spa-split-multimodule", "spa-clean");
    let o = sky(&clean, &["build", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    let s = check_stream(&o);
    assert_eq!(s["errors"], 0, "{:?}", o.lines);
    let _ = std::fs::remove_dir_all(&clean);

    // A type error in a shared module.
    let bad = copy_fixture("spa-split-multimodule", "spa-type");
    let mut domain = std::fs::read_to_string(bad.join("src/Domain.sky")).unwrap();
    // Appended after the file's last line: two blank lines, `bad : Int`,
    // `bad =`, then `    "x"`, the 0-based line the error points at.
    let line = domain.lines().count() + 4;
    domain.push_str("\n\nbad : Int\nbad =\n    \"x\"\n");
    std::fs::write(bad.join("src/Domain.sky"), domain).unwrap();
    let o = sky(&bad, &["build", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    let e = ds
        .iter()
        .find(|d| d["code"] == "E2001")
        .unwrap_or_else(|| panic!("the type error: {ds:?}"));
    assert_eq!(e["file"], "src/Domain.sky", "{e}");
    assert_eq!(e["range"]["start"]["line"], line, "{e}");
    assert!(
        !ds.iter().any(|d| d["message"]
            .as_str()
            .unwrap()
            .contains("human-readable report")),
        "a real diagnostic, not the pointer to stderr: {ds:?}"
    );
    let _ = std::fs::remove_dir_all(&bad);

    // A warning from the backend leg (a memoised CAF in the backend-only
    // `Store` module), relayed with `half` and mapped to the app's own file.
    let warn = copy_fixture("spa-split-multimodule", "spa-leg");
    let store = std::fs::read_to_string(warn.join("src/Store.sky"))
        .unwrap()
        .replace(
            "import Domain exposing (..)\n",
            "import Domain exposing (..)\nimport Sky.Core.Uuid as Uuid\n",
        )
        .replace(
            "    \"todos.json\"",
            "    \"todos-\" ++ (Task.run Uuid.v4 |> Result.withDefault \"\") ++ \".json\"",
        );
    std::fs::write(warn.join("src/Store.sky"), store).unwrap();
    let o = sky(&warn, &["build", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 0, "{}", o.stderr);
    check_stream(&o);
    let w = diags(&o)
        .into_iter()
        .find(|d| d["severity"] == "warning")
        .unwrap_or_else(|| panic!("the relayed leg warning: {:?}", o.lines));
    assert_eq!(w["half"], "backend", "{w}");
    assert_eq!(w["file"], "src/Store.sky", "{w}");
    assert!(w["range"].is_object(), "{w}");
    let _ = std::fs::remove_dir_all(&warn);
}

/// A Sky.Spa split that refuses the app as a whole is reported as its own
/// diagnostic (unlocated: the split names no source node), not as a pointer to
/// stderr.
#[test]
fn spa_split_refusal_is_its_own_diagnostic() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = copy_fixture("spa-split-multimodule", "spa-refuse");
    let domain = std::fs::read_to_string(dir.join("src/Domain.sky"))
        .unwrap()
        .replace(
            "import Std.Codec as Codec exposing (Codec)\n",
            "import Std.Codec as Codec exposing (Codec)\n\
             import Sky.Core.Result as Result\n\
             import Sky.Core.Task as Task\n\
             import Sky.Core.Uuid as Uuid\n",
        )
        + "\n\nstamp : String\nstamp =\n    Task.run Uuid.v4 |> Result.withDefault \"\"\n";
    std::fs::write(dir.join("src/Domain.sky"), domain).unwrap();
    let main = std::fs::read_to_string(dir.join("src/Main.sky"))
        .unwrap()
        .replace(
            "( { todos = [], draft = \"\" }, Cmd.none )",
            "( { todos = [], draft = stamp }, Cmd.none )",
        );
    std::fs::write(dir.join("src/Main.sky"), main).unwrap();
    let o = sky(&dir, &["build", "--format", "json", "src/Main.sky"]);
    assert_eq!(o.code, 1, "{}", o.stderr);
    check_stream(&o);
    let ds = diags(&o);
    assert!(
        ds.iter().any(|d| d["severity"] == "error"
            && d["message"].as_str().unwrap().contains("cannot auto-split")),
        "{ds:?}"
    );
    assert!(
        !ds.iter().any(|d| d["message"]
            .as_str()
            .unwrap()
            .contains("human-readable report")),
        "{ds:?}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky test --format json` and `sky check --format json` name the file a
/// parse or type error is in, for a module under `src/`, a helper under
/// `tests/`, and a Sky path dependency — with a module that sorts before the
/// broken one, the case in which `sky test` used to name ANOTHER file.
#[test]
fn diagnostics_name_their_own_file_for_every_module_kind() {
    let root = scratch("ownfile");
    let dir = root.join("app");
    let dep = root.join("dep");
    for d in [dir.join("src"), dir.join("tests"), dep.join("src")] {
        std::fs::create_dir_all(d).unwrap();
    }
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ownfile\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n\n\
         [dependencies]\n\"widgets\" = { path = \"../dep\" }\n",
    )
    .unwrap();
    std::fs::write(
        dep.join("sky.toml"),
        "name = \"widgets\"\nversion = \"0.1.0\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    let module = |name: &str, value: &str| {
        format!(
            "module {name} exposing (v)\n\n\
             import Sky.Core.Prelude exposing (..)\n\n\n\
             v : Int\n\
             v =\n    {value}\n"
        )
    };
    let suite = |name: &str, import: &str| {
        format!(
            "module {name} exposing (tests)\n\n\
             import {import}\n\
             import Sky.Core.Prelude exposing (..)\n\
             import Sky.Test as Test exposing (Test)\n\n\n\
             tests : List Test\n\
             tests =\n    \
             [ Test.test \"v\" (\\_ -> Test.equal 2 {import}.v) ]\n"
        )
    };
    let w = |p: PathBuf, s: String| std::fs::write(p, s).unwrap();
    w(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Lib\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Widget\n\n\
         main =\n    println (String.fromInt (Lib.v + Widget.v))\n"
            .to_string(),
    );
    w(dir.join("src/Aaa.sky"), module("Aaa", "1"));
    w(dir.join("src/Lib.sky"), module("Lib", "(1 * 2))"));
    w(dir.join("tests/Aab.sky"), module("Aab", "1"));
    w(dir.join("tests/Helper.sky"), module("Helper", "[ 2 ]]"));
    w(dep.join("src/Aac.sky"), module("Aac", "1"));
    w(dep.join("src/Widget.sky"), module("Widget", "1 ++ \"x\""));
    w(dir.join("tests/LibTest.sky"), suite("LibTest", "Lib"));
    w(
        dir.join("tests/HelperTest.sky"),
        suite("HelperTest", "Helper"),
    );
    w(
        dir.join("tests/WidgetTest.sky"),
        suite("WidgetTest", "Widget"),
    );

    let files = |o: &Out| -> Vec<(String, String, u64)> {
        diags(o)
            .iter()
            .filter(|d| d["severity"] == "error")
            .map(|d| {
                (
                    d["file"].as_str().unwrap_or("").to_string(),
                    d["code"].as_str().unwrap_or("").to_string(),
                    d["range"]["start"]["line"].as_u64().unwrap_or(0),
                )
            })
            .collect()
    };
    for (s, want) in [
        ("tests/LibTest.sky", ("src/Lib.sky", "E0001")),
        ("tests/HelperTest.sky", ("tests/Helper.sky", "E0001")),
        ("tests/WidgetTest.sky", ("../dep/src/Widget.sky", "E2001")),
    ] {
        let o = sky(&dir, &["test", s, "--format", "json"]);
        check_stream(&o);
        assert_eq!(o.code, 2, "{s}: {:?}\n{}", o.lines, o.stderr);
        assert_eq!(
            files(&o),
            vec![(want.0.to_string(), want.1.to_string(), 7)],
            "{s}: the error names its own file (0-based line 7)"
        );
    }
    // `sky check` stops at the parse error in src/ (the gate runs before the
    // type check); fixed, it reaches the path dependency's type error.
    let o = sky(&dir, &["check", "src/Main.sky", "--format", "json"]);
    check_stream(&o);
    assert_eq!(
        files(&o),
        vec![("src/Lib.sky".to_string(), "E0001".to_string(), 7)]
    );
    w(dir.join("src/Lib.sky"), module("Lib", "2"));
    let o = sky(&dir, &["check", "src/Main.sky", "--format", "json"]);
    check_stream(&o);
    assert_eq!(
        files(&o),
        vec![("../dep/src/Widget.sky".to_string(), "E2001".to_string(), 7)]
    );
    let _ = std::fs::remove_dir_all(&root);
}
