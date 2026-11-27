//! v0.32.0: a kernel-implicit type name is not a wildcard over same-named
//! stdlib types with a different runtime form.
//!
//! The bare `Decoder` (`hir::KERNEL_IMPLICIT_TYPES`) used to be "module
//! unknown" to `ty::nominal::same`, so it unified with `Std.Db.Decode.Decoder`,
//! `Std.Config.Decoder` and `Std.Xml.Decode.Decoder`. A row decoder passed to
//! `Json.Decode.decodeString` passed `sky check` and panicked in `rt.AsInt`.
//! The bare name now has a HOME (`hir::KERNEL_IMPLICIT_HOMES`): it is the
//! JSON decoder, declared in `Sky.Core.Json.Decode`.

use std::path::PathBuf;
use std::sync::OnceLock;

fn root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| ty::reject_corpus::load_stdlib(&root()))
}

fn codes(imports: &str, body: &str) -> Vec<String> {
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         {imports}\n\n\n{body}\n"
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
        .map(|d| d.code.0.clone())
        .collect()
}

const IMPORTS: &str = "import Sky.Core.Json.Decode as Decode\n\
                       import Std.Db as Db\n\
                       import Std.Db.Decode as DbDecode\n\
                       import Std.Config as Config\n\
                       import Std.Xml.Decode as XmlDecode";

fn run_json(dec: &str) -> String {
    format!(
        "main =\n    case Decode.decodeString ({dec}) \"3\" of\n        \
         Ok _ ->\n            println \"ok\"\n\n        Err _ ->\n            println \"err\"\n"
    )
}

#[test]
fn a_json_decoder_still_runs_as_a_json_decoder() {
    assert_eq!(
        codes(IMPORTS, &run_json("Decode.int")),
        Vec::<String>::new()
    );
    // The bare, unimported spelling is the JSON decoder.
    let body = format!(
        "dec : Decoder Int\ndec =\n    Decode.int\n\n\n{}",
        run_json("dec")
    );
    assert_eq!(codes(IMPORTS, &body), Vec::<String>::new());
}

#[test]
fn a_db_decoder_is_not_a_json_decoder() {
    assert_eq!(codes(IMPORTS, &run_json("DbDecode.int \"age\"")), ["E2001"]);
    // Through a bare `Decoder` annotation (the old wildcard).
    let body = format!(
        "dec : Decoder Int\ndec =\n    DbDecode.int \"age\"\n\n\n{}",
        run_json("dec")
    );
    assert_eq!(codes(IMPORTS, &body), ["E2001"]);
}

#[test]
fn a_json_decoder_is_not_a_db_decoder() {
    let body = "run : Db -> Task Error (List Int)\nrun db =\n    \
                Db.queryDecode db \"SELECT 1\" [] Decode.int\n\n\n\
                main =\n    println \"x\"\n";
    assert_eq!(codes(IMPORTS, body), ["E2001"]);
    let ok = "run : Db -> Task Error (List Int)\nrun db =\n    \
              Db.queryDecode db \"SELECT 1\" [] (DbDecode.int \"n\")\n\n\n\
              main =\n    println \"x\"\n";
    assert_eq!(codes(IMPORTS, ok), Vec::<String>::new());
}

#[test]
fn config_and_xml_decoders_are_not_json_decoders() {
    assert_eq!(codes(IMPORTS, &run_json("Config.int")), ["E2001"]);
    assert_eq!(codes(IMPORTS, &run_json("XmlDecode.string")), ["E2001"]);
}

/// The audit of `hir::KERNEL_IMPLICIT_TYPES` (v0.32.0). A kernel-implicit name
/// that some stdlib module ALSO declares as a union is a candidate collision:
/// the bare name could stand for a type with another runtime form. Each such
/// name either has a HOME (the bare name IS that declaration) or is listed here
/// with the reason the bare name is not a second runtime form.
const REVIEWED_WITHOUT_HOME: &[(&str, &str)] = &[
    // `Sky.Core.Error.Error` is the runtime form of the kernel `Error`: the
    // same value, so the bare name and the declaration agree.
    ("Error", "same runtime form as Sky.Core.Error.Error"),
    // `Std.Html.Attributes`, `Std.Html.Document`, `Std.Ui` and `Std.Xml.Encode`
    // declare their own `Attribute`; every stdlib signature names its own (by
    // an import or a local declaration), and two qualified ones never unify.
    (
        "Attribute",
        "no stdlib signature writes the bare kernel Attribute",
    ),
    // `Std.App`, `Std.Live`, `Std.Spa` and `Sky.Http.Server` declare `Route`.
    ("Route", "no stdlib signature writes the bare kernel Route"),
    // `Std.Db.Store` and `Std.Ai.Store` declare `Store`.
    ("Store", "no stdlib signature writes the bare kernel Store"),
];

#[test]
fn every_kernel_implicit_name_that_a_stdlib_union_shares_is_reviewed() {
    let mut declared: std::collections::BTreeMap<String, Vec<String>> = Default::default();
    let stdlib = root().join("sky-stdlib");
    let mut stack = vec![stdlib.clone()];
    while let Some(dir) = stack.pop() {
        for e in std::fs::read_dir(&dir).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_some_and(|x| x == "sky") {
                let text = std::fs::read_to_string(&p).unwrap();
                for line in text.lines() {
                    let Some(rest) = line.strip_prefix("type ") else {
                        continue;
                    };
                    if rest.starts_with("alias ") {
                        continue;
                    }
                    let name = rest.split_whitespace().next().unwrap_or("");
                    if hir::KERNEL_IMPLICIT_TYPES.contains(&name) {
                        let rel = p.strip_prefix(&stdlib).unwrap().display().to_string();
                        declared.entry(name.to_string()).or_default().push(rel);
                    }
                }
            }
        }
    }
    for (name, files) in &declared {
        let home = hir::KERNEL_IMPLICIT_HOMES.iter().find(|(n, _)| n == name);
        let reviewed = REVIEWED_WITHOUT_HOME.iter().any(|(n, _)| n == name);
        assert!(
            home.is_some() || reviewed,
            "kernel-implicit `{name}` is also declared as a union in {files:?}: \
             give it a home (hir::KERNEL_IMPLICIT_HOMES) or review it here"
        );
        if let Some((_, module)) = home {
            let file = format!("{}.sky", module.replace('.', "/"));
            assert!(
                files.contains(&file),
                "the home of `{name}` ({module}) must declare it; declared in {files:?}"
            );
        }
    }
    for (name, _) in REVIEWED_WITHOUT_HOME {
        assert!(declared.contains_key(*name), "stale review entry `{name}`");
    }
}
