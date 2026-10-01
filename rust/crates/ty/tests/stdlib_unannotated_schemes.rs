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

/// What a module header exposes.
#[derive(Debug, PartialEq)]
enum Exposing {
    All,
    Names(Vec<String>),
}

/// Parse `module X exposing ( ... )` with balanced parentheses, so an item
/// after a `T(..)` line and a bare `exposing (..)` are both seen. (The first
/// version cut the header at the first `")\n"`, which skipped every item after
/// a `T(..)` line and never saw `exposing (..)`.)
fn exposed_values(src: &str) -> Exposing {
    let code: String = src
        .lines()
        .map(|l| l.find("--").map_or(l, |i| &l[..i]))
        .collect::<Vec<_>>()
        .join("\n");
    let none = Exposing::Names(Vec::new());
    let Some(start) = code.find("module ") else {
        return none;
    };
    let after = &code[start..];
    let Some(ex) = after.find("exposing") else {
        return none;
    };
    let Some(open) = after[ex..].find('(').map(|o| ex + o) else {
        return none;
    };
    let mut depth = 0usize;
    let mut items = Vec::new();
    let mut cur = String::new();
    for c in after[open..].chars() {
        match c {
            '(' => {
                depth += 1;
                if depth > 1 {
                    cur.push(c);
                }
            }
            ')' => {
                depth -= 1;
                if depth == 0 {
                    items.push(std::mem::take(&mut cur));
                    break;
                }
                cur.push(c);
            }
            ',' if depth == 1 => items.push(std::mem::take(&mut cur)),
            _ => cur.push(c),
        }
    }
    if items.len() == 1 && items[0].trim() == ".." {
        return Exposing::All;
    }
    Exposing::Names(
        items
            .iter()
            .map(|i| {
                i.trim()
                    .chars()
                    .take_while(|c| c.is_alphanumeric() || *c == '_')
                    .collect::<String>()
            })
            .filter(|n| !n.is_empty())
            .collect(),
    )
}

#[test]
fn the_header_parser_sees_every_exposed_item() {
    assert_eq!(
        exposed_values("module A exposing (..)\n\nx = 1\n"),
        Exposing::All
    );
    assert_eq!(
        exposed_values(
            "module A exposing\n    ( T(..)\n    , run -- the runner\n    , U(..)\n    , after\n    )\n\nx = 1\n"
        ),
        Exposing::Names(vec!["T".into(), "run".into(), "U".into(), "after".into()])
    );
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
        let exports = exposed_values(&src);
        let r = db.resolve(m);
        for td in &r.top_defs {
            let n = td.name.as_str();
            let exported = match &exports {
                Exposing::All => true,
                Exposing::Names(ns) => ns.iter().any(|w| w == n),
            };
            if !exported || !n.starts_with(|c: char| c.is_ascii_lowercase()) {
                continue;
            }
            // A body-inferred `app_check_sigs` scheme does not count: the
            // stdlib states its types, so a reader and `sky doc` see them.
            if world.value_sigs.contains_key(&td.def) || world.check_sigs.contains_key(&td.def) {
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

/// The seeded `Sky.Test.runMain` scheme checks its argument and leaves the
/// result free, so both `main` shapes the test files use still compile.
#[test]
fn sky_test_run_main_is_checked() {
    let imp = "import Sky.Test as Test";
    let v = check(imp, "main =\n    Test.runMain 5");
    assert!(
        v.rejected() && v.observed_codes.iter().any(|c| c == "E2001"),
        "{:?} {}",
        v.observed_codes,
        v.first_msg
    );
    assert_accepts(
        "runMain over a test list",
        imp,
        "tests =\n    [ Test.test \"t\" (\\_ -> Test.pass) ]\n\n\
         main : Task Error ()\nmain =\n    Test.runMain tests",
    );
}
