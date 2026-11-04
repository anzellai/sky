//! A deliberately restricted proof gate, not an effect-family heuristic.
//! Unsupported shapes receive the original encoder and reference execution.

use super::Ctx;
use base::DefId;
use hir::{Body, Expr, ExprId, Pattern, Res};

impl Ctx<'_> {
    pub(super) fn validated_auto_model_encoder(&self, expr: ExprId) -> bool {
        let Some(trusted) = self.trusted_auto_codec else {
            return false;
        };
        let Expr::Var(Res::Def(def)) = &self.body.exprs[expr] else {
            return false;
        };
        let Some(encoder) = self.defs.get(def) else {
            return false;
        };
        eligible(&encoder.body, trusted, &|d| {
            self.defs.get(&d).map(|e| &e.body)
        })
    }
}

fn call(body: &Body, mut id: ExprId) -> (ExprId, Vec<ExprId>) {
    let mut args = Vec::new();
    while let Expr::Call(head, tail) = &body.exprs[id] {
        args.splice(0..0, tail.iter().copied());
        id = *head;
    }
    (id, args)
}

fn eligible<'a>(
    body: &Body,
    trusted: (DefId, DefId),
    lookup: &impl Fn(DefId) -> Option<&'a Body>,
) -> bool {
    let [param] = body.params.as_slice() else {
        return false;
    };
    let Pattern::Var(local) = &body.pats[*param] else {
        return false;
    };
    let Some(root) = body.root else {
        return false;
    };
    let (head, args) = call(body, root);
    if !matches!(&body.exprs[head], Expr::Var(Res::Def(d)) if *d == trusted.0) || args.len() != 2 {
        return false;
    }
    if !matches!(&body.exprs[args[1]], Expr::Var(Res::Local(l)) if l == local) {
        return false;
    }
    let (auto, witnesses) = call(body, args[0]);
    if !matches!(&body.exprs[auto], Expr::Var(Res::Def(d)) if *d == trusted.1)
        || witnesses.len() != 1
    {
        return false;
    }
    let Expr::Var(Res::Def(blank)) = &body.exprs[witnesses[0]] else {
        return false;
    };
    let Some(blank_body) = lookup(*blank) else {
        return false;
    };
    let mut visiting = std::collections::HashSet::from([*blank]);
    let mut budget = 4096;
    blank_body.params.is_empty()
        && blank_body
            .root
            .is_some_and(|r| literal(blank_body, r, lookup, &mut visiting, &mut budget))
}

fn literal<'a>(
    body: &Body,
    id: ExprId,
    lookup: &impl Fn(DefId) -> Option<&'a Body>,
    visiting: &mut std::collections::HashSet<DefId>,
    budget: &mut usize,
) -> bool {
    base::ensure_stack(|| {
        if *budget == 0 {
            return false;
        }
        *budget -= 1;
        match &body.exprs[id] {
            Expr::Int(_) | Expr::Str(_) | Expr::Chr(_) | Expr::Bool(_) | Expr::Unit => true,
            Expr::Float(n) => n.is_finite(),
            Expr::List(xs) | Expr::Tuple(xs) => xs
                .iter()
                .all(|x| literal(body, *x, lookup, visiting, budget)),
            Expr::Record(fs) => fs
                .iter()
                .all(|(_, x)| literal(body, *x, lookup, visiting, budget)),
            Expr::Negate(x) => {
                matches!(&body.exprs[*x], Expr::Int(_) | Expr::Float(_))
                    && literal(body, *x, lookup, visiting, budget)
            }
            Expr::Var(Res::Ctor(c)) => c.arity == 0,
            Expr::Var(Res::Def(d)) => {
                if !visiting.insert(*d) {
                    return false;
                }
                let proven = lookup(*d).is_some_and(|alias| {
                    alias.params.is_empty()
                        && alias
                            .root
                            .is_some_and(|root| literal(alias, root, lookup, visiting, budget))
                });
                visiting.remove(d);
                proven
            }
            Expr::Call(_, _) => {
                let (head, args) = call(body, id);
                matches!(&body.exprs[head], Expr::Var(Res::Ctor(c)) if usize::from(c.arity) == args.len())
                    && args
                        .iter()
                        .all(|x| literal(body, *x, lookup, visiting, budget))
            }
            // CAF aliases are admitted only by recursively proving literal data.
            // Function calls, lets, effects and operators retain reference execution.
            _ => false,
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::{SkyDb, SourceDb};
    use std::collections::BTreeMap;

    fn check(blank: &str, encoder: &str, grant: bool) -> bool {
        let mut db = SourceDb::new();
        let codec = db.add_module(
            "Codec",
            syntax::parse(
                "module Codec exposing (..)\nauto x = x\ntoJson c m = m\n",
                FileId(0),
            ),
        );
        let source = format!("module Main exposing (..)\nimport Codec as C\nrecordAlias = {{ text = \"\" }}\nsecondAlias = recordAlias\nimpureAlias = helper ()\ncycleA = cycleB\ncycleB = cycleA\nblank =\n    {blank}\n\nencode m =\n    {encoder}\n");
        let main = db.add_module("Main", syntax::parse(&source, FileId(1)));
        let codec = db.resolve(codec);
        let resolved = db.resolve(main);
        let def = |r: &hir::ResolveResult, name: &str| {
            r.top_defs
                .iter()
                .find(|d| d.name.as_str() == name)
                .unwrap()
                .def
        };
        let trusted = (def(&codec, "toJson"), def(&codec, "auto"));
        let bodies: BTreeMap<_, _> = resolved.bodies.iter().map(|(d, b)| (*d, b)).collect();
        grant
            && eligible(bodies[&def(&resolved, "encode")], trusted, &|d| {
                bodies.get(&d).copied()
            })
    }

    #[test]
    fn accepts_only_direct_canonical_encoder_with_literal_blank() {
        assert!(check(
            "{ text = \"\", items = [], count = -1 }",
            "C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check(
            "{ text = \"\" }",
            "C.toJson (C.auto blank) m",
            false
        ));
        assert!(!check("helper ()", "C.toJson (C.auto blank) m", true));
        assert!(!check(
            "{ text = \"\" }",
            "C.toJson (C.auto blank) { m | text = \"changed\" }",
            true
        ));
        assert!(!check(
            "{ text = \"\" }",
            "let x = helper () in C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check("{ text = \"\" }", "C.toJson (custom blank) m", true));
        assert!(!check("{ text = \"\" }", "helper m", true));
        assert!(check("secondAlias", "C.toJson (C.auto blank) m", true));
        assert!(check(
            "[ secondAlias, secondAlias ]",
            "C.toJson (C.auto blank) m",
            true
        ));
        assert!(!check("impureAlias", "C.toJson (C.auto blank) m", true));
        assert!(!check("cycleA", "C.toJson (C.auto blank) m", true));
        let excessive = format!("[ {} ]", vec!["secondAlias"; 4096].join(", "));
        assert!(!check(&excessive, "C.toJson (C.auto blank) m", true));
    }
}
