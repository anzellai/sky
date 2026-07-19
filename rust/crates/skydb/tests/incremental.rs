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

use base::DefId;
use hir::{ResolveResult, SkyDb};
use salsa::Setter;
use skydb::{SkyDatabase, SourceFile};
use std::sync::{Arc, Mutex};

// ---- fixtures -------------------------------------------------------------

const LIB_V1: &str = "module Lib exposing (greeting)\n\ngreeting = \"hi\"\n";
// V2 adds an EXPORT (`shout`) — an exports change a dependent must observe.
const LIB_V2: &str =
    "module Lib exposing (greeting, shout)\n\ngreeting = \"hi\"\n\nshout = \"HI\"\n";
const APP: &str = "module App exposing (main)\n\nimport Lib exposing (greeting)\n\nmain = greeting\n";
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
    assert_eq!(first, second, "same-revision resolution must be byte-stable");
    assert!(
        !executed(&logs, "resolve_query"),
        "re-demanding App.resolve in the same revision must hit the memo; log={logs:?}"
    );
}
