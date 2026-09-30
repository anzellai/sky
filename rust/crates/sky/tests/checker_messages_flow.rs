//! End-to-end regressions for checker diagnostics (v0.27.0 audit stream S3d):
//! what `sky check` prints, and that a program it accepts builds.
//!
//! * C-13 — a recursive `type alias` is refused at its declaration with
//!   `[E1016]`, never reaching `go build` (`invalid recursive type`).
//! * C-14 — `import Sky.Core.Prelude exposing (String, Int)` is accepted.
//! * C-15 — `import Page.B` makes `Page.B.x` a valid qualified reference.
//! * C-16 — `[E1004]` names the file and line of the shadowing declaration.
//! * E-18 — one type error is printed once, not once per `case` arm.

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

fn project(tag: &str, files: &[(&str, &str)]) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-checkmsg-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"checkmsg\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    for (rel, src) in files {
        let p = dir.join("src").join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, src).unwrap();
    }
    dir
}

/// Run `sky <verb> src/Main.sky`; (success, stdout+stderr).
fn sky(dir: &Path, verb: &str) -> (bool, String) {
    let out = Command::new(SKY)
        .args([verb, "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

const PRELUDE: &str = "import Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n";

// ---------------------------------------------------------------- C-13

#[test]
fn recursive_alias_used_as_a_value_is_e1016_not_a_go_build_failure() {
    let main = format!(
        "module Main exposing (main)\n\n{PRELUDE}\n\
         type alias Node =\n    {{ name : String, next : Maybe Node }}\n\n\
         leaf : Node\nleaf =\n    {{ name = \"b\", next = Nothing }}\n\n\
         main =\n    println leaf.name\n"
    );
    let dir = project("c13-value", &[("Main.sky", &main)]);
    let (ok, log) = sky(&dir, "check");
    assert!(!ok, "a recursive alias must be refused:\n{log}");
    assert!(log.contains("[E1016]"), "{log}");
    assert!(
        log.contains("docs/migration/v0.27.md#recursive-type-alias"),
        "{log}"
    );
    assert!(
        !log.contains("go build"),
        "must be refused before codegen:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn recursive_alias_used_by_a_recursive_function_is_e1016() {
    let main = format!(
        "module Main exposing (main)\n\n{PRELUDE}import Sky.Core.String as String\n\n\
         type alias Node =\n    {{ name : String, next : Maybe Node }}\n\n\
         depth : Node -> Int\ndepth n =\n    case n.next of\n        Just m ->\n            1 + depth m\n\n        Nothing ->\n            1\n\n\
         main =\n    println (String.fromInt (depth {{ name = \"a\", next = Nothing }}))\n"
    );
    let dir = project("c13-fn", &[("Main.sky", &main)]);
    let (ok, log) = sky(&dir, "check");
    assert!(!ok, "{log}");
    assert!(
        log.contains("[E1016]"),
        "the user must see the real cause, not a `record vs Node` mismatch:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------- C-14

#[test]
fn prelude_exposing_builtin_types_checks_and_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let main = "module Main exposing (main)\n\n\
                import Sky.Core.Prelude exposing (Result(..), String, Int)\n\
                import Std.Log exposing (println)\n\n\
                type alias R =\n    { name : String, n : Int }\n\n\
                r : R\nr =\n    { name = \"ok\", n = 1 }\n\n\
                main =\n    println r.name\n";
    let dir = project("c14", &[("Main.sky", main)]);
    let (ok, log) = sky(&dir, "check");
    assert!(ok, "{log}");
    assert!(!log.contains("E1012"), "{log}");
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------- C-15

#[test]
fn full_module_path_qualifier_checks_builds_and_runs() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let page = "module Page.B exposing (Model, Msg(..), init, label)\n\n\
                import Sky.Core.Prelude exposing (..)\n\n\
                type alias Model =\n    { title : String }\n\n\
                type Msg\n    = Rename String\n\n\
                init : Model\ninit =\n    { title = \"t\" }\n\n\
                label : Model -> String\nlabel m =\n    \"L:\" ++ m.title\n";
    let main = format!(
        "module Main exposing (main)\n\n{PRELUDE}import Page.B\n\n\
         step : Page.B.Msg -> Page.B.Model -> Page.B.Model\n\
         step msg m =\n    case msg of\n        Page.B.Rename s ->\n            {{ m | title = s }}\n\n\
         main =\n    println (Page.B.label (step (Page.B.Rename \"x\") Page.B.init) ++ Page.B.init.title ++ B.label B.init)\n"
    );
    let dir = project("c15", &[("Page/B.sky", page), ("Main.sky", &main)]);
    let (ok, log) = sky(&dir, "build");
    assert!(ok, "`Page.B.x` after `import Page.B` is valid Elm:\n{log}");
    let out = Command::new(dir.join("sky-out").join("app"))
        .current_dir(&dir)
        .output()
        .expect("run app");
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("L:xtL:t"), "{text}");
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------- C-16

#[test]
fn e1004_names_the_file_and_line_of_the_shadowing_type() {
    let api = "module Github.Api exposing (..)\n\n\
               import Sky.Core.Prelude exposing (..)\n\n\
               type Error\n    = NotFound\n\n\
               code : Int\ncode =\n    1\n";
    let main = format!(
        "module Main exposing (main)\n\n{PRELUDE}import Github.Api as Api\nimport Sky.Core.String as String\n\n\
         main =\n    println (String.fromInt Api.code)\n"
    );
    let dir = project("c16", &[("Github/Api.sky", api), ("Main.sky", &main)]);
    let (ok, log) = sky(&dir, "check");
    assert!(!ok, "{log}");
    assert!(
        log.contains("src/Github/Api.sky:5:6 [E1004]"),
        "the [E1004] header must carry the file and line:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------- E-18

#[test]
fn one_type_error_is_printed_once() {
    let main = format!(
        "module Main exposing (main)\n\n{PRELUDE}import Sky.Core.Crypto as Crypto\n\
         import Sky.Core.Secret as Secret exposing (Secret)\n\n\
         seal : Secret -> String -> String\nseal aesKey plain =\n\
         \x20   case Crypto.aesGcmEncrypt aesKey plain of\n        Ok s ->\n            s\n\n        Err _ ->\n            \"err\"\n\n\
         main =\n    println (seal (Secret.fromString \"0123456789abcdef0123456789abcdef\") \"hi\")\n"
    );
    let dir = project("e18", &[("Main.sky", &main)]);
    let (ok, log) = sky(&dir, "check");
    assert!(!ok, "{log}");
    assert_eq!(
        log.matches("[E2001]").count(),
        1,
        "one mismatch, one diagnostic:\n{log}"
    );
    assert!(
        log.contains("docs/migration/v0.27.md#aead-encrypt-is-a-task"),
        "the AEAD-is-a-Task migration hint:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
