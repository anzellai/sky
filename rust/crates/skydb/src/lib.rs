//! `skydb` — the salsa database + the leaf queries. Ties inputs → parse → …;
//! the place "the whole compiler" is assembled (doc 02).
//!
//! **Stage A + Stage B (doc 12 §M0→M1, doc 01 "all edges are salsa queries").**
//! The M0 spike (one `source_text` input + a trivial `line_count` query) is now
//! promoted to the real leaf of the query DAG:
//!
//! * **Stage A — inputs.** The source set is modelled as salsa inputs: one
//!   [`SourceFile`] `#[salsa::input]` per module, holding `(file_id, text)` —
//!   the salsa-native encoding of the module set the hand-rolled
//!   `hir::db::SourceDb` used to hold in a `Vec<ModuleInfo>`. The driver
//!   `set_*`s these; everything downstream is a pure, memoised function of them.
//! * **Stage B — `parse` leaf query.** [`parse`] is `#[salsa::tracked]`: a pure,
//!   memoised function of a `SourceFile` input, matching the doc-01 data-flow
//!   node `parse(FileId) -> Lossless CST + parse diagnostics`.
//!
//! **Salsa version: real `salsa` 0.28** (the current jar-less API). Inputs are
//! salsa structs; tracked fns are the derived queries. `#![forbid(unsafe_code)]`
//! is deliberately absent from THIS crate (only): the salsa proc-macros expand
//! to `unsafe impl`s. Every frontend crate that authors compiler logic keeps
//! `forbid` — salsa is quarantined here, which is why the leaf queries live in
//! `skydb` rather than in the forbid-clean `hir`/`ty`/`lower` that consume them
//! (see the Stage-B report / doc 12 for the layering the resolve stage resolves).

use base::{DefId, ModuleId, Name};
use hir::{compute_exports, DefKind, DefLoc, ImportSource, ModuleExports, ResolveResult, SkyDb};
use std::collections::HashMap;
use std::rc::Rc;
use std::sync::{Arc, Mutex};

/// A registered module: its dotted name and the [`SourceFile`] input it parses
/// from. Indexed by `ModuleId` (the vector position), mirroring the eager
/// `hir::SourceDb`'s registry so `ModuleId` semantics are identical across both
/// database backends.
struct ModuleReg {
    name: String,
    file: SourceFile,
}

/// The Sky query database. Its only mutable state is salsa's storage plus the
/// append-only module registry (L1) — no global `IORef`/`unsafePerformIO`
/// equivalents anywhere in the compiler. The registry holds the module set
/// (dotted-name → `ModuleId` → `SourceFile` input) that the salsa queries key
/// off; it is set once by the driver at assembly time and read-only thereafter.
#[salsa::db]
#[derive(Default, Clone)]
pub struct SkyDatabase {
    storage: salsa::Storage<Self>,
    modules: Vec<ModuleReg>,
    by_name: HashMap<String, ModuleId>,
    kernel: HashMap<String, String>,
}

impl Clone for ModuleReg {
    fn clone(&self) -> Self {
        ModuleReg {
            name: self.name.clone(),
            file: self.file,
        }
    }
}

#[salsa::db]
impl salsa::Database for SkyDatabase {}

/// A definition's content-keyed identity — the salsa-native successor to the
/// hand-rolled `hir::DefTable` `IndexSet` interner (the resolve-stage purity
/// blocker). Keyed by `(module, name, kind)`, exactly the old interner's key, so
/// a value and a type of the same name in the same module stay distinct. Being
/// `#[salsa::interned]`, the identity is content-derived and order-independent by
/// construction: a pure `#[salsa::tracked]` query can mint one without mutating
/// shared state. The raw `salsa::Id` index is exposed as the compiler-wide
/// [`base::DefId`] `u32` so downstream (`ty`/`lower`/`sky-lsp`) is unchanged.
///
/// Determinism note: `DefId` *values* differ from the old insertion-order
/// scheme, but no consumer keys emitted output off the raw int (audited: codegen
/// never sees a `DefId`; every output ordering keys off names/spans/`Ty`). The
/// `repro` gate is the byte-stability guard for this change.
#[salsa::interned(no_lifetime)]
pub struct DefKey {
    #[returns(copy)]
    pub module: u32,
    pub name: String,
    #[returns(copy)]
    pub kind: DefKind,
}

/// Mint / recover the stable `DefId` for `(module, name, kind)` via salsa interning.
fn intern_def_id(db: &dyn salsa::Database, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
    let key = DefKey::new(db, module.index(), name.as_str().to_string(), kind);
    DefId(salsa::plumbing::AsId::as_id(&key).index())
}

/// Recover a definition's `(module, name, kind)` location from its `DefId`.
/// Every `DefId` in circulation was minted by [`intern_def_id`], so the id always
/// names a live interned `DefKey`.
fn def_id_loc(db: &dyn salsa::Database, def: DefId) -> DefLoc {
    // Safe in practice: `def.0` is the index of a `DefKey` this db interned.
    let id = unsafe { salsa::Id::from_index(def.0) };
    let key = <DefKey as salsa::plumbing::FromId>::from_id(id);
    DefLoc {
        module: ModuleId(key.module(db)),
        name: Name::new(key.name(db)),
        kind: key.kind(db),
    }
}

/// `module_exports` as a `#[salsa::tracked]` query (doc 05 §7, §8) — the salsa
/// successor to `hir::SourceDb::module_exports` + its deleted `RefCell` memo.
/// Pure + memoised: a module's exports are a function of that module's own parse
/// (+ its `exposing` clause), never recursing into other modules, so the
/// cross-module query graph stays cycle-free (no 5-round fixpoint, L2). Interning
/// runs through salsa's content-keyed `DefKey` (the closure), so this is a pure
/// tracked query with no shared-`RefCell` mutation (the resolve-stage purity fix).
///
/// `no_eq`: `ModuleExports` has no `PartialEq`, matching `parse` — backdating is
/// a later LSP-incrementality refinement, not required on the cold build path.
///
/// Keyed by `(module, file)`: `file` (a salsa input) carries the memoisation +
/// invalidation edge (editing the file recomputes this query and nothing else),
/// while `module` pins the `ModuleId` the interned `DefKey`s mint under — kept
/// explicit rather than derived from `file_id` so it stays correct even when a
/// local module shadows a stdlib one (the override case reuses the shadowed
/// `ModuleId` but a distinct `SourceFile`).
#[salsa::tracked(no_eq)]
pub fn module_exports(
    db: &dyn salsa::Database,
    module: ModuleId,
    file: SourceFile,
) -> ModuleExports {
    let p = parse(db, file);
    let tree = p.tree();
    compute_exports(module, &tree, &mut |m, n, k| intern_def_id(db, m, n, k))
}

/// The salsa view a tracked query needs to reach the resolver. The resolver
/// (`hir::resolve`) is authored against the forbid-clean [`SkyDb`] surface and
/// reaches cross-module data through it; a `#[salsa::tracked]` query, however,
/// only receives a salsa database, not a `&dyn SkyDb`. This `#[salsa::db]`
/// supertrait bridges the two: [`SkyDatabase`] implements it by handing back
/// `self` as a `&dyn SkyDb`, so the tracked query can call `hir::resolve` while
/// the reads it performs (`parse`, `module_exports`, `DefKey` interning) are
/// recorded against the executing query by salsa's runtime.
///
/// The registry reads the resolver also performs (`module_name`,
/// `module_by_name`, `classify_import`, `kernel_pseudo`) hit [`SkyDatabase`]'s
/// append-only, set-once-at-assembly fields — untracked by salsa, which is sound
/// here because that registry never mutates across revisions (only `SourceFile`
/// text inputs do). This is the sanctioned "untracked db field behind a
/// `#[salsa::db]` trait" pattern (salsa's own `backdate_untracked_db_field`
/// test), safe precisely because of the read-only-after-assembly invariant.
#[salsa::db]
pub trait ResolveDb: salsa::Database {
    /// This database as the forbid-clean resolver interface.
    fn sky_db(&self) -> &dyn SkyDb;
}

#[salsa::db]
impl ResolveDb for SkyDatabase {
    fn sky_db(&self) -> &dyn SkyDb {
        self
    }
}

/// `resolve` as a `#[salsa::tracked]` query (doc 05 §1, doc 12 L2) — the salsa
/// successor to the eager `hir::resolve` free function on the build path. Pure +
/// memoised: a module's resolution is a function of its own `parse` plus the
/// `module_exports` of the modules it imports, each reached through a salsa
/// query, so salsa captures the full parse/exports → resolve dependency graph.
/// Editing one module's `SourceFile` text invalidates exactly its own `resolve`
/// (and any module that imports it), never an unrelated sibling — the property
/// the incremental-correctness harness asserts.
///
/// `no_eq`: `ResolveResult` has no `PartialEq` (it carries `Body` arenas),
/// matching `parse`/`module_exports` — over-invalidation on an edit is sound; a
/// backdate refinement is a later LSP-incrementality concern, not required for
/// build-path correctness. Keyed by `(module, file)` for the same reason
/// `module_exports` is: `file` carries the invalidation edge, `module` pins the
/// `ModuleId` the interned `DefKey`s mint under.
#[salsa::tracked(no_eq)]
pub fn resolve_query(db: &dyn ResolveDb, module: ModuleId, _file: SourceFile) -> ResolveResult {
    hir::resolve(db.sky_db(), module)
}

/// `type_world` as a `#[salsa::tracked]` query (Stage D-2 — doc 01's `infer`
/// upstream, doc 12 risk #M1). The salsa successor to the eager `ty::World::build`
/// that the build path rebuilt ≥3× (`check_modules`, `Typer::new`,
/// `lower::collect_types`); routed through [`ty::TyDb::type_world`] those sites now
/// share **one** memoised assembly.
///
/// **Backdated** (deliberately NOT `no_eq`): `ty::World` is `PartialEq`, and the
/// world is a pure function of every module's *declarations* (annotations, unions,
/// aliases) + pass-3 inference of the stdlib combinators — never of an app def's
/// body. So a body-only edit re-executes this query (its `parse` dep changed) but
/// yields a value-equal `World`; salsa backdates it, and every dependent
/// [`infer_query`] validates from memo instead of re-inferring. That backdate is
/// the mechanism the incremental harness's "sibling `infer` not recomputed after a
/// body edit" scenario proves. Reads flow through `db.sky_db()` (the `ResolveDb`
/// bridge) so the `parse`/`resolve`/interning edges are recorded against this
/// query exactly as they are for [`resolve_query`].
#[salsa::tracked]
pub fn type_world_query(db: &dyn ResolveDb) -> ty::World {
    // DECLARATIONS ONLY (passes 1-4). The body-derived channels (`app_check_sigs`
    // / `any_result_check_sigs` / `record_result_sigs`, passes 5-7) are NOT baked
    // in here — they read app defs' BODIES, which would couple every `infer` to
    // every body and break backdating. The checker takes them via
    // [`check_world_query`]; the lowering path demands `record_result_sigs`
    // per-def via [`record_result_sig_query`]. So a body-only app edit re-executes
    // this query to a *value-equal* world → salsa backdates → an unrelated def's
    // `infer` validates from memo instead of re-inferring (the headline
    // incremental property; incremental-test `body_edit_recomputes_only_that_defs_infer`).
    ty::World::build_decls(db.sky_db())
}

/// The FULL world (declarations + the body-derived CHECK-ONLY channels, passes
/// 1-6) that the accept/reject checker (`ty::check_modules`) consumes. Kept
/// SEPARATE from [`type_world_query`] deliberately: this query reads every app
/// def's body (passes 5-6), so it does NOT backdate on a body edit — which is
/// fine because it is demanded only by the whole-program checker + the per-def
/// [`record_result_sig_query`], never by the per-def lowering `infer_query` whose
/// backdating the incremental harness asserts. Value-identical to the pre-split
/// `World::build` the checker read directly.
#[salsa::tracked]
pub fn check_world_query(db: &dyn ResolveDb) -> ty::World {
    ty::World::build(db.sky_db())
}

/// The D2 record-result scheme for one def (pass 7), demanded LAZILY by the
/// lowering `infer_query` only for defs that appear as a record-update base in the
/// body under inference (`ty::update_base_defs`). Built against the FULL
/// [`check_world_query`] (passes 1-6) so the inferred field types are
/// byte-identical to the pre-split bulk pass. Because `infer_query` demands this
/// ONLY when the body actually has such an update, a body with none creates no
/// dependency on any other def's body — preserving backdating for the common case
/// (e.g. `App.main = greeting`).
/// `_module` / `_file` are salsa **carrier** keys (a tracked fn needs a
/// salsa-interned key tuple; `DefId` alone is not a salsa struct). They are the
/// CONSUMING def's module/file — the value depends only on `def` + the world, so
/// they at most cause a benign duplicate memo when the same base is updated from
/// two different modules; correctness (invalidation) flows through the
/// `check_world_query` read below, which every app body edit re-executes.
#[salsa::tracked]
pub fn record_result_sig_query(
    db: &dyn ResolveDb,
    def: DefId,
    _module: ModuleId,
    _file: SourceFile,
) -> Option<ty::Scheme> {
    let world = check_world_query(db);
    world.record_result_scheme_for(db.sky_db(), def)
}

/// Per-def call-site param-record harvest for the lowering `infer_query` (#166,
/// UNANNOTATED case). For a `callee` that record-updates one of its own params
/// but has no signature to close the row from, this returns the CONCRETE record
/// (`Ty::Record(_, None)`) each direct caller passes at each param position —
/// so the update's open row can close to the real Model instead of collapsing to
/// the narrow subset of updated fields (which codegen would emit as an anon
/// struct, silently dropping every un-updated field / nil-panicking an ADT one).
///
/// Demanded ONLY when `ty::body_updates_a_param(body)` — so a body without a
/// param-update creates no dependency, preserving backdating for the common case.
/// Built against the FULL [`check_world_query`] so caller inference is
/// byte-identical to the eager backend's whole-program harvest
/// (`World::harvest_callsite_param_records`). Cheap when `callee` is only ever
/// reached reflectively (e.g. the TEA `update` dispatch): the internal `calls_it`
/// scan finds no direct `Call` site and skips all caller inference.
/// `_module` / `_file` are salsa carrier keys (the CONSUMING def's module/file);
/// the value depends only on `callee` + the world, so at worst they cause a benign
/// duplicate memo, and invalidation flows through the `check_world_query` read.
#[salsa::tracked]
pub fn callsite_param_records_query(
    db: &dyn ResolveDb,
    def: DefId,
    _module: ModuleId,
    _file: SourceFile,
) -> Vec<Option<ty::Ty>> {
    let world = check_world_query(db);
    ty::callsite_param_records_for(db.sky_db(), &world, def)
}

/// `infer(DefId)` as a `#[salsa::tracked]` query (doc 01 `infer(DefId) -> types,
/// per-region type map`) — memoised at **per-def granularity**, the ideal
/// incremental unit. Builds the def's per-expression/per-local type table against
/// the memoised [`type_world_query`], reached via [`ty::compute_body_types`]
/// (the single inference body the eager `SourceDb` backend also runs, so the
/// typed table is byte-identical whichever backend produced it).
///
/// The union-find stays **local to this query's execution**: `compute_body_types`
/// creates the `Infer` (and its `UnionFind`), drives it, and reads every result
/// back to a canonical `ty::Ty` before returning — a `TyVarId` never escapes into
/// the memoised `BodyTypes` (doc 12 #M1; verified: `infer_def_typed` calls
/// `read_back` on the result + every recorded expr/local var).
///
/// `no_eq`: `BodyTypes` carries `HashMap`s and is consumed eagerly by the lowerer
/// (not yet a query), so recompute-on-demand is fine — no backdate needed. Keyed
/// by `(def, module, file)`: `file` (the def's own module `SourceFile`, an input)
/// is the salsa-struct a tracked fn requires as a key; `module` + `def` let the
/// query reach the body via `resolve(module)` WITHOUT reading the interned
/// `DefKey` (which would be revision-unsafe at the caller's non-query boundary).
/// The body invalidation edge is captured through that `resolve(module)` read, not
/// the key (`file` is stable across edits — `set_text` mutates the input in place).
#[salsa::tracked(no_eq)]
pub fn infer_query(
    db: &dyn ResolveDb,
    def: DefId,
    module: ModuleId,
    _file: SourceFile,
) -> ty::BodyTypes {
    // DECLARATIONS world (passes 1-4) — backdates on unrelated app body edits.
    let world = type_world_query(db);
    let sky = db.sky_db();

    // D2 (lowering path): the `Expr::Update` arm consults `record_result_sigs` for
    // a def used as a record-update base. Demand those per-def (against the full
    // check-world) and splice them into a world clone ONLY when the body actually
    // has such an update — so a body with none (the common case) depends solely on
    // `type_world` (backdated) + this module's `resolve`, and its `infer`
    // validates from memo after an unrelated body edit.
    let resolved = sky.resolve(module);
    if let Some(body) = resolved.bodies.get(&def) {
        let bases = ty::update_base_defs(body);
        // #166: an UNANNOTATED def that record-updates one of its own params has
        // no sig to close the row from — harvest the concrete record its callers
        // pass. Demanded ONLY when the body actually has such a param-update, so
        // the common case keeps its `type_world`-only (backdated) dependency.
        let updates_param = ty::body_updates_a_param(body);
        if !bases.is_empty() || updates_param {
            let mut w = world.clone();
            for d in bases {
                if let Some(scheme) = record_result_sig_query(db, d, module, _file) {
                    w.record_result_sigs.insert(d, scheme.clone());
                }
            }
            if updates_param {
                let recs = callsite_param_records_query(db, def, module, _file);
                if recs.iter().any(|o| o.is_some()) {
                    w.callsite_param_records.insert(def, recs.clone());
                }
            }
            return ty::compute_body_types(&w, sky, module, def);
        }
    }
    ty::compute_body_types(world, sky, module, def)
}

// ---- Stage E: lower + codegen as a tracked query (doc 01 bottom-of-DAG) ----
//
// The nodes BELOW `infer` — lowering (typed Go-IR) + codegen (Go source) — are
// closed here as a single **whole-program** `#[salsa::tracked]` query. This is
// the documented granularity FLOOR (doc 01 "build(project)"), chosen because the
// Rust lowerer (`lower::lower_program_cfg`) is inherently whole-program: DCE from
// `main`, the single-`Model` TEA detection, cross-module `collect_types`,
// ambiguous-name resolution, and the per-def arg→param coercion maps all read
// EVERY def before producing the one ordered `Vec<GoItem>`; and the emitted
// artifact is a single `main.go` (`codegen::emit_program`), not a per-module Go
// file — so there is no honest per-`ModuleId` `go_module` output unit to key on.
// A coarser-but-memoised `go_program` still closes the DAG end-to-end (every
// build with unchanged inputs is a cache hit) and gives `infer_query` its real
// build-path consumer: `go_program`'s execution reads `type_world` + `resolve` +
// per-def `infer` through the `TyDb`/`SkyDb` bridges, so salsa records the whole
// sub-DAG below `infer` against it. Emitted bytes are byte-identical to the
// prior eager `lower_program_cfg` + `emit_program` call pair (the `build-run` +
// `repro` gates are the guard).

/// The salsa view the Stage-E `go_program` query needs to reach the lowerer.
/// Mirrors [`ResolveDb`]: `lower::lower_program_cfg` is authored against the
/// forbid-clean [`ty::TyDb`] surface, but a `#[salsa::tracked]` query only
/// receives a salsa database. [`SkyDatabase`] hands back `self` as a
/// `&dyn TyDb`, so the query drives lowering while every read it performs
/// (`type_world`, `resolve`, per-def `infer`, `parse`, interning) is recorded
/// against the executing `go_program` query — closing the DAG below `infer`.
#[salsa::db]
pub trait LowerDb: salsa::Database {
    /// This database as the forbid-clean type-database interface the lowerer consumes.
    fn ty_db(&self) -> &dyn ty::TyDb;
}

#[salsa::db]
impl LowerDb for SkyDatabase {
    fn ty_db(&self) -> &dyn ty::TyDb {
        self
    }
}

/// The build-time lowering configuration (`sky.toml` port/defaults + the pinned
/// Go-FFI surface) modelled as a salsa **input** so it participates in the query
/// key. The driver creates one per build and never mutates it, so `go_program`
/// keyed on it is a memo hit across re-demands within a build/LSP session while a
/// `SourceFile` text edit still invalidates through the parse/resolve/infer edges
/// `go_program` records internally. Held as one opaque `LowerConfig` field
/// (the FFI table is large; salsa stores it once, borrowed via `returns(ref)`).
#[salsa::input]
pub struct BuildConfig {
    /// The lowerer's whole-program config for this build.
    #[returns(ref)]
    pub cfg: lower::LowerConfig,
}

/// The product of the Stage-E `go_program` query: the emitted Go source (when
/// lowering produced a buildable program) plus the diagnostics + FFI-usage set
/// the build driver needs. `source` is `None` when there is no `main` in the
/// entry module or lowering raised a hard error — the driver surfaces `errors`
/// exactly as it did off the eager `LowerOutput`.
#[derive(Clone, Debug)]
pub struct GoProgram {
    /// The emitted `main.go` bytes, or `None` when lowering found no entry / erred.
    pub source: Option<String>,
    pub warnings: Vec<String>,
    pub errors: Vec<String>,
    /// True when `main` was found + lowered.
    pub entry_ok: bool,
    /// Sky module paths of the Go-FFI packages the emitted program actually calls.
    pub ffi_used: std::collections::BTreeSet<String>,
    /// True when the program imports `Std.Live.*` / `Sky.Http.Server.*` — the
    /// emitted `main.go` blank-imports `sky-app/rt/console_app`, so the build
    /// driver must materialise `rt/console_app` for the import to resolve.
    pub console_needed: bool,
}

/// `go_program` — the Stage-E tracked query (doc 01 `typed_hir → go_module →
/// build`, landed at the whole-program floor). Lowers the whole program from
/// `entry` against the memoised type world + per-def inference, then renders the
/// Go source. Pure + memoised: re-demanding with unchanged inputs is a cache hit;
/// any `SourceFile` edit that transitively reaches a lowered def re-executes it.
///
/// `no_eq`: `GoProgram` carries the emitted source + `Vec`/`BTreeSet` diagnostics
/// and is consumed eagerly by the build driver (written to disk + `go build`), so
/// recompute-on-demand is the right shape — no backdate needed, matching
/// [`infer_query`]/[`resolve_query`]. Keyed by `(entry, config)`: `config` (a
/// salsa input) carries the FFI/port memo edge; the source/inference edges are
/// captured through the `db.ty_db()` reads the lowerer performs while executing.
#[salsa::tracked(no_eq)]
pub fn go_program(db: &dyn LowerDb, entry: ModuleId, config: BuildConfig) -> GoProgram {
    let cfg = config.cfg(db);
    let out = lower::lower_program_cfg(db.ty_db(), entry, cfg);
    // Emit only when lowering yielded a buildable program — otherwise the driver
    // reports `errors`/`entry_ok` and never writes/`go build`s (unchanged from
    // the eager path).
    let source = if out.entry_ok && out.errors.is_empty() {
        Some(codegen::emit_program(&out.items, out.console_needed))
    } else {
        None
    };
    GoProgram {
        source,
        warnings: out.warnings,
        errors: out.errors,
        entry_ok: out.entry_ok,
        ffi_used: out.ffi_used,
        console_needed: out.console_needed,
    }
}

/// A source file's text keyed by its `file_id` — the Stage-A **input** (doc 01
/// `source_text(FileId)`). One per module; the module set is the collection of
/// these inputs. The driver `set_*`s them; every query is a pure function of
/// them (L2 — editing one file invalidates only what transitively depends on it).
#[salsa::input]
pub struct SourceFile {
    /// The interned file id (matches `base::FileId`'s raw index).
    #[returns(copy)]
    pub file_id: u32,
    /// The full source text. `returns(ref)` so the `parse` query borrows it
    /// rather than cloning the whole module body every call.
    #[returns(ref)]
    pub text: String,
}

/// The `parse` leaf query (Stage B, doc 01 / doc 04). Pure + memoised: a
/// `SourceFile` input → its lossless CST + parse diagnostics. Never panics;
/// broken input yields `ERROR` nodes + diagnostics inside the returned `Parse`
/// (L7, L8). This is the salsa-tracked successor to the driver's inline
/// `syntax::parse(text, FileId)` call.
///
/// `no_eq`: `syntax::Parse` (a rowan `GreenNode` + `Vec<Diagnostic>`) has no
/// `PartialEq`, so salsa cannot backdate on it. Correctness is unaffected — the
/// cold build path parses each file exactly once; the LSP rebuilds per request.
/// Wiring backdating (an `Eq` on `Parse`, or a green-node identity compare) is a
/// future incremental-LSP refinement, not a Stage-B requirement.
#[salsa::tracked(no_eq)]
pub fn parse(db: &dyn salsa::Database, file: SourceFile) -> syntax::Parse {
    syntax::parse(file.text(db), base::FileId(file.file_id(db)))
}

impl SkyDatabase {
    /// A fresh db with the kernel pseudo-module table populated (`Std.Db`,
    /// `Sky.Core.List`, …) — the salsa-backed peer of `hir::SourceDb::new`.
    pub fn with_kernel() -> Self {
        SkyDatabase {
            kernel: hir::KERNEL_MODULES
                .iter()
                .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                .collect(),
            ..Default::default()
        }
    }

    /// A kernel-populated db that mirrors every salsa event's `kind` (stringified)
    /// into `sink`. This is the observability seam the incremental-correctness
    /// harness (and, later, LSP recompute profiling) uses to assert WHICH queries
    /// re-execute after a `SourceFile` edit — a `WillExecute` line names the query
    /// that recomputed, a `DidValidateMemoizedValue` line names one served from
    /// memo. The default constructors install no callback (zero overhead); only
    /// this path pays the per-event `format!` + lock.
    pub fn with_kernel_events(sink: Arc<Mutex<Vec<String>>>) -> Self {
        SkyDatabase {
            storage: salsa::Storage::new(Some(Box::new(move |event: salsa::Event| {
                sink.lock().unwrap().push(format!("{:?}", event.kind));
            }))),
            kernel: hir::KERNEL_MODULES
                .iter()
                .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                .collect(),
            ..Default::default()
        }
    }

    /// Mint a [`SourceFile`] input for a module's text. `file_id` is the module's
    /// eventual `ModuleId` index (the driver assigns them in load order); the
    /// `parse` + `module_exports` queries key off the returned handle. Creation is
    /// `&self` (salsa input), so this composes before the `&mut self`
    /// [`add_module`] registration.
    pub fn new_source(&self, file_id: u32, text: String) -> SourceFile {
        SourceFile::new(self, file_id, text)
    }

    /// Register a parsed module under its dotted name. A later add with the same
    /// name overrides (local modules shadow stdlib) — identical to
    /// `hir::SourceDb::add_module`.
    pub fn add_module(&mut self, name: &str, file: SourceFile) -> ModuleId {
        if let Some(&id) = self.by_name.get(name) {
            self.modules[id.index() as usize].file = file;
            return id;
        }
        let id = ModuleId(self.modules.len() as u32);
        self.modules.push(ModuleReg {
            name: name.to_string(),
            file,
        });
        self.by_name.insert(name.to_string(), id);
        id
    }

    /// The [`SourceFile`] input backing a registered module (for reading its
    /// parse-error diagnostics off the driver's build path).
    pub fn source_file(&self, m: ModuleId) -> SourceFile {
        self.modules[m.index() as usize].file
    }

    /// Replace one module's source text in place (doc 01 `set_source_text`, doc
    /// 10 §"Incremental for free"). This is the *only* mutation an incremental
    /// driver (the LSP `didChange`, `sky watch`) performs: salsa marks
    /// `parse(file)` and its transitive dependents maybe-dirty, and the next
    /// demand recomputes only that sub-DAG — every unrelated module's memoised
    /// `resolve`/`infer` stands. Kept here so the salsa `Setter` stays quarantined
    /// in `skydb` (the LSP crate never imports salsa; L1).
    pub fn set_source_text(&mut self, file: SourceFile, text: String) {
        use salsa::Setter;
        file.set_text(self).to(text);
    }

    /// Intern an ordered module set as Stage-A [`SourceFile`] inputs. Returns the
    /// handles in input order, so a caller may treat position `i` as the module's
    /// ordinal (matching `base::ModuleId(i)`). Interning is `&self` (salsa input
    /// creation), so a shared reference to the db suffices.
    pub fn intern_module_set<I>(&self, sources: I) -> Vec<SourceFile>
    where
        I: IntoIterator<Item = String>,
    {
        sources
            .into_iter()
            .enumerate()
            .map(|(i, text)| SourceFile::new(self, i as u32, text))
            .collect()
    }
}

/// The salsa-backed implementation of the resolution-database interface. Every
/// method the forbid-clean frontend (`hir::resolve`, `ty`, `lower`) calls routes
/// to a salsa query or the append-only registry — the same surface
/// `hir::SourceDb` implements eagerly, so swapping one for the other is invisible
/// to every call site. This is the seam that lets salsa storage live here (out
/// of the `#![forbid(unsafe_code)]` crates) while the query authors stay clean.
impl SkyDb for SkyDatabase {
    fn module_name(&self, m: ModuleId) -> &str {
        &self.modules[m.index() as usize].name
    }
    fn module_parse(&self, m: ModuleId) -> &syntax::Parse {
        parse(self, self.modules[m.index() as usize].file)
    }
    fn module_by_name(&self, name: &str) -> Option<ModuleId> {
        self.by_name.get(name).copied()
    }
    fn classify_import(&self, path: &str) -> ImportSource {
        if let Some(id) = self.by_name.get(path) {
            return ImportSource::Dep(*id);
        }
        if let Some(pseudo) = self.kernel.get(path) {
            return ImportSource::Kernel(pseudo.clone());
        }
        ImportSource::Foreign(path.to_string())
    }
    fn kernel_pseudo(&self, qualifier: &str) -> Option<&str> {
        self.kernel.get(qualifier).map(String::as_str)
    }
    fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        Rc::new(module_exports(self, m, self.modules[m.index() as usize].file).clone())
    }
    fn resolve(&self, m: ModuleId) -> Rc<ResolveResult> {
        // Salsa build path: route to the tracked `resolve_query` so the
        // parse/exports → resolve edges are memoised + invalidated natively.
        // Clone out of the memo into an `Rc` (the shared owning shape both
        // backends return), exactly like `module_exports` above.
        Rc::new(resolve_query(self, m, self.modules[m.index() as usize].file).clone())
    }
    fn module_ids(&self) -> Vec<ModuleId> {
        (0..self.modules.len() as u32).map(ModuleId).collect()
    }
    fn intern_def(&self, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
        intern_def_id(self, module, name, kind)
    }
    fn def_loc(&self, def: DefId) -> Option<DefLoc> {
        Some(def_id_loc(self, def))
    }
}

/// The salsa-backed [`ty::TyDb`]: the assembled world and each def's typed table
/// route to the memoised [`type_world_query`] / [`infer_query`]. Mirrors the
/// `SkyDb` impl above (route to a tracked query, clone out of the memo into the
/// owning `Rc` shape both backends return) — the seam that keeps salsa in `skydb`
/// while `ty`/`lower` author against the forbid-clean `TyDb` interface.
impl ty::TyDb for SkyDatabase {
    fn type_world(&self) -> Rc<ty::World> {
        Rc::new(type_world_query(self).clone())
    }
    fn check_world(&self) -> Rc<ty::World> {
        Rc::new(check_world_query(self).clone())
    }
    fn body_types_of(&self, module: ModuleId, def: DefId) -> Rc<ty::BodyTypes> {
        // `module` comes from the caller (revision-safe — no interned read here);
        // `source_file` reads the append-only registry, also revision-safe.
        let file = self.source_file(module);
        Rc::new(infer_query(self, def, module, file).clone())
    }
    fn as_sky_db(&self) -> &dyn SkyDb {
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn one_input_one_parse_query_end_to_end() {
        let db = SkyDatabase::default();
        let file = SourceFile::new(
            &db,
            0,
            "module Main exposing (main)\n\nmain = 1\n".to_string(),
        );
        // The tracked query returns a reference to the memoised value in 0.28.
        let p = parse(&db, file);
        assert_eq!(p.error_node_count(), 0);
        // L8 losslessness through the salsa boundary: reprint == input text.
        assert_eq!(p.reprint(), "module Main exposing (main)\n\nmain = 1\n");
    }

    #[test]
    fn parse_through_salsa_matches_direct_parse() {
        // Byte-for-byte determinism gate at the leaf: routing a parse through the
        // salsa input+query must produce the identical CST the driver's inline
        // `syntax::parse` produced (the invariant the build path relies on).
        let src = "module M exposing (..)\n\ntype T = A | B\n\nf x =\n    case x of\n        A -> 1\n        B -> 2\n";
        let db = SkyDatabase::default();
        let file = SourceFile::new(&db, 3, src.to_string());
        let via_salsa = parse(&db, file);
        let direct = syntax::parse(src, base::FileId(3));
        assert_eq!(via_salsa.reprint(), direct.reprint());
        assert_eq!(via_salsa.error_node_count(), direct.error_node_count());
    }

    #[test]
    fn intern_module_set_preserves_order() {
        let db = SkyDatabase::default();
        let files = db.intern_module_set(["module A\n".to_string(), "module B\n".to_string()]);
        assert_eq!(files.len(), 2);
        assert_eq!(files[0].file_id(&db), 0);
        assert_eq!(files[1].file_id(&db), 1);
        assert_eq!(parse(&db, files[0]).reprint(), "module A\n");
        assert_eq!(parse(&db, files[1]).reprint(), "module B\n");
    }
}
