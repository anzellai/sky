//! The notes a bind-line block gives when its `in` body has the wrong type
//! (it names the fix for the block's type), and when a name bound with `=`
//! to a `Task` (or another type with an `andThen`) is used as its result
//! (it names the bind line that gives the result).

use base::{FileId, ModuleId};
use hir::SourceDb;
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
    suggestion: Option<String>,
}

fn check(body: &str, extra: &[(&str, &str)]) -> Vec<Got> {
    check_with(body, extra, "")
}

/// As [`check`], with `more` added to the module's imports.
fn check_with(body: &str, extra: &[(&str, &str)], more: &str) -> Vec<Got> {
    let mut db = SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let mut ids: Vec<ModuleId> = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, FileId(100 + i as u32))));
    }
    let mut imports: String = extra.iter().map(|(n, _)| format!("import {n}\n")).collect();
    imports.push_str(more);
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
                suggestion: d.suggestion,
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
        &["wrap the value in `Task.succeed`", "`Task.succeed b`"],
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
    assert!(!got[0].message.contains("wrap the value"), "{got:#?}");
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

// ---- 4. every note names a fix only when the fix type-checks ------------

/// The source with the fix the first `[E2001]` note names applied: a bind
/// note turns `name = rhs` into `name <- rhs`; a "To fix it" note replaces
/// the labelled expression (or the bind line it names) with the backticked
/// fix, `err` taken as `Error.notFound`. `None` when the note names no fix.
fn apply_named_fix(body: &str, got: &[Got]) -> Option<String> {
    let e = got.iter().find(|g| g.code == "E2001")?;
    let m = &e.message;
    if let Some(i) = m.find("bind it with `") {
        let rest = &m[i + "bind it with `".len()..];
        let fix = &rest[..rest.find('`')?];
        let (name, rhs) = fix.split_once(" <- ")?;
        let eq = format!("{name} = {rhs}");
        assert!(body.contains(&eq), "{eq:?} not in {body}");
        return Some(body.replacen(&eq, fix, 1));
    }
    let i = m.find("To fix it")?;
    let tail = &m[i..];
    let start = tail.find(": `")? + 3;
    let end = start + tail[start..].find('`')?;
    let fix = tail[start..end].replace("err ", "Error.notFound ");
    if let Some((pat, _)) = fix.split_once(" <- ") {
        let line = body
            .lines()
            .find(|l| l.trim_start().starts_with(&format!("{pat} <- ")))?;
        let indent = &line[..line.len() - line.trim_start().len()];
        return Some(body.replacen(line, &format!("{indent}{fix}"), 1));
    }
    let at = e.at.trim();
    let pos = body.rfind(at)?;
    Some(format!(
        "{}{}{}",
        &body[..pos],
        fix,
        &body[pos + at.len()..]
    ))
}

/// The note names a fix, and that fix applied to the source type-checks.
fn named_fix_type_checks(body: &str) -> String {
    let got = check(body, &[]);
    let fixed = apply_named_fix(body, &got).unwrap_or_else(|| panic!("no fix named: {got:#?}"));
    let after = check(&fixed, &[]);
    assert!(after.is_empty(), "fix does not check:\n{fixed}\n{after:#?}");
    got.iter()
        .find(|g| g.code == "E2001")
        .map(|g| g.message.clone())
        .unwrap()
}

const FETCH_USER: &str = "type alias User =\n    { name : String }\n\n\nfetchUser : Int -> Task Error User\nfetchUser n =\n    Task.succeed { name = \"a\" }\n\n\n";

#[test]
fn a_task_bound_with_eq_and_read_as_a_record_names_the_bind_line() {
    let m = named_fix_type_checks(&format!(
        "{FETCH_USER}go : Task Error ()\ngo =\n    let\n        user = fetchUser 1\n    in\n    println user.name"
    ));
    assert!(m.contains("bind it with `user <- fetchUser 1`"), "{m}");
}

#[test]
fn a_task_bound_with_eq_and_passed_to_from_int_names_the_bind_line() {
    let m = named_fix_type_checks(
        "go : Task Error ()\ngo =\n    let\n        x = Task.succeed 1\n    in\n    println (String.fromInt x)",
    );
    assert!(m.contains("bind it with `x <- Task.succeed 1`"), "{m}");
}

#[test]
fn a_task_bound_with_eq_used_in_arithmetic_names_the_bind_line() {
    let m = named_fix_type_checks(&format!(
        "{FETCH}go : Task Error ()\ngo =\n    let\n        s = fetch 1\n        _ <- println \"x\"\n    in\n    println (String.fromInt (s + 1))"
    ));
    assert!(m.contains("bind it with `s <- fetch 1`"), "{m}");
}

#[test]
fn a_task_bound_with_eq_and_appended_names_the_bind_line() {
    let m = named_fix_type_checks(
        "fetchS : Int -> Task Error String\nfetchS n =\n    Task.succeed \"a\"\n\n\ngo : Task Error ()\ngo =\n    let\n        s = fetchS 1\n    in\n    println (s ++ \"!\")",
    );
    assert!(m.contains("bind it with `s <- fetchS 1`"), "{m}");
}

#[test]
fn the_bind_note_is_not_given_when_the_result_does_not_fit_either() {
    // `x` is a Task of Int; `String.length` wants a String: binding the
    // result gives an Int, which does not fit, so no bind note.
    let got = check(
        "go : Task Error ()\ngo =\n    let\n        x = Task.succeed 1\n    in\n    println (String.fromInt (String.length x))",
        &[],
    );
    assert!(got.iter().any(|g| g.code == "E2001"), "{got:#?}");
    assert!(
        got.iter().all(|g| !g.message.contains("to use its result")),
        "{got:#?}"
    );
    // A record without the field.
    let got = check(
        &format!("{FETCH_USER}go : Task Error ()\ngo =\n    let\n        user = fetchUser 1\n    in\n    println user.email"),
        &[],
    );
    assert!(
        got.iter().all(|g| !g.message.contains("to use its result")),
        "{got:#?}"
    );
}

#[test]
fn an_in_body_of_another_type_names_no_wrap_that_does_not_fit() {
    // `x` is an Int, the block needs `Task Error String`: `Task.succeed x`
    // would not fit, so no wrap advice, only the two types.
    let got = check(
        "go : Task Error String\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    x",
        &[],
    );
    one_e2001_at(
        &got,
        "x",
        &["has type `Int`", "this block needs `Task Error String`"],
    );
    assert!(!got[0].message.contains("wrap"), "{got:#?}");
    assert!(!got[0].message.contains("To fix it"), "{got:#?}");
}

#[test]
fn an_in_body_that_is_a_function_says_how_many_arguments_it_needs() {
    let got = check(
        "go : Task Error Int\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    Task.succeed",
        &[],
    );
    one_e2001_at(
        &got,
        "Task.succeed",
        &[
            "it needs 1 more argument(s)",
            "this block needs `Task Error Int`",
        ],
    );
    assert!(!got[0].message.contains("wrap"), "{got:#?}");
    assert!(!got[0].message.contains("To fix it"), "{got:#?}");
}

#[test]
fn a_unit_body_in_a_task_block_names_a_wrap_that_checks() {
    let m = named_fix_type_checks(
        "go : Task Error ()\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    ()",
    );
    assert!(m.contains("`Task.succeed ()`"), "{m}");
}

#[test]
fn every_body_and_conversion_note_names_a_fix_that_checks() {
    for body in [
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n        b <- Task.succeed (a + 1)\n    in\n    b",
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Just a",
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Ok a",
        "step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    a + 1",
        "step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    Just a",
        "step : Int -> Maybe Int\nstep n =\n    let\n        a <- Just n\n    in\n    a",
        "dec : Decoder Int\ndec =\n    let\n        a <- Decode.int\n    in\n    a",
        "go : Task Error Int\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    String.toInt \"3\"",
        "go =\n    let\n        x <- Task.succeed 1\n    in\n    x + 1",
        // A bind line of another type in the block: the conversion of that line.
        "go : Task Error Int\ngo =\n    let\n        a <- Task.succeed 1\n        b <- String.toInt \"2\"\n    in\n    Task.succeed (a + b)",
    ] {
        named_fix_type_checks(body);
    }
}

#[test]
fn a_conversion_that_does_not_fit_is_not_named() {
    // A `Result String Int` converted with `Task.fromResult` is a
    // `Task String Int`, not the block's `Task Error Int`.
    let got = check(
        "r : Result String Int\nr =\n    Ok 1\n\n\ngo : Task Error Int\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    r",
        &[],
    );
    one_e2001_at(&got, "r", &["this block needs `Task Error Int`"]);
    assert!(!got[0].message.contains("Task.fromResult"), "{got:#?}");
    assert!(!got[0].message.contains("To fix it"), "{got:#?}");
    // A Task in a Maybe block: no conversion exists, so none is named.
    let got = check(
        "go : Maybe Int\ngo =\n    let\n        x <- String.toInt \"1\"\n    in\n    Task.succeed x",
        &[],
    );
    one_e2001_at(&got, "Task.succeed x", &["this block needs `Maybe Int`"]);
    assert!(!got[0].message.contains("To fix it"), "{got:#?}");
    // A same-head body with another content type: the types only.
    let got = check(
        "go : Task Error String\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    Task.succeed x",
        &[],
    );
    assert!(
        got.iter().all(|g| !g.message.contains("To fix it")),
        "{got:#?}"
    );
}

// ---- 5. the expected type sets the block's type ---------------------------

const PARSE: &str = "parse : String -> Result Error Int\nparse s =\n    Result.fromMaybe (Error.invalidInput \"nan\") (String.toInt s)\n\n\n";

/// The source with `defs` before `body`, its one `[E2001]` at `at` naming
/// `fix`, and the fix applied checks clean.
fn expected_blames_line(body: &str, at: &str, fix: &str) {
    let src = format!("{FETCH_USER}{PARSE}{body}");
    let got = check(&src, &[]);
    one_e2001_at(&got, at, &[fix]);
    let fixed = apply_named_fix(&src, &got).unwrap_or_else(|| panic!("no fix: {got:#?}"));
    let after = check(&fixed, &[]);
    assert!(after.is_empty(), "fix does not check:\n{fixed}\n{after:#?}");
}

#[test]
fn a_result_first_line_in_an_annotated_task_block_is_blamed() {
    // Judge probe e15: the block is a Task block (the annotation), so the
    // Result line is converted, never the `in` body wrapped in `Ok`.
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    let\n        n <- parse s\n    in\n    Task.succeed n",
        "<-",
        "n <- Task.fromResult (parse s)",
    );
}

#[test]
fn a_maybe_first_line_in_an_annotated_result_block_is_blamed() {
    // e15 swapped: a Maybe line in a Result block.
    expected_blames_line(
        "go : String -> Result Error Int\ngo s =\n    let\n        n <- String.toInt s\n    in\n    Ok n",
        "<-",
        "n <- Result.fromMaybe err (String.toInt s)",
    );
}

#[test]
fn a_maybe_first_line_before_a_task_line_is_blamed() {
    // Judge probe e2.
    let body = "go : String -> Task Error Int\ngo s =\n    let\n        n <- String.toInt s\n        u <- fetchUser n\n    in\n    Task.succeed (String.length u.name + n)";
    expected_blames_line(
        body,
        "<-",
        "n <- Task.fromResult (Result.fromMaybe err (String.toInt s))",
    );
    let got = check(&format!("{FETCH_USER}{PARSE}{body}"), &[]);
    assert!(!got[0].message.contains("Maybe` block"), "{got:#?}");
}

#[test]
fn a_maybe_second_line_after_a_task_line_is_blamed() {
    // Judge probe e2b.
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    let\n        u <- fetchUser 1\n        n <- String.toInt s\n    in\n    Task.succeed (String.length u.name + n)",
        "<-",
        "n <- Task.fromResult (Result.fromMaybe err (String.toInt s))",
    );
}

#[test]
fn a_result_first_line_before_a_task_line_is_blamed() {
    // Judge probe e12.
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    let\n        n <- parse s\n        u <- fetchUser n\n    in\n    Task.succeed (String.length u.name)",
        "<-",
        "n <- Task.fromResult (parse s)",
    );
}

#[test]
fn a_result_second_line_after_a_task_line_is_blamed() {
    // Judge probe e12b.
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    let\n        u <- fetchUser 1\n        n <- parse s\n    in\n    Task.succeed (String.length u.name + n)",
        "<-",
        "n <- Task.fromResult (parse s)",
    );
}

#[test]
fn a_block_in_an_if_or_case_tail_takes_the_annotated_type() {
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    if s == \"\" then\n        Task.succeed 0\n    else\n        let\n            n <- parse s\n        in\n        Task.succeed n",
        "<-",
        "n <- Task.fromResult (parse s)",
    );
    expected_blames_line(
        "go : String -> Task Error Int\ngo s =\n    case s of\n        \"\" ->\n            Task.succeed 0\n\n        _ ->\n            let\n                n <- parse s\n            in\n            Task.succeed n",
        "<-",
        "n <- Task.fromResult (parse s)",
    );
}

// ---- 6. a fix is named only when the whole fixed definition checks -------

/// Judge probe d1: `a <- String.toInt "3"` would be a `Maybe` line in a
/// `Task` block, so the bind fix is not named; the `Maybe` idiom is.
const D1: &str = "go : Task Error ()\ngo =\n    let\n        a = String.toInt \"3\"\n        t <- Task.succeed 1\n    in\n    println (String.fromInt (a + t))";
/// Judge probe d4: `t <- Task.succeed 3` would be a `Task` line in a
/// `Maybe` block.
const D4: &str = "go : Maybe Int\ngo =\n    let\n        t = Task.succeed 3\n        m <- Just 1\n    in\n    Just (m + t)";
/// Judge probe d5: the bind line would make the `Int` body a `Maybe` block.
const D5: &str = "f : Int\nf =\n    let\n        a = String.toInt \"3\"\n    in\n    a + 1";
/// Judge probe d6: a `Maybe` bind line in a `Task` body.
const D6: &str = "go : Task Error ()\ngo =\n    let\n        a = String.toInt \"3\"\n    in\n    println (String.fromInt (a + 1))";
/// Judge probes a2, b3, b4: a conversion with the `err` placeholder.
const A2: &str = "go : String -> Task Error Int\ngo s =\n    let\n        n <- String.toInt s\n        m <- Task.succeed 2\n    in\n    Task.succeed (n + m)";
const B3: &str = "go s =\n    let\n        n <- Task.succeed 1\n        m <- String.toInt s\n    in\n    Task.succeed (n + m)";
const B4: &str = "go : String -> Task Error Int\ngo s =\n    let\n        n <- Task.succeed 1\n    in\n    String.toInt s";

fn only_e2001(got: &[Got]) -> &Got {
    let e: Vec<_> = got.iter().filter(|g| g.code == "E2001").collect();
    assert_eq!(e.len(), 1, "{got:#?}");
    e[0]
}

#[test]
fn a_bind_fix_that_fails_in_the_def_is_not_named_and_the_idiom_is() {
    for body in [D1, D5, D6] {
        let got = check(body, &[]);
        let e = only_e2001(&got);
        assert!(!e.message.contains("bind it with"), "{body}\n{got:#?}");
        assert!(!e.message.contains("To fix it"), "{body}\n{got:#?}");
        assert!(
            e.message.contains(
                "`String.toInt` gives a `Maybe Int`: handle `Nothing` with `case` or \
                 `Maybe.withDefault`"
            ),
            "{body}\n{got:#?}"
        );
    }
    let got = check(D4, &[]);
    let e = only_e2001(&got);
    assert!(!e.message.contains("bind it with"), "{got:#?}");
    assert!(
        e.message.contains("`Task.succeed` gives a `Task"),
        "{got:#?}"
    );
}

#[test]
fn a_conversion_names_what_err_is() {
    for body in [A2, B3, B4] {
        let got = check(body, &[]);
        let e = only_e2001(&got);
        assert!(
            e.message
                .contains("where `err` is the `Error` to report (e.g. `Error.invalidInput \"…\"`)"),
            "{body}\n{got:#?}"
        );
    }
}

#[test]
fn a_type_name_takes_a_or_an() {
    let got = check(
        "f : Int\nf =\n    let\n        a <- String.toInt \"3\"\n    in\n    a + 1",
        &[],
    );
    let e = only_e2001(&got);
    assert!(e.message.contains("an `Int`"), "{got:#?}");
    assert!(!e.message.contains("a `Int`"), "{got:#?}");
    assert!(
        e.message
            .contains("handle `Nothing` with `case` or `Maybe.withDefault`"),
        "{got:#?}"
    );
}

#[test]
fn the_checked_combinator_hint_blames_the_call_that_mismatches() {
    // Judge probe s1: the clash is in `Decode.decodeString`'s argument, a
    // Db decoder where a Json decoder is wanted, not in `Result.map`.
    let got = check_with(
        "v : String\nv =\n    Result.withDefault \"e\" (Result.map String.fromInt (Decode.decodeString (DbDecode.int \"age\") \"{}\"))",
        &[],
        "import Std.Db.Decode as DbDecode\n",
    );
    let e = only_e2001(&got);
    assert!(
        e.at.trim_start_matches('(')
            .starts_with("Decode.decodeString"),
        "{got:#?}"
    );
    assert!(
        !e.suggestion
            .as_deref()
            .unwrap_or("")
            .contains("`Result.map` is type-checked"),
        "{got:#?}"
    );
    // A clash in `Result.map`'s own argument keeps the hint.
    let got = check(
        "v : Int\nv =\n    Result.withDefault 0 (Result.map String.length (Ok 1))",
        &[],
    );
    assert!(
        got.iter().any(|g| g
            .suggestion
            .as_deref()
            .is_some_and(|s| s.contains("`Result.map` is type-checked"))),
        "{got:#?}"
    );
}

/// Property: every note-producing fixture either names no fix, or names one
/// that, applied to the source, type-checks clean.
#[test]
fn every_note_fixture_names_only_fixes_that_check() {
    let fixtures = [
        D1, D4, D5, D6, A2, B3, B4,
        "f : Int\nf =\n    let\n        a <- String.toInt \"3\"\n    in\n    a + 1",
        "go : Task Error ()\ngo =\n    let\n        a <- String.toInt \"3\"\n        t <- Task.succeed 1\n    in\n    println (String.fromInt (a + t))",
        "go : Maybe Int\ngo =\n    let\n        t <- Task.succeed 3\n        m <- Just 1\n    in\n    Just (m + t)",
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n        b <- Task.succeed (a + 1)\n    in\n    b",
        "step : Int -> Task Error Int\nstep n =\n    let\n        a <- Task.succeed n\n    in\n    Just a",
        "step : Int -> Result Error Int\nstep n =\n    let\n        a <- Ok n\n    in\n    a + 1",
        "step : Int -> Maybe Int\nstep n =\n    let\n        a <- Just n\n    in\n    a",
        "go : Task Error Int\ngo =\n    let\n        x <- Task.succeed 1\n    in\n    String.toInt \"3\"",
        "go : Maybe Int\ngo =\n    let\n        a = String.toInt \"1\"\n    in\n    Just (a + 1)",
        "go : Decoder Int\ngo =\n    let\n        a = Decode.int\n    in\n    Decode.succeed (a + 1)",
    ];
    let mut named = 0;
    for body in fixtures {
        let got = check(body, &[]);
        assert!(got.iter().any(|g| g.code == "E2001"), "{body}\n{got:#?}");
        if let Some(fixed) = apply_named_fix(body, &got) {
            named += 1;
            let after = check(&fixed, &[]);
            assert!(after.is_empty(), "fix does not check:\n{fixed}\n{after:#?}");
        }
    }
    assert!(named >= 8, "only {named} fixtures named a fix");
}
