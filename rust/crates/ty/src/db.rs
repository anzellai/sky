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
use crate::infer::{Infer, RecordOverlay};
use crate::sig::World;
use base::{DefId, ModuleId};
use hir::SkyDb;
use std::rc::Rc;
use std::sync::Arc;

/// The type-layer database: [`SkyDb`] plus the memoisable typed artefacts.
///
/// Every method returns an owning handle (never a borrow into a memo) so the
/// two backends share one signature regardless of where the value lives — the
/// same shape `SkyDb::resolve` uses. The worlds come as an `Arc`: the salsa
/// memo stores them behind one, so every caller shares the memoised world
/// instead of copying it.
pub trait TyDb: SkyDb {
    /// The assembled sig-world (stdlib + deps + entry). On the salsa backend
    /// this is a single memoised query, so the build's several rebuild sites
    /// (`check_modules`, `Typer::new`, `lower`'s `collect_types`) share one
    /// assembly instead of rebuilding it each.
    fn type_world(&self) -> Arc<World>;

    /// The FULL world including the body-derived CHECK-ONLY channels (passes 5-6:
    /// `app_check_sigs` / `any_result_check_sigs`), consumed by the accept/reject
    /// checker (`check_modules`, `!use_inferred`). Distinct from [`TyDb::type_world`]
    /// (declarations only, passes 1-4) so that the salsa backend's `type_world`
    /// stays body-independent and backdates on app body edits while the checker
    /// still sees the F1c / D1 pins. On the eager backend both are plain builds.
    fn check_world(&self) -> Arc<World>;

    /// The per-expression/per-local type table for one def — doc 01's
    /// `infer(DefId)`. On the salsa backend this is a `#[salsa::tracked]` query
    /// memoised at per-def granularity. The def's `module` is passed by the caller
    /// (the lowerer/checker always has it in scope from its `resolve` walk) so the
    /// module/`SourceFile` is obtained WITHOUT reading the interned `DefKey` at
    /// this non-query boundary — reading interned data here would panic across a
    /// salsa revision that hasn't yet re-interned the key (the `sky watch` /
    /// incremental path). The memo and its consumers share the entire immutable
    /// result, including the owned result/signature types, rather than cloning
    /// those recursive trees at each bridge.
    fn body_types_of(&self, module: ModuleId, def: DefId) -> Arc<BodyTypes>;

    /// CHECK-derived numeric dictionaries. Ordinary BodyTypes are unchanged.
    fn numeric_elaboration_of(
        &self,
        module: ModuleId,
        def: DefId,
    ) -> Rc<crate::numeric::NumericElaboration> {
        Rc::new(crate::numeric::compute_numeric_elaboration(
            &self.check_world(),
            self.as_sky_db(),
            module,
            def,
        ))
    }

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
    compute_body_types_with(world, None, db, module, def)
}

/// [`compute_body_types`] with per-def record entries read in place of the
/// world's own ([`RecordOverlay`]): identical to running it against a clone of
/// `world` with the overlay's entries inserted.
pub fn compute_body_types_with(
    world: &World,
    overlay: Option<&RecordOverlay>,
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
        .with_inferred(true)
        .with_overlay(overlay);
    // v0.29.2: a def whose annotation names a `number` variable is inferred
    // against that annotation, the variable kept generic
    // (`SuperType::NumVar`). Inferred without it, an integer literal pinned
    // the variable to `Int`: `double : number -> number; double x = x * 2`
    // lowered `x * 2` as Int arithmetic and `double 2.5` gave `4`. The
    // checker accepts such a body only since integer literals stand for a
    // `number` variable (v0.29.2), so no def that compiled before changes.
    if let Some(scheme) = world.value_sigs.get(&def).filter(|s| {
        s.vars.iter().any(|v| {
            crate::unify::SuperType::from_var_name(v.as_str())
                .is_some_and(|b| b.has(crate::unify::SuperType::Number))
        })
    }) {
        infer = infer.with_pinned_number_vars(scheme.clone());
    }
    // NB: `signature` is discarded here — the lowerer path only reads
    // `result`/`exprs`/`locals`. It is populated so a stray read stays consistent
    // with the tooling path, but codegen never consumes it (bug-b additivity).
    let bt = BodyTypes::from_tables(infer.infer_def_tables(body));
    pin_witness_types(world, overlay, db, def, body, bt)
}

/// The types of a `Codec.auto` witness, pinned by the def's annotation
/// (v0.29.2 sixth Judge).
///
/// The lowering view infers a def WITHOUT its annotation (so codegen stays
/// byte-identical to the oracle). For `cA : Codec RList` with
/// `cA = Codec.auto { id = "", v = [] }` that leaves the empty list's element a
/// variable: the witness lowered to `struct{ Id string; V []any }`, and
/// every read failed with `Codec.auto: cannot decode kind interface`, though
/// the checker (which uses the annotation) passed `[E2009]` on it. Here, and
/// only for a witness whose lowering type holds a variable element, the
/// witness subtree takes the types the annotated inference gives, when those
/// have no variable element. Every other expression keeps its type, so no
/// other emitted Go changes.
fn pin_witness_types(
    world: &World,
    overlay: Option<&RecordOverlay>,
    db: &dyn SkyDb,
    def: DefId,
    body: &hir::Body,
    bt: BodyTypes,
) -> BodyTypes {
    use crate::codec_elem::{has_var_elem, witness_exprs};
    let Some(scheme) = world.value_sigs.get(&def) else {
        return bt;
    };
    let open: Vec<hir::ExprId> = witness_exprs(body, db)
        .into_iter()
        .filter(|w| bt.expr(*w).is_some_and(|t| has_var_elem(&t)))
        .collect();
    if open.is_empty() {
        return bt;
    }
    let mut infer = Infer::new(world, db)
        .with_self_def(Some(def))
        .with_overlay(overlay)
        .with_record_exprs(true);
    infer.infer_def_against(body, scheme);
    let (recorded, table) = infer.recorded_expr_table();
    let annotated: std::collections::HashMap<hir::ExprId, crate::Ty> = recorded
        .into_iter()
        .map(|(e, r)| (e, table.get(r)))
        .collect();
    let mut exprs: std::collections::HashMap<hir::ExprId, crate::Ty> = bt.exprs().collect();
    let mut changed = false;
    for w in open {
        if !annotated.get(&w).is_some_and(|t| !has_var_elem(t)) {
            continue;
        }
        let Some(ws) = body.expr_span(w) else {
            continue;
        };
        for (e, _) in body.exprs.iter() {
            let inside = body.expr_span(e).is_some_and(|s| {
                s.file == ws.file && s.range.0 >= ws.range.0 && s.range.1 <= ws.range.1
            });
            if !inside {
                continue;
            }
            if let Some(t) = annotated.get(&e) {
                if exprs.get(&e) != Some(t) {
                    exprs.insert(e, t.clone());
                    changed = true;
                }
            }
        }
    }
    if !changed {
        return bt;
    }
    let locals = bt.locals().collect();
    BodyTypes::new(bt.result.clone(), bt.signature.clone(), exprs, locals)
}

/// Eager backend. `SourceDb` has no incremental store, so every call recomputes —
/// the status quo for the LSP + accept/reject gates, unchanged in behaviour.
impl TyDb for hir::SourceDb {
    fn type_world(&self) -> Arc<World> {
        // Declarations only (passes 1-4). The eager backend has no incremental
        // store, so this is a fresh subset build; its consumers (`Typer`, the
        // alias-expander in `lower`) read only declaration channels.
        Arc::new(World::build_decls(self))
    }
    fn check_world(&self) -> Arc<World> {
        Arc::new(World::build(self))
    }
    fn body_types_of(&self, module: ModuleId, def: DefId) -> Arc<BodyTypes> {
        // FULL world so the lowering path's `Expr::Update` arm sees pass-7
        // `record_result_sigs` (byte-identical to the pre-split eager behaviour;
        // passes 5-6 are `!use_inferred`-gated so they never perturb lowering).
        let world = World::build(self);
        Arc::new(compute_body_types(&world, self, module, def))
    }
    fn as_sky_db(&self) -> &dyn SkyDb {
        self
    }
}
