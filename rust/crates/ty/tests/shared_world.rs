//! Gates on the shared-world corpus path (`ty::shared`, CI/test-architecture
//! v2 §1.3 route C-2, §1.4 hazards (a) and (b)).
//!
//! v2 §1.4 states both hazards are "designed for, and each ships a gate". These
//! are those gates. Each one carries its own falsifier *inline*: the test does
//! not merely assert the safe behaviour, it also exhibits the unsafe behaviour
//! the design avoids, so the assertion cannot quietly become vacuous if the
//! mechanism is removed.

use ty::shared::{Fallback, SharedWorld, WorldSource};

fn p(src: &str) -> syntax::Parse {
    syntax::parse(src, base::FileId(0))
}

/// A minimal "stdlib" base: enough to have a module set, a bare alias name and a
/// shadowable module name, without loading the real 87-module stdlib.
fn base() -> Vec<(String, syntax::Parse)> {
    vec![
        (
            "Std.Log".to_string(),
            p("module Std.Log exposing (tag)\n\ntype alias Tag = String\n\ntag : String\ntag = \"log\"\n"),
        ),
        (
            "Std.Cfg".to_string(),
            p("module Std.Cfg exposing (limit)\n\nlimit : Int\nlimit = 3\n"),
        ),
    ]
}

// ---------------------------------------------------------------------------
// v2 §1.4 (a) — DefId leakage across cases
// ---------------------------------------------------------------------------

/// **Gate `corpus.defid-noleak`.** Consecutive cases must not be judged against
/// each other's `DefId`-keyed signatures.
///
/// # Correction to v2 §1.4(a)
///
/// v2 prescribes this gate as *"run two consecutive cases that both declare
/// `Main.main`; assert the `DefId` sets they intern are **disjoint**"*. Measured:
/// **forking does not produce disjoint `DefId`s, and disjointness is not the
/// property that matters.**
///
/// `DefTable::intern` keys on `(module.index(), name, kind)`, and a fork clones
/// the base interner — so two forks of the same base, each adding a module named
/// `Main` at the same next index, mint the *same* `DefId` for `Main.main` by
/// construction. Both forks are internally consistent; the ids simply coincide
/// across two disjoint universes. Requiring disjointness would fail a correct
/// implementation, and the only way to satisfy it would be to perturb id
/// allocation for no benefit.
///
/// What actually has to hold — and what the hazard is really about — is that
/// case *N+1*'s **world** carries none of case *N*'s entries. That is what this
/// gate asserts, and the inline falsifier below exhibits the genuine leak: a
/// naive path that reuses ONE world across both cases judges case B's
/// unannotated `main` against case A's declared `main : Int`, and reports a type
/// error for a program that is clean on its own. Forking removes it.
#[test]
fn defid_no_leak_across_consecutive_cases() {
    let shared = SharedWorld::new(&base());

    // A declares `main : Int` (annotated → lands in `value_sigs`, keyed by DefId).
    let case_a = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\nmain : Int\nmain = 1\n"),
    )];
    // B leaves `main` UNANNOTATED with a String body, so B contributes no
    // `value_sigs` entry of its own — any `main : Int` in force can only have
    // come from A.
    let case_b = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\nmain = \"two\"\n"),
    )];

    let a = shared.check_case(&case_a, &["Main".to_string()]);
    let b = shared.check_case(&case_b, &["Main".to_string()]);
    assert_eq!(a.out.type_errors, 0);
    assert_eq!(
        b.out.type_errors, 0,
        "case B was judged against case A's `main : Int`: {:?}",
        b.out
            .diagnostics
            .iter()
            .map(|d| d.message.clone())
            .collect::<Vec<_>>()
    );

    // The ids DO coincide — recorded here so the correction above is a measured
    // fact in the test, not a claim in a comment.
    assert_eq!(
        a.case_def_ids, b.case_def_ids,
        "if forks ever stop minting coinciding ids, v2 §1.4(a)'s disjointness \
         wording becomes satisfiable and this gate should be revisited"
    );

    // --- inline falsifier: ONE world reused across both cases (no fork) ---
    let mut naive_db = hir::SourceDb::new();
    for (n, parse) in base() {
        naive_db.add_module(&n, parse);
    }
    let mut naive_world = ty::World::build(&naive_db);

    let ida = naive_db.add_module("Main", case_a[0].1.clone());
    {
        let scoped = ty::shared::ScopedDb::new(&naive_db, vec![ida]);
        naive_world.extend_decls(&scoped, false);
        naive_world.extend_bodies(&scoped);
    }
    // Same name → same ModuleId → same DefId; the world still holds A's sig.
    let idb = naive_db.add_module("Main", case_b[0].1.clone());
    {
        let scoped = ty::shared::ScopedDb::new(&naive_db, vec![idb]);
        naive_world.extend_decls(&scoped, false);
        naive_world.extend_bodies(&scoped);
    }
    let leaked = ty::check_modules_with_world(
        &naive_db,
        std::rc::Rc::new(naive_world),
        &[idb],
    );
    assert!(
        leaked.type_errors > 0,
        "the cross-case leak this gate guards is not reproducible — the \
         assertion above may be vacuous"
    );
}

/// The fork must also keep the two cases' *verdicts* independent: case A is
/// well-typed, case B misuses its own `main`, and neither may be judged against
/// the other's signature.
#[test]
fn consecutive_cases_do_not_leak_verdicts() {
    let shared = SharedWorld::new(&base());
    let good = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\nmain : Int\nmain = 1\n"),
    )];
    let bad = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\nmain : Int\nmain = \"not an int\"\n"),
    )];

    let a = shared.check_case(&good, &["Main".to_string()]);
    assert_eq!(a.out.type_errors, 0, "well-typed case reported errors");
    let b = shared.check_case(&bad, &["Main".to_string()]);
    assert!(b.out.type_errors > 0, "ill-typed case was accepted");
    // …and running the good one again after the bad one is still clean.
    let c = shared.check_case(&good, &["Main".to_string()]);
    assert_eq!(
        c.out.type_errors, 0,
        "the previous case's errors leaked into a later case"
    );
}

// ---------------------------------------------------------------------------
// v2 §1.4 (b) — a prebuilt world is WRONG for a shadowing case
// ---------------------------------------------------------------------------

/// **The shadowing fallback fires, and is a reported state.** A case declaring a
/// module named like a base module must NOT be checked against the prebuilt
/// world — that world still contains the shadowed module's declarations.
#[test]
fn shadowing_case_falls_back_and_is_counted() {
    let shared = SharedWorld::new(&base());

    let shadow = vec![
        (
            "Std.Log".to_string(),
            p("module Std.Log exposing (tag)\n\ntag : Int\ntag = 7\n"),
        ),
        (
            "Main".to_string(),
            p("module Main exposing (main)\n\nimport Std.Log exposing (tag)\n\nmain : Int\nmain = tag\n"),
        ),
    ];

    assert_eq!(
        shared.fallback_reason(&shadow),
        Some(Fallback::ShadowsStdlibModule),
        "a case shadowing a base module was not detected before forking"
    );

    let c = shared.check_case(&shadow, &["Main".to_string()]);
    assert_eq!(
        c.source,
        WorldSource::Rebuilt(Fallback::ShadowsStdlibModule),
        "the shadowing case took the shared path — it must be a REPORTED rebuild"
    );

    // The shadowed `tag : Int` is what must be in force: `main : Int = tag`
    // type-checks only against the case's own Std.Log, not the base's `String`.
    assert_eq!(
        c.out.type_errors, 0,
        "the case was checked against the SHADOWED module's declarations: {:?}",
        c.out
            .diagnostics
            .iter()
            .map(|d| d.message.clone())
            .collect::<Vec<_>>()
    );

    // A non-shadowing case still takes the shared path, so the fallback is a
    // narrow, counted state rather than the default.
    let plain = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\nmain : Int\nmain = 1\n"),
    )];
    assert_eq!(shared.fallback_reason(&plain), None);
    assert_eq!(
        shared.check_case(&plain, &["Main".to_string()]).source,
        WorldSource::Shared
    );
}

/// **The bare-alias-collision fallback fires.** The bare `aliases` table is
/// last-writer-wins and completed before any signature expands, so a case alias
/// colliding on a bare name can change how a BASE signature expands — something
/// a world whose base pass 2 has already run cannot represent.
#[test]
fn bare_alias_collision_falls_back_and_is_counted() {
    let shared = SharedWorld::new(&base());

    // `Tag` is declared by the base's `Std.Log`.
    let collide = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\ntype alias Tag = Int\n\nmain : Tag\nmain = 1\n"),
    )];
    assert_eq!(
        shared.fallback_reason(&collide),
        Some(Fallback::BareAliasCollision)
    );
    assert_eq!(
        shared.check_case(&collide, &["Main".to_string()]).source,
        WorldSource::Rebuilt(Fallback::BareAliasCollision)
    );

    // A non-colliding alias name keeps the shared path.
    let fine = vec![(
        "Main".to_string(),
        p("module Main exposing (main)\n\ntype alias Marker = Int\n\nmain : Marker\nmain = 1\n"),
    )];
    assert_eq!(shared.fallback_reason(&fine), None);
    assert_eq!(
        shared.check_case(&fine, &["Main".to_string()]).source,
        WorldSource::Shared
    );
}

// ---------------------------------------------------------------------------
// Equivalence at unit scale (the corpus-scale proof is `xtask shared-world`)
// ---------------------------------------------------------------------------

/// The shared path and the whole-program path agree on a multi-module case,
/// including the inferred type table — not just the error counts.
#[test]
fn shared_and_whole_program_agree_on_a_multi_module_case() {
    let shared = SharedWorld::new(&base());
    let case = vec![
        (
            "App.Util".to_string(),
            p("module App.Util exposing (double)\n\ndouble n = n + n\n"),
        ),
        (
            "Main".to_string(),
            p("module Main exposing (main)\n\nimport App.Util exposing (double)\n\nmain = double 21\n"),
        ),
    ];
    let to_check = vec!["App.Util".to_string(), "Main".to_string()];

    let c = shared.check_case(&case, &to_check);
    assert_eq!(c.source, WorldSource::Shared);

    let mut db = hir::SourceDb::new();
    for (n, parse) in base() {
        db.add_module(&n, parse);
    }
    let mut ids = Vec::new();
    for (n, parse) in &case {
        ids.push(db.add_module(n, parse.clone()));
    }
    let reference = ty::check_modules(&db, &ids);

    assert_eq!(c.out.type_errors, reference.type_errors);
    assert_eq!(c.out.name_errors, reference.name_errors);
    assert_eq!(
        c.out.exhaustiveness_warnings,
        reference.exhaustiveness_warnings
    );

    let render = |o: &ty::CheckOutput| {
        let mut v: Vec<String> = o
            .def_types
            .iter()
            .map(|t| format!("{}.{}|{}", t.module, t.name, t.ty.render()))
            .collect();
        v.sort();
        v
    };
    assert_eq!(
        render(&c.out),
        render(&reference),
        "shared world produced a different inferred type table"
    );
}
