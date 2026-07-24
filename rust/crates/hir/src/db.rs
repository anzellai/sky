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

struct ModuleInfo {
    name: String,
    parse: syntax::Parse,
}

/// The resolution database.
///
/// The old `exports_cache: RefCell<HashMap<…>>` hand-rolled memo is gone (the
/// resolve-stage salsa port): on the real build path `module_exports` is a
/// `#[salsa::tracked]` query on `skydb::SkyDatabase`, which memoises it natively;
/// this eager `SourceDb` (LSP + test path) simply recomputes on demand — cheap,
/// and it never sat on a hot loop.
pub struct SourceDb {
    modules: Vec<ModuleInfo>,
    by_name: HashMap<String, ModuleId>,
    kernel: HashMap<String, String>,
    defs: RefCell<DefTable>,
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
        }
    }

    /// Register a parsed module under its dotted name. A later add with the same
    /// name overrides (local modules shadow stdlib in the per-example db).
    pub fn add_module(&mut self, name: &str, parse: syntax::Parse) -> ModuleId {
        if let Some(&id) = self.by_name.get(name) {
            self.modules[id.index() as usize].parse = parse;
            return id;
        }
        let id = ModuleId(self.modules.len() as u32);
        self.modules.push(ModuleInfo {
            name: name.to_string(),
            parse,
        });
        self.by_name.insert(name.to_string(), id);
        id
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
        // Eager path (LSP + tests): recompute on demand. Cheap and never on a
        // hot loop; the salsa `SkyDatabase` is the memoised build-path backend.
        Rc::new(crate::resolve::resolve(self, m))
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
