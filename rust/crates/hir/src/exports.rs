//! `module_exports` — what a dependency publishes (doc 05 §7). Successor to
//! `DepInfo` / `filterDepByExports`. Computed **purely from the module's own
//! parse + its `exposing` clause** — it never recurses into other modules, so
//! the cross-module query graph has no cycles and needs no 5-round fixpoint
//! (doc 05 §8).

use crate::cst::{self, CtorExposure, ExposedItem};
use crate::ids::{DefKind, DefTable};
use base::{DefId, ModuleId, Name};
use syntax::ast::{self, AstNode};
use syntax::SyntaxKind;

/// An exported data constructor.
#[derive(Clone, Debug)]
pub struct ExportedCtor {
    pub name: Name,
    pub def: DefId,
    pub type_: DefId,
    pub index: u16,
    pub arity: u16,
}

/// An exported union type + its exposed constructors.
#[derive(Clone, Debug)]
pub struct ExportedUnion {
    pub name: Name,
    pub def: DefId,
    pub arity: u16,
    pub ctors: Vec<ExportedCtor>,
}

/// An exported type alias.
#[derive(Clone, Debug)]
pub struct ExportedAlias {
    pub name: Name,
    pub def: DefId,
    pub arity: u16,
    /// True when the alias body is a record — its name doubles as a positional
    /// constructor value (C9).
    pub is_record: bool,
}

/// Everything a module publishes, already narrowed by its `exposing` clause.
#[derive(Clone, Debug)]
pub struct ModuleExports {
    pub module: ModuleId,
    /// Exported top-level value names (functions/values + record-alias ctors).
    pub values: Vec<(Name, DefId)>,
    pub unions: Vec<ExportedUnion>,
    pub aliases: Vec<ExportedAlias>,
}

impl ModuleExports {
    fn empty(module: ModuleId) -> Self {
        ModuleExports {
            module,
            values: Vec::new(),
            unions: Vec::new(),
            aliases: Vec::new(),
        }
    }
}

impl ModuleExports {
    pub fn value(&self, name: &str) -> Option<DefId> {
        self.values
            .iter()
            .find(|(n, _)| n.as_str() == name)
            .map(|(_, d)| *d)
    }
    pub fn ctor(&self, name: &str) -> Option<(&ExportedUnion, &ExportedCtor)> {
        self.unions
            .iter()
            .find_map(|u| u.ctors.iter().find(|c| c.name.as_str() == name).map(|c| (u, c)))
    }
    pub fn type_(&self, name: &str) -> Option<(DefId, u16)> {
        if let Some(u) = self.unions.iter().find(|u| u.name.as_str() == name) {
            return Some((u.def, u.arity));
        }
        self.aliases
            .iter()
            .find(|a| a.name.as_str() == name)
            .map(|a| (a.def, a.arity))
    }
}

/// A locally-declared union, before export filtering.
struct LocalUnion {
    name: String,
    arity: u16,
    ctors: Vec<(String, u16)>, // (name, arity), index = position
}

struct LocalAlias {
    name: String,
    arity: u16,
    is_record: bool,
}

/// Compute a module's exports from its parsed tree.
pub fn compute_exports(
    module: ModuleId,
    tree: &ast::SourceFile,
    defs: &mut DefTable,
) -> ModuleExports {
    // ---- collect local declarations ----
    let mut local_values: Vec<String> = Vec::new();
    let mut local_unions: Vec<LocalUnion> = Vec::new();
    let mut local_aliases: Vec<LocalAlias> = Vec::new();

    for decl in tree.decls() {
        match decl {
            ast::Decl::Value(v) => {
                if let Some(n) = v.name() {
                    if n.kind() == SyntaxKind::LowerIdent {
                        local_values.push(n.text().to_string());
                    }
                }
            }
            ast::Decl::Union(u) => {
                let name = match u.name() {
                    Some(t) => t.text().to_string(),
                    None => continue,
                };
                let arity = cst::decl_type_vars(u.syntax()).len() as u16;
                let ctors = u
                    .variants()
                    .iter()
                    .filter_map(|var| {
                        let cn = var.name()?.text().to_string();
                        let cargs = cst::child_types(var.syntax()).len() as u16;
                        Some((cn, cargs))
                    })
                    .collect();
                local_unions.push(LocalUnion { name, arity, ctors });
            }
            ast::Decl::Alias(a) => {
                let name = match a.name() {
                    Some(t) => t.text().to_string(),
                    None => continue,
                };
                let arity = cst::decl_type_vars(a.syntax()).len() as u16;
                let is_record = a
                    .ty()
                    .map(|t| matches!(t, ast::Type::Record(_)))
                    .unwrap_or(false);
                local_aliases.push(LocalAlias {
                    name,
                    arity,
                    is_record,
                });
            }
            ast::Decl::TypeAnno(_) | ast::Decl::Foreign(_) => {}
        }
    }

    // ---- read the exposing clause (no header ⇒ expose all) ----
    let clause = tree
        .module_header()
        .and_then(|h| cst::header_exposing(&h))
        .map(|n| cst::read_exposing(&n));
    let expose_all = clause.as_ref().map(|c| c.all).unwrap_or(true);

    let mut exports = ModuleExports::empty(module);

    // Helper to mint ids.
    let value_def = |defs: &mut DefTable, n: &str| defs.intern(module, &Name::new(n), DefKind::Value);

    // ---- unions ----
    for u in &local_unions {
        let exposed_ctors: Option<CtorExposure> = if expose_all {
            Some(CtorExposure::All)
        } else {
            clause.as_ref().and_then(|c| {
                c.items.iter().find_map(|it| match it {
                    ExposedItem::Type { name, ctors } if *name == u.name => Some(ctors.clone()),
                    _ => None,
                })
            })
        };
        let Some(ctor_exp) = exposed_ctors else {
            continue; // type not exposed at all
        };
        let type_def = defs.intern(module, &Name::new(&u.name), DefKind::TypeCon);
        let mut ctors = Vec::new();
        for (i, (cn, carity)) in u.ctors.iter().enumerate() {
            let keep = match &ctor_exp {
                CtorExposure::None => false,
                CtorExposure::All => true,
                CtorExposure::Some(list) => list.iter().any(|x| x == cn),
            };
            if !keep {
                continue;
            }
            ctors.push(ExportedCtor {
                name: Name::new(cn),
                def: defs.intern(module, &Name::new(cn), DefKind::Ctor),
                type_: type_def,
                index: i as u16,
                arity: *carity,
            });
        }
        exports.unions.push(ExportedUnion {
            name: Name::new(&u.name),
            def: type_def,
            arity: u.arity,
            ctors,
        });
    }

    // ---- aliases ----
    for a in &local_aliases {
        let exposed = expose_all
            || clause.as_ref().is_some_and(|c| {
                c.items.iter().any(|it| match it {
                    ExposedItem::Type { name, .. } => *name == a.name,
                    ExposedItem::Value(v) => *v == a.name,
                    _ => false,
                })
            });
        if !exposed {
            continue;
        }
        let def = defs.intern(module, &Name::new(&a.name), DefKind::TypeAlias);
        exports.aliases.push(ExportedAlias {
            name: Name::new(&a.name),
            def,
            arity: a.arity,
            is_record: a.is_record,
        });
        // record alias name doubles as a positional constructor value (C9).
        if a.is_record {
            let vd = value_def(defs, &a.name);
            exports.values.push((Name::new(&a.name), vd));
        }
    }

    // ---- values ----
    for v in &local_values {
        let exposed = expose_all
            || clause.as_ref().is_some_and(|c| {
                c.items
                    .iter()
                    .any(|it| matches!(it, ExposedItem::Value(x) if x == v))
            });
        if exposed {
            let vd = value_def(defs, v);
            exports.values.push((Name::new(v), vd));
        }
    }

    // ---- re-exports (lenient): explicitly-listed names not declared locally ----
    // A module may re-expose an imported name. We don't chase the origin here;
    // we publish the name so an importer resolves it (avoids a false class-(a)).
    if let (false, Some(c)) = (expose_all, clause.as_ref()) {
        for it in &c.items {
            if let ExposedItem::Value(v) = it {
                let known = exports.values.iter().any(|(n, _)| n.as_str() == v);
                if !known {
                    let vd = value_def(defs, v);
                    exports.values.push((Name::new(v), vd));
                }
            }
        }
    }

    exports
}
