//! Regression: a Go FFI type named like one of the app's own types (v0.27.0).
//!
//! `useIt : GoHttp.Client -> String` under `import Net.Http as GoHttp`, in an
//! app that also declares `type alias Client = { name : String }`. The checker
//! resolved `GoHttp.Client` by falling back to its bare name, so it became the
//! app's record: `useIt { name = "x" }` type-checked, and passing the real Go
//! value (`GoHttp.defaultClient ()`) built and then panicked at run time with
//! `CoerceFailure: expected main.Main_Client_R, got *http.Client`. The
//! lowering made the same choice (the current module's `Client`).
//!
//! A Go FFI type now has its own identity in the checker and the Go-opaque
//! shape (`any`) in the lowering. This drives a real Go-stdlib binding
//! (`net/http`, no network needed) end to end:
//!   * the Go value flows from one FFI call, through an annotated helper, into
//!     another FFI call, and the program RUNS (it used to panic);
//!   * the app record passed where the Go type is expected is REJECTED by
//!     `sky check` (it used to check clean).
//!
//! The floor-touching change (the Go FFI boundary, doc 14 §4.1) was
//! authorised by the user on 2026-09-29 for this fix only.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

const HEAD: &str = "module Main exposing (main)\n\n\
import Net.Http as GoHttp\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Result as Result\n\
import Std.Log exposing (println)\n\n\n\
type alias Client =\n    { name : String }\n\n\n\
useIt : GoHttp.Client -> Result Error String\n\
useIt c =\n    \
GoHttp.clientCloseIdleConnections c |> Result.map (\\_ -> \"closed\")\n\n\n";

const RUNS: &str = "mine : Client\n\
mine =\n    { name = \"mine\" }\n\n\n\
main =\n    \
case GoHttp.defaultClient () |> Result.andThen useIt of\n        \
Ok s ->\n            \
println (s ++ \" \" ++ mine.name)\n\n        \
Err _ ->\n            \
println \"err\"\n";

const REJECTED: &str = "main =\n    \
case useIt { name = \"x\" } of\n        \
Ok s ->\n            \
println s\n\n        \
Err _ ->\n            \
println \"err\"\n";

fn scratch_project(tag: &str, body: &str) -> PathBuf {
    let uniq = format!(
        "sky-ffi-go-type-{tag}-{}-{}",
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
        "name = \"ffi-go-type\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"net/http\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), format!("{HEAD}{body}")).unwrap();
    dir
}

fn run(dir: &Path, prog: &str, args: &[&str]) -> (bool, String) {
    let out = Command::new(prog)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

#[test]
fn a_go_value_flows_through_an_annotated_helper_and_runs() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch_project("runs", RUNS);
    let (ok, log) = run(&dir, SKY, &["install"]);
    assert!(ok, "sky install (net/http) failed:\n{log}");
    let (ok, log) = run(&dir, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "sky build failed:\n{log}");
    let app = dir.join("sky-out").join("app");
    let (ok, out) = run(&dir, app.to_str().unwrap(), &[]);
    assert!(
        ok && !out.contains("CoerceFailure"),
        "the Go client must reach the second FFI call without a panic:\n{out}"
    );
    assert!(out.contains("closed mine"), "got:\n{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn an_app_record_where_the_go_type_is_expected_is_rejected() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch_project("rejected", REJECTED);
    let (ok, log) = run(&dir, SKY, &["install"]);
    assert!(ok, "sky install (net/http) failed:\n{log}");
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(
        !ok,
        "a record is not a Go *http.Client, must be rejected:\n{log}"
    );
    assert!(
        log.contains("[E2001]") && log.contains("Client"),
        "a type error naming the Go type:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
