//! Entry-vs-dependency type-check consistency for an ANNOTATED-record-typed
//! module (issue: a module accepted as a build entry was REJECTED with a bogus
//! "record is missing field(s)" error when it was a non-entry dependency of a
//! multi-module project).
//!
//! ## The defect this locks
//!
//! A module's type-check verdict must be a function of that module + its imports
//! + the declarations it can reach — NEVER of the mere *presence* of an
//! unrelated module elsewhere in the project's source root.
//!
//! The break: a signature that references a type alias it neither declares nor
//! imports (`Auth.wrap : Req -> String`, with `Req` declared in `Http` and NOT
//! imported by `Auth`) resolves that reference through the compiler's BARE,
//! program-wide alias fallback table (`crate::sig::World::aliases`). That table
//! was LAST-WRITER-WINS across modules, and the module loader adds stdlib-style
//! modules before app modules, so a later app module that happened to declare a
//! same-named `type alias Req` (with a *different* field set) silently
//! OVERWROTE the entry and poisoned the earlier signature. The verdict for a
//! third module (`B`, which correctly imports `Http.Req` and calls `Auth.wrap`)
//! then flipped depending on whether the colliding module was loaded — the exact
//! entry-passes / dependency-fails split.
//!
//! ## The fixture
//!
//!   * `Http`    — declares `type alias Req = { 8 String fields }`.
//!   * `Auth`    — `wrap : Req -> String`, referencing `Req` WITHOUT importing
//!                 it (exercises the bare-alias leniency fallback).
//!   * `B`       — imports `Http.Req` + `Auth.wrap`; `blank : Req` is an 8-field
//!                 record literal; `v = wrap blank`. B is the annotated-record
//!                 module under test.
//!   * `A`       — the "entry" module; imports NOTHING from B.
//!   * `Collide` — an unrelated app module declaring its OWN, SMALLER
//!                 `type alias Req = { 5 String fields }`.
//!
//! Loading order mirrors the real project (`Http` before `Collide`), so the old
//! last-writer-wins table let `Collide.Req` clobber `Http.Req`.
//!
//! ## The assertion
//!
//! `B` type-checks with ZERO type errors BOTH without `Collide` loaded and with
//! it loaded. Before the fix the second case reported the bogus missing-fields
//! error (RED); after the fix both are clean (GREEN).

use hir::SourceDb;

const HTTP: &str = "module Http exposing (Req)\n\
    \n\
    type alias Req =\n\
    \x20   { f1 : String\n\
    \x20   , f2 : String\n\
    \x20   , f3 : String\n\
    \x20   , f4 : String\n\
    \x20   , f5 : String\n\
    \x20   , f6 : String\n\
    \x20   , f7 : String\n\
    \x20   , f8 : String\n\
    \x20   }\n";

// `wrap` references `Req` but does NOT import it — resolves via the bare-alias
// leniency fallback, exactly like the stdlib `Std.Auth.setSlidingCookie`.
const AUTH: &str = "module Auth exposing (wrap)\n\
    \n\
    wrap : Req -> String\n\
    wrap r =\n\
    \x20   r.f1\n";

const B: &str = "module B exposing (v)\n\
    \n\
    import Http exposing (Req)\n\
    import Auth exposing (wrap)\n\
    \n\
    blank : Req\n\
    blank =\n\
    \x20   { f1 = \"a\"\n\
    \x20   , f2 = \"b\"\n\
    \x20   , f3 = \"c\"\n\
    \x20   , f4 = \"d\"\n\
    \x20   , f5 = \"e\"\n\
    \x20   , f6 = \"f\"\n\
    \x20   , f7 = \"g\"\n\
    \x20   , f8 = \"h\"\n\
    \x20   }\n\
    \n\
    v : String\n\
    v =\n\
    \x20   wrap blank\n";

const A: &str = "module A exposing (main)\n\
    \n\
    main : String\n\
    main =\n\
    \x20   \"hi\"\n";

// An unrelated app module that declares its OWN, smaller, same-named alias.
const COLLIDE: &str = "module Collide exposing (sample)\n\
    \n\
    type alias Req =\n\
    \x20   { f1 : String\n\
    \x20   , f2 : String\n\
    \x20   , f3 : String\n\
    \x20   , f4 : String\n\
    \x20   , f5 : String\n\
    \x20   }\n\
    \n\
    sample : Req\n\
    sample =\n\
    \x20   { f1 = \"a\", f2 = \"b\", f3 = \"c\", f4 = \"d\", f5 = \"e\" }\n";

fn parse(src: &str) -> syntax::Parse {
    syntax::parse(src, base::FileId(0))
}

/// Build a db with the given modules (in order), then return `B`'s type-error
/// count. `A` is added first as the notional entry (imports nothing from B).
fn b_type_errors(with_collide: bool) -> usize {
    let mut db = SourceDb::new();
    // `A` (entry) first, then the `Http`→`Auth`→`B` chain. `Collide` — when
    // present — is added AFTER `Http`, so a last-writer-wins bare-alias table
    // would let it clobber `Http.Req` (the pre-fix defect).
    db.add_module("A", parse(A));
    db.add_module("Http", parse(HTTP));
    db.add_module("Auth", parse(AUTH));
    let b_mid = db.add_module("B", parse(B));
    if with_collide {
        db.add_module("Collide", parse(COLLIDE));
    }
    ty::check_modules(&db, &[b_mid]).type_errors
}

#[test]
fn dependency_module_typechecks_without_collision() {
    // Sanity: `B` is well-typed on its own (no colliding module present).
    let errs = b_type_errors(false);
    assert_eq!(
        errs, 0,
        "PRECONDITION — B (blank : Http.Req; v = wrap blank) should type-check \
         clean with no colliding module, got {errs} type error(s)"
    );
}

#[test]
fn colliding_unrelated_module_does_not_change_the_verdict() {
    // The defect: loading an unrelated `Collide` (its own smaller `Req`) flips
    // B's verdict, because `Auth.wrap`'s un-imported `Req` resolves through the
    // program-wide bare-alias table that `Collide` clobbered.
    let without = b_type_errors(false);
    let with = b_type_errors(true);
    assert_eq!(
        with, without,
        "ENTRY/DEPENDENCY DIVERGENCE — B's type-error count changed from \
         {without} to {with} merely because an unrelated module (`Collide`, \
         with its own same-named `type alias Req`) was loaded. A module's \
         verdict must not depend on unrelated modules' presence."
    );
    assert_eq!(
        with, 0,
        "SPURIOUS REJECT — B was rejected ({with} type error(s)) as a \
         dependency alongside an unrelated same-named alias, though it is \
         well-typed (it imports the 8-field `Http.Req` and calls `Auth.wrap`)."
    );
}
