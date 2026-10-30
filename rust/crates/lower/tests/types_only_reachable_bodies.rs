//! Regression: lowering infers the types of the bodies the program can reach,
//! not of every body of every interned module (v0.29.0).
//!
//! The build interns the whole stdlib whether a program uses it or not, and
//! lowering used to run per-def body inference over every top-level def of
//! every interned module before its DCE work-list pruned the unused ones. A
//! build's memory then grew with the size of the stdlib rather than with the
//! program: when `Std.Db.Store` grew by several thousand lines, the warm build
//! of the 50-field benchmark app crossed its peak-RSS budget (build-perf, both
//! `web` and `web:app`), although the app used only a few Store functions.
//!
//! This pins the bound: a module with many unused functions adds nothing to
//! the bodies lowering types.

use base::FileId;
use hir::SourceDb;

#[test]
fn lowering_types_only_the_bodies_main_reaches() {
    const UNUSED: usize = 40;
    let mut lib = String::from("module Lib exposing (..)\n\nused : Int -> Int\nused x =\n    helper x + 1\n\nhelper : Int -> Int\nhelper x =\n    x * 2\n\n");
    for i in 0..UNUSED {
        lib.push_str(&format!(
            "unused{i} : Int -> Int\nunused{i} x =\n    x + {i}\n\n"
        ));
    }
    let main = "module Main exposing (main)\n\
                import Lib\n\n\
                main =\n    Lib.used 1\n";
    let mut db = SourceDb::new();
    db.add_module("Lib", syntax::parse(&lib, FileId(0)));
    let mid = db.add_module("Main", syntax::parse(main, FileId(1)));

    let before = lower::bodies_typed();
    let out = lower::lower_program(&db, mid);
    let typed = lower::bodies_typed() - before;

    assert!(out.entry_ok, "the program lowers: {:?}", out.errors);
    assert!(out.errors.is_empty(), "{:?}", out.errors);
    // `main`, `Lib.used` and `Lib.helper`: the closure of `main`.
    assert_eq!(
        typed, 3,
        "lowering must type only the {} bodies main reaches, not the {UNUSED} unused ones \
         (typed {typed})",
        3
    );
    // The reachable defs are still lowered.
    let text = format!("{:?}", out.items);
    assert!(
        text.contains("Lib_used") && text.contains("Lib_helper"),
        "{text}"
    );
    assert!(
        !text.contains("Lib_unused0"),
        "an unused def is not emitted"
    );
}
