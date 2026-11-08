//! Immutable HIR ownership for World passes that retain targets across scans.
//! A target owns its exact resolved-module snapshot instead of copying arenas.
use base::DefId;
use hir::{Body, ResolveResult};
use std::sync::Arc;

pub(crate) struct RetainedBody {
    owner: Arc<ResolveResult>,
    def: DefId,
}

impl RetainedBody {
    pub(crate) fn new(owner: &Arc<ResolveResult>, def: DefId) -> Option<Self> {
        owner.bodies.get(&def)?;
        Some(Self {
            owner: Arc::clone(owner),
            def,
        })
    }
}

impl std::ops::Deref for RetainedBody {
    type Target = Body;
    fn deref(&self) -> &Body {
        &self.owner.bodies[&self.def]
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use hir::{SkyDb, SourceDb};

    #[test]
    fn world_targets_share_body_and_expression_arenas_across_owner_drop_and_revision() {
        let mut db = SourceDb::new();
        let source = "module Main exposing (..)\nanyHelper : any -> any\nanyHelper x = let pair = (x, [x, x]) in x\nboundHelper x = { value = x }\nmain = boundHelper 1\n";
        let parsed = syntax::parse(source, base::FileId(0));
        assert!(parsed.errors().is_empty() && parsed.error_node_count() == 0);
        let module = db.add_module("Main", parsed);
        let resolved = db.resolve(module);
        let names: std::collections::BTreeSet<_> = resolved
            .top_defs
            .iter()
            .map(|top| top.name.as_str().to_string())
            .collect();
        assert_eq!(
            names,
            std::collections::BTreeSet::from([
                "anyHelper".to_string(),
                "boundHelper".to_string(),
                "main".to_string(),
            ])
        );
        // The annotation and value each append a TopDef but use the same
        // DefId; the value replaces its annotation-only body. World targets
        // iterate the unique bodies map, not those declaration records.
        let annotated: Vec<_> = resolved
            .top_defs
            .iter()
            .filter(|top| top.name.as_str() == "anyHelper")
            .map(|top| top.def)
            .collect();
        assert_eq!(annotated.len(), 2);
        assert_eq!(annotated[0], annotated[1]);
        let targets: Vec<_> = resolved
            .bodies
            .keys()
            .map(|def| RetainedBody::new(&resolved, *def).unwrap())
            .collect();
        assert_eq!(targets.len(), 3);
        assert!(RetainedBody::new(&resolved, DefId(u32::MAX)).is_none());
        let mut addresses = Vec::new();
        for target in &targets {
            let original = &resolved.bodies[&target.def];
            assert!(std::ptr::eq(&**target, original));
            assert_eq!(target.root, original.root);
            let expressions: Vec<_> = target
                .exprs
                .iter()
                .map(|(id, expr)| {
                    assert!(std::ptr::eq(expr, &original.exprs[id]));
                    (id, expr as *const hir::Expr)
                })
                .collect();
            addresses.push((&**target as *const Body, target.root, expressions));
        }
        drop(resolved);
        let replacement = syntax::parse(
            "module Main exposing (..)\nanyHelper x = 11\nboundHelper x = 12\nmain = 13\n",
            base::FileId(0),
        );
        assert!(replacement.errors().is_empty() && replacement.error_node_count() == 0);
        db.add_module("Main", replacement);
        let next = db.resolve(module);
        for (target, (body_address, root, expressions)) in targets.iter().zip(addresses) {
            assert!(!Arc::ptr_eq(&target.owner, &next));
            assert_eq!(&**target as *const Body, body_address);
            assert_eq!(target.root, root);
            for (id, address) in expressions {
                assert_eq!(&target.exprs[id] as *const hir::Expr, address);
            }
        }
    }
}
