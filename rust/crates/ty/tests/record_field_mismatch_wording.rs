//! Regression: the `[E2001]` record-field message names the right side.
//!
//! A record literal passed as a function ARGUMENT that lacked a field of the
//! parameter's declared record was reported as having an UNKNOWN field
//! ("record has unknown field(s) not in the declared type: b") when the field
//! was MISSING. The same literal as an annotated value was worded correctly.
//! The unifier reads its first side as the actual value and its second as the
//! declared type, but a function's parameter sits in contravariant position:
//! `unify(callee, arg -> result)` meets the declared parameter on the first
//! side and the argument on the second. The orientation now flips under a
//! function's parameter, so every site states the truth:
//!
//! * a field the declared type has and the value lacks is "missing";
//! * a field the value has and the declared type lacks is "unknown".
//!
//! Each case asserts the exact wording and that the reversed wording is absent.

use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn messages(body: &str) -> Vec<String> {
    let main = format!(
        "module Main exposing (main)\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         type alias P =\n    {{ a : String, b : String }}\n\n\
         show : P -> String\nshow q =\n    q.a\n\n\
         {body}"
    );
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    let mut db = hir::SourceDb::new();
    for path in files {
        let src = std::fs::read_to_string(&path).unwrap();
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .unwrap_or_default();
        if !name.is_empty() {
            db.add_module(&name, parse);
        }
    }
    let mid = db.add_module("Main", syntax::parse(&main, base::FileId(1)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect()
}

const MISSING_B: &str = "record is missing field(s): b";
const UNKNOWN_C: &str = "record has unknown field(s) not in the declared type: c";
const BOTH: &str =
    "record is missing field(s): b, and has unknown field(s) not in the declared type: c";

/// Exactly one `[E2001]` error, it contains `want`, and none of `never`.
fn expect(body: &str, want: &str, never: &[&str]) {
    let msgs = messages(body);
    let e2001: Vec<&String> = msgs.iter().filter(|m| m.starts_with("[E2001]")).collect();
    assert_eq!(e2001.len(), 1, "one record clash, one error: {msgs:#?}");
    assert!(e2001[0].contains(want), "want `{want}` in: {msgs:#?}");
    for n in never {
        assert!(!e2001[0].contains(n), "must not say `{n}`: {msgs:#?}");
    }
}

const NOT_UNKNOWN_B: &str = "not in the declared type: b";
const NOT_MISSING_C: &str = "missing field(s): c";

// ---- argument position (the reported bug) ----------------------------------

#[test]
fn argument_missing_field_is_missing() {
    expect(
        "main =\n    println (show { a = \"x\" })\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn argument_unknown_field_is_unknown() {
    expect(
        "main =\n    println (show { a = \"x\", b = \"y\", c = \"z\" })\n",
        UNKNOWN_C,
        &[NOT_MISSING_C, "is missing"],
    );
}

#[test]
fn argument_missing_and_unknown_names_both() {
    expect(
        "main =\n    println (show { a = \"x\", c = \"z\" })\n",
        BOTH,
        &[NOT_UNKNOWN_B, NOT_MISSING_C],
    );
}

#[test]
fn piped_argument_missing_field_is_missing() {
    expect(
        "main =\n    println ({ a = \"x\" } |> show)\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn backward_piped_argument_unknown_field_is_unknown() {
    expect(
        "main =\n    println (show <| { a = \"x\", b = \"y\", c = \"z\" })\n",
        UNKNOWN_C,
        &[NOT_MISSING_C, "is missing"],
    );
}

// ---- annotated value --------------------------------------------------------

#[test]
fn annotated_value_missing_field_is_missing() {
    expect(
        "p : P\np =\n    { a = \"x\" }\n\nmain =\n    println p.a\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn annotated_value_unknown_field_is_unknown() {
    expect(
        "p : P\np =\n    { a = \"x\", b = \"y\", c = \"z\" }\n\nmain =\n    println p.a\n",
        UNKNOWN_C,
        &[NOT_MISSING_C, "is missing"],
    );
}

#[test]
fn annotated_value_missing_and_unknown_names_both() {
    expect(
        "p : P\np =\n    { a = \"x\", c = \"z\" }\n\nmain =\n    println p.a\n",
        BOTH,
        &[NOT_UNKNOWN_B, NOT_MISSING_C],
    );
}

// ---- other expected-type sites ---------------------------------------------

#[test]
fn function_result_missing_field_is_missing() {
    expect(
        "mk : String -> P\nmk s =\n    { a = s }\n\nmain =\n    println (mk \"x\").a\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn list_element_missing_field_is_missing() {
    expect(
        "ps : List P\nps =\n    [ { a = \"x\", b = \"y\" }, { a = \"x\" } ]\n\nmain =\n    println \"x\"\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn if_branch_missing_field_is_missing() {
    expect(
        "p : P\np =\n    if True then\n        { a = \"x\", b = \"y\" }\n\n    else\n        { a = \"x\" }\n\nmain =\n    println p.a\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn case_branch_unknown_field_is_unknown() {
    expect(
        "p : P\np =\n    case 1 of\n        1 ->\n            { a = \"x\", b = \"y\" }\n\n        _ ->\n            { a = \"x\", b = \"y\", c = \"z\" }\n\nmain =\n    println p.a\n",
        UNKNOWN_C,
        &[NOT_MISSING_C, "is missing"],
    );
}

#[test]
fn let_bound_value_passed_on_missing_field_is_missing() {
    // A let binding carries no annotation the checker reads, so the clash
    // surfaces where the bound record meets `show`'s declared parameter.
    expect(
        "main =\n    let\n        q =\n            { a = \"x\" }\n    in\n    println (show q)\n",
        MISSING_B,
        &[NOT_UNKNOWN_B, "unknown field"],
    );
}

#[test]
fn record_update_of_an_absent_field_names_it_missing() {
    // `{ p | c = … }` asks for a field the closed `P` does not have.
    expect(
        "p : P\np =\n    { a = \"x\", b = \"y\" }\n\nmain =\n    println { p | c = \"z\" }.a\n",
        "missing field(s): c",
        &["unknown field"],
    );
}

// ---- accepted twins ---------------------------------------------------------

#[test]
fn exact_records_check_clean() {
    let msgs = messages(
        "p : P\np =\n    { a = \"x\", b = \"y\" }\n\nmain =\n    println (show { a = \"x\", b = \"y\" } ++ show p)\n",
    );
    assert!(msgs.is_empty(), "{msgs:#?}");
}
