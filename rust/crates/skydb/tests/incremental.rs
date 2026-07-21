//! Incremental-correctness harness (doc 11 §L2, doc 12 §M1 risk #M1) — the
//! salsa **invalidation** unit tests doc 11 flags as still-missing (until now
//! incrementality was only exercised cold, through the LSP suite over the eager
//! `SourceDb`; a stale-memo / over-or-under-invalidation bug would pass every
//! one of the 8 from-scratch gates while being wrong after an edit).
//!
//! Each test:
//!
//! 1. assembles a small multi-module `SkyDatabase` from in-memory sources,
//! 2. runs a tracked query (`resolve` — which pulls `parse` + `module_exports`),
//! 3. edits ONE file's `SourceFile` text input (`set_text`),
//! 4. re-runs and asserts BOTH (a) + (b) below.
//!
//! (a) The RIGHT set of queries recompute — captured from salsa's
//! `EventKind::WillExecute` stream via the db's event sink, asserting an
//! unrelated module did NOT re-execute while the affected one DID.
//!
//! (b) The incremental result is CORRECT — byte-equal to a fresh from-scratch
//! computation, compared through a `DefId`-independent projection (raw `DefId`
//! values are interning-order-dependent across databases, so they are rendered
//! through `def_loc`).
//!
//! Extension seam: add a scenario by calling `Fixture::build`, editing a file,
//! then `assert_recompute` + `assert_matches_fresh`. New tracked queries become
//! verifiable for invalidation by asserting on their `WillExecute` name here.

use base::{DefId, ModuleId};
use hir::{ResolveResult, SkyDb};
use lower::LowerConfig;
use salsa::Setter;
use skydb::{go_program, BuildConfig, SkyDatabase, SourceFile};
use std::sync::{Arc, Mutex};
use ty::TyDb;

// ---- fixtures -------------------------------------------------------------

const LIB_V1: &str = "module Lib exposing (greeting)\n\ngreeting = \"hi\"\n";
// V2 adds an EXPORT (`shout`) — an exports change a dependent must observe.
const LIB_V2: &str =
    "module Lib exposing (greeting, shout)\n\ngreeting = \"hi\"\n\nshout = \"HI\"\n";
const APP: &str =
    "module App exposing (main)\n\nimport Lib exposing (greeting)\n\nmain = greeting\n";
// Other is an unrelated sibling — imports nothing from Lib/App.
const OTHER_V1: &str = "module Other exposing (x)\n\nx = 1\n";
const OTHER_V2: &str = "module Other exposing (x, y)\n\nx = 1\n\ny = 2\n";

struct Fixture {
    db: SkyDatabase,
    log: Arc<Mutex<Vec<String>>>,
    lib: SourceFile,
    /// Held for extension (a future scenario editing `App` directly); the current
    /// scenarios edit `lib`/`other`.
    #[allow(dead_code)]
    app: SourceFile,
    other: SourceFile,
    lib_id: base::ModuleId,
    app_id: base::ModuleId,
    other_id: base::ModuleId,
}

impl Fixture {
    /// Assemble `Lib` (0), `App` (1, imports Lib), `Other` (2, unrelated) into a
    /// salsa db whose every event `kind` is mirrored into `log`.
    fn build(lib: &str, app: &str, other: &str) -> Self {
        let log = Arc::new(Mutex::new(Vec::new()));
        let mut db = SkyDatabase::with_kernel_events(log.clone());
        let lib_f = db.new_source(0, lib.to_string());
        let lib_id = db.add_module("Lib", lib_f);
        let app_f = db.new_source(1, app.to_string());
        let app_id = db.add_module("App", app_f);
        let other_f = db.new_source(2, other.to_string());
        let other_id = db.add_module("Other", other_f);
        Fixture {
            db,
            log,
            lib: lib_f,
            app: app_f,
            other: other_f,
            lib_id,
            app_id,
            other_id,
        }
    }

    fn clear_log(&self) {
        self.log.lock().unwrap().clear();
    }

    fn take_log(&self) -> Vec<String> {
        std::mem::take(&mut *self.log.lock().unwrap())
    }
}

/// A `WillExecute` line naming `query` appeared — i.e. `query` re-ran.
fn executed(logs: &[String], query: &str) -> bool {
    logs.iter()
        .any(|l| l.starts_with("WillExecute") && l.contains(query))
}

/// A `DidValidateMemoizedValue` line naming `query` appeared — i.e. `query`
/// was served from memo without re-executing.
fn validated(logs: &[String], query: &str) -> bool {
    logs.iter()
        .any(|l| l.starts_with("DidValidateMemoizedValue") && l.contains(query))
}

// ---- DefId-independent projection (correctness comparison) ----------------

fn loc_str(db: &SkyDatabase, def: DefId) -> String {
    match db.def_loc(def) {
        Some(l) => format!("m{}:{}:{:?}", l.module.index(), l.name.as_str(), l.kind),
        None => format!("?{}", def.0),
    }
}

/// Replace every `DefId(N)` token in `s` with `DefId[m<mod>:<name>:<kind>]` so
/// the rendering is stable across databases (raw `DefId` ints depend on
/// interning order, which differs incremental-vs-fresh; the content key does not).
fn norm_def_ids(db: &SkyDatabase, s: &str) -> String {
    let mut out = String::new();
    let mut rest = s;
    while let Some(pos) = rest.find("DefId(") {
        out.push_str(&rest[..pos]);
        let after = &rest[pos + "DefId(".len()..];
        let end = after
            .find(|c: char| !c.is_ascii_digit())
            .unwrap_or(after.len());
        if let Ok(n) = after[..end].parse::<u32>() {
            out.push_str(&format!("DefId[{}]", loc_str(db, DefId(n))));
            rest = &after[end..];
            rest = rest.strip_prefix(')').unwrap_or(rest);
        } else {
            out.push_str("DefId(");
            rest = after;
        }
    }
    out.push_str(rest);
    out
}

/// A deterministic, `DefId`-independent rendering of a `ResolveResult`. Avoids
/// blanket `{:#?}` because the `qualifiers` field is a `HashMap` (nondeterministic
/// Debug order); every other field is rendered in its stable (Vec / IndexMap /
/// arena) order.
fn project(db: &SkyDatabase, rr: &ResolveResult) -> String {
    let mut s = String::new();
    for d in &rr.diagnostics {
        s += &format!("diag {} {}\n", d.code.0, d.message);
    }
    for td in &rr.top_defs {
        s += &format!("topdef {} = {}\n", td.name.as_str(), loc_str(db, td.def));
    }
    for ca in &rr.class_a {
        s += &format!(
            "classA {:?} {} {:?} {}\n",
            ca.qualifier, ca.name, ca.kind, ca.reason
        );
    }
    for cb in &rr.class_b {
        s += &format!(
            "classB {} {:?} {} {:?}\n",
            cb.package, cb.qualifier, cb.name, cb.kind
        );
    }
    // qualifiers: HashMap → render SORTED for determinism.
    let mut quals: Vec<_> = rr.qualifiers.iter().collect();
    quals.sort_by(|a, b| a.0.cmp(b.0));
    for (k, v) in quals {
        s += &format!("qual {k} -> {v:?}\n");
    }
    // bodies: IndexMap (insertion order); each body's arenas in alloc order.
    for (def, body) in &rr.bodies {
        s += &format!("body {} params={}\n", loc_str(db, *def), body.params.len());
        for (i, (_, e)) in body.exprs.iter().enumerate() {
            s += &format!("  e{i} {}\n", norm_def_ids(db, &format!("{e:?}")));
        }
        for (i, (_, p)) in body.pats.iter().enumerate() {
            s += &format!("  p{i} {}\n", norm_def_ids(db, &format!("{p:?}")));
        }
        for (i, (_, t)) in body.types.iter().enumerate() {
            s += &format!("  t{i} {}\n", norm_def_ids(db, &format!("{t:?}")));
        }
        s += &format!("  root={:?} anno={:?}\n", body.root, body.anno);
    }
    s
}

// ---- tests ----------------------------------------------------------------

/// (2a) An edit to an UNRELATED sibling module must NOT recompute a module that
/// doesn't depend on it, and the edited module's own resolution must recompute —
/// and both results must still be byte-correct vs a from-scratch build.
#[test]
fn unrelated_sibling_edit_does_not_recompute_dependent() {
    let mut fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    // Cold-run all three so every memo is populated.
    let _ = fx.db.resolve(fx.app_id);
    let _ = fx.db.resolve(fx.other_id);
    let _ = fx.db.resolve(fx.lib_id);

    // Edit `Other` (App imports only Lib, so App is independent of Other).
    fx.other.set_text(&mut fx.db).to(OTHER_V2.to_string());

    // Window 1: demand App — must be served entirely from memo.
    fx.clear_log();
    let app_res = fx.db.resolve(fx.app_id);
    let l1 = fx.take_log();
    assert!(
        !executed(&l1, "resolve_query"),
        "App.resolve MUST NOT recompute after an unrelated Other edit; log={l1:?}"
    );
    assert!(
        !executed(&l1, "parse") && !executed(&l1, "module_exports"),
        "no parse/module_exports recompute reachable from App; log={l1:?}"
    );
    assert!(
        validated(&l1, "resolve_query"),
        "App.resolve MUST validate from memo (proves it was checked, not skipped); log={l1:?}"
    );

    // Window 2: demand Other — its own parse + resolve MUST recompute.
    fx.clear_log();
    let other_res = fx.db.resolve(fx.other_id);
    let l2 = fx.take_log();
    assert!(
        executed(&l2, "resolve_query"),
        "Other.resolve MUST recompute after its own edit; log={l2:?}"
    );
    assert!(
        executed(&l2, "parse"),
        "Other.parse MUST recompute after its own edit; log={l2:?}"
    );

    // Correctness: incremental == fresh-from-scratch on the FINAL sources.
    let fresh = Fixture::build(LIB_V1, APP, OTHER_V2);
    assert_eq!(
        project(&fx.db, &app_res),
        project(&fresh.db, &fresh.db.resolve(fresh.app_id)),
        "incremental App.resolve diverged from a fresh build"
    );
    assert_eq!(
        project(&fx.db, &other_res),
        project(&fresh.db, &fresh.db.resolve(fresh.other_id)),
        "incremental Other.resolve diverged from a fresh build"
    );
}

/// (2b) An edit that changes an EXPORT consumed by a dependent module MUST
/// recompute that dependent (through the `module_exports` → `resolve` edge),
/// while a module that does NOT import the edited one stays memoised — and both
/// results remain byte-correct vs a from-scratch build.
#[test]
fn export_change_recomputes_dependent_only() {
    let mut fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let _ = fx.db.resolve(fx.app_id);
    let _ = fx.db.resolve(fx.other_id);

    // Edit `Lib`'s exports; App imports Lib.
    fx.lib.set_text(&mut fx.db).to(LIB_V2.to_string());

    // App depends on Lib's exports → App.resolve + Lib.parse + Lib.module_exports
    // MUST all recompute.
    fx.clear_log();
    let app_res = fx.db.resolve(fx.app_id);
    let la = fx.take_log();
    assert!(
        executed(&la, "resolve_query"),
        "App.resolve MUST recompute after Lib's exports change; log={la:?}"
    );
    assert!(
        executed(&la, "module_exports"),
        "Lib.module_exports MUST recompute; log={la:?}"
    );
    assert!(
        executed(&la, "parse"),
        "Lib.parse MUST recompute; log={la:?}"
    );

    // Other imports nothing from Lib → must stay memoised.
    fx.clear_log();
    let other_res = fx.db.resolve(fx.other_id);
    let lo = fx.take_log();
    assert!(
        !executed(&lo, "resolve_query"),
        "Other.resolve MUST NOT recompute on a Lib edit; log={lo:?}"
    );

    // Correctness: incremental == fresh-from-scratch on the FINAL sources.
    let fresh = Fixture::build(LIB_V2, APP, OTHER_V1);
    assert_eq!(
        project(&fx.db, &app_res),
        project(&fresh.db, &fresh.db.resolve(fresh.app_id)),
        "incremental App.resolve diverged from a fresh build after export change"
    );
    assert_eq!(
        project(&fx.db, &other_res),
        project(&fresh.db, &fresh.db.resolve(fresh.other_id)),
        "incremental Other.resolve diverged from a fresh build"
    );
}

/// Determinism guard: two projections of the same resolution on the same
/// revision are byte-identical (memoised value is stable), and re-demanding does
/// not re-execute.
#[test]
fn same_revision_resolution_is_stable() {
    let fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let first = project(&fx.db, &fx.db.resolve(fx.app_id));
    fx.clear_log();
    let second = project(&fx.db, &fx.db.resolve(fx.app_id));
    let logs = fx.take_log();
    assert_eq!(
        first, second,
        "same-revision resolution must be byte-stable"
    );
    assert!(
        !executed(&logs, "resolve_query"),
        "re-demanding App.resolve in the same revision must hit the memo; log={logs:?}"
    );
}

// ---- Stage D-2: `type_world` + `infer(DefId)` invalidation ----------------
//
// These extend the harness to the two tracked queries Stage D-2 introduced:
// `type_world_query` (the world, assembled once + backdated) and `infer_query`
// (per-def type table). The seams are demanded through the forbid-clean `TyDb`
// trait (`db.type_world()` / `db.body_types_of(def)`), which route to the
// queries — so the salsa events (`WillExecute` / `DidValidateMemoizedValue`) name
// them exactly as the resolve-stage assertions above name `resolve_query`.

/// A body-only edit: `x = 1` → `x = "changed"` (exports unchanged, so `App` is
/// unaffected; only the inferred TYPE of `x` moves number→String).
const OTHER_BODY_EDIT: &str = "module Other exposing (x)\n\nx = \"changed\"\n";

/// The `DefId` of a named top-level def in a module (via its resolution).
fn def_of(db: &SkyDatabase, m: ModuleId, name: &str) -> DefId {
    let rr = db.resolve(m);
    rr.top_defs
        .iter()
        .find(|td| td.name.as_str() == name)
        .unwrap_or_else(|| panic!("no top-level def `{name}` in module {}", m.index()))
        .def
}

/// A deterministic, DB-independent rendering of a `BodyTypes` — sorted so the
/// `HashMap` iteration order can't perturb the bytes. `Ty` and the arena
/// `ExprId`/`LocalId` keys carry no raw `DefId`, so no normalisation is needed:
/// the projection is directly comparable incremental-vs-fresh.
fn project_bt(bt: &ty::BodyTypes) -> String {
    let mut s = format!("result {:?}\n", bt.result);
    let mut es: Vec<(String, String)> = bt
        .exprs
        .iter()
        .map(|(k, v)| (format!("{k:?}"), format!("{v:?}")))
        .collect();
    es.sort();
    for (k, v) in es {
        s += &format!("expr {k} = {v}\n");
    }
    let mut ls: Vec<(String, String)> = bt
        .locals
        .iter()
        .map(|(k, v)| (format!("{k:?}"), format!("{v:?}")))
        .collect();
    ls.sort();
    for (k, v) in ls {
        s += &format!("local {k} = {v}\n");
    }
    s
}

/// (D-2 a) The world is assembled **once and reused**: re-demanding `type_world`
/// in the same revision hits the memo (no `WillExecute`), and its value — plus
/// each def's `infer` table — is byte-stable on repeated demand (per-def memo).
#[test]
fn type_world_and_infer_are_memoised_within_a_revision() {
    let fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let main_def = def_of(&fx.db, fx.app_id, "main");

    // First demand populates both memos.
    let w1 = fx.db.type_world();
    let bt1 = project_bt(&fx.db.body_types_of(fx.app_id, main_def));

    // Second demand in the SAME revision must be served from memo, byte-stable.
    fx.clear_log();
    let w2 = fx.db.type_world();
    let bt2 = project_bt(&fx.db.body_types_of(fx.app_id, main_def));
    let logs = fx.take_log();

    assert!(
        *w1 == *w2,
        "type_world must be value-stable within a revision"
    );
    assert_eq!(
        bt1, bt2,
        "infer(App.main) must be byte-stable within a revision"
    );
    assert!(
        !executed(&logs, "type_world_query"),
        "re-demanding type_world in the same revision must hit the memo; log={logs:?}"
    );
    assert!(
        !executed(&logs, "infer_query"),
        "re-demanding infer(App.main) in the same revision must hit the memo; log={logs:?}"
    );
}

/// (D-2 b) The headline incremental property. A **body-only** edit to `Other`
/// recomputes `infer(Other.x)` (its own body changed) but NOT `infer(App.main)`
/// in an unrelated module — because `type_world` **backdates** (a body edit
/// touches no annotation/union/alias, so `World::build` re-executes to a
/// value-equal world) and `App`'s own `resolve` is untouched. Asserts BOTH the
/// recompute-set (by query name) AND correctness (byte-equal a fresh build).
///
/// GATED (live, not `#[ignore]`d). This property regressed once — `World::build`'s
/// per-def inference passes (`app_check_sigs` F1c + the D1/D2 check-only channels)
/// read app defs' BODIES, so a body edit no longer re-executed `World::build` to a
/// *value-equal* world, `type_world` failed to backdate, and `App.main`'s `infer`
/// recomputed (an incremental-PERFORMANCE regression, results stayed byte-equal).
/// Closed by the salsa-granularity refinement (`type_world` now builds only the
/// body-INDEPENDENT `World::build_decls`; the body-derived channels moved to
/// `check_world` + a per-def `record_result_sig` query — see
/// `skydb::type_world_query`). This test asserts BOTH the recompute-set (by query
/// name, via the salsa event stream) AND byte-correctness, so a future change that
/// re-couples `type_world` to app bodies fails HERE.
#[test]
fn body_edit_recomputes_only_that_defs_infer() {
    let mut fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let main_def = def_of(&fx.db, fx.app_id, "main");
    let x_def = def_of(&fx.db, fx.other_id, "x");

    // Cold: populate every memo (world + both defs' infer tables).
    let _ = fx.db.type_world();
    let _ = fx.db.body_types_of(fx.app_id, main_def);
    let _ = fx.db.body_types_of(fx.other_id, x_def);

    // Edit ONLY Other's body.
    fx.other
        .set_text(&mut fx.db)
        .to(OTHER_BODY_EDIT.to_string());

    // Window 1: demand the UNRELATED def. `type_world` re-executes (its `parse`
    // dep changed) but BACKDATES; App's resolve is untouched → infer(App.main)
    // validates from memo without re-executing.
    fx.clear_log();
    let main_bt = project_bt(&fx.db.body_types_of(fx.app_id, main_def));
    let l1 = fx.take_log();
    assert!(
        executed(&l1, "type_world_query"),
        "type_world must re-execute (its parse dep changed) to be checked for backdate; log={l1:?}"
    );
    assert!(
        !executed(&l1, "infer_query"),
        "infer(App.main) MUST NOT recompute after an unrelated body edit (type_world backdated); log={l1:?}"
    );
    assert!(
        validated(&l1, "infer_query"),
        "infer(App.main) MUST validate from memo (checked, not skipped); log={l1:?}"
    );

    // Window 2: demand the EDITED def. Its own `resolve` changed → re-executes.
    fx.clear_log();
    let x_bt = project_bt(&fx.db.body_types_of(fx.other_id, x_def));
    let l2 = fx.take_log();
    assert!(
        executed(&l2, "infer_query"),
        "infer(Other.x) MUST recompute after its own body edit; log={l2:?}"
    );

    // Correctness: incremental == fresh-from-scratch on the FINAL sources.
    let fresh = Fixture::build(LIB_V1, APP, OTHER_BODY_EDIT);
    let fresh_main = def_of(&fresh.db, fresh.app_id, "main");
    let fresh_x = def_of(&fresh.db, fresh.other_id, "x");
    assert_eq!(
        main_bt,
        project_bt(&fresh.db.body_types_of(fresh.app_id, fresh_main)),
        "incremental infer(App.main) diverged from a fresh build"
    );
    assert_eq!(
        x_bt,
        project_bt(&fresh.db.body_types_of(fresh.other_id, fresh_x)),
        "incremental infer(Other.x) diverged from a fresh build after body edit"
    );
}

// A `Lib` carrying an ANNOTATED export — editing an annotation changes the
// sig-world's VALUE (unlike a body edit), so `type_world` does NOT backdate.
const LIB_ANNO_V1: &str =
    "module Lib exposing (greeting)\n\ngreeting : String\n\ngreeting = \"hi\"\n";
// V2 adds a NEW annotated export (`shout`) → a new `value_sigs` entry → the world
// value differs → no backdate → dependents' `infer` re-executes.
const LIB_ANNO_V2: &str = "module Lib exposing (greeting, shout)\n\ngreeting : String\n\ngreeting = \"hi\"\n\nshout : String\n\nshout = \"HI\"\n";

/// (D-2 c) The complement to (b): an **annotation** edit changes `type_world`'s
/// value (no backdate), so a dependent def's `infer` DOES recompute — proving the
/// sig → infer edge fires (and correctness is preserved).
///
/// Granularity note: `infer(App.main)` recomputes here even though `main` never
/// references the newly-added `shout` — every `infer` depends on the single
/// aggregate `type_world`, so ANY sig change invalidates all of them. Making that
/// selective (infer depends only on the sigs of the defs it references) is the
/// Stage-E refinement — per-referenced-def `sig(DefId)` queries; the aggregate
/// world is the current, honest granularity floor. This test asserts the
/// achievable subset: the dependent recomputes + stays correct.
#[test]
fn annotation_edit_recomputes_dependent_infer() {
    let mut fx = Fixture::build(LIB_ANNO_V1, APP, OTHER_V1);
    let main_def = def_of(&fx.db, fx.app_id, "main");

    // Cold: populate world + infer(App.main).
    let world_before = fx.db.type_world();
    let _ = fx.db.body_types_of(fx.app_id, main_def);

    // Edit Lib's annotations (adds `shout : String`).
    fx.lib.set_text(&mut fx.db).to(LIB_ANNO_V2.to_string());

    fx.clear_log();
    let main_bt = project_bt(&fx.db.body_types_of(fx.app_id, main_def));
    let world_after = fx.db.type_world();
    let l = fx.take_log();

    assert!(
        *world_before != *world_after,
        "an annotation edit MUST change type_world's value (no backdate)"
    );
    assert!(
        executed(&l, "type_world_query"),
        "type_world MUST re-execute after an annotation edit; log={l:?}"
    );
    assert!(
        executed(&l, "infer_query"),
        "infer(App.main) MUST recompute after a sig-world change; log={l:?}"
    );

    // Correctness: incremental == fresh-from-scratch on the FINAL sources.
    let fresh = Fixture::build(LIB_ANNO_V2, APP, OTHER_V1);
    let fresh_main = def_of(&fresh.db, fresh.app_id, "main");
    assert_eq!(
        main_bt,
        project_bt(&fresh.db.body_types_of(fresh.app_id, fresh_main)),
        "incremental infer(App.main) diverged from a fresh build after annotation edit"
    );
}

// ---- Stage E: `go_program` (lower + codegen) invalidation --------------------
//
// These close the harness at the bottom of the DAG. `go_program(entry, config)`
// is the WHOLE-PROGRAM floor (doc 01 `build(project)`): it lowers the program +
// renders Go by reading `type_world`/`resolve`/per-def `infer` through the db, so
// any `SourceFile` edit that reaches a lowered def invalidates it, while an
// unchanged revision is a memo hit. The events name `go_program` exactly as the
// upstream assertions name `resolve_query`/`infer_query`. Asserts BOTH the
// recompute-set (by query name) AND correctness (byte-equal emitted Go vs a fresh
// build) — the same two-axis contract as D-1/D-2.

// A body-only edit to `Lib.greeting` (exports unchanged). `App.main = greeting`
// lowers `Lib.greeting` (DCE-reachable), so the emitted Go's string literal moves
// `"hi"` → `"bye"` — a visible codegen delta that also proves re-execution.
const LIB_BODY_EDIT: &str = "module Lib exposing (greeting)\n\ngreeting = \"bye\"\n";

/// (E a) The world below `infer` is closed + memoised: the first `go_program`
/// demand emits source, and re-demanding it in the SAME revision hits the memo
/// (no `WillExecute`) and returns byte-identical Go.
#[test]
fn go_program_memoised_within_a_revision() {
    let fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let cfg = BuildConfig::new(&fx.db, LowerConfig::default());

    // First demand populates the memo + emits source.
    let s1 = go_program(&fx.db, fx.app_id, cfg).source.clone();
    assert!(
        s1.is_some(),
        "go_program must emit Go source for the App entry"
    );

    // Second demand in the SAME revision must be served from memo, byte-stable.
    fx.clear_log();
    let s2 = go_program(&fx.db, fx.app_id, cfg).source.clone();
    let logs = fx.take_log();
    assert_eq!(s1, s2, "same-revision go_program must be byte-stable");
    assert!(
        !executed(&logs, "go_program"),
        "re-demanding go_program in the same revision must hit the memo; log={logs:?}"
    );
}

/// (E b) The headline Stage-E property. A body edit to a LOWERED dep (`Lib.greeting`,
/// reachable from `App.main`) re-executes `go_program`, the emitted Go reflects the
/// edit, and the incremental result is byte-identical to a from-scratch build on
/// the final sources. Asserts BOTH the recompute-set AND byte-equal correctness.
#[test]
fn body_edit_reexecutes_go_program_byte_correct() {
    let mut fx = Fixture::build(LIB_V1, APP, OTHER_V1);
    let cfg = BuildConfig::new(&fx.db, LowerConfig::default());

    // Cold: populate the memo.
    let before = go_program(&fx.db, fx.app_id, cfg).source.clone();
    assert!(before.is_some(), "cold go_program must emit source");

    // Edit ONLY Lib's body (exports unchanged).
    fx.lib.set_text(&mut fx.db).to(LIB_BODY_EDIT.to_string());

    // Demand go_program: its `resolve(Lib)`/`infer(Lib.greeting)` deps changed →
    // it MUST re-execute, and the emitted Go MUST reflect the edited literal.
    fx.clear_log();
    let after = go_program(&fx.db, fx.app_id, cfg).source.clone();
    let logs = fx.take_log();
    assert!(
        executed(&logs, "go_program"),
        "go_program MUST re-execute after a body edit to a lowered dep; log={logs:?}"
    );
    assert!(
        before != after,
        "the emitted Go must change to reflect the edited body"
    );

    // Correctness: incremental == fresh-from-scratch on the FINAL sources.
    let fresh = Fixture::build(LIB_BODY_EDIT, APP, OTHER_V1);
    let fresh_cfg = BuildConfig::new(&fresh.db, LowerConfig::default());
    let fresh_src = go_program(&fresh.db, fresh.app_id, fresh_cfg)
        .source
        .clone();
    assert_eq!(
        after, fresh_src,
        "incremental go_program diverged from a fresh build after a body edit"
    );
}
