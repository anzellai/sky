#![forbid(unsafe_code)]
//! `hir` — desugared, name-resolved high-level IR: imports, scopes, `DefId`
//! resolution, module items (doc 02, doc 05).
//!
//! M0 stub: the item/resolution vocabulary is seeded so downstream crates have
//! types to name. M2 fills in real resolution (explicit-alias-wins qualifiers,
//! E1001 collisions, Prelude-ctor shadowing).

use base::{DefId, ModuleId, Name, Span};

/// A resolved top-level item in a module (doc 05).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Item {
    pub def: DefId,
    pub name: Name,
    pub span: Span,
    pub kind: ItemKind,
}

/// The syntactic category of a resolved item. Exhaustive by design (L6) — no
/// catch-all arm; new kinds force a compile error at every match site.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum ItemKind {
    Value,
    TypeAlias,
    UnionType,
    Import,
}

/// The name-resolution result for one module: its items and how names bind.
#[derive(Clone, Debug, Default)]
pub struct ResolvedModule {
    pub items: Vec<Item>,
}

impl ResolvedModule {
    /// Resolve a `DefId` back to its item within this module.
    pub fn item(&self, def: DefId) -> Option<&Item> {
        self.items.iter().find(|i| i.def == def)
    }
}

/// Placeholder resolution entry point. M2 replaces this with a real
/// `resolve(ModuleId)` salsa query living in `skydb` over this crate's types.
pub fn resolve_stub(_module: ModuleId) -> ResolvedModule {
    ResolvedModule::default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;

    #[test]
    fn resolves_an_item_by_def() {
        let m = ResolvedModule {
            items: vec![Item {
                def: DefId(0),
                name: Name::new("main"),
                span: Span::new(FileId(0), 0, 4),
                kind: ItemKind::Value,
            }],
        };
        assert_eq!(m.item(DefId(0)).map(|i| i.name.as_str()), Some("main"));
        assert!(m.item(DefId(1)).is_none());
    }
}
