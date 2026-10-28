//! Every stdlib module BODY type-checks.
//!
//! A build trusts the stdlib's signatures and never checks its bodies (see
//! `ty::stdlib_bodies`). A stdlib record literal that lacked a newly added
//! field built clean and passed every gate. These tests hold the stdlib to the
//! checker a user module gets, and prove the check can fail.

use std::path::PathBuf;
use std::sync::OnceLock;
use ty::reject_corpus::load_stdlib;
use ty::stdlib_bodies::{check_stdlib_modules, ALLOWED_PRELUDE_SHADOWS};

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

/// The stdlib with one module's source edited by `edit`.
fn overlay(module: &str, edit: impl Fn(&str) -> String) -> Vec<(String, syntax::Parse)> {
    let mut found = false;
    let out = stdlib()
        .iter()
        .map(|(n, p)| {
            if n == module {
                found = true;
                let src = edit(&p.syntax().text().to_string());
                (n.clone(), syntax::parse(&src, base::FileId(0)))
            } else {
                (n.clone(), p.clone())
            }
        })
        .collect();
    assert!(found, "no stdlib module {module}");
    out
}

#[test]
fn every_stdlib_module_body_type_checks() {
    let report = check_stdlib_modules(stdlib());
    assert!(
        report.modules.len() >= 100,
        "loaded only {} stdlib modules",
        report.modules.len()
    );
    assert!(
        report.clean(),
        "stdlib bodies that do not type-check:\n{}",
        report.render()
    );
}

/// The shape that shipped: a record literal in a stdlib body that lacks a
/// field its annotated type requires. `Std.Ai.Agent.toolRespCodec` is
/// annotated `Codec Provider.ToolResponse`; dropping `continuation` from its
/// `ToolCall` witness must be refused, as it is in a user module.
#[test]
fn a_record_literal_missing_a_field_in_a_stdlib_body_is_refused() {
    let from = "arguments = \"\", continuation = Nothing } ]";
    let world = overlay("Std.Ai.Agent", |src| {
        assert_eq!(src.matches(from).count(), 1, "mutation site moved");
        src.replacen(from, "arguments = \"\" } ]", 1)
    });
    let report = check_stdlib_modules(&world);
    let agent = report
        .modules
        .iter()
        .find(|m| m.module == "Std.Ai.Agent")
        .expect("Std.Ai.Agent checked");
    assert!(
        agent
            .errors
            .iter()
            .any(|e| e.contains("[E2001]") && e.contains("continuation")),
        "missing-field record literal not refused: {:?}",
        agent.errors
    );
}

/// A body that contradicts its own annotation is refused.
#[test]
fn a_body_that_contradicts_its_annotation_is_refused() {
    let world = overlay("Sky.Core.Maybe", |src| {
        format!("{src}\n\nprobeBad : Int\nprobeBad =\n    \"not an int\"\n")
    });
    let report = check_stdlib_modules(&world);
    let errs: Vec<&String> = report
        .modules
        .iter()
        .filter(|m| m.module == "Sky.Core.Maybe")
        .flat_map(|m| &m.errors)
        .collect();
    assert!(
        errs.iter()
            .any(|e| e.contains("[E2001]") && e.contains("probeBad")),
        "ill-typed stdlib body not refused: {errs:?}"
    );
}

/// `Sky.Ffi` stays a user-code rule: the stdlib is granted it.
#[test]
fn the_stdlib_keeps_its_ffi_grant() {
    let report = check_stdlib_modules(stdlib());
    assert!(
        !report.render().contains("[E1011]"),
        "stdlib refused Sky.Ffi:\n{}",
        report.render()
    );
}

/// The `[E1004]` allowance list can only shrink: an entry that stops firing
/// fails the check.
#[test]
fn a_stale_prelude_shadow_allowance_fails() {
    let a = &ALLOWED_PRELUDE_SHADOWS[ALLOWED_PRELUDE_SHADOWS.len() - 1];
    assert_eq!(a.module, "Sky.Config");
    let world = overlay("Sky.Config", |src| {
        src.replace("    | Error\n", "    | ErrorLevel\n")
            .replace("        Error ->\n", "        ErrorLevel ->\n")
    });
    let report = check_stdlib_modules(&world);
    assert!(
        report
            .stale_allowances
            .iter()
            .any(|s| s.starts_with("Sky.Config:")),
        "a removed shadow did not mark its allowance stale"
    );
    assert!(!report.clean());
}
