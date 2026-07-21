//! Interned identity — `DefId` allocation + the `Res` resolution enum (doc 05
//! §2). Collapses the Haskell `VarHome`/`CtorHome`/`TypeHome` triple into one
//! `DefId` space plus an exhaustive resolution enum (L6). `Res::Error` is a
//! first-class recovery value (L7) — never a silent `VarLocal` fall-through.

use base::Interner;
use base::{DefId, ModuleId, Name};

/// What a `DefId` names. Part of the interner key so a value and a type of the
/// same name in the same module get distinct ids (doc 05 §2).
#[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub enum DefKind {
    Value,
    Ctor,
    TypeCon,
    TypeAlias,
}

/// A local binding id, unique within a resolved body (lambda/let/case/param).
/// `Res::Local` carries this — today's `VarLocal` is payload-free
/// (Environment.hs:38); we make the binding site explicit (doc 05 §2).
#[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub struct LocalId(pub u32);

/// The resolution outcome a resolved reference points at — the union of the
/// three Haskell `*Home` types, made exhaustive (doc 05 §2).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Res {
    /// A lambda/let/case-pattern binding, by its body-local id.
    Local(LocalId),
    /// A top-level value defined in some module (this or an imported one).
    Def(DefId),
    /// A stdlib kernel function `(kernel-module, function)` — resolves to a
    /// runtime symbol, no Sky-source definition site (doc 05 §9).
    Kernel { module: Name, func: Name },
    /// A data constructor.
    Ctor(CtorRef),
    /// A reference into a Go FFI package (`sky add`). The FFI surface is a
    /// doc-09 / M3 dependency and is not built yet, so these resolve leniently
    /// to a package + name pair rather than a `DefId`. Never a resolver bug —
    /// tracked as class-(b) by the gate harness.
    Foreign { package: Name, name: Name },
    /// Resolution failed. Resolution continues; a diagnostic was emitted (L7).
    Error,
}

/// A resolved data-constructor reference (successor to `CtorHome`,
/// Environment.hs:54). The full union + annotation the Haskell `CtorHome`
/// inlined are recovered on demand from the owning type, not cloned here.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct CtorRef {
    /// The constructor's own `DefId`.
    pub def: DefId,
    /// The union type it belongs to.
    pub type_: DefId,
    /// Constructor index within the union.
    pub index: u16,
    /// Number of arguments.
    pub arity: u16,
}

/// A resolved type-constructor reference (successor to `TypeHome`).
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub struct TypeRes {
    pub con: DefId,
    pub arity: u16,
}

/// Where a `DefId` came from — recoverable for goto-def / debugging.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct DefLoc {
    pub module: ModuleId,
    pub name: Name,
    pub kind: DefKind,
}

/// Append-only `DefId` interner keyed by `(module, name, kind)` — the
/// register-on-first-mention pattern (doc 05 §2). Deterministic in allocation
/// order (L4).
#[derive(Clone, Debug)]
pub struct DefTable {
    inner: Interner<(u32, String, DefKind)>,
}

impl Default for DefTable {
    fn default() -> Self {
        DefTable::new()
    }
}

impl DefTable {
    pub fn new() -> Self {
        DefTable {
            inner: Interner::new(),
        }
    }

    /// Intern a definition, returning its stable `DefId`. Idempotent per key.
    pub fn intern(&mut self, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
        let id = self
            .inner
            .intern((module.index(), name.as_str().to_string(), kind));
        DefId(id)
    }

    /// Recover a definition's location from its id.
    pub fn loc(&self, def: DefId) -> Option<DefLoc> {
        self.inner.lookup(def.index()).map(|(m, n, k)| DefLoc {
            module: ModuleId(*m),
            name: Name::new(n),
            kind: *k,
        })
    }
}
