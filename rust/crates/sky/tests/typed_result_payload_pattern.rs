//! Regression: a `case` on a Result / Maybe whose Go type is already typed
//! reads the payload field directly, without a runtime narrowing (v0.27.0,
//! Phase 7 part A).
//!
//! `_subj` is bound with `:=` to the lowered subject's exact Go type, so for
//! `_subj : rt.SkyResult[Sky_Core_Error_Error, int]` the field
//! `_subj.ErrValue` already IS a `Sky_Core_Error_Error` and `_subj.OkValue`
//! already IS an `int`. `bind_field_pat` built the selector as `any`
//! regardless and wrapped it in a `GenericErase` Coerce, so the emitted Go
//! boxed a typed value into `any` and asserted it straight back:
//!
//! ```go
//! v_1 := /* generic erase */ rt.AsInt(_subj.OkValue)
//! v_2 := /* generic erase */ rt.Coerce[Sky_Core_Error_Error](_subj.ErrValue)
//! ```
//!
//! doc 14 origin R8, lever §5.2, closeable by §1 (both shapes are known at
//! emit time and are equal). The coerce-floor golden carries the drop.
//!
//! Two legs, per the doctrine in `cli_verb_flow.rs`: the EMITTED-GO leg always
//! runs (`sky build` writes `sky-out/main.go` before `go build`); the RUN leg
//! needs a Go toolchain and fails, never skips, without one.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const SRC: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.Error as Error\n\
     import Sky.Core.String as String\n\
     import Std.Log exposing (println)\n\n\
     parse : String -> Result Error Int\n\
     parse s =\n\
     \x20   case String.toInt s of\n\
     \x20       Just n ->\n            Ok n\n\n\
     \x20       Nothing ->\n            Err (Error.invalidInput (\"not a number: \" ++ s))\n\n\
     describe : String -> String\n\
     describe s =\n\
     \x20   case parse s of\n\
     \x20       Ok n ->\n            \"ok \" ++ String.fromInt (n + 1)\n\n\
     \x20       Err e ->\n            \"err \" ++ Error.toString e\n\n\
     firstName : Maybe String -> String\n\
     firstName m =\n\
     \x20   case m of\n\
     \x20       Just n ->\n            n\n\n\
     \x20       Nothing ->\n            \"anon\"\n\n\
     main =\n\
     \x20   println (describe \"41\" ++ \" / \" ++ describe \"x\" ++ \" / \" ++ firstName (Just \"ada\"))\n";

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-typedpayload-{tag}-{}-{}",
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
        "name = \"typedpayload\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), SRC).unwrap();
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

/// The Go body of `func <name>(` in `src`, up to the closing brace column-0.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let needle = format!("func {name}(");
    let at = src
        .find(&needle)
        .unwrap_or_else(|| panic!("emitted Go must define {name}:\n{src}"));
    let rest = &src[at..];
    let end = rest.find("\n}").map(|i| i + 2).unwrap_or(rest.len());
    &rest[..end]
}

#[test]
fn typed_payload_fields_are_read_without_a_narrowing() {
    let dir = scratch("emit");
    let log = build(&dir);
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (log:\n{log})"
    );
    let go = std::fs::read_to_string(&main_go).unwrap();

    let describe = func_body(&go, "Main_describe");
    assert!(
        describe.contains("rt.SkyResult[Sky_Core_Error_Error, int]")
            || go.contains("func Main_parse(v_0 string) rt.SkyResult[Sky_Core_Error_Error, int]"),
        "the fixture's premise: `parse` returns a typed Result:\n{go}"
    );
    for bad in [
        "rt.AsInt(_subj.OkValue)",
        "rt.Coerce[Sky_Core_Error_Error](_subj.ErrValue)",
    ] {
        assert!(
            !describe.contains(bad),
            "`{bad}` re-narrows a field whose Go type is already known:\n{describe}"
        );
    }
    assert!(
        describe.contains("_subj.OkValue") && describe.contains("_subj.ErrValue"),
        "both payloads are still read, directly:\n{describe}"
    );

    let first = func_body(&go, "Main_firstName");
    assert!(
        !first.contains("rt.AsString(_subj.JustValue)"),
        "`rt.AsString(_subj.JustValue)` re-narrows a `string` field:\n{first}"
    );
    assert!(first.contains("_subj.JustValue"), "{first}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn typed_payload_patterns_build_and_compute() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("run");
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log:\n{log})");
    let out = Command::new(&bin)
        .current_dir(&dir)
        .output()
        .expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "output:\n{combined}");
    assert!(
        combined.contains("ok 42 / err InvalidInput: not a number: x / ada"),
        "output:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
