//! The `ty`-layer database interface (Stage D-2 — doc 01 `infer(DefId)` node,
//! doc 12 risk #M1 "union-find local to the inference query").
//!
//! [`TyDb`] is to the type layer what [`hir::SkyDb`] is to resolution: the
//! forbid-clean interface that `check`/`lower` call to obtain the memoisable
//! typed artefacts — the assembled [`World`] and a def's [`BodyTypes`] — without
//! naming salsa. Two backends implement it, exactly as they implement `SkyDb`:
//!
//! * [`hir::SourceDb`] (eager) — computes on every call (LSP + accept/reject
//!   gates; no incremental store to memoise into).
//! * `skydb::SkyDatabase` (salsa) — routes each method to a `#[salsa::tracked]`
//!   query (`type_world_query` / `infer_query`), so the world is **assembled
//!   once and reused** across the build's rebuild sites, and `infer(DefId)` is
//!   memoised at per-def granularity (the ideal incremental unit).
//!
//! The union-find stays **local to one inference run** (`Infer::uf`): every
//! `infer_*` entry point reads back to a canonical [`Ty`] before returning, so a
//! `TyVarId` never escapes into a memoised value (doc 12 #M1). `compute_body_types`
//! is the single shared body both backends run — the eager and memoised paths are
//! byte-identical by construction.

use crate::check::BodyTypes;
use crate::infer::Infer;
use crate::sig::World;
use base::{DefId, ModuleId};
use hir::SkyDb;
use std::rc::Rc;

/// The type-layer database: [`SkyDb`] plus the memoisable typed artefacts.
///
/// Every method returns an owned/`Rc` value (never a borrow into a memo) so the
/// two backends share one signature regardless of where the value lives — the
/// same shape `SkyDb::resolve` uses.
pub trait TyDb: SkyDb {
    /// The assembled sig-world (stdlib + deps + entry). On the salsa backend
    /// this is a single memoised query, so the build's several rebuild sites
    /// (`check_modules`, `Typer::new`, `lower`'s `collect_types`) share one
    /// assembly instead of rebuilding it each.
    fn type_world(&self) -> Rc<World>;

    /// The per-expression/per-local type table for one def — doc 01's
    /// `infer(DefId)`. On the salsa backend this is a `#[salsa::tracked]` query
    /// memoised at per-def granularity. The def's `module` is passed by the caller
    /// (the lowerer/checker always has it in scope from its `resolve` walk) so the
    /// module/`SourceFile` is obtained WITHOUT reading the interned `DefKey` at
    /// this non-query boundary — reading interned data here would panic across a
    /// salsa revision that hasn't yet re-interned the key (the `sky watch` /
    /// incremental path).
    fn body_types_of(&self, module: ModuleId, def: DefId) -> Rc<BodyTypes>;

    /// This database viewed as the resolution interface, so a `TyDb` value can be
    /// handed to `Infer::new` (which threads a `&dyn SkyDb`). Mirrors
    /// `ResolveDb::sky_db`.
    fn as_sky_db(&self) -> &dyn SkyDb;
}

/// Infer one def's typed table against an already-assembled [`World`]. The single
/// shared inference body: `TyDb`'s eager and memoised backends both call this, so
/// the typed table the lowerer consumes is identical whichever backend produced
/// it (the byte-for-byte behaviour the `repro`/`build-run` gates guard).
///
/// Fetches the body from `resolve(module)` — the same body the eager
/// `Typer::body_types` receives from the lowerer's own `db.resolve` walk — so the
/// two paths agree. `module` is the def's own module (passed in, not derived from
/// an interned `DefKey`, so this is revision-safe). A bodyless def
/// (annotation-only / type decl) yields an empty table. The `Infer` (and its
/// union-find) is created, driven, and read back to canonical `Ty` entirely within
/// this function; nothing transient escapes.
pub fn compute_body_types(
    world: &World,
    db: &dyn SkyDb,
    module: ModuleId,
    def: DefId,
) -> BodyTypes {
    let resolved = db.resolve(module);
    let Some(body) = resolved.bodies.get(&def) else {
        return BodyTypes::default();
    };
    let mut infer = Infer::new(world, db)
        .with_self_def(Some(def))
        .with_inferred(true);
    let (result, exprs, locals) = infer.infer_def_typed(body);
    BodyTypes {
        result,
        exprs,
        locals,
    }
}

/// Eager backend. `SourceDb` has no incremental store, so every call recomputes —
/// the status quo for the LSP + accept/reject gates, unchanged in behaviour.
impl TyDb for hir::SourceDb {
    fn type_world(&self) -> Rc<World> {
        Rc::new(World::build(self))
    }
    fn body_types_of(&self, module: ModuleId, def: DefId) -> Rc<BodyTypes> {
        let world = World::build(self);
        Rc::new(compute_body_types(&world, self, module, def))
    }
    fn as_sky_db(&self) -> &dyn SkyDb {
        self
    }
}
