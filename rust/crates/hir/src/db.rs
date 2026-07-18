//! The resolution database (doc 05 §1, §8). A plain db-threaded value — no
//! globals (L1). Owns the parsed modules, the `DefId` interner, and a memoised
//! `module_exports` cache; cross-module visibility is a demand-driven lookup
//! (`module_exports(dep)`), never a pre-pass or a 5-round fixpoint (L2).
//!
//! The salsa integration (doc 05 §1) would wrap these same functions as tracked
//! queries; the value-threaded form here is the acceptable plain-function
//! variant the task permits, structured so a salsa port is mechanical.

use crate::exports::{compute_exports, ModuleExports};
use crate::ids::DefTable;
use crate::kernel::KERNEL_MODULES;
use base::ModuleId;
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
pub struct SourceDb {
    modules: Vec<ModuleInfo>,
    by_name: HashMap<String, ModuleId>,
    kernel: HashMap<String, String>,
    defs: RefCell<DefTable>,
    exports_cache: RefCell<HashMap<u32, Rc<ModuleExports>>>,
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
            exports_cache: RefCell::new(HashMap::new()),
        }
    }

    /// Register a parsed module under its dotted name. A later add with the same
    /// name overrides (local modules shadow stdlib in the per-example db).
    pub fn add_module(&mut self, name: &str, parse: syntax::Parse) -> ModuleId {
        if let Some(&id) = self.by_name.get(name) {
            self.modules[id.index() as usize].parse = parse;
            self.exports_cache.borrow_mut().remove(&id.index());
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

    /// `module_exports(m)` — memoised, computed purely from `m`'s parse (doc 05
    /// §8: no recursion into other modules, so no cycles).
    pub fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        if let Some(e) = self.exports_cache.borrow().get(&m.index()) {
            return e.clone();
        }
        let tree = self.modules[m.index() as usize].parse.tree();
        let exports = compute_exports(m, &tree, &mut self.defs.borrow_mut());
        let rc = Rc::new(exports);
        self.exports_cache
            .borrow_mut()
            .insert(m.index(), rc.clone());
        rc
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
