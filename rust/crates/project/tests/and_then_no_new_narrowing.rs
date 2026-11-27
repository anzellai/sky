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

/// `Result.mapError f r` whose argument and callback lower to proven Go types
/// goes to the typed twin `rt.Result_mapErrorT[E, F, A]` (doc 14 R5, the
/// erased kernel's `any` return, lever §5.3). A bind line per step with its
/// own `mapError` then stays on `rt.Result_andThenT`: no narrowing of the
/// bound values.
const MAP_ERROR_SRC: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Result as Result\n\
import Sky.Core.String as String\n\
import Sky.Core.Error as Error exposing (Error)\n\
import Std.Log exposing (println)\n\n\n\
toInt : String -> Result Error Int\n\
toInt s =\n    Result.fromMaybe (Error.invalidInput s) (String.toInt s)\n\n\n\
tag : String -> Error -> ( String, String )\n\
tag field e =\n    ( field, Error.toString e )\n\n\n\
steps : String -> String -> Result ( String, String ) Int\n\
steps a b =\n    let\n        x <- toInt a |> Result.mapError (tag \"a\")\n        y <- toInt b |> Result.mapError (\\e -> ( \"b\", Error.toString e ))\n    in\n    Ok (x + y)\n\n\n\
main =\n    println (String.fromInt (Result.withDefault 0 (steps \"1\" \"2\")))\n";

#[test]
fn a_proven_map_error_lowers_to_the_typed_twin() {
    let go = emit("map-error", MAP_ERROR_SRC);
    let body = func_body(&go, "Main_steps");
    assert_eq!(
        body.matches("rt.Result_mapErrorT[Sky_Core_Error_Error, rt.T2[string, string], int](")
            .count(),
        2,
        "{body}"
    );
    assert!(!body.contains("rt.Result_mapError("), "{body}");
    assert!(body.contains("rt.Result_andThenT["), "{body}");
    for erased in ["rt.AsInt(", "rt.Coerce[", "rt.ResultCoerce"] {
        assert!(!body.contains(erased), "{erased} in:\n{body}");
    }
}

/// The same chain with no `Sky.Core.Result` import: `Result.mapError` is the
/// Prelude kernel `rt.Result_mapError`, and a proven call goes to the same
/// typed twin.
#[test]
fn a_proven_prelude_map_error_lowers_to_the_typed_twin() {
    let src = MAP_ERROR_SRC.replace("import Sky.Core.Result as Result\n", "");
    let go = emit("map-error-prelude", &src);
    let body = func_body(&go, "Main_steps");
    assert_eq!(
        body.matches("rt.Result_mapErrorT[Sky_Core_Error_Error, rt.T2[string, string], int](")
            .count(),
        2,
        "{body}"
    );
    assert!(!body.contains("rt.Result_mapError("), "{body}");
    for erased in ["rt.AsInt(", "rt.Coerce[", "rt.ResultCoerce"] {
        assert!(!body.contains(erased), "{erased} in:\n{body}");
    }
}

/// The sign-up shape of `apps/ledger` (`Update.createAccount`): a `Maybe`
/// payload, a `_ <-` line, and a `mapError` per step with its own lambda.
/// Every bind line stays on `rt.Result_andThenT` and every `mapError` on
/// `rt.Result_mapErrorT`.
const STEPS_SRC: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.String as String\n\
import Sky.Core.Error as Error exposing (Error)\n\
import Std.Log exposing (println)\n\n\n\
type alias User =\n    { id : Int, email : String }\n\n\n\
find : String -> Result Error (Maybe User)\n\
find _ =\n    Ok Nothing\n\n\n\
hashOf : String -> Result Error String\n\
hashOf p =\n    Ok p\n\n\n\
insert : String -> String -> Result Error Int\n\
insert _ _ =\n    Ok 1\n\n\n\
unregistered : Maybe User -> Result ( String, String ) ()\n\
unregistered m =\n    case m of\n        Just _ ->\n            Err ( \"email\", \"taken\" )\n\n        Nothing ->\n            Ok ()\n\n\n\
create : String -> String -> Result ( String, String ) Int\n\
create email password =\n    let\n        existing <- find email |> Result.mapError (\\e -> ( \"email\", Error.toString e ))\n        _ <- unregistered existing\n        hash <- hashOf password |> Result.mapError (\\e -> ( \"password\", Error.toString e ))\n        id <- insert email hash |> Result.mapError (\\e -> ( \"email\", Error.toString e ))\n    in\n    Ok id\n\n\n\
main =\n    println (String.fromInt (Result.withDefault 0 (create \"a\" \"b\")))\n";

#[test]
fn the_ledger_sign_up_chain_stays_typed() {
    let go = emit("steps", STEPS_SRC);
    let body = func_body(&go, "Main_create");
    assert_eq!(body.matches("rt.Result_mapErrorT[").count(), 3, "{body}");
    assert_eq!(body.matches("rt.Result_andThenT[").count(), 4, "{body}");
    for erased in [
        "rt.AsInt(",
        "rt.AsString(",
        "rt.Coerce[",
        "rt.ResultCoerce",
        "rt.MaybeCoerce",
    ] {
        assert!(!body.contains(erased), "{erased} in:\n{body}");
    }
}

/// The `Std.Db.Store` ladder shape (`condValues`, `encodeCheckedRows`): a
/// recursive list walk whose second bind line binds the rest of the list, so
/// the nested continuation takes a slice (`\more -> Ok (v :: more)`). Every
/// bind line and every hand-written nested `andThen` of that shape stays on
/// the typed twin. A lambda is lowered at the slot's own Go types
/// (`lower_lambda` widens only a struct param, which `provable` refuses), so
/// a slice parameter needs no per-call rebuild (doc 14 R12, lever §5.3).
const LIST_LADDER_SRC: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Maybe as Maybe\n\
import Sky.Core.Result as Result\n\
import Sky.Core.String as String\n\
import Sky.Core.List as List\n\
import Sky.Core.Json.Encode as Encode exposing (Value)\n\
import Sky.Core.Error as Error exposing (Error)\n\
import Std.Log exposing (println)\n\n\n\
one : String -> Result Error Int\n\
one s =\n    Result.fromMaybe (Error.invalidInput s) (String.toInt s)\n\n\n\
many : List String -> Result Error (List Int)\n\
many xs =\n    case xs of\n        [] ->\n            Ok []\n\n        x :: rest ->\n            let\n                v <- one x\n                more <- many rest\n            in\n            Ok (v :: more)\n\n\n\
manyHand : List String -> Result Error (List Int)\n\
manyHand xs =\n    case xs of\n        [] ->\n            Ok []\n\n        x :: rest ->\n            one x |> Result.andThen (\\v -> manyHand rest |> Result.andThen (\\more -> Ok (v :: more)))\n\n\n\
manyMaybe : List String -> Maybe (List Int)\n\
manyMaybe xs =\n    case xs of\n        [] ->\n            Just []\n\n        x :: rest ->\n            let\n                v <- String.toInt x\n                more <- manyMaybe rest\n            in\n            Just (v :: more)\n\n\n\
asValue : String -> Result Error Value\n\
asValue s =\n    Ok (Encode.string s)\n\n\n\
encodeAll : List String -> Result Error (List Value)\n\
encodeAll xs =\n    case xs of\n        [] ->\n            Ok []\n\n        x :: rest ->\n            let\n                v <- asValue x\n                more <- encodeAll rest\n            in\n            Ok (v :: more)\n\n\n\
pairs : List String -> Result Error ( String, List Int )\n\
pairs xs =\n    let\n        ns <- many xs\n        ms <- manyHand xs\n    in\n    Ok ( String.join \",\" xs, ns ++ ms )\n\n\n\
main =\n    println (String.fromInt (List.length (Result.withDefault [] (many [ \"1\" ]))) ++ String.fromInt (List.length (Result.withDefault [] (manyHand [ \"1\" ]))) ++ String.fromInt (List.length (Maybe.withDefault [] (manyMaybe [ \"1\" ]))) ++ String.fromInt (List.length (Tuple.second (Result.withDefault ( \"\", [] ) (pairs [ \"1\" ])))) ++ String.fromInt (List.length (Result.withDefault [] (encodeAll [ \"1\" ]))))\n";

#[test]
fn a_nested_bind_over_a_list_stays_on_the_typed_twin() {
    let go = emit("list-ladder", LIST_LADDER_SRC);
    for (f, twin) in [
        ("Main_many", "rt.Result_andThenT["),
        ("Main_manyHand", "rt.Result_andThenT["),
        ("Main_manyMaybe", "rt.Maybe_andThenT["),
        ("Main_pairs", "rt.Result_andThenT["),
        // The bound value is a `Json.Encode.Value`, whose Go type is `any`.
        ("Main_encodeAll", "rt.Result_andThenT["),
    ] {
        let body = func_body(&go, f);
        assert_eq!(body.matches(twin).count(), 2, "{f}:\n{body}");
        for erased in [
            "Sky_Core_Result_andThen(",
            "Sky_Core_Maybe_andThen(",
            "rt.AsListT",
            "rt.Coerce[",
            "rt.CoerceFuncSlot",
            "rt.ResultCoerce",
            // `String.toInt` keeps its own kernel `rt.MaybeCoerce[int]`.
            "rt.MaybeCoerce[any]",
            "rt.MaybeCoerce[[]",
        ] {
            assert!(!body.contains(erased), "{f}: {erased} in:\n{body}");
        }
    }
}
