//! Regressions for `case` diagnostics (v0.27.0 audit: E-18 / M-2 and the
//! constructor-pattern arity item).
//!
//! * A scrutinee of the wrong type is ONE error, not one per arm: `case
//!   Crypto.aesGcmEncrypt key plain of Ok … ; Err …` on the `Task` it returns
//!   since v0.27.0 printed the same `[E2001]` twice. It also carries the
//!   migration hint.
//! * A constructor pattern with the wrong number of sub-patterns is `[E2007]`
//!   at the pattern, naming the constructor and both counts.
//!
//! Each rejection has an accepted twin, so a checker that rejected everything
//! would fail.

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

fn errors(main: &str) -> Vec<diagnostics::Diagnostic> {
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
    let mid = db.add_module("Main", syntax::parse(main, base::FileId(1)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.Crypto as Crypto\n\
    import Sky.Core.Secret as Secret exposing (Secret)\n\
    import Sky.Core.Task as Task\n\
    import Std.Log exposing (println)\n\n";

#[test]
fn a_task_scrutinee_matched_as_a_result_is_one_error_with_the_aead_hint() {
    let main = format!(
        "{HEAD}seal : Secret -> String -> String\nseal key plain =\n    case Crypto.aesGcmEncrypt key plain of\n        Ok ct ->\n            ct\n\n        Err _ ->\n            \"\"\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = errors(&main);
    let e2001: Vec<_> = errs.iter().filter(|d| d.code.0 == "E2001").collect();
    assert_eq!(e2001.len(), 1, "one wrong scrutinee, one error: {errs:?}");
    let hint = e2001[0].suggestion.clone().unwrap_or_default();
    assert!(
        hint.contains("Task.run (Crypto.aesGcmEncrypt key plain)"),
        "{hint}"
    );
    assert!(
        hint.ends_with("See docs/migration/v0.27.md#aead-encrypt-is-a-task"),
        "{hint}"
    );
}

#[test]
fn the_same_case_through_task_run_checks_clean() {
    let main = format!(
        "{HEAD}seal : Secret -> String -> String\nseal key plain =\n    case Task.run (Crypto.aesGcmEncrypt key plain) of\n        Ok ct ->\n            ct\n\n        Err _ ->\n            \"\"\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = errors(&main);
    assert!(errs.is_empty(), "{errs:?}");
}

#[test]
fn arms_that_disagree_with_each_other_are_still_an_error() {
    // Joining the patterns before meeting the scrutinee must not lose the
    // mismatch between two arms.
    let main = format!(
        "{HEAD}f : Maybe Int -> Int\nf m =\n    case m of\n        Just n ->\n            n\n\n        Ok k ->\n            k\n\n        _ ->\n            0\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = errors(&main);
    assert!(
        errs.iter().any(|d| d.code.0 == "E2001"),
        "`Just` and `Ok` in one case must be rejected: {errs:?}"
    );
}

#[test]
fn a_ctor_pattern_with_too_many_arguments_is_an_arity_error_at_the_pattern() {
    let main = format!(
        "{HEAD}describe : Maybe Int -> String\ndescribe m =\n    case m of\n        Just a b ->\n            \"two\"\n\n        Nothing ->\n            \"none\"\n\n\
         main =\n    println (describe (Just 1))\n"
    );
    let errs = errors(&main);
    let e = errs
        .iter()
        .find(|d| d.code.0 == "E2007")
        .unwrap_or_else(|| panic!("expected [E2007], got {errs:?}"));
    assert!(
        e.message
            .contains("The constructor `Just` takes 1 argument, but this pattern gives it 2"),
        "{}",
        e.message
    );
    assert!(
        errs.iter().all(|d| d.code.0 != "E2001"),
        "no cascade type mismatch at the case head: {errs:?}"
    );
    // The label points at the pattern `Just a b`.
    let (s, t) = e.labels[0].span.range;
    assert_eq!(&main[s as usize..t as usize], "Just a b", "{e:?}");
}

#[test]
fn a_ctor_pattern_with_too_few_arguments_is_an_arity_error() {
    let main = format!(
        "{HEAD}type Pair\n    = Pair Int Int\n\n\
         first : Pair -> Int\nfirst p =\n    case p of\n        Pair a ->\n            a\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = errors(&main);
    assert!(
        errs.iter().any(|d| d.code.0 == "E2007"
            && d.message
                .contains("`Pair` takes 2 arguments, but this pattern gives it 1")),
        "{errs:?}"
    );
}

#[test]
fn a_ctor_pattern_with_the_right_arity_checks_clean() {
    let main = format!(
        "{HEAD}type Pair\n    = Pair Int Int\n\n\
         first : Pair -> Int\nfirst p =\n    case p of\n        Pair a _ ->\n            a\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = errors(&main);
    assert!(errs.is_empty(), "{errs:?}");
}
