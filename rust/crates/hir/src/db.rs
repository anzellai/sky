//! The resolution database (doc 05 §1, §8). A plain db-threaded value — no
//! globals (L1). Owns the parsed modules, the `DefId` interner, and a memoised
//! `module_exports` cache; cross-module visibility is a demand-driven lookup
//! (`module_exports(dep)`), never a pre-pass or a 5-round fixpoint (L2).
//!
//! The salsa integration (doc 05 §1) would wrap these same functions as tracked
//! queries; the value-threaded form here is the acceptable plain-function
//! variant the task permits, structured so a salsa port is mechanical.

use crate::exports::{compute_exports, ModuleExports};
use crate::ids::{DefKind, DefLoc, DefTable};
use crate::kernel::KERNEL_MODULES;
use base::{DefId, ModuleId, Name};
use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

/// Where an import path resolves (doc 05 §5). A parsed Sky module wins over a
/// kernel pseudo-module of the same path (we resolve exposed/qualified names
/// against the real exports; the kernel fallback still covers bare qualifiers).
#[derive(Clone, Debug)]
pub enum ImportSource {
    /// A user/stdlib Sky-source module with real exports.
    Dep(ModuleId),
    /// A Go-implemented kernel pseudo-module (`Std.Db`, `Sky.Core.List`, …).
    Kernel(String),
    /// A Go FFI package (`sky add`) — the FFI surface (doc 09 / M3) is not built
    /// yet, so references through it resolve leniently and are tracked class-(b).
    Foreign(String),
}

#[derive(Clone)]
struct ModuleInfo {
    name: String,
    parse: syntax::Parse,
    /// The dotted import paths this module's parse names, captured at
    /// `add_module` time. This is the *dependency footprint* of
    /// [`SourceDb::resolve`] for this module (see the memo invalidation rule on
    /// [`SourceDb::add_module`]) — `resolve(m)` reads `m`'s own parse, plus
    /// `classify_import(p)` and `module_exports(dep)` for each import path `p`,
    /// and nothing else.
    imports: Vec<String>,
}

/// The dotted import paths a parse names — the resolve dependency footprint.
fn import_paths(parse: &syntax::Parse) -> Vec<String> {
    let tree = parse.tree();
    let mut out: Vec<String> = Vec::new();
    for imp in tree.imports() {
        if let Some(p) = imp.name().map(|n| n.text()) {
            if !p.is_empty() && !out.contains(&p) {
                out.push(p);
            }
        }
    }
    out
}

/// The resolution database.
///
/// The old `exports_cache: RefCell<HashMap<…>>` hand-rolled memo is gone (the
/// resolve-stage salsa port): on the real build path `module_exports` is a
/// `#[salsa::tracked]` query on `skydb::SkyDatabase`, which memoises it natively.
///
/// # The `resolve` memo — why it is back, with a measurement
///
/// The removed memo's docstring used to claim this eager `SourceDb`
/// "simply recomputes on demand — cheap, and it never sat on a hot loop."
/// **That sentence was wrong and is not to be reinstated.** A `sample(1)`
/// profile of `xtask reject` (10,953 samples, 2026-08-10) attributes
/// **75.4 % of the entire gate** to `hir::resolve` re-running per stdlib
/// module underneath `ty::sig::resolve_type_names` — 0.975 s of the measured
/// 1.293 s per corpus case. The reason is structural, not incidental:
/// `resolve_type_names` (`ty/src/sig.rs:1533`) calls `db.resolve(m)` **once per
/// type annotation and once per alias body**, and `World::build_decls` runs it
/// across all 87 stdlib modules. That is thousands of full module resolutions
/// per world build, of a function that is a pure function of its inputs.
///
/// So it sits on the hottest loop in CI. Do not remove this memo without
/// re-running that profile.
///
/// # Correctness — the invalidation obligation
///
/// `resolve(m)` is a pure function of: `m`'s own parse; `classify_import(p)`
/// for each import path `p` in `m`; and `module_exports(dep)` (hence `dep`'s
/// parse) for each `p` that classifies to a `Dep`. [`SourceDb::add_module`]
/// **overwrites** the parse on a name collision, and a newly added name can
/// flip an import from `Kernel`/`Foreign` to `Dep` — so both paths invalidate,
/// and both invalidate *dependents*, not just the module itself. See the rule
/// documented on `add_module`.
///
/// # `Clone` is the fork
///
/// The shared-world corpus path (`ty::shared`) clones a **pristine base**
/// carrying the stdlib modules, their memoised resolutions and their interner,
/// once per case — so each case appends its modules at fresh indices in an
/// interner no other case shares. Without that, `DefTable::intern`'s
/// `(module.index(), name, kind)` key would hand consecutive cases the SAME
/// `DefId` for `Main.main` and every `DefId`-keyed channel of the assembled
/// `World` would leak case *N*'s data into case *N+1*. Parses are
/// reference-counted so the module clone is cheap; the `DefTable` clone is the
/// real cost, and it is exactly the price of that isolation.
#[derive(Clone)]
pub struct SourceDb {
    modules: Vec<ModuleInfo>,
    by_name: HashMap<String, ModuleId>,
    kernel: HashMap<String, String>,
    defs: RefCell<DefTable>,
    /// Memoised `resolve(m)`. Invalidated by `add_module` per the rule above.
    resolved: RefCell<HashMap<ModuleId, Rc<crate::ResolveResult>>>,
    /// (hits, misses) — observability for the memo, so a test can *prove* the
    /// memo memoises rather than asserting it. Not part of the db's value.
    resolve_hits: std::cell::Cell<u64>,
    resolve_misses: std::cell::Cell<u64>,
}

impl Default for SourceDb {
    fn default() -> Self {
        Self::new()
    }
}

impl SourceDb {
    pub fn new() -> Self {
        let kernel = KERNEL_MODULES
            .iter()
            .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
            .collect();
        SourceDb {
            modules: Vec::new(),
            by_name: HashMap::new(),
            kernel,
            defs: RefCell::new(DefTable::new()),
            resolved: RefCell::new(HashMap::new()),
            resolve_hits: std::cell::Cell::new(0),
            resolve_misses: std::cell::Cell::new(0),
        }
    }

    /// Register a parsed module under its dotted name. A later add with the same
    /// name overrides (local modules shadow stdlib in the per-example db).
    ///
    /// # Memo invalidation (a correctness obligation, not an optimisation)
    ///
    /// Adding a module can invalidate a *previously computed* `resolve(y)` for
    /// some **other** module `y`, in two distinct ways:
    ///
    /// 1. **Overwrite.** The collision branch below replaces `id`'s parse. Every
    ///    `y` that imports this name read the OLD parse's exports, so `y`'s
    ///    resolution is stale — as is `id`'s own.
    /// 2. **New name.** A module `y` that imports path `N` classified `N` as
    ///    `Kernel` or `Foreign` while no module was registered under `N`
    ///    (`classify_import`: parsed dep > kernel > foreign). Registering `N`
    ///    flips that classification to `Dep`, so `y`'s resolution is stale even
    ///    though `y` itself was untouched.
    ///
    /// Invalidating only the added module's own entry — the narrow reading —
    /// would leave case 2 and the dependents half of case 1 serving stale
    /// resolutions. So both branches invalidate `{ y : N ∈ imports(y) }`, and
    /// the overwrite branch additionally invalidates `id`.
    ///
    /// This is exact rather than conservative (a blanket `clear()`) on purpose:
    /// the shared-world corpus runner forks a db whose stdlib resolutions are
    /// already memoised and then adds the case's modules, and a blanket clear
    /// would throw away precisely the work the memo exists to keep.
    pub fn add_module(&mut self, name: &str, parse: syntax::Parse) -> ModuleId {
        let imports = import_paths(&parse);
        if let Some(&id) = self.by_name.get(name) {
            self.modules[id.index() as usize].parse = parse;
            self.modules[id.index() as usize].imports = imports;
            self.invalidate_resolve_for(name, Some(id));
            return id;
        }
        let id = ModuleId(self.modules.len() as u32);
        self.modules.push(ModuleInfo {
            name: name.to_string(),
            parse,
            imports,
        });
        self.by_name.insert(name.to_string(), id);
        self.invalidate_resolve_for(name, None);
        id
    }

    /// Drop memoised resolutions invalidated by `name` being (re)registered.
    /// See the rule on [`SourceDb::add_module`].
    fn invalidate_resolve_for(&mut self, name: &str, overwritten: Option<ModuleId>) {
        let memo = self.resolved.get_mut();
        if memo.is_empty() {
            return;
        }
        if let Some(id) = overwritten {
            memo.remove(&id);
        }
        // Dependents: any module whose import list names `name`.
        let stale: Vec<ModuleId> = self
            .modules
            .iter()
            .enumerate()
            .filter(|(_, mi)| mi.imports.iter().any(|p| p == name))
            .map(|(i, _)| ModuleId(i as u32))
            .collect();
        for y in stale {
            memo.remove(&y);
        }
    }

    /// `(hits, misses)` for the `resolve` memo. Observability only — lets a test
    /// prove the memo is actually memoising instead of asserting it.
    pub fn resolve_memo_stats(&self) -> (u64, u64) {
        (self.resolve_hits.get(), self.resolve_misses.get())
    }

    /// Number of live memoised resolutions (test/observability).
    pub fn resolve_memo_len(&self) -> usize {
        self.resolved.borrow().len()
    }

    pub fn module_name(&self, m: ModuleId) -> &str {
        &self.modules[m.index() as usize].name
    }

    pub fn module_parse(&self, m: ModuleId) -> &syntax::Parse {
        &self.modules[m.index() as usize].parse
    }

    pub fn module_by_name(&self, name: &str) -> Option<ModuleId> {
        self.by_name.get(name).copied()
    }

    /// Classify an import path (doc 05 §5). Parsed module > kernel > foreign.
    pub fn classify_import(&self, path: &str) -> ImportSource {
        if let Some(id) = self.by_name.get(path) {
            return ImportSource::Dep(*id);
        }
        if let Some(pseudo) = self.kernel.get(path) {
            return ImportSource::Kernel(pseudo.clone());
        }
        ImportSource::Foreign(path.to_string())
    }

    /// Is `qualifier` a kernel pseudo-module (bare kernel qualifier fallback)?
    pub fn kernel_pseudo(&self, qualifier: &str) -> Option<&str> {
        self.kernel.get(qualifier).map(String::as_str)
    }

    /// `module_exports(m)` — computed purely from `m`'s parse (doc 05 §8: no
    /// recursion into other modules, so no cycles). Recomputed per call (the
    /// former `RefCell` memo is gone; the salsa build path memoises natively).
    pub fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        let tree = self.modules[m.index() as usize].parse.tree();
        let defs = &self.defs;
        let exports = compute_exports(m, &tree, &mut |mm, n, k| defs.borrow_mut().intern(mm, n, k));
        Rc::new(exports)
    }

    /// Mint / recover a `DefId` for a name in a module.
    pub fn defs(&self) -> &RefCell<DefTable> {
        &self.defs
    }

    /// All registered module ids, in insertion order (deterministic, L4).
    pub fn module_ids(&self) -> impl Iterator<Item = ModuleId> {
        (0..self.modules.len() as u32).map(ModuleId)
    }
}

/// The resolution-database interface the forbid-clean frontend crates
/// (`hir::resolve`, `ty`, `lower`, `sky-lsp`) call through (doc 05 §1, §8, L1).
///
/// This is the seam the salsa port needs: `hir` cannot host salsa (its
/// proc-macros expand to `unsafe impl`, incompatible with `#![forbid(unsafe_code)]`
/// — doc 02), so the query authors reach the database only via these methods.
/// The eager, hand-rolled [`SourceDb`] implements it directly; a salsa-backed
/// `skydb::SkyDatabase` implements the same surface with `#[salsa::tracked]`
/// `module_exports` + `#[salsa::interned]` `DefId`s — swapping the storage
/// without touching a single call site. Deliberately borrow-returning where the
/// implementation can hand out a `&self`-tied reference (`module_name`,
/// `module_parse`, `kernel_pseudo`); `intern_def`/`def_loc` replace the old
/// `defs() -> &RefCell<DefTable>` accessor so the interner can live behind
/// content-keyed salsa storage that has no `RefCell` to lend out.
pub trait SkyDb {
    /// The dotted module name for a registered module id.
    fn module_name(&self, m: ModuleId) -> &str;
    /// The parsed CST for a registered module id.
    fn module_parse(&self, m: ModuleId) -> &syntax::Parse;
    /// The module id registered under a dotted name, if any.
    fn module_by_name(&self, name: &str) -> Option<ModuleId>;
    /// Classify an import path (parsed dep > kernel pseudo > foreign package).
    fn classify_import(&self, path: &str) -> ImportSource;
    /// The kernel pseudo-module a bare qualifier names, if any.
    fn kernel_pseudo(&self, qualifier: &str) -> Option<&str>;
    /// A module's exports, narrowed by its `exposing` clause (memoised).
    fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports>;
    /// Resolve a module to its `ResolveResult` (doc 05 §1). The salsa-backed
    /// `skydb::SkyDatabase` routes this to the `#[salsa::tracked]` `resolve_query`
    /// (so the parse/exports → resolve dependency edges are captured for
    /// incremental invalidation); the eager [`SourceDb`] recomputes on demand.
    /// Returned behind `Rc` so both backends share one owning shape (the tracked
    /// query clones out of its memo, `SourceDb` computes fresh), mirroring
    /// [`SkyDb::module_exports`].
    fn resolve(&self, m: ModuleId) -> Rc<crate::ResolveResult>;
    /// All registered module ids, in insertion order (deterministic, L4).
    fn module_ids(&self) -> Vec<ModuleId>;
    /// Mint / recover the stable `DefId` for `(module, name, kind)` — the
    /// register-on-first-mention interner (successor to `defs().borrow_mut()`).
    fn intern_def(&self, module: ModuleId, name: &Name, kind: DefKind) -> DefId;
    /// Recover a definition's location from its id (successor to
    /// `defs().borrow().loc()`).
    fn def_loc(&self, def: DefId) -> Option<DefLoc>;
}

impl SkyDb for SourceDb {
    fn module_name(&self, m: ModuleId) -> &str {
        SourceDb::module_name(self, m)
    }
    fn module_parse(&self, m: ModuleId) -> &syntax::Parse {
        SourceDb::module_parse(self, m)
    }
    fn module_by_name(&self, name: &str) -> Option<ModuleId> {
        SourceDb::module_by_name(self, name)
    }
    fn classify_import(&self, path: &str) -> ImportSource {
        SourceDb::classify_import(self, path)
    }
    fn kernel_pseudo(&self, qualifier: &str) -> Option<&str> {
        SourceDb::kernel_pseudo(self, qualifier)
    }
    fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        SourceDb::module_exports(self, m)
    }
    fn resolve(&self, m: ModuleId) -> Rc<crate::ResolveResult> {
        // Memoised per module — see the measurement + invalidation rule on
        // `SourceDb` and `SourceDb::add_module`. `resolve` does not re-enter
        // `resolve` (it reaches other modules only through `module_exports` /
        // `classify_import`), so the borrow below cannot alias; it is still
        // released before computing, so a future re-entrant path degrades to a
        // duplicate computation rather than a `RefCell` panic.
        if let Some(r) = self.resolved.borrow().get(&m) {
            self.resolve_hits.set(self.resolve_hits.get() + 1);
            return r.clone();
        }
        self.resolve_misses.set(self.resolve_misses.get() + 1);
        let r = Rc::new(crate::resolve::resolve(self, m));
        self.resolved.borrow_mut().insert(m, r.clone());
        r
    }
    fn module_ids(&self) -> Vec<ModuleId> {
        SourceDb::module_ids(self).collect()
    }
    fn intern_def(&self, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
        self.defs.borrow_mut().intern(module, name, kind)
    }
    fn def_loc(&self, def: DefId) -> Option<DefLoc> {
        self.defs.borrow().loc(def)
    }
}

#[cfg(test)]
mod resolve_memo_tests {
    use super::*;

    fn p(src: &str) -> syntax::Parse {
        syntax::parse(src, base::FileId(0))
    }

    /// The memo memoises: repeated `resolve` of the same module computes once.
    #[test]
    fn memo_hits_after_first_resolve() {
        let mut db = SourceDb::new();
        let m = db.add_module("Main", p("module Main exposing (main)\n\nmain = 1\n"));
        assert_eq!(db.resolve_memo_stats(), (0, 0));
        let a = SkyDb::resolve(&db, m);
        assert_eq!(db.resolve_memo_stats(), (0, 1), "first call must miss");
        for _ in 0..10 {
            let b = SkyDb::resolve(&db, m);
            // Same memo entry, not a recomputation.
            assert!(Rc::ptr_eq(&a, &b));
        }
        assert_eq!(
            db.resolve_memo_stats(),
            (10, 1),
            "ten repeats must be ten hits and still one computation"
        );
    }

    /// Overwrite invalidation: `add_module` under an existing name replaces the
    /// parse, and the memo must not keep serving the OLD module's resolution.
    /// This is the path `SourceDb::add_module` documents, and the path Layer 1's
    /// same-named-module axis exercises constantly.
    #[test]
    fn overwrite_invalidates_the_overwritten_module() {
        let mut db = SourceDb::new();
        let m = db.add_module("Std.Log", p("module Std.Log exposing (alpha)\n\nalpha = 1\n"));
        let before = SkyDb::resolve(&db, m);
        let n_before = before.top_defs.len();
        assert_eq!(n_before, 1);

        // Shadow it with a DIFFERENT body under the same name.
        let m2 = db.add_module(
            "Std.Log",
            p("module Std.Log exposing (beta, gamma)\n\nbeta = 1\n\ngamma = 2\n"),
        );
        assert_eq!(m, m2, "same name must reuse the ModuleId (the overwrite path)");

        let after = SkyDb::resolve(&db, m);
        assert!(
            !Rc::ptr_eq(&before, &after),
            "stale memo served after an overwrite"
        );
        let names: Vec<String> = after
            .top_defs
            .iter()
            .map(|d| d.name.as_str().to_string())
            .collect();
        assert_eq!(names.len(), 2, "resolution must reflect the NEW parse: {names:?}");
        assert!(names.iter().any(|n| n == "beta"));
    }

    /// Overwrite invalidation reaches DEPENDENTS. `App` imports `Lib`; `App`'s
    /// resolution reads `Lib`'s exports, so replacing `Lib` must invalidate
    /// `App` too — the half a "drop the entry for that ModuleId" reading misses.
    #[test]
    fn overwrite_invalidates_dependents() {
        let mut db = SourceDb::new();
        let lib = db.add_module("Lib", p("module Lib exposing (alpha)\n\nalpha = 1\n"));
        let app = db.add_module(
            "App",
            p("module App exposing (main)\n\nimport Lib exposing (alpha)\n\nmain = alpha\n"),
        );
        let app_before = SkyDb::resolve(&db, app);
        let _ = SkyDb::resolve(&db, lib);
        assert_eq!(db.resolve_memo_len(), 2);

        // Replace Lib so `alpha` no longer exists.
        db.add_module("Lib", p("module Lib exposing (omega)\n\nomega = 1\n"));
        assert!(
            !db.resolved.borrow().contains_key(&app),
            "dependent App was not invalidated when its dependency Lib was overwritten"
        );
        let app_after = SkyDb::resolve(&db, app);
        assert!(!Rc::ptr_eq(&app_before, &app_after));
    }

    /// Adding a NEW module invalidates dependents whose import of that name had
    /// already been classified as kernel/foreign. `classify_import` prefers a
    /// parsed module, so registering the name flips the classification.
    #[test]
    fn new_module_invalidates_prior_foreign_importers() {
        let mut db = SourceDb::new();
        let app = db.add_module(
            "App",
            p("module App exposing (main)\n\nimport Later exposing (thing)\n\nmain = thing\n"),
        );
        let _ = SkyDb::resolve(&db, app);
        assert_eq!(db.resolve_memo_len(), 1);

        // `Later` did not exist; now it does.
        db.add_module("Later", p("module Later exposing (thing)\n\nthing = 1\n"));
        assert!(
            !db.resolved.borrow().contains_key(&app),
            "App still memoised against the pre-registration Foreign classification of `Later`"
        );
    }

    /// Invalidation is EXACT, not a blanket clear: an unrelated module's memo
    /// entry survives. This is the property the shared-world corpus runner
    /// depends on — adding a case's modules must not evict the stdlib's work.
    #[test]
    fn invalidation_spares_unrelated_modules() {
        let mut db = SourceDb::new();
        let unrelated = db.add_module("Far", p("module Far exposing (x)\n\nx = 1\n"));
        let lib = db.add_module("Lib", p("module Lib exposing (alpha)\n\nalpha = 1\n"));
        let _ = SkyDb::resolve(&db, unrelated);
        let _ = SkyDb::resolve(&db, lib);
        assert_eq!(db.resolve_memo_len(), 2);

        db.add_module("Lib", p("module Lib exposing (beta)\n\nbeta = 1\n"));
        assert!(
            db.resolved.borrow().contains_key(&unrelated),
            "blanket invalidation: `Far` was evicted although it imports nothing"
        );
        assert!(!db.resolved.borrow().contains_key(&lib));
    }
}
