//! v0.32.0 regression (docs/design/let-bind-lines.md §4): a hand-written
//! nested `Maybe.andThen` / `Result.andThen` and the same chain written with
//! bind lines lower with no runtime narrowing that the v0.31 compiler did not
//! emit.
//!
//! The annotation `Sky.Core.Maybe.andThen : (a -> Maybe b) -> Maybe a -> Maybe b`
//! (and `Result.andThen`) makes each call site type its continuation at the
//! instance (`func(int) rt.SkyMaybe[int]`). The def itself still erases to
//! `any` (doc 14 R12). With the annotation alone `func_shape_eta` wrapped every
//! continuation: an `rt.AsInt` per parameter (R2), an `rt.MaybeCoerce[any]` per
//! result and an `rt.MaybeCoerce[int]` round trip on each inner call (R1).
//! Both shapes are known at emit time (doc 14 §1), so a proven call goes to
//! the typed twin `rt.Maybe_andThenT` / `rt.Result_andThenT` (lever §5.3 on a
//! Sky def, `lower.rs` `and_then_plan`), and an unproven one keeps the
//! erased call.

use project::emit_example_source;
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root");
    }
}

fn emit(tag: &str, main: &str) -> String {
    let dir = std::env::temp_dir().join(format!("sky-and-then-emit-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"and-then-emit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    let out = emit_example_source(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    out.unwrap_or_else(|e| panic!("emit failed: {e}"))
}

fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let head = format!("func {name}(");
    let start = src
        .find(&head)
        .unwrap_or_else(|| panic!("{name} is emitted:\n{src}"));
    let rest = &src[start..];
    let end = rest[1..]
        .find("\nfunc ")
        .map(|i| i + 1)
        .unwrap_or(rest.len());
    &rest[..end]
}

const SRC: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Maybe as Maybe\n\
import Sky.Core.Result as Result\n\
import Sky.Core.String as String\n\
import Sky.Core.Error as Error exposing (Error)\n\
import Std.Log exposing (println)\n\n\n\
pair : String -> String -> Maybe Int\n\
pair a b =\n    String.toInt a\n        |> Maybe.andThen (\\x -> String.toInt b |> Maybe.andThen (\\y -> Just (x + y)))\n\n\n\
pairBind : String -> String -> Maybe Int\n\
pairBind a b =\n    let\n        x <- String.toInt a\n        y <- String.toInt b\n    in\n    Just (x + y)\n\n\n\
parsed : String -> Result Error Int\n\
parsed a =\n    Result.fromMaybe (Error.invalidInput \"x\") (String.toInt a) |> Result.andThen (\\x -> Ok (x + 1))\n\n\n\
both : Result Error Int -> Result Error Int -> Result Error Int\n\
both r s =\n    r |> Result.andThen (\\x -> s |> Result.andThen (\\y -> Ok (x + y)))\n\n\n\
main =\n    println (String.fromInt (Maybe.withDefault 0 (pair \"1\" \"2\")) ++ String.fromInt (Maybe.withDefault 0 (pairBind \"1\" \"2\")) ++ String.fromInt (Result.withDefault 0 (both (Ok 1) (Ok 2))) ++ String.fromInt (Result.withDefault 0 (parsed \"3\")))\n";

#[test]
fn a_nested_and_then_chain_adds_no_narrowing() {
    let go = emit("nested", SRC);
    for f in ["Main_pair", "Main_pairBind", "Main_both"] {
        let body = func_body(&go, f);
        assert!(
            !body.contains("func(_e"),
            "{f}: a continuation is eta-wrapped over the erased andThen (its \
             parameters narrowed with `rt.AsInt`):\n{body}"
        );
        let wraps = body.matches("MaybeCoerce[any](").count();
        assert!(
            wraps <= 2,
            "{f}: {wraps} `MaybeCoerce[any]` widenings, v0.31 had at most the two \
             `String.toInt` arguments:\n{body}"
        );
    }
}

/// Phase G2 (doc 14 R12, lever §5.3 on a Sky def): a `Maybe.andThen` chain
/// (hand-written or bind lines) and a `Result.andThen` whose error type the
/// argument proves lower to the typed twins `rt.Maybe_andThenT` /
/// `rt.Result_andThenT`: typed continuations, Go `+`, no `rt.Add`, no
/// `MaybeCoerce[any]` round trip.
#[test]
fn a_proven_and_then_chain_lowers_to_the_typed_twin() {
    let go = emit("typed", SRC);
    for f in ["Main_pair", "Main_pairBind"] {
        let body = func_body(&go, f);
        assert!(
            body.contains("rt.Maybe_andThenT[int, int]("),
            "{f}:\n{body}"
        );
        assert!(
            body.contains("func(v_2 int) rt.SkyMaybe[int]"),
            "{f}:\n{body}"
        );
        assert!(!body.contains("rt.Add("), "{f}:\n{body}");
        assert!(!body.contains("MaybeCoerce[any]("), "{f}:\n{body}");
    }
    let body = func_body(&go, "Main_parsed");
    assert!(
        body.contains("rt.Result_andThenT[Sky_Core_Error_Error, int, int]("),
        "{body}"
    );
    // The continuation is typed in place (no eta wrapper); the `rt.AsInt` of
    // the `Result.fromMaybe` argument is that def's own erased result.
    assert!(
        body.contains("func(v_1 int) rt.SkyResult[Sky_Core_Error_Error, int]")
            && !body.contains("func(_e"),
        "{body}"
    );
}
