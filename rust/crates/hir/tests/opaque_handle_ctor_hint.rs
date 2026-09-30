//! S1 request (A-1b migration): code that builds or matches one of the five
//! handle constructors v0.27.0 made opaque (`WebSocket`, `WebSocketServer`,
//! `StreamId`, `StreamWriter`, `Cache`) is refused with a message that says
//! why and points at the migration guide. Any other hidden constructor keeps
//! the plain message.
//!
//! The stdlib modules here are stand-ins in the v0.27.0 shape (type exported,
//! constructor not), so the test does not depend on which branch has landed
//! the stdlib change.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};

const CACHE: &str = "module Std.Cache exposing (Cache, new)\n\n\
                     type Cache k v\n    = Cache Int\n\n\
                     new : Int -> Cache k v\nnew n =\n    Cache n\n";

const OTHER: &str = "module Lib.Box exposing (Box, make)\n\n\
                     type Box\n    = Box Int\n\n\
                     make : Int -> Box\nmake n =\n    Box n\n";

fn resolve_main(main: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Std.Cache", syntax::parse(CACHE, FileId(0)));
    db.add_module("Lib.Box", syntax::parse(OTHER, FileId(1)));
    db.add_module("Main", syntax::parse(main, FileId(2)));
    resolve(&db, db.module_by_name("Main").unwrap())
}

fn messages(r: &ResolveResult) -> String {
    r.diagnostics
        .iter()
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect::<Vec<_>>()
        .join("\n")
}

const ANCHOR: &str = "See docs/migration/v0.27.md#opaque-handle-constructors";

#[test]
fn building_a_cache_handle_names_the_change() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Cache as Cache\n\nmain =\n    Cache.Cache 3\n",
    );
    let m = messages(&r);
    assert!(
        m.contains("constructor `Cache` of `Std.Cache` is no longer exported"),
        "{m}"
    );
    assert!(m.contains(ANCHOR), "{m}");
}

#[test]
fn matching_a_cache_handle_names_the_change() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Cache as Cache\n\n\
         idOf c =\n    case c of\n        Cache.Cache n ->\n            n\n\nmain =\n    1\n",
    );
    let m = messages(&r);
    assert!(m.contains(ANCHOR), "{m}");
}

#[test]
fn exposing_the_handle_constructors_names_the_change() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Std.Cache exposing (Cache(..))\n\nmain =\n    1\n",
    );
    let m = messages(&r);
    assert!(m.contains("[E1013]"), "{m}");
    assert!(m.contains(ANCHOR), "{m}");
}

#[test]
fn another_hidden_constructor_keeps_the_plain_message() {
    let r = resolve_main(
        "module Main exposing (main)\nimport Lib.Box as Box\n\nmain =\n    Box.Box 3\n",
    );
    let m = messages(&r);
    assert!(m.contains("without its constructors"), "{m}");
    assert!(!m.contains("migration"), "{m}");
}
