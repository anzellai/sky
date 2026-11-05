//! Isolated source proof, deliberately conservative. Canonical source identity
//! grants and a closed immutable input/output capability are separate required
//! premises. Neither FfiTrust nor callPure spelling grants purity here.
use base::DefId;
use hir::{Body, Expr, ExprId, LocalDef, LocalId, Pattern, Res};
use std::borrow::Borrow;
use std::collections::{BTreeMap, BTreeSet};

pub struct PureIntrinsic {
    pub arity: usize,
}
pub struct SynchronousHof {
    pub arity: usize,
    pub callback_slot: usize,
    pub callback_arity: usize,
}
pub struct PurityGrant {
    // Exact canonical DefIDs audited TOTAL and deterministic on closed values.
    // Exclude enum-name registry reads, mutable factories, FFI, Task execution,
    // partial arithmetic and comparison/show over foreign values initially.
    pub intrinsic: BTreeMap<DefId, PureIntrinsic>,
    pub hofs: BTreeMap<DefId, SynchronousHof>,
    // Exact resolved total kernels only; driver owns this set.
    pub kernels: BTreeSet<(String, String, usize)>,
}
pub struct PureCallback {
    pub def: DefId,
    pub bound: Vec<ExprId>,
    #[cfg(test)]
    pub remaining: usize,
}
fn spine(body: &Body, mut id: ExprId) -> (ExprId, Vec<ExprId>) {
    let mut args = Vec::new();
    loop {
        match &body.exprs[id] {
            Expr::Call(head, xs) => {
                args.splice(0..0, xs.iter().copied());
                id = *head;
            }
            Expr::Binop {
                op,
                res: Res::Kernel { module, func },
                lhs,
                rhs,
            } if module.as_str() == "Basics" && op.as_str() == "|>" && func.as_str() == "apR" => {
                args.insert(0, *lhs);
                id = *rhs;
            }
            Expr::Binop {
                op,
                res: Res::Kernel { module, func },
                lhs,
                rhs,
            } if module.as_str() == "Basics" && op.as_str() == "<|" && func.as_str() == "apL" => {
                args.insert(0, *rhs);
                id = *lhs;
            }
            _ => break,
        }
    }
    (id, args)
}
pub fn pure_callback<B: Borrow<Body>>(
    bodies: &BTreeMap<DefId, B>,
    caller: &Body,
    expression: ExprId,
    remaining: usize,
    grants: &PurityGrant,
) -> Option<PureCallback> {
    let (head, bound) = spine(caller, expression);
    let Expr::Var(Res::Def(def)) = &caller.exprs[head] else {
        return None;
    };
    if Borrow::<Body>::borrow(bodies.get(def)?).params.len() != bound.len() + remaining {
        return None;
    }
    let mut proof = Proof {
        bodies,
        grants,
        checking: BTreeSet::new(),
        checked: BTreeSet::new(),
        nodes_left: 65536,
        locals: BTreeMap::new(),
        local_checking: BTreeSet::new(),
        descent: None,
        denied: BTreeSet::new(),
    };
    for a in &bound {
        if !proof.expr(caller, *a) {
            return None;
        }
    }
    if !proof.def(*def) {
        return None;
    }
    Some(PureCallback {
        def: *def,
        bound,
        #[cfg(test)]
        remaining,
    })
}
#[cfg(test)]
pub(super) fn rejected_definitions<B: Borrow<Body>>(
    bodies: &BTreeMap<DefId, B>,
    caller: &Body,
    expression: ExprId,
    remaining: usize,
    grants: &PurityGrant,
) -> BTreeSet<DefId> {
    let (head, bound) = spine(caller, expression);
    let Expr::Var(Res::Def(def)) = &caller.exprs[head] else {
        return BTreeSet::new();
    };
    if !bodies
        .get(def)
        .is_some_and(|b| Borrow::<Body>::borrow(b).params.len() == bound.len() + remaining)
    {
        return BTreeSet::from([*def]);
    }
    let mut proof = Proof {
        bodies,
        grants,
        checking: BTreeSet::new(),
        checked: BTreeSet::new(),
        nodes_left: 65536,
        locals: BTreeMap::new(),
        local_checking: BTreeSet::new(),
        descent: None,
        denied: BTreeSet::new(),
    };
    for a in bound {
        if !proof.expr(caller, a) {
            return proof.denied;
        }
    }
    proof.def(*def);
    proof.denied
}
struct Proof<'a, B: Borrow<Body>> {
    bodies: &'a BTreeMap<DefId, B>,
    grants: &'a PurityGrant,
    checking: BTreeSet<DefId>,
    checked: BTreeSet<DefId>,
    nodes_left: usize,
    locals: BTreeMap<LocalId, LocalDef>,
    local_checking: BTreeSet<LocalId>,
    descent: Option<Descent>,
    denied: BTreeSet<DefId>,
}
#[derive(Clone)]
struct Descent {
    function: LocalId,
    parameter: LocalId,
    tails: BTreeSet<LocalId>,
}
impl<B: Borrow<Body>> Proof<'_, B> {
    fn def(&mut self, d: DefId) -> bool {
        if self.nodes_left == 0 {
            return false;
        }
        self.nodes_left -= 1;
        base::ensure_stack(|| self.def_inner(d))
    }
    fn def_inner(&mut self, d: DefId) -> bool {
        if self.checked.contains(&d) {
            return true;
        }
        if !self.checking.insert(d) {
            return false;
        } // no recursion theorem
        let saved_locals = std::mem::take(&mut self.locals);
        let saved_checking = std::mem::take(&mut self.local_checking);
        let saved_descent = self.descent.take();
        let good = self
            .bodies
            .get(&d)
            .map(Borrow::<Body>::borrow)
            .and_then(|b| b.root.map(|r| (b, r)))
            .is_some_and(|(b, r)| self.expr(b, r));
        self.locals = saved_locals;
        self.local_checking = saved_checking;
        self.descent = saved_descent;
        self.checking.remove(&d);
        if good {
            self.checked.insert(d);
        } else {
            self.denied.insert(d);
        }
        good
    }
    fn application(&mut self, d: DefId, count: usize) -> bool {
        if self.nodes_left == 0 {
            return false;
        }
        self.nodes_left -= 1;
        base::ensure_stack(|| self.application_inner(d, count))
    }
    fn application_inner(&mut self, d: DefId, count: usize) -> bool {
        if let Some(p) = self.grants.intrinsic.get(&d) {
            return p.arity == count;
        }
        let Some(b) = self.bodies.get(&d).map(Borrow::<Body>::borrow) else {
            return false;
        };
        if count < b.params.len() {
            return true;
        } // only closure construction
        if count == b.params.len() {
            return self.def(d);
        }
        // Point-free pure aliases (e.g. Font.color = Ui.fontColor). Never
        // pretend a nonliteral CAF returning a function has a fixed callee.
        if !b.params.is_empty() || !self.checking.insert(d) {
            return false;
        }
        let good = match b.root.map(|r| &b.exprs[r]) {
            Some(Expr::Var(Res::Def(next))) => self.application(*next, count),
            Some(Expr::Var(Res::Kernel { module, func })) => {
                self.grants
                    .kernels
                    .contains(&(module.as_str().into(), func.as_str().into(), count))
            }
            _ => false,
        };
        self.checking.remove(&d);
        good
    }
    fn local_application(
        &mut self,
        b: &Body,
        local: LocalId,
        args: Option<&[ExprId]>,
        arity: usize,
    ) -> bool {
        base::ensure_stack(|| self.local_application_inner(b, local, args, arity))
    }
    fn local_application_inner(
        &mut self,
        b: &Body,
        local: LocalId,
        args: Option<&[ExprId]>,
        arity: usize,
    ) -> bool {
        let Some(def) = self.locals.get(&local).cloned() else {
            return false;
        };
        if arity < def.params.len() {
            return true;
        } // closure construction only
        if arity != def.params.len() {
            return false;
        }
        if self.local_checking.contains(&local) {
            // The ONLY recursive edge admitted is an exact actual argument
            // bound to a strict cons tail of this invocation's own parameter.
            return self.descent.as_ref().is_some_and(|d| {
                d.function == local && args.is_some_and(|xs| {
                    xs.len() == 1
                        && matches!(&b.exprs[xs[0]],Expr::Var(Res::Local(t)) if d.tails.contains(t))
                })
            });
        }
        self.local_checking.insert(local);
        let saved = self.descent.take();
        self.descent = match def.params.as_slice() {
            [p] => match (&b.pats[*p], &b.exprs[def.body]) {
                (Pattern::Var(parameter), Expr::Case { subject, .. }) if matches!(&b.exprs[*subject],Expr::Var(Res::Local(x)) if x==parameter) => {
                    Some(Descent {
                        function: local,
                        parameter: *parameter,
                        tails: BTreeSet::new(),
                    })
                }
                _ => None,
            },
            _ => None,
        };
        let good = self.expr(b, def.body);
        self.descent = saved;
        self.local_checking.remove(&local);
        good
    }
    fn callback(&mut self, b: &Body, id: ExprId, arity: usize) -> bool {
        base::ensure_stack(|| self.callback_inner(b, id, arity))
    }
    fn callback_inner(&mut self, b: &Body, id: ExprId, arity: usize) -> bool {
        let (head, args) = spine(b, id);
        if !args.iter().all(|a| self.expr(b, *a)) {
            return false;
        }
        match &b.exprs[head] {
            Expr::Lambda { params, body } if args.is_empty() && params.len() == arity => {
                self.expr(b, *body)
            }
            Expr::Var(Res::Def(d)) => {
                if let Some(p) = self.grants.intrinsic.get(d) {
                    return p.arity == args.len() + arity;
                }
                self.application(*d, args.len() + arity)
            }
            Expr::Var(Res::Local(f)) => self.local_application(b, *f, None, args.len() + arity),
            Expr::Accessor(_) if args.is_empty() && arity == 1 => true,
            _ => false,
        }
    }
    fn irrefutable_tuple_binding(&mut self, b: &Body, root: hir::PatId) -> bool {
        // Root Unit/Anything are task-forcing discards in lower_let_def. Only
        // actual tuple destructures enter the non-forcing pattern path.
        if !matches!(&b.pats[root], Pattern::Tuple(_)) {
            return false;
        }
        let mut pending = vec![root];
        while let Some(pat) = pending.pop() {
            if self.nodes_left == 0 {
                return false;
            }
            self.nodes_left -= 1;
            match &b.pats[pat] {
                Pattern::Var(_) | Pattern::Unit | Pattern::Anything => {}
                Pattern::Tuple(parts) if parts.len() >= 2 => pending.extend(parts.iter().copied()),
                // Only nested Anything is safe: lower_let_def materialises a
                // tuple then pattern_test drops that slot without forcing it.
                // Aliases, records and every refutable pattern stay excluded.
                _ => return false,
            }
        }
        true
    }
    fn expr(&mut self, b: &Body, id: ExprId) -> bool {
        if self.nodes_left == 0 {
            return false;
        }
        self.nodes_left -= 1;
        base::ensure_stack(|| self.expr_inner(b, id))
    }
    fn expr_inner(&mut self, b: &Body, id: ExprId) -> bool {
        match &b.exprs[id] {
            Expr::Error | Expr::Var(Res::Error) | Expr::Var(Res::Foreign { .. }) => false,
            Expr::Call(..)
            | Expr::Binop {
                res: Res::Kernel { .. },
                ..
            } if {
                let (head, _) = spine(b, id);
                head != id
            } =>
            {
                let (head, args) = spine(b, id);
                // Arguments are strict and checked even for constructors/HOFs.
                if !args.iter().all(|a| self.expr(b, *a)) {
                    return false;
                }
                match &b.exprs[head] {
                    Expr::Var(Res::Def(d)) => {
                        if let Some(h) = self.grants.hofs.get(d) {
                            return args.len() == h.arity
                                && h.callback_slot < args.len()
                                && self.callback(b, args[h.callback_slot], h.callback_arity);
                        }
                        if let Some(p) = self.grants.intrinsic.get(d) {
                            return args.len() <= p.arity;
                        }
                        self.application(*d, args.len())
                    }
                    Expr::Var(Res::Kernel { module, func }) => self.grants.kernels.contains(&(
                        module.as_str().into(),
                        func.as_str().into(),
                        args.len(),
                    )),
                    Expr::Var(Res::Ctor(_)) => true,
                    Expr::Var(Res::Local(f)) => {
                        self.local_application(b, *f, Some(&args), args.len())
                    }
                    _ => false, // local/unknown callable, callPure and latent effects
                }
            }
            Expr::Var(Res::Def(d)) => {
                if self.grants.intrinsic.contains_key(d) || self.grants.hofs.contains_key(d) {
                    return true;
                }
                let Some(body) = self.bodies.get(d).map(Borrow::<Body>::borrow) else {
                    return false;
                };
                // A function value does not run its body. CAF evaluation does.
                !body.params.is_empty() || self.def(*d)
            }
            Expr::Var(Res::Kernel { .. }) => false,
            Expr::Var(_) => true,
            Expr::Lambda { .. } => true, // latent body only run via audited HOF
            Expr::Binop {
                res: Res::Kernel { module, func },
                lhs,
                rhs,
                ..
            } => {
                self.grants
                    .kernels
                    .contains(&(module.as_str().into(), func.as_str().into(), 2))
                    && self.expr(b, *lhs)
                    && self.expr(b, *rhs)
            }
            Expr::Binop { .. } => false,
            Expr::Let { defs, body } => {
                let saved = self.locals.clone();
                for d in defs {
                    if !d.params.is_empty() && d.binders.len() == 1 {
                        self.locals.insert(d.binders[0].1, d.clone());
                    }
                }
                let good = defs.iter().all(|d| {
                    if !d.binders.is_empty() {
                        !d.params.is_empty() || self.expr(b, d.body)
                    } else {
                        d.params.is_empty()
                            && d.pat.is_some_and(|p| self.irrefutable_tuple_binding(b, p))
                            && self.expr(b, d.body)
                    }
                }) && self.expr(b, *body);
                self.locals = saved;
                good
            }
            Expr::Negate(x) | Expr::Access(x, _) => self.expr(b, *x),
            Expr::List(xs) | Expr::Tuple(xs) => xs.iter().all(|x| self.expr(b, *x)),
            Expr::Record(fs) => fs.iter().all(|(_, x)| self.expr(b, *x)),
            Expr::Update { base, fields } => {
                self.expr(b, *base) && fields.iter().all(|(_, x)| self.expr(b, *x))
            }
            Expr::If { arms, els } => {
                arms.iter()
                    .all(|(a, c)| self.expr(b, *a) && self.expr(b, *c))
                    && self.expr(b, *els)
            }
            Expr::Case { subject, branches } => {
                if !self.expr(b, *subject) {
                    return false;
                }
                let saved = self.descent.clone();
                let descending=saved.as_ref().is_some_and(|d|matches!(&b.exprs[*subject],Expr::Var(Res::Local(x)) if *x==d.parameter||d.tails.contains(x)));
                let mut good = true;
                for branch in branches {
                    self.descent = saved.clone();
                    if descending {
                        if let Pattern::Cons(_, tail) = &b.pats[branch.pat] {
                            if let Pattern::Var(id) = &b.pats[*tail] {
                                if let Some(d) = &mut self.descent {
                                    d.tails.insert(*id);
                                }
                            }
                        }
                    }
                    if !self.expr(b, branch.body) {
                        good = false;
                        break;
                    }
                }
                self.descent = saved;
                good
            }
            _ => true,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::{SkyDb, SourceDb};
    // Every existing purity case runs both storage forms, including denied
    // effects/recursion, extracted callbacks, and exhausted node budgets.
    fn pure_callback(
        bodies: &BTreeMap<DefId, Body>,
        caller: &Body,
        expression: ExprId,
        remaining: usize,
        grants: &PurityGrant,
    ) -> Option<PureCallback> {
        let borrowed: BTreeMap<_, _> = bodies.iter().map(|(d, body)| (*d, body)).collect();
        let owned_result = super::pure_callback(bodies, caller, expression, remaining, grants);
        let borrowed_result =
            super::pure_callback(&borrowed, caller, expression, remaining, grants);
        let signature = |result: &Option<PureCallback>| {
            result
                .as_ref()
                .map(|p| (p.def, p.bound.clone(), p.remaining))
        };
        assert_eq!(
            signature(&owned_result),
            signature(&borrowed_result),
            "owned and borrowed HIR must yield identical callback and capture ExprIds"
        );
        assert_eq!(
            super::rejected_definitions(bodies, caller, expression, remaining, grants),
            super::rejected_definitions(&borrowed, caller, expression, remaining, grants),
            "owned and borrowed HIR must identify the same rejected definitions"
        );
        owned_result
    }
    fn fixture(extra: &str, entry: &str) -> (BTreeMap<DefId, Body>, BTreeMap<String, DefId>) {
        let mut db = SourceDb::new();
        let text=format!("module Main exposing (..)\nindexedMap f xs = xs\nrow saved index value = {{ name = value, saved = saved, index = index }}\n{extra}\nentry = {entry}\n");
        let parsed = syntax::parse(&text, FileId(0));
        assert!(
            parsed.errors().is_empty() && parsed.error_node_count() == 0,
            "purity fixture must parse cleanly"
        );
        let id = db.add_module("Main", parsed);
        let resolved = db.resolve(id);
        let names = resolved
            .top_defs
            .iter()
            .map(|d| (d.name.as_str().to_string(), d.def))
            .collect();
        (
            resolved
                .bodies
                .iter()
                .map(|(d, b)| (*d, b.clone()))
                .collect(),
            names,
        )
    }
    fn empty() -> PurityGrant {
        PurityGrant {
            intrinsic: BTreeMap::new(),
            hofs: BTreeMap::new(),
            kernels: BTreeSet::new(),
        }
    }
    #[test]
    fn captures_actual_bound_args_not_whole_model() {
        let (b, n) = fixture("", "row \"saved\"");
        let caller = &b[&n["entry"]];
        let result = pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).unwrap();
        assert_eq!(result.def, n["row"]);
        assert_eq!(result.bound.len(), 1);
        assert_eq!(result.remaining, 2);
    }
    #[test]
    fn rejects_partial_abi_recursion_unknown_calls_and_effectful_cafs() {
        for (extra, entry) in [
            ("", "row"),
            ("loop a b = loop a b", "loop"),
            ("bad a b = missing a b", "bad"),
            (
                "stamp = missing ()\nbad a b = { stamp = stamp, a = a, b = b }",
                "bad",
            ),
        ] {
            let (b, n) = fixture(extra, entry);
            let caller = &b[&n["entry"]];
            assert!(
                pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_none(),
                "{extra} {entry}"
            );
        }
    }
    #[test]
    fn synchronous_hof_checks_latent_callback_body() {
        for (extra, expected) in [
            (
                "outer saved index value = indexedMap (\\i x -> { x = x, i = i }) value",
                true,
            ),
            (
                "outer saved index value = indexedMap (\\i x -> missing x) value",
                false,
            ),
        ] {
            let (b, n) = fixture(extra, "outer \"saved\"");
            let caller = &b[&n["entry"]];
            let mut g = empty();
            g.hofs.insert(
                n["indexedMap"],
                SynchronousHof {
                    arity: 2,
                    callback_slot: 0,
                    callback_arity: 2,
                },
            );
            assert_eq!(
                pure_callback(&b, caller, caller.root.unwrap(), 2, &g).is_some(),
                expected
            );
        }
    }
    #[test]
    fn follows_only_literal_function_aliases_and_denies_wildcard_force() {
        let (b, n) = fixture(
            "rowAlias = row\nouter saved i value = rowAlias saved i value",
            "outer \"saved\"",
        );
        let caller = &b[&n["entry"]];
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_some());
        let (b, n) = fixture(
            "outer saved i value = let _ = 1 in { value = value }",
            "outer \"saved\"",
        );
        let caller = &b[&n["entry"]];
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_none());
    }
    #[test]
    fn validates_partial_intrinsic_hof_and_field_accessor() {
        let (b, n) = fixture(
            "primitive a b c = b\nouter saved i value = indexedMap (primitive saved) value",
            "outer \"saved\"",
        );
        let caller = &b[&n["entry"]];
        let mut g = empty();
        g.intrinsic
            .insert(n["primitive"], PureIntrinsic { arity: 3 });
        // primitive saved still expects TWO callback arguments.
        g.hofs.insert(
            n["indexedMap"],
            SynchronousHof {
                arity: 2,
                callback_slot: 0,
                callback_arity: 2,
            },
        );
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &g).is_some());
        let (b, n) = fixture(
            "map f xs = xs\nouter saved i value = map .name value",
            "outer \"saved\"",
        );
        let caller = &b[&n["entry"]];
        let mut g = empty();
        g.hofs.insert(
            n["map"],
            SynchronousHof {
                arity: 2,
                callback_slot: 0,
                callback_arity: 1,
            },
        );
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &g).is_some());
    }

    #[test]
    fn irrefutable_tuple_destructures_prove_strict_rhs_and_deny_extracted_calls() {
        for (expression, expected) in [
            (
                "let (a, b) = (saved, value) in { first = a, second = b }",
                true,
            ),
            (
                "let (a, b, c) = (saved, i, value) in { a = a, b = b, c = c }",
                true,
            ),
            (
                "let (a, (b, c)) = (saved, (i, value)) in { a = a, b = b, c = c }",
                true,
            ),
            ("let (a, ()) = (value, ()) in a", true),
            ("let (f, a) = (\\x -> x / 0, value) in a", true),
            ("let (f, a) = (\\x -> x, value) in f a", false),
            ("let (a, b) = (value, 1 / 0) in a", false),
            ("let (a, _) = (saved, value) in a", true),
            ("let (a, (_, c)) = (saved, (i, value)) in c", true),
            ("let () = () in value", false),
            ("let _ = value in value", false),
            ("let _ = Task.succeed () in value", false),
            ("let (a, _) = (value, Task.succeed ()) in a", false),
            ("let { name } = value in name", false),
            ("let [a] = [value] in a", false),
            ("let (Box a) = Box saved in a", false),
            ("let (a, 0) = (saved, 0) in a", false),
        ] {
            let extra = format!("type Box = Box String\nouter saved i value = {expression}");
            let (b, n) = fixture(&extra, "outer \"saved\"");
            let caller = &b[&n["entry"]];
            let outer = &b[&n["outer"]];
            let Expr::Let { defs, .. } = &outer.exprs[outer.root.unwrap()] else {
                panic!("tuple fixture must resolve to an actual let");
            };
            assert_eq!(defs.len(), 1);
            if expression.starts_with("let (") && !expression.starts_with("let ()") {
                assert!(defs[0].binders.is_empty() && defs[0].params.is_empty());
                assert!(
                    defs[0].pat.is_some(),
                    "actual destructure must retain its pattern"
                );
            }
            assert_eq!(
                pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_some(),
                expected,
                "{expression}"
            );
        }
    }

    #[test]
    fn tuple_destructure_proof_requires_actual_pattern_and_pattern_budget() {
        let (mut bodies, names) = fixture(
            "outer saved i value = let (a, b) = (saved, value) in a",
            "outer \"saved\"",
        );
        let outer = bodies.get_mut(&names["outer"]).unwrap();
        let root = outer.root.unwrap();
        let Expr::Let { defs, .. } = &mut outer.exprs[root] else {
            unreachable!()
        };
        let original = defs[0].pat.unwrap();
        defs[0].pat = None;
        let caller = &bodies[&names["entry"]];
        assert!(pure_callback(&bodies, caller, caller.root.unwrap(), 2, &empty()).is_none());
        // Malformed/unsupported tuple shapes must not turn an empty binder list
        // into a blanket purity grant. A large pattern must consume the same
        // bounded proof budget as expression traversal, without recursive walk.
        let outer = bodies.get_mut(&names["outer"]).unwrap();
        let unit = outer.pats.alloc(Pattern::Unit);
        let mut nested = original;
        for _ in 0..40_000 {
            nested = outer.pats.alloc(Pattern::Tuple(vec![nested, unit]));
        }
        let Expr::Let { defs, .. } = &mut outer.exprs[root] else {
            unreachable!()
        };
        defs[0].pat = Some(nested);
        let caller = &bodies[&names["entry"]];
        assert!(pure_callback(&bodies, caller, caller.root.unwrap(), 2, &empty()).is_none());
    }

    #[test]
    fn proves_only_exact_strict_local_list_tail_recursion() {
        let recursive = |edge: &str| {
            format!("outer saved i value =\n    let\n        go chars =\n            case chars of\n                c :: rest -> {edge}\n                [] -> []\n    in go value")
        };
        for (edge, expected) in [
            ("go rest", true),
            ("go chars", false),
            ("go (c :: rest)", false),
            ("go (same rest)", false),
        ] {
            let extra = format!("same x = x\n{}", recursive(edge));
            let (b, n) = fixture(&extra, "outer \"saved\"");
            let caller = &b[&n["entry"]];
            assert_eq!(
                pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_some(),
                expected,
                "{edge}"
            );
        }
    }
    #[test]
    fn denies_mutual_local_recursion_even_if_one_edge_uses_tail() {
        let extra="outer saved i value =\n    let\n        go chars =\n            case chars of\n                c :: rest -> helper rest\n                [] -> []\n        helper chars = go chars\n    in go value";
        let (b, n) = fixture(extra, "outer \"saved\"");
        let caller = &b[&n["entry"]];
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_none());
    }
    #[test]
    fn nonrecursive_local_helper_is_pure_but_unknown_local_function_arg_is_not() {
        let (b, n) = fixture(
            "outer saved i value = let digit x = { x = x } in digit value",
            "outer \"saved\"",
        );
        let caller = &b[&n["entry"]];
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_some());
        let (b, n) = fixture("outer saved i value = value i", "outer \"saved\"");
        let caller = &b[&n["entry"]];
        assert!(pure_callback(&b, caller, caller.root.unwrap(), 2, &empty()).is_none());
    }

    #[test]
    fn deep_literal_alias_proof_grows_stack_and_large_expression_stops_at_budget() {
        std::thread::Builder::new()
            .stack_size(2 * 1024 * 1024)
            .spawn(|| {
                fn function() -> Body {
                    let mut body = Body::default();
                    for n in 0..3 {
                        let p = body.pats.alloc(Pattern::Var(LocalId(n)));
                        body.params.push(p);
                    }
                    body
                }
                let mut bodies = BTreeMap::new();
                for n in 1..=20_000u32 {
                    let mut b = Body::default();
                    let root = b.exprs.alloc(Expr::Var(Res::Def(DefId(n + 1))));
                    b.root = Some(root);
                    bodies.insert(DefId(n), b);
                }
                let mut leaf = function();
                let root = leaf.exprs.alloc(Expr::Var(Res::Local(LocalId(2))));
                leaf.root = Some(root);
                bodies.insert(DefId(20_001), leaf);
                let mut outer = function();
                let head = outer.exprs.alloc(Expr::Var(Res::Def(DefId(1))));
                let args = (0..3)
                    .map(|n| outer.exprs.alloc(Expr::Var(Res::Local(LocalId(n)))))
                    .collect();
                let root = outer.exprs.alloc(Expr::Call(head, args));
                outer.root = Some(root);
                bodies.insert(DefId(0), outer);
                let mut caller = Body::default();
                let head = caller.exprs.alloc(Expr::Var(Res::Def(DefId(0))));
                let arg = caller.exprs.alloc(Expr::Str("saved".into()));
                let call = caller.exprs.alloc(Expr::Call(head, vec![arg]));
                caller.root = Some(call);
                assert!(pure_callback(&bodies, &caller, call, 2, &empty()).is_some());
                let outer = bodies.get_mut(&DefId(0)).unwrap();
                let mut values = Vec::new();
                for _ in 0..70_000 {
                    values.push(outer.exprs.alloc(Expr::Int(1)));
                }
                let root = outer.exprs.alloc(Expr::List(values));
                outer.root = Some(root);
                assert!(
                    pure_callback(&bodies, &caller, call, 2, &empty()).is_none(),
                    "proof budget must return reference fallback"
                );
            })
            .unwrap()
            .join()
            .unwrap();
    }
}
