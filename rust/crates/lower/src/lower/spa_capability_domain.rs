//! Whole reachable-program entitlement for private immutable-value capabilities.
//! Called only after canonical stdlib source provenance was granted. `defs` is
//! the existing reference closure incl lambdas, let functions, subscriptions,
//! update/Cmd continuations and device/config roots, NOT just view references.
use super::*;

pub(super) fn permitted(
    db: &dyn SkyDb,
    defs: &BTreeMap<DefId, DefEntry>,
    aliases: &HashMap<DefId, String>,
    trusted_modules: &BTreeSet<ModuleId>,
) -> bool {
    for (d, e) in defs {
        // Even a canonical module's actual direct Go import can carry a mutable
        // returned container. Conservative until that exact boundary is proved.
        if e.body
            .exprs
            .iter()
            .any(|(_, x)| matches!(x, Expr::Var(Res::Foreign { .. })))
        {
            return false;
        }
        let canonical = db
            .def_loc(*d)
            .is_some_and(|loc| trusted_modules.contains(&loc.module));
        if canonical {
            continue;
        }
        if let Some(raw) = aliases.get(d) {
            // The literal target, NEVER helper spelling/config markers, is the
            // contract. These immutable config setters do not publish models.
            if matches!(
                alias_go_name(raw).as_str(),
                "rt.Spa_withGeneratedModelEncoder" | "rt.Spa_withPreparedModelEncoder"
            ) {
                continue;
            }
            return false;
        }
        for (_, x) in e.body.exprs.iter() {
            match x {
                Expr::Var(Res::Kernel { module, .. }) if module.as_str() == "Ffi" => return false,
                Expr::Var(Res::Def(callee))
                    if defs.get(callee).is_some_and(|e| e.module_name == "Sky.Ffi") =>
                {
                    return false
                }
                _ => {}
            }
        }
    }
    true
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::SourceDb;
    fn inventory(source: &str) -> (SourceDb, BTreeMap<DefId, DefEntry>, HashMap<DefId, String>) {
        let mut db = SourceDb::new();
        let parsed = syntax::parse(source, FileId(0));
        assert!(
            parsed.errors().is_empty() && parsed.error_node_count() == 0,
            "domain fixture must parse cleanly"
        );
        let m = db.add_module("Main", parsed);
        let r = db.resolve(m);
        let defs: BTreeMap<_, _> = r
            .top_defs
            .iter()
            .filter_map(|d| {
                r.bodies.get(&d.def).map(|_| {
                    (
                        d.def,
                        DefEntry {
                            module_name: "Main".into(),
                            name: d.name.as_str().into(),
                            body: DefBody {
                                owner: std::sync::Arc::clone(&r),
                                def: d.def,
                            },
                            types: BodyTypes::default(),
                            task_discards: BTreeSet::new(),
                            numeric: std::rc::Rc::new(ty::numeric::NumericElaboration::default()),
                            sig: None,
                        },
                    )
                })
            })
            .collect();
        let aliases = defs
            .iter()
            .filter_map(|(d, e)| detect_kernel_alias(&e.body).map(|s| (*d, s)))
            .collect();
        (db, defs, aliases)
    }
    #[test]
    fn grants_only_literal_audited_private_alias_targets() {
        for (target, allowed) in [
            ("Spa_withGeneratedModelEncoder", true),
            ("Spa_withPreparedModelEncoder", true),
            ("List_reverse", false),
            ("Sync_ref", false),
        ] {
            let (db,defs,aliases)=inventory(&format!("module Main exposing (..)\nimport Sky.Ffi as Ffi\nmain = Ffi.kernel \"{target}\"\n"));
            assert!(
                !aliases.is_empty(),
                "test must exercise actual detected kernel alias"
            );
            assert_eq!(
                permitted(&db, &defs, &aliases, &BTreeSet::new()),
                allowed,
                "{target}"
            );
        }
    }
    #[test]
    fn denies_foreign_refs_even_inside_latent_body_and_other_roots() {
        let (db, mut defs, aliases) = inventory("module Main exposing (..)\nmain = 1\n");
        let e = defs.values_mut().next().unwrap();
        let foreign = e.body.exprs.alloc(Expr::Var(Res::Foreign {
            package: Name::new("foreign/pkg"),
            name: Name::new("reuseBuffer"),
        }));
        let lambda = e.body.exprs.alloc(Expr::Lambda {
            params: vec![],
            body: foreign,
        });
        e.body.root = Some(lambda);
        assert!(!permitted(&db, &defs, &aliases, &BTreeSet::new()));
    }
    #[test]
    fn namespace_spelling_does_not_grant_canonical_alias_trust() {
        let (db,mut defs,aliases)=inventory("module Main exposing (..)\nimport Sky.Ffi as Ffi\nmain = Ffi.kernel \"List_reverse\"\n");
        for e in defs.values_mut() {
            e.module_name = "Std.Fake".into();
        }
        assert!(!permitted(&db, &defs, &aliases, &BTreeSet::new()));
    }
}
