//! A stdlib function with no annotation (`Sky.Core.Result.map`, the `Maybe`
//! combinators, ...) must be checked with the type its body gives it. S3c
//! found `Ok "s" |> Result.map k` accepted with `k : Int -> Int`.

use std::path::PathBuf;
use std::sync::OnceLock;
use ty::reject_corpus::{evaluate_modules, load_stdlib, Verdict};

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        load_stdlib(&dir)
    })
}

fn check(imports: &str, body: &str) -> Verdict {
    let src = format!(
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n{imports}\n\n\
         k : Int -> Int\nk n =\n    n + 1\n\n{body}\n"
    );
    evaluate_modules("case", &[(String::new(), src)], stdlib())
}

const IMPORTS: &str = "import Sky.Core.Result as Result\nimport Sky.Core.Maybe as Maybe";

fn assert_rejects(label: &str, imports: &str, body: &str) {
    let v = check(imports, body);
    assert!(
        v.rejected() && v.observed_codes.iter().any(|c| c == "E2001"),
        "{label}: must be REJECTED with [E2001], got {:?} ({})",
        v.observed_codes,
        v.first_msg
    );
}

fn assert_accepts(label: &str, imports: &str, body: &str) {
    let v = check(imports, body);
    assert!(
        !v.rejected(),
        "{label}: must be ACCEPTED, got {:?} ({})",
        v.observed_codes,
        v.first_msg
    );
}

#[test]
fn result_map_checks_the_function_against_the_ok_value() {
    assert_rejects(
        "imported, piped",
        IMPORTS,
        "main =\n    Ok \"s\" |> Result.map k",
    );
    assert_rejects(
        "imported, applied",
        IMPORTS,
        "main =\n    Result.map k (Ok \"s\")",
    );
    assert_rejects(
        "ambient qualifier",
        "",
        "main =\n    Ok \"s\" |> Result.map k",
    );
    assert_accepts("well typed", IMPORTS, "main =\n    Ok 1 |> Result.map k");
}

#[test]
fn the_other_unannotated_combinators_are_checked_too() {
    for (label, body) in [
        ("Maybe.map", "main =\n    Just \"s\" |> Maybe.map k"),
        (
            "Maybe.withDefault",
            "main =\n    Maybe.withDefault \"x\" (Just 1) + 1",
        ),
        (
            "Result.withDefault",
            "main =\n    Result.withDefault \"x\" (Ok 1) + 1",
        ),
        (
            "Result.andThen",
            "main =\n    Ok \"s\" |> Result.andThen (\\n -> Ok (k n))",
        ),
        (
            "Result.mapError",
            "main =\n    Err \"e\" |> Result.mapError k",
        ),
        (
            "Maybe.andThen",
            "main =\n    Just \"s\" |> Maybe.andThen (\\n -> Just (k n))",
        ),
    ] {
        assert_rejects(label, IMPORTS, body);
    }
}

/// The class: every exported stdlib VALUE has a scheme the checker uses, an
/// annotation or a check-only seed. A value with neither is checked as a
/// wildcard at every call site (how `Result.map` went unchecked).
#[test]
fn every_exported_stdlib_value_has_a_checked_scheme() {
    use hir::SkyDb;
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop());
    }
    let mut db = hir::SourceDb::new();
    let mut srcs = std::collections::HashMap::new();
    for (name, parse) in stdlib().iter() {
        db.add_module(name, parse.clone());
        let path = dir
            .join("sky-stdlib")
            .join(format!("{}.sky", name.replace('.', "/")));
        srcs.insert(
            name.clone(),
            std::fs::read_to_string(path).unwrap_or_default(),
        );
    }
    let world = ty::World::build(&db);
    let mut out = Vec::new();
    for m in db.module_ids() {
        let mname = db.module_name(m).to_string();
        let src = srcs.get(&mname).cloned().unwrap_or_default();
        let code: String = src
            .lines()
            .filter(|l| !l.trim_start().starts_with("--"))
            .collect::<Vec<_>>()
            .join("\n");
        let start = code.find("module ").unwrap_or(0);
        let header_end = code[start..].find(")\n").map_or(code.len(), |e| start + e);
        let header = &code[start..header_end];
        let all = header.contains("exposing (..)");
        let r = db.resolve(m);
        for td in &r.top_defs {
            let n = td.name.as_str();
            let exported = all
                || header
                    .split(|c: char| !(c.is_alphanumeric() || c == '_'))
                    .any(|w| w == n);
            if !exported || !n.starts_with(|c: char| c.is_ascii_lowercase()) {
                continue;
            }
            if world.value_sigs.contains_key(&td.def)
                || world.check_sigs.contains_key(&td.def)
                || world.app_check_sigs.contains_key(&td.def)
            {
                continue;
            }
            out.push(format!("{mname}.{n}"));
        }
    }
    out.sort();
    assert!(
        out.is_empty(),
        "exported stdlib values with no checked scheme (annotate them in the `.sky` \
         source, or seed a check-only scheme in `World::seed_check_sigs`): {out:?}"
    );
}

/// The new refusal names the v0.27.0 change and its migration anchor.
#[test]
fn a_newly_checked_combinator_error_carries_the_migration_hint() {
    let src = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
               import Sky.Core.Result as Result\n\n\
               k : Int -> Int\nk n =\n    n + 1\n\n\
               main =\n    Ok \"s\" |> Result.map k\n";
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let m = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    let out = ty::check_modules(&db, &[m]);
    let d = out
        .diagnostics
        .iter()
        .find(|d| d.code.0 == "E2001")
        .expect("an E2001");
    let hint = d.suggestion.clone().unwrap_or_default();
    assert!(
        hint.contains("since v0.27.0")
            && hint.contains("Fix:")
            && hint.ends_with("docs/migration/v0.27.md#stdlib-combinators-are-checked"),
        "{hint:?}"
    );
}
