//! Isolated OWNER proof prototype, NOT activation/purity/result certification.
//! Driver must grant exact canonical App/config/builder DefIDs while original
//! source identities are intact. No config flag or spelling can mint the grant.
use base::DefId;
use hir::{Body, Expr, ExprId, Res};
use std::borrow::Borrow;
use std::collections::{BTreeMap, BTreeSet, VecDeque};

pub struct TrustedSpaEntry {
    pub app: DefId,
    pub config: DefId,
    // Audited immutable config setters ONLY, with exact source call arity and
    // the config argument last. Arbitrary functions cannot be added by users.
    pub builders: BTreeMap<DefId, usize>,
}

pub struct ViewOwnership {
    pub view_root: DefId,
    pub owners: BTreeSet<DefId>,
    // Even a view-only function's event/let-function bodies are NOT immediate.
    pub immediate: BTreeMap<DefId, BTreeSet<ExprId>>,
}

impl ViewOwnership {
    pub fn permits(&self, owner: DefId, expression: ExprId) -> bool {
        self.owners.contains(&owner)
            && self
                .immediate
                .get(&owner)
                .is_some_and(|s| s.contains(&expression))
    }
}

fn pipe(body: &Body, id: ExprId) -> Option<(ExprId, ExprId)> {
    let Expr::Binop {
        op,
        res: Res::Kernel { module, func },
        lhs,
        rhs,
    } = &body.exprs[id]
    else {
        return None;
    };
    if module.as_str() != "Basics" {
        return None;
    }
    match (op.as_str(), func.as_str()) {
        ("|>", "apR") => Some((*rhs, *lhs)),
        ("<|", "apL") => Some((*lhs, *rhs)),
        _ => None,
    }
}

fn call(body: &Body, mut id: ExprId) -> (ExprId, Vec<ExprId>) {
    let mut arguments = Vec::new();
    loop {
        if let Expr::Call(head, args) = &body.exprs[id] {
            arguments.splice(0..0, args.iter().copied());
            id = *head;
        } else if let Some((head, arg)) = pipe(body, id) {
            arguments.insert(0, arg);
            id = head;
        } else {
            break;
        }
    }
    (id, arguments)
}

// Accept only an UNALIASED main App(config-literal |> audited setters) spine.
// Config cannot escape through a let/CAF, unknown builder, Task, return tuple,
// unknown stdlib/FFI consumer, or another use site on this accepted path.
fn entry_slot<B: Borrow<Body>>(
    main: DefId,
    bodies: &BTreeMap<DefId, B>,
    grant: &TrustedSpaEntry,
) -> Option<(DefId, ExprId)> {
    let body = Borrow::<Body>::borrow(bodies.get(&main)?);
    if !body.params.is_empty() {
        return None;
    }
    let (head, args) = call(body, body.root?);
    if !matches!(&body.exprs[head], Expr::Var(Res::Def(d)) if *d == grant.app) || args.len() != 1 {
        return None;
    }
    let mut config = args[0];
    loop {
        let (head, args) = call(body, config);
        let Expr::Var(Res::Def(d)) = &body.exprs[head] else {
            return None;
        };
        if *d == grant.config {
            let [record] = args.as_slice() else {
                return None;
            };
            let Expr::Record(fields) = &body.exprs[*record] else {
                return None;
            };
            let mut views = fields.iter().filter(|(name, _)| name.as_str() == "view");
            let (_, view) = views.next()?;
            if views.next().is_some() {
                return None;
            }
            let Expr::Var(Res::Def(root)) = &body.exprs[*view] else {
                return None;
            };
            if Borrow::<Body>::borrow(bodies.get(root)?).params.len() != 1 {
                return None;
            }
            return Some((*root, *view));
        }
        let arity = *grant.builders.get(d)?;
        if arity == 0 || args.len() != arity {
            return None;
        }
        config = *args.last()?;
    }
}

pub fn prove_view_ownership<B: Borrow<Body>>(
    main: DefId,
    bodies: &BTreeMap<DefId, B>,
    grant: &TrustedSpaEntry,
) -> Option<ViewOwnership> {
    let (view_root, registration) = entry_slot(main, bodies, grant)?;
    let mut direct: BTreeMap<DefId, BTreeSet<DefId>> = BTreeMap::new();
    let mut escaped = BTreeSet::new();
    let mut immediate = BTreeMap::new();
    for (owner, body) in bodies {
        let body = Borrow::<Body>::borrow(body);
        let mut work = body.root.map(|r| vec![(r, true)]).unwrap_or_default();
        let mut now_sites = BTreeSet::new();
        while let Some((id, now)) = work.pop() {
            if now {
                now_sites.insert(id);
            }
            if matches!(&body.exprs[id], Expr::Call(_, _)) || pipe(body, id).is_some() {
                let (head, args) = call(body, id);
                if let Expr::Var(Res::Def(callee)) = &body.exprs[head] {
                    if now
                        && bodies
                            .get(callee)
                            .is_some_and(|b| Borrow::<Body>::borrow(b).params.len() == args.len())
                    {
                        direct.entry(*owner).or_default().insert(*callee);
                    } else {
                        escaped.insert(*callee);
                    }
                } else {
                    work.push((head, now));
                }
                work.extend(args.into_iter().map(|a| (a, now)));
                continue;
            }
            match &body.exprs[id] {
                Expr::Var(Res::Def(d)) => {
                    if !(*owner == main && id == registration && *d == view_root) {
                        escaped.insert(*d);
                    }
                }
                Expr::Lambda { body, .. } => work.push((*body, false)),
                Expr::Let { defs, body } => {
                    work.push((*body, now));
                    work.extend(defs.iter().map(|d| (d.body, now && d.params.is_empty())));
                }
                Expr::List(xs) | Expr::Tuple(xs) => work.extend(xs.iter().map(|x| (*x, now))),
                Expr::Record(fields) => work.extend(fields.iter().map(|(_, x)| (*x, now))),
                Expr::Update { base, fields } => {
                    work.push((*base, now));
                    work.extend(fields.iter().map(|(_, x)| (*x, now)));
                }
                Expr::Negate(x) | Expr::Access(x, _) => work.push((*x, now)),
                Expr::Binop { lhs, rhs, .. } => {
                    work.push((*lhs, now));
                    work.push((*rhs, now));
                }
                Expr::If { arms, els } => {
                    work.push((*els, now));
                    work.extend(arms.iter().flat_map(|(c, b)| [(*c, now), (*b, now)]));
                }
                Expr::Case { subject, branches } => {
                    work.push((*subject, now));
                    work.extend(branches.iter().map(|b| (b.body, now)));
                }
                _ => {}
            }
        }
        immediate.insert(*owner, now_sites);
    }
    let closure = |seeds: BTreeSet<DefId>| {
        let mut found = seeds.clone();
        let mut work: Vec<_> = seeds.into_iter().collect();
        while let Some(d) = work.pop() {
            for child in direct.get(&d).into_iter().flatten() {
                if found.insert(*child) {
                    work.push(*child);
                }
            }
        }
        found
    };
    let reachable = closure(BTreeSet::from([view_root]));
    // Kahn residual = cycles AND everything downstream of those cycles.
    // Both are deliberately denied; do not merely test direct self recursion.
    let mut degree: BTreeMap<_, usize> = bodies.keys().map(|d| (*d, 0)).collect();
    for targets in direct.values() {
        for d in targets {
            if let Some(n) = degree.get_mut(d) {
                *n += 1;
            }
        }
    }
    let mut queue: VecDeque<_> = degree
        .iter()
        .filter_map(|(d, n)| (*n == 0).then_some(*d))
        .collect();
    while let Some(d) = queue.pop_front() {
        for target in direct.get(&d).into_iter().flatten() {
            if let Some(n) = degree.get_mut(target) {
                *n -= 1;
                if *n == 0 {
                    queue.push_back(*target);
                }
            }
        }
    }
    let mut bad = escaped;
    bad.extend(bodies.keys().filter(|d| !reachable.contains(*d)).copied());
    bad.extend(degree.iter().filter_map(|(d, n)| (*n != 0).then_some(*d)));
    let bad = closure(bad);
    let owners = reachable.difference(&bad).copied().collect();
    Some(ViewOwnership {
        view_root,
        owners,
        immediate,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::{SkyDb, SourceDb};

    fn check(
        extra: &str,
        main: &str,
        body: &str,
    ) -> (
        Option<ViewOwnership>,
        BTreeMap<String, DefId>,
        BTreeMap<DefId, Body>,
    ) {
        let mut db = SourceDb::new();
        let api_parse = syntax::parse(
            "module Api exposing (..)\napp x = x\nconfig x = x\nbuilder x cfg = cfg\n",
            FileId(0),
        );
        assert!(
            api_parse.errors().is_empty() && api_parse.error_node_count() == 0,
            "ownership API fixture must parse cleanly"
        );
        let api = db.add_module("Api", api_parse);
        let text = format!("module Main exposing (..)\nimport Api\ninit x = x\nupdate msg model = model\nspaView model = view model\nview model = editor model\neditor model = products model\nproducts model = {body}\n{extra}\nmain = {main}\n");
        let main_parse = syntax::parse(&text, FileId(1));
        assert!(
            main_parse.errors().is_empty() && main_parse.error_node_count() == 0,
            "ownership Main fixture must parse cleanly"
        );
        let source = db.add_module("Main", main_parse);
        let ar = db.resolve(api);
        let mr = db.resolve(source);
        let mut bodies: BTreeMap<_, _> = ar.bodies.iter().map(|(d, b)| (*d, b.clone())).collect();
        bodies.extend(mr.bodies.iter().map(|(d, b)| (*d, b.clone())));
        let names: BTreeMap<_, _> = mr
            .top_defs
            .iter()
            .map(|d| (d.name.as_str().to_string(), d.def))
            .collect();
        let api_id = |name: &str| {
            ar.top_defs
                .iter()
                .find(|d| d.name.as_str() == name)
                .unwrap()
                .def
        };
        let grant = TrustedSpaEntry {
            app: api_id("app"),
            config: api_id("config"),
            builders: BTreeMap::from([(api_id("builder"), 2)]),
        };
        let proof = prove_view_ownership(names["main"], &bodies, &grant);
        let borrowed: BTreeMap<_, _> = bodies.iter().map(|(d, body)| (*d, body)).collect();
        let borrowed_proof = prove_view_ownership(names["main"], &borrowed, &grant);
        let signature = |proof: &Option<ViewOwnership>| {
            proof
                .as_ref()
                .map(|p| (p.view_root, p.owners.clone(), p.immediate.clone()))
        };
        assert_eq!(
            signature(&proof),
            signature(&borrowed_proof),
            "owned and borrowed HIR must yield identical roots, owners, and immediate ExprIds"
        );
        (proof, names, bodies)
    }
    const MAIN: &str =
        "Api.app (Api.config { init = init, update = update, view = spaView } |> Api.builder 1)";

    #[test]
    fn admits_named_faceplan_shaped_owner_chain_and_canonical_pipe() {
        let (proof, names, bodies) = check("", MAIN, "model");
        let proof = proof.unwrap();
        assert!(proof.owners.contains(&names["products"]));
        assert!(proof.permits(names["products"], bodies[&names["products"]].root.unwrap()));
        assert!(!proof.owners.contains(&names["update"]));
    }

    #[test]
    fn denies_nonview_and_latent_entries_and_aliases() {
        for extra in [
            "other x = products x\nleaked = other",
            "other = \\x -> products x",
            "other x = let later y = products y in later",
            "escapedProducts = products",
            "other x = sink products x\nsink f x = f x",
        ] {
            let (proof, names, bodies) = check(extra, MAIN, "model");
            if extra == "escapedProducts = products" {
                let escaped = &bodies[&names["escapedProducts"]];
                assert!(
                    matches!(&escaped.exprs[escaped.root.unwrap()],
                    Expr::Var(Res::Def(d)) if *d == names["products"]),
                    "negative fixture must contain an actual resolved function escape"
                );
            }
            assert!(
                !proof.unwrap().owners.contains(&names["products"]),
                "{extra}"
            );
        }
    }

    #[test]
    fn denies_recursive_owner_paths() {
        let (p, n, _) = check("", MAIN, "products model");
        assert!(!p.unwrap().owners.contains(&n["products"]));
        let (p, n, _) = check("loop x = products x", MAIN, "loop model");
        assert!(!p.unwrap().owners.contains(&n["products"]));
    }

    #[test]
    fn denies_config_alias_escape_unknown_builder_and_wrong_registration() {
        for main in [
            "Api.app (evil (Api.config { view = spaView }))",
            "evil (Api.app (Api.config { view = spaView }))",
            "let cfg = Api.config { view = spaView } in Api.app cfg",
            "Api.app (Api.config { view = (\\m -> spaView m) })",
        ] {
            assert!(check("evil x = x", main, "model").0.is_none(), "{main}");
        }
        let (proof, names, _) = check("leak = spaView", MAIN, "model");
        assert!(!proof.unwrap().owners.contains(&names["products"]));
    }
}
