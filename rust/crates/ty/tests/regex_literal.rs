//! v0.29.0 `Sky.Core.Regex`: a `Regex` is compiled by `Regex.fromString`, a
//! literal pattern is checked when the program compiles (`[E2014]`), and code
//! that still passes a pattern String gets a hint naming the migration.

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

fn check(body: &str, import: &str) -> Vec<diagnostics::Diagnostic> {
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         {import}\n\n\n{body}\n"
    );
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    ty::check_modules(&db, &[m])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .collect()
}

const IMPORT: &str = "import Sky.Core.Regex as Regex exposing (Regex)";

fn codes(ds: &[diagnostics::Diagnostic]) -> Vec<String> {
    ds.iter().map(|d| d.code.0.clone()).collect()
}

fn re_def(pattern_expr: &str) -> String {
    format!(
        "re : Regex\nre =\n    Regex.fromString {pattern_expr}\n        |> Result.withDefault Regex.never\n\n\n\
         main =\n    Regex.contains re \"x\"\n"
    )
}

#[test]
fn valid_literal_patterns_compile() {
    for p in [
        r#""""\s+""""#,
        r#""""\b\w+\b""""#,
        r#""""\d{2,4}""""#,
        r#""""\\d""""#,
        r#""\\s+""#,
        r#""""\{{name}}""""#,
        r#""""[a-z]{2}""""#,
    ] {
        let ds = check(&re_def(p), IMPORT);
        assert!(ds.is_empty(), "{p} was refused: {ds:?}");
    }
}

#[test]
fn an_invalid_literal_pattern_is_e2014_with_the_re2_text() {
    for (p, text) in [
        (r#""(""#, "missing closing ): `(`"),
        (r#""""[a""""#, "missing closing ]: `[a`"),
        (r#""""\8""""#, "invalid escape sequence: `\\8`"),
        (r#""a{3,2}""#, "invalid repeat count: `{3,2}`"),
    ] {
        let ds = check(&re_def(p), IMPORT);
        assert_eq!(codes(&ds), ["E2014"], "{p}: {ds:?}");
        assert!(ds[0].message.contains(text), "{p}: {}", ds[0].message);
        assert!(
            ds[0].message.contains("E2014-invalid-regex"),
            "{}",
            ds[0].message
        );
        assert_eq!(ds[0].labels.len(), 1, "{p}: the literal is labelled");
    }
}

#[test]
fn every_spelling_of_from_string_is_checked() {
    // Through a pipeline, a backward pipe, an exposed name, and the kernel
    // qualifier with no import at all.
    let pipe =
        "re : Regex\nre =\n    \"(\" |> Regex.fromString |> Result.withDefault Regex.never\n\n\n\
                main =\n    Regex.contains re \"x\"\n";
    assert_eq!(codes(&check(pipe, IMPORT)), ["E2014"]);
    let back =
        "re : Regex\nre =\n    Result.withDefault Regex.never (Regex.fromString <| \"(\")\n\n\n\
                main =\n    Regex.contains re \"x\"\n";
    assert_eq!(codes(&check(back, IMPORT)), ["E2014"]);
    let exposed = "re =\n    fromString \"(\"\n\n\n\
                   main =\n    re\n";
    assert_eq!(
        codes(&check(
            exposed,
            "import Sky.Core.Regex exposing (Regex, fromString)"
        )),
        ["E2014"]
    );
    let bare = "main =\n    Regex.fromString \"(\"\n";
    assert_eq!(codes(&check(bare, "")), ["E2014"]);
}

#[test]
fn an_interpolated_or_computed_pattern_is_left_to_the_result() {
    // Interpolated: not a literal, so not checked here (the run-time Result is).
    let interp = "open : String\nopen =\n    \"(\"\n\n\n\
                  re : Result Error Regex\nre =\n    Regex.fromString \"\"\"{{open}}a\"\"\"\n\n\n\
                  main =\n    re\n";
    assert!(check(interp, IMPORT).is_empty());
    // Computed.
    let computed =
        "re : String -> Result Error Regex\nre p =\n    Regex.fromString (p ++ \"(\")\n\n\n\
                    main =\n    re \"a\"\n";
    assert!(check(computed, IMPORT).is_empty());
}

#[test]
fn a_pattern_string_where_a_regex_is_needed_names_the_migration() {
    for call in [
        "Regex.contains \"a+\" \"aaa\"",
        "Regex.find \"a+\" \"aaa\"",
        "Regex.findAll \"a+\" \"aaa\"",
        "Regex.replace \"a+\" \"b\" \"aaa\"",
        "Regex.split \",\" \"a,b\"",
        "Regex.match \"a\" \"a\"",
    ] {
        let ds = check(&format!("main =\n    {call}\n"), IMPORT);
        let d = ds
            .iter()
            .find(|d| d.code.0 == "E2001")
            .unwrap_or_else(|| panic!("{call}: an E2001, got {ds:?}"));
        let hint = d.suggestion.clone().unwrap_or_default();
        assert!(
            hint.contains("since v0.29.0")
                && hint.contains("Regex.fromString")
                && hint.contains("docs/migration/v0.29.md#regex-takes-a-compiled-regex"),
            "{call}: {hint}"
        );
    }
}

#[test]
fn a_regex_cannot_be_stored_with_a_codec() {
    let body = "import Std.Codec as Codec exposing (Codec)\n\n\n\
                type alias Saved =\n    { re : Regex }\n\n\n\
                codec : Codec Saved\ncodec =\n    Codec.auto { re = Regex.never }\n\n\n\
                main =\n    codec\n";
    let ds = check(body, IMPORT);
    assert!(
        ds.iter().any(|d| d.message.contains("cannot be encoded")),
        "{ds:?}"
    );
}
