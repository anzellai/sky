//! The notes a bind-line block gives when its `in` body has the wrong type
//! (it names the fix for the block's type), and when a name bound with `=`
//! to a `Task` (or another type with an `andThen`) is used as its result
//! (it names the bind line that gives the result).

use base::{FileId, ModuleId};
use hir::{SkyDb, SourceDb};
use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

const HEADER: &str = "import Sky.Core.Prelude exposing (..)\n\
                      import Sky.Core.Task as Task\n\
                      import Sky.Core.Maybe as Maybe\n\
                      import Sky.Core.Result as Result\n\
                      import Sky.Core.Error as Error exposing (Error)\n\
                      import Sky.Core.Json.Decode as Decode exposing (Decoder)\n\
                      import Std.Log exposing (println)\n";

/// One diagnostic: its code, its message, and the source text its first
/// label covers.
#[derive(Debug)]
struct Got {
    code: String,
    message: String,
    at: String,
}

fn check(body: &str, extra: &[(&str, &str)]) -> Vec<Got> {
    let mut db = SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let mut ids: Vec<ModuleId> = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, FileId(100 + i as u32))));
    }
    let imports: String = extra.iter().map(|(n, _)| format!("import {n}\n")).collect();
    let src = format!(
        "module Main exposing (main)\n\n{HEADER}{imports}\n\n{body}\n\n\nmain =\n    println \"x\"\n"
    );
    ids.push(db.add_module("Main", syntax::parse(&src, FileId(0))));
    let _ = db.module_by_name("Main");
    ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.severity != diagnostics::Severity::Info)
        .map(|d| {
            let at = d
                .labels
                .first()
                .map(|l| src[l.span.range.0 as usize..l.span.range.1 as usize].to_string())
                .unwrap_or_default();
            Got {
                code: d.code.0,
                message: d.message,
                at,
            }
        })
        .collect()
}

/// Exactly one `[E2001]`, at `at`, whose message contains every `needle`.
fn one_e2001_at(got: &[Got], at: &str, needles: &[&str]) {
    let e: Vec<_> = got.iter().filter(|g| g.code == "E2001").collect();
    assert_eq!(e.len(), 1, "{got:#?}");
    assert_eq!(e[0].at.trim(), at, "{got:#?}");
    for n in needles {
        assert!(e[0].message.contains(n), "missing {n:?}: {got:#?}");
    }
}

// ---- 2. the `in` body has the wrong type for the block ------------------

#[test]
fn a_plain_value_body_in_a_task_block_names_task_succeed() {
    // Judge probe e_body2.
    let got = check(
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n        b <- Task.succeed (a + 1)\n    in\n    b",
        &[],
    );
    one_e2001_at(
        &got,
        "b",
        &["wrap a plain value in `Task.succeed`", "`Task.succeed b`"],
    );
}

#[test]
fn a_maybe_body_in_a_task_block_names_the_conversion() {
    // Judge probe e_body4.
    let got = check(
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Just a",
        &[],
    );
    one_e2001_at(
        &got,
        "Just a",
        &["Task.fromResult (Result.fromMaybe err (Just a))"],
    );
    assert!(!got[0].message.contains("wrap a plain value"), "{got:#?}");
}

#[test]
fn a_result_body_in_a_task_block_names_task_from_result() {
    let got = check(
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Ok a",
        &[],
    );
    one_e2001_at(&got, "Ok a", &["Task.fromResult (Ok a)"]);
}

#[test]
fn a_plain_value_body_in_a_result_block_names_ok() {
    let got = check(
        "step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    a + 1",
        &[],
    );
    one_e2001_at(&got, "a + 1", &["`Ok`", "`Ok (a + 1)`"]);
}

#[test]
fn a_maybe_body_in_a_result_block_names_result_from_maybe() {
    let got = check(
        "step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    Just a",
        &[],
    );
    one_e2001_at(&got, "Just a", &["Result.fromMaybe err (Just a)"]);
}

#[test]
fn a_plain_value_body_in_a_maybe_block_names_just() {
    let got = check(
        "step : Int -> Maybe Int\nstep n =\n    let\n        a <- Just n\n    in\n    a",
        &[],
    );
    one_e2001_at(&got, "a", &["`Just`", "`Just a`"]);
}

#[test]
fn a_plain_value_body_in_a_decoder_block_names_decode_succeed() {
    let got = check(
        "dec : Decoder Int\ndec =\n    let\n        a <- Decode.int\n    in\n    a",
        &[],
    );
    one_e2001_at(&got, "a", &["`Decode.succeed`", "`Decode.succeed a`"]);
}

#[test]
fn a_plain_value_body_in_a_user_type_block_names_its_succeed() {
    let lib = "module Lib.Parse exposing (Parser, andThen, succeed, int)\n\n\ntype Parser a\n    = Parser (String -> Maybe a)\n\n\nandThen : (a -> Parser b) -> Parser a -> Parser b\nandThen f (Parser p) =\n    Parser (\\s -> Nothing)\n\n\nsucceed : a -> Parser a\nsucceed a =\n    Parser (\\_ -> Just a)\n\n\nint : Parser Int\nint =\n    Parser (\\_ -> Just 1)\n";
    let got = check(
        "p : Lib.Parse.Parser Int\np =\n    let\n        a <- Lib.Parse.int\n    in\n    a",
        &[("Lib.Parse", lib)],
    );
    one_e2001_at(&got, "a", &["`Parse.succeed`: `Parse.succeed a`"]);
}

#[test]
fn a_body_of_the_right_type_gives_no_note() {
    let got = check(
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Task.succeed a",
        &[],
    );
    assert!(got.is_empty(), "{got:#?}");
}

/// Each fix a note names, applied to its block (`err` replaced by an
/// error value), type-checks clean.
#[test]
fn every_named_fix_type_checks() {
    let lib = "module Lib.Parse exposing (Parser, andThen, succeed, int)\n\n\ntype Parser a\n    = Parser (String -> Maybe a)\n\n\nandThen : (a -> Parser b) -> Parser a -> Parser b\nandThen f (Parser p) =\n    Parser (\\s -> Nothing)\n\n\nsucceed : a -> Parser a\nsucceed a =\n    Parser (\\_ -> Just a)\n\n\nint : Parser Int\nint =\n    Parser (\\_ -> Just 1)\n";
    let cases: [(&str, &[(&str, &str)]); 9] = [
        ("step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n        b <- Task.succeed (a + 1)\n    in\n    Task.succeed b", &[]),
        ("step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Task.fromResult (Result.fromMaybe Error.notFound (Just a))", &[]),
        ("step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Task.fromResult (Ok a)", &[]),
        ("step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    Ok (a + 1)", &[]),
        ("step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    Result.fromMaybe Error.notFound (Just a)", &[]),
        ("step : Int -> Maybe Int\nstep n =\n    let\n        a <- Just n\n    in\n    Just a", &[]),
        ("dec : Decoder Int\ndec =\n    let\n        a <- Decode.int\n    in\n    Decode.succeed a", &[]),
        ("p : Lib.Parse.Parser Int\np =\n    let\n        a <- Lib.Parse.int\n    in\n    Parse.succeed a", &[("Lib.Parse", lib)]),
        (&"fetch : Int -> Task Error Int\nfetch n =\n    Task.succeed n\n\n\ngo : Task Error Int\ngo =\n    let\n        a <- fetch 1\n    in\n    fetch (a + 1)", &[]),
    ];
    for (body, extra) in cases {
        let got = check(body, extra);
        assert!(got.is_empty(), "{body}: {got:#?}");
    }
}

// ---- 3. a name bound with `=` used as its result ------------------------

const FETCH: &str = "fetch : Int -> Task Error Int\nfetch n =\n    Task.succeed n\n\n\n";

#[test]
fn a_task_bound_with_eq_and_used_as_a_number_names_the_bind_line() {
    let got = check(
        &format!("{FETCH}go : Task Error Int\ngo =\n    let\n        a = fetch 1\n    in\n    fetch (a + 1)"),
        &[],
    );
    let e: Vec<_> = got.iter().filter(|g| g.code == "E2001").collect();
    assert_eq!(e.len(), 1, "{got:#?}");
    assert!(
        e[0].message
            .contains("`a` is a `Task`; to use its result, bind it with `a <- fetch 1`"),
        "{got:#?}"
    );
}

#[test]
fn a_task_bound_with_eq_and_passed_as_an_int_names_the_bind_line() {
    let got = check(
        &format!(
            "{FETCH}go : Task Error Int\ngo =\n    let\n        a = fetch 1\n    in\n    fetch a"
        ),
        &[],
    );
    assert!(
        got.iter()
            .any(|g| g.code == "E2001" && g.message.contains("bind it with `a <- fetch 1`")),
        "{got:#?}"
    );
}

#[test]
fn a_maybe_and_a_decoder_bound_with_eq_name_the_bind_line() {
    let got = check(
        "go : Maybe Int\ngo =\n    let\n        a = String.toInt \"1\"\n    in\n    Just (a + 1)",
        &[],
    );
    assert!(
        got.iter().any(|g| g.code == "E2001"
            && g.message.contains(
                "`a` is a `Maybe`; to use its result, bind it with `a <- String.toInt \"1\"`"
            )),
        "{got:#?}"
    );
    let got = check(
        "go : Decoder Int\ngo =\n    let\n        a = Decode.int\n    in\n    Decode.succeed (a + 1)",
        &[],
    );
    assert!(
        got.iter().any(|g| g.code == "E2001"
            && g.message
                .contains("`a` is a `Decoder`; to use its result, bind it with `a <- Decode.int`")),
        "{got:#?}"
    );
}

#[test]
fn passing_a_task_to_a_function_that_takes_a_task_gives_no_note() {
    // Correct code: no error, so no note.
    let got = check(
        &format!("{FETCH}go : Task Error Int\ngo =\n    let\n        a = fetch 1\n    in\n    Task.map (\\x -> x + 1) a"),
        &[],
    );
    assert!(got.is_empty(), "{got:#?}");
    // A Task given where a Task of another type is wanted: the error is
    // about the Task's contents, so the bind-line note would be wrong.
    let got = check(
        "need : Task Error String -> Task Error String\nneed t =\n    t\n\n\ngo : Task Error String\ngo =\n    let\n        a = Task.succeed 1\n    in\n    need a",
        &[],
    );
    assert!(got.iter().any(|g| g.code == "E2001"), "{got:#?}");
    assert!(
        got.iter().all(|g| !g.message.contains("to use its result")),
        "{got:#?}"
    );
}

#[test]
fn a_name_from_a_bind_line_or_a_parameter_gives_no_note() {
    // `a` is a parameter (a Task), not an `=` line.
    let got = check("go : Task Error Int -> Int\ngo a =\n    a + 1", &[]);
    assert!(got.iter().any(|g| g.code == "E2001"), "{got:#?}");
    assert!(
        got.iter().all(|g| !g.message.contains("to use its result")),
        "{got:#?}"
    );
    // A plain value with no `andThen` (a List) gives no note.
    let got = check(
        "go : Int\ngo =\n    let\n        a = [ 1 ]\n    in\n    a + 1",
        &[],
    );
    assert!(
        got.iter().all(|g| !g.message.contains("to use its result")),
        "{got:#?}"
    );
}
