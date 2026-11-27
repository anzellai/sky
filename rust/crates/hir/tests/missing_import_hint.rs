//! An `[E1001]` for a qualified name `Q.x` whose qualifier `Q` no import
//! names says which import to add, when exactly one stdlib module is written
//! `Q` (its last segment, or its last two segments run together, as
//! `DbDecode` for `Std.Db.Decode`) and exports `x`. No hint when two such
//! modules export `x`, or when `Q` is imported and lacks `x`.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

const ERROR: &str = "module Sky.Core.Error exposing (Error, invalidInput)\n\n\
                     type Error\n    = Error String\n\n\
                     invalidInput : String -> Error\ninvalidInput s =\n    Error s\n";
const JSON_DECODE: &str = "module Sky.Core.Json.Decode exposing (int)\n\nint : Int\nint =\n    1\n";
const DB_DECODE: &str =
    "module Std.Db.Decode exposing (int, column)\n\nint : Int\nint =\n    2\n\ncolumn : Int\ncolumn =\n    3\n";

fn resolve_main(main: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Sky.Core.Error", syntax::parse(ERROR, FileId(0)));
    db.add_module(
        "Sky.Core.Json.Decode",
        syntax::parse(JSON_DECODE, FileId(1)),
    );
    db.add_module("Std.Db.Decode", syntax::parse(DB_DECODE, FileId(2)));
    db.add_module("Main", syntax::parse(main, FileId(3)));
    resolve(&db, db.module_by_name("Main").unwrap())
}

fn e1001(main_body: &str, imports: &str) -> String {
    let r = resolve_main(&format!(
        "module Main exposing (main)\n{imports}\nmain =\n    {main_body}\n"
    ));
    let m: Vec<_> = r
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1001")
        .map(|d| d.message.clone())
        .collect();
    assert_eq!(m.len(), 1, "{:?}", r.diagnostics);
    m[0].clone()
}

#[test]
fn an_unimported_qualifier_names_the_import() {
    let m = e1001("Error.invalidInput \"bad\"", "");
    assert!(m.contains("Undefined name: Error.invalidInput"), "{m}");
    assert!(m.contains("Add `import Sky.Core.Error as Error`."), "{m}");
}

#[test]
fn a_two_segment_alias_names_the_import() {
    let m = e1001("DbDecode.column", "");
    assert!(m.contains("Add `import Std.Db.Decode as DbDecode`."), "{m}");
}

#[test]
fn a_unique_exporter_under_a_shared_last_segment_names_the_import() {
    // Two modules end in `Decode`; only `Std.Db.Decode` exports `column`.
    let m = e1001("Decode.column", "");
    assert!(m.contains("Add `import Std.Db.Decode as Decode`."), "{m}");
}

#[test]
fn an_ambiguous_qualifier_gives_no_import_hint() {
    // `Sky.Core.Json.Decode` and `Std.Db.Decode` both export `int`.
    let m = e1001("Decode.int", "");
    assert!(!m.contains("Add `import"), "{m}");
}

#[test]
fn an_imported_qualifier_without_the_name_gives_no_import_hint() {
    let m = e1001("Error.nothingHere", "import Sky.Core.Error as Error\n");
    assert!(!m.contains("Add `import"), "{m}");
    let m = e1001("Error.nothingHere", "");
    assert!(!m.contains("Add `import"), "{m}");
}
