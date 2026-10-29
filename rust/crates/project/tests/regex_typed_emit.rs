//! v0.29.0 regression for two runtime-narrowing increases that came with the
//! compiled `Regex` and `App.withPersistExclude` (`xtask coerce-floor`).
//!
//! 1. A compiled Regex is built, defaulted and read with no runtime narrowing.
//!    `Regex.fromString` over a proven String goes to `rt.Regex_fromStringT`,
//!    which returns `rt.SkyResult[Sky_Core_Error_Error, Sky_Core_Regex_Regex]`
//!    (doc 14 origin R5, the kernel return, lever §5.3). `Regex.never` in a
//!    `Regex` slot is `rt.Regex_neverT()` (R5, §5.3). `Result.withDefault` over
//!    a proven `rt.SkyResult[E, A]` is `rt.Result_withDefaultT[E, A]` (R12, the
//!    caller cost of the erased polymorphic def, and R5 on the Prelude kernel
//!    path), so its result needs no `rt.Coerce[Sky_Core_Regex_Regex]`.
//!
//! 2. The persist-exclude field list is not a field of the `App` record. Every
//!    `(App a)` unwrap of that unsealed stdlib ADT narrows each payload field
//!    (R6), so a `List String` field cost one `rt.AsListT[string]` per builder
//!    in every `Std.App` program. It travels in the web config instead
//!    (`WebOpts.persistExclude`), which is narrowed once as a whole.

use project::emit_example_source;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

fn scratch(tag: &str, main: &str) -> PathBuf {
    let uniq = format!(
        "sky-regex-emit-{tag}-{}-{}",
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
        "name = \"regex-emit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    dir
}

fn emit(tag: &str, main: &str) -> String {
    let dir = scratch(tag, main);
    let out = emit_example_source(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    out.unwrap_or_else(|e| panic!("emit failed: {e}"))
}

/// The emitted Go function `name`, from its `func` line to the next top-level
/// declaration.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let head = format!("func {name}(");
    let start = src
        .find(&head)
        .unwrap_or_else(|| panic!("{name} is emitted:\n{src}"));
    let rest = &src[start..];
    let end = rest[1..]
        .find("\nfunc ")
        .or_else(|| rest[1..].find("\nvar "))
        .map(|i| i + 1)
        .unwrap_or(rest.len());
    &rest[..end]
}

const REGEX_PRELUDE: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Regex as Regex exposing (Regex)\n\
import Sky.Core.Error exposing (Error)\n\
import Std.Log exposing (println)\n\n\n\
number : Regex\n\
number =\n    Regex.fromString \"^[0-9]+$\" |> Result.withDefault Regex.never\n\n\n\
compiled : Result Error Regex -> Regex\n\
compiled r =\n    Result.withDefault Regex.never r\n\n\n\
main =\n    \
let\n        \
_ = println (if Regex.contains number \"12\" then \"yes\" else \"no\")\n    \
in\n    \
println (if Regex.contains (compiled (Regex.fromString \"a\")) \"a\" then \"yes\" else \"no\")\n";

const REGEX_DEF: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Regex as Regex exposing (Regex)\n\
import Sky.Core.Result as Result\n\
import Std.Log exposing (println)\n\n\n\
number : Regex\n\
number =\n    Regex.fromString \"^[0-9]+$\" |> Result.withDefault Regex.never\n\n\n\
main =\n    println (if Regex.contains number \"12\" then \"yes\" else \"no\")\n";

#[test]
fn a_compiled_regex_is_built_and_defaulted_with_no_narrowing() {
    for (tag, main, funcs) in [
        (
            "prelude",
            REGEX_PRELUDE,
            &["Main_number__cafInit", "Main_compiled"][..],
        ),
        ("def", REGEX_DEF, &["Main_number__cafInit"][..]),
    ] {
        let src = emit(tag, main);
        for f in funcs {
            let body = func_body(&src, f);
            assert!(
                body.contains("rt.Result_withDefaultT["),
                "{tag} {f}:\n{body}"
            );
            assert!(body.contains("rt.Regex_neverT()"), "{tag} {f}:\n{body}");
            assert!(
                !body.contains("rt.Coerce[") && !body.contains("rt.ResultCoerce"),
                "{tag} {f} narrows nothing:\n{body}"
            );
        }
        let body = func_body(&src, "Main_number__cafInit");
        assert!(body.contains("rt.Regex_fromStringT("), "{tag}:\n{body}");
    }
}

const APP: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.App as App\n\
import Std.Cmd as Cmd exposing (Cmd)\n\
import Std.Sub as Sub exposing (Sub)\n\n\n\
init _ =\n    ( 0, Cmd.none )\n\n\n\
update : Int -> Int -> ( Int, Cmd Int )\n\
update msg model =\n    ( model + msg, Cmd.none )\n\n\n\
view : Int -> String\n\
view model =\n    String.fromInt model\n\n\n\
main =\n    \
App.cli { init = init, update = update, view = view, subscriptions = \\_ -> Sub.none }\n        \
|> App.withPersistExclude [ \"count\" ]\n        \
|> App.run\n";

#[test]
fn the_app_record_carries_no_persist_exclude_field() {
    let src = emit("app", APP);
    assert!(
        !src.contains("PersistExclude any") && !src.contains("PersistExclude []string;"),
        "the App payload has no PersistExclude field (one rt.AsListT per unwrap)"
    );
    assert!(
        !src.contains("PersistExclude: rt.AsListT"),
        "no per-unwrap narrowing of the exclude list"
    );
}
