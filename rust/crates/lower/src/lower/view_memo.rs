//! Compiler-owned source certificates + explicit-context alternate lowering.
//! Prototype: no user flag and no activation without ALL gates succeeding.
use super::*;
mod clones;
mod ownership;
mod purity;

#[derive(Clone)]
pub(super) struct Site {
    callback: DefId,
    callback_expr: ExprId,
    list_expr: ExprId,
    bound_count: usize,
    site: usize,
}
pub(super) struct Plan {
    root: DefId,
    pub route: BTreeSet<DefId>,
    pub sites: BTreeMap<DefId, Vec<Site>>,
}

/// Audited *construction* primitives only. Ordinary Ui/Html wrappers are NOT
/// trusted wholesale: their canonical source bodies are checked recursively.
/// Total data-domain primitives may allocate but never call supplied callbacks.
pub(super) const INTRINSICS: &[(&str, &str, usize)] = &[
    ("Sky.Core.String", "fromInt", 1),
    ("Sky.Core.String", "fromFloat", 1),
    ("Sky.Core.String", "length", 1),
    ("Sky.Core.String", "isEmpty", 1),
    ("Sky.Core.String", "join", 2),
    ("Sky.Core.String", "split", 2),
    ("Sky.Core.String", "replace", 3),
    ("Sky.Core.String", "slice", 3),
    ("Sky.Core.String", "toUpper", 1),
    ("Sky.Core.String", "toLower", 1),
    ("Sky.Core.String", "trim", 1),
    ("Sky.Core.String", "trimStart", 1),
    ("Sky.Core.String", "trimEnd", 1),
    ("Sky.Core.String", "reverse", 1),
    ("Sky.Core.String", "append", 2),
    ("Sky.Core.String", "concat", 1),
    ("Sky.Core.String", "contains", 2),
    ("Sky.Core.String", "startsWith", 2),
    ("Sky.Core.String", "endsWith", 2),
    ("Sky.Core.String", "padLeft", 3),
    ("Sky.Core.String", "padRight", 3),
    ("Sky.Core.String", "left", 2),
    ("Sky.Core.String", "right", 2),
    ("Sky.Core.String", "dropLeft", 2),
    ("Sky.Core.String", "dropRight", 2),
    ("Sky.Core.String", "toInt", 1),
    ("Sky.Core.String", "toFloat", 1),
    ("Sky.Core.List", "length", 1),
    ("Sky.Core.List", "isEmpty", 1),
    ("Sky.Core.List", "reverse", 1),
    ("Sky.Core.List", "take", 2),
    ("Sky.Core.List", "drop", 2),
    ("Sky.Core.List", "append", 2),
    ("Sky.Core.List", "concat", 1),
    ("Sky.Core.Math", "abs", 1),
    ("Sky.Core.Math", "min", 2),
    ("Sky.Core.Math", "max", 2),
    ("Sky.Core.Math", "round", 1),
    ("Sky.Core.Math", "floor", 1),
    ("Sky.Core.Math", "ceil", 1),
    ("Sky.Core.String", "toList", 1),
    ("Sky.Core.String", "fromList", 1),
    ("Sky.Core.Char", "isAlpha", 1),
    ("Sky.Core.Char", "isDigit", 1),
    ("Sky.Core.Char", "isAlphaNum", 1),
    ("Sky.Core.Char", "toLower", 1),
    ("Sky.Core.Char", "toUpper", 1),
    ("Sky.Core.Result", "withDefault", 2),
    ("Sky.Core.Maybe", "withDefault", 2),
    // Exact canonical Regex handles are constructed/read only; no raw Go FFI
    // may be reachable in this frontend (whole-program entitlement gate).
    ("Sky.Core.Regex", "fromString", 1),
    ("Sky.Core.Regex", "never", 0),
    ("Sky.Core.Regex", "contains", 2),
    ("Sky.Core.Regex", "find", 2),
    ("Sky.Core.Regex", "findMatch", 2),
    ("Sky.Core.Regex", "replace", 3),
    // Audited closed attribute -> CSS construction, no user handler invocation.
    // Avoid trusting every Ui function or its recursive renderer wholesale.
    ("Std.Ui", "onPseudo", 2),
];
// Known synchronous callbacks. Purity checks their bodies and ALL supplied
// operands even when a particular branch/list would not invoke a callback.
pub(super) const HOFS: &[(&str, &str, usize, usize, usize)] = &[
    ("Sky.Core.List", "map", 2, 0, 1),
    ("Sky.Core.List", "indexedMap", 2, 0, 2),
    ("Sky.Core.List", "filter", 2, 0, 1),
    ("Sky.Core.List", "filterMap", 2, 0, 1),
    ("Sky.Core.List", "concatMap", 2, 0, 1),
    ("Sky.Core.List", "any", 2, 0, 1),
    ("Sky.Core.List", "all", 2, 0, 1),
    ("Sky.Core.List", "foldl", 3, 0, 2),
    ("Sky.Core.List", "foldr", 3, 0, 2),
    ("Sky.Core.Maybe", "map", 2, 0, 1),
    ("Sky.Core.Maybe", "andThen", 2, 0, 1),
    ("Sky.Core.Regex", "replaceWith", 3, 1, 1),
];
pub(super) const BUILDERS: &[(&str, usize)] = &[
    ("withRoutes", 2),
    ("withNotFound", 2),
    ("withOnNavigate", 2),
    ("withHead", 2),
    ("withModelDecoder", 2),
    ("withModelEncoder", 2),
    ("withPersistProtectedFields", 2),
    ("withPersistDecoder", 2),
    ("withPersistSeedFields", 2),
    ("withPersistExcludeFields", 2),
    ("withHoldFree", 2),
    ("withGuard", 2),
];

pub(super) fn plan(
    main: DefId,
    defs: &BTreeMap<DefId, DefEntry>,
    kernel_alias: &HashMap<DefId, String>,
    grant: &[(String, String, DefId)],
) -> Option<Plan> {
    let id = |m: &str, n: &str| {
        grant
            .iter()
            .find(|(a, b, _)| a == m && b == n)
            .map(|(_, _, d)| *d)
    };
    let app = id("Std.Spa", "app")?;
    let config = id("Std.Spa", "config")?;
    // Cheap reference fallback before any proof inventory allocation. All
    // non-SPA builds and unknown main spines must retain their existing cost.
    let main_body = &defs.get(&main)?.body;
    let Expr::Call(main_head, main_args) = &main_body.exprs[main_body.root?] else {
        return None;
    };
    if main_args.len() != 1
        || !matches!(&main_body.exprs[*main_head],Expr::Var(Res::Def(d)) if *d==app)
    {
        return None;
    }
    let mut builders = BTreeMap::new();
    for (name, arity) in BUILDERS {
        if let Some(d) = id("Std.Spa", name) {
            builders.insert(d, *arity);
        }
    }
    // Generated helpers have no trusted spelling. Only an actual resolved
    // literal kernel alias to these audited immutable config setters qualifies.
    for (d, raw) in kernel_alias {
        match alias_go_name(raw).as_str() {
            "rt.Spa_withGeneratedModelEncoder" => {
                builders.insert(*d, 2);
            }
            "rt.Spa_withPreparedModelEncoder" => {
                builders.insert(*d, 4);
            }
            _ => {}
        }
    }
    // Proofs borrow the reached HIR; only their local lookup table is allocated.
    let bodymap: BTreeMap<_, _> = defs.iter().map(|(d, e)| (*d, &*e.body)).collect();
    let owner = ownership::prove_view_ownership(
        main,
        &bodymap,
        &ownership::TrustedSpaEntry {
            app,
            config,
            builders,
        },
    )?;
    if !owner.owners.contains(&owner.view_root) {
        return None;
    }
    let mut pure = purity::PurityGrant {
        intrinsic: BTreeMap::new(),
        hofs: BTreeMap::new(),
        kernels: BTreeSet::new(),
    };
    for (m, n, arity) in INTRINSICS {
        if let Some(d) = id(m, n) {
            pure.intrinsic
                .insert(d, purity::PureIntrinsic { arity: *arity });
        }
    }
    for (m, n, arity, slot, cb) in HOFS {
        if let Some(d) = id(m, n) {
            pure.hofs.insert(
                d,
                purity::SynchronousHof {
                    arity: *arity,
                    callback_slot: *slot,
                    callback_arity: *cb,
                },
            );
        }
    }
    for name in [
        "add", "sub", "mul", "idiv", "mod", "append", "eq", "neq", "lt", "gt", "le", "ge", "and",
        "or",
    ] {
        pure.kernels.insert(("Basics".into(), name.into(), 2));
    }
    pure.kernels.insert(("List".into(), "cons".into(), 2));
    pure.kernels.insert(("Basics".into(), "not".into(), 1));
    pure.kernels.insert(("Basics".into(), "modBy".into(), 2));
    pure.kernels.insert(("Basics".into(), "clamp".into(), 3));
    for n in ["min", "max"] {
        pure.kernels.insert(("Basics".into(), n.into(), 2));
    }
    for n in ["toFloat", "abs", "fst", "snd", "identity"] {
        pure.kernels.insert(("Basics".into(), n.into(), 1));
    }
    // Built-in-qualified String/List calls resolve to kernels, while explicit
    // imports resolve to DefIDs. Their grant is still conditional on intact
    // original canonical source identities.
    for (m, n, a) in INTRINSICS {
        if id(m, n).is_some() {
            let kernel = m.rsplit('.').next().unwrap_or(m);
            pure.kernels.insert((kernel.into(), (*n).into(), *a));
        }
    }
    let indexed = id("Sky.Core.List", "indexedMap")?;
    let mut sites: BTreeMap<DefId, Vec<Site>> = BTreeMap::new();
    let mut next = 0;
    let mut ordered: Vec<_> = owner.owners.iter().copied().collect();
    ordered.sort_by(|a, b| {
        (&defs[a].module_name, &defs[a].name).cmp(&(&defs[b].module_name, &defs[b].name))
    });
    for d in &ordered {
        let b = &defs[d].body;
        for (e, node) in b.exprs.iter() {
            let Expr::Call(head, args) = node else {
                continue;
            };
            if args.len() != 2
                || !owner.permits(*d, e)
                || !matches!(&b.exprs[*head],Expr::Var(Res::Def(x)) if *x==indexed)
            {
                continue;
            }
            let Some(cb) = purity::pure_callback(&bodymap, b, args[0], 2, &pure) else {
                continue;
            };
            sites.entry(*d).or_default().push(Site {
                callback: cb.def,
                callback_expr: args[0],
                list_expr: args[1],
                bound_count: cb.bound.len(),
                site: next,
            });
            next += 1;
        }
    }
    if sites.is_empty() {
        return None;
    }
    // Minimal backwards ancestor closure; direct calls only. All selected
    // source owners already passed no-alias/no-latent/no-cycle proof.
    let mut route: BTreeSet<_> = sites.keys().copied().collect();
    loop {
        let mut added = false;
        for d in &owner.owners {
            if route.contains(d) {
                continue;
            }
            let b = &defs[d].body;
            if b.exprs.iter().any(|(e,x)|owner.permits(*d,e)&&matches!(x,Expr::Call(h,_) if matches!(&b.exprs[*h],Expr::Var(Res::Def(c)) if route.contains(c)))) {
                route.insert(*d);added=true;
            }
        }
        if !added {
            break;
        }
    }
    if !route.contains(&owner.view_root) {
        return None;
    }
    Some(Plan {
        root: owner.view_root,
        route,
        sites,
    })
}

impl Ctx<'_> {
    /// Called only for alternate memo-owner lowering, after the original f/xs
    /// operands have each lowered ONCE. No probe-and-relower.
    pub(super) fn try_view_memo_hof(
        &mut self,
        hof: ListHof,
        go: &str,
        args: &[ExprId],
        f: &GoExpr,
        xs: &GoExpr,
        actual: &GoTy,
    ) -> Option<GoExpr> {
        let site = self
            .view_memo_sites?
            .iter()
            .find(|s| args.len() == 2 && args[0] == s.callback_expr && args[1] == s.list_expr)?
            .clone();
        if !matches!(hof, ListHof::IndexedMap) || go != "rt.List_indexedMap" {
            return None;
        }
        let callback = self.defs.get(&site.callback)?;
        let target = top_go_name(&callback.module_name, &callback.name);
        let types = self.def_param_tys.get(&site.callback)?.clone();
        let ret = self.def_result_tys.get(&site.callback)?.clone();
        let module = callback.module_name.clone();
        let actual_params: Vec<_> = types.iter().map(|t| self.goty_in(t, &module)).collect();
        let actual_ret = self.goty_in(&ret, &module);
        if actual_params.len() != site.bound_count + 2 || !matches!(xs.ty, GoTy::Slice(_)) {
            return None;
        }
        let target_ty = GoTy::Func(actual_params, Box::new(actual_ret));
        // Strictly recognize our existing make_partial/with_frozen_values IR;
        // arbitrary closures have no exposed capture environment and fall back.
        let (outer, mut operands) = if site.bound_count == 0 {
            if !matches!(&f.kind,GoExprKind::Ident(n) if n==&target) || f.ty != target_ty {
                return None;
            }
            (None, vec![])
        } else {
            let GoExprKind::Call(fun, values) = &f.kind else {
                return None;
            };
            let GoExprKind::FuncLit(ps, _, body) = &fun.kind else {
                return None;
            };
            if values.len() != site.bound_count + 1
                || ps.len() != values.len()
                || !matches!(&values[0].kind,GoExprKind::Ident(n) if n==&target)
                || values[0].ty != target_ty
                || ps.iter().zip(values).any(|(p, v)| p.ty != v.ty)
            {
                return None;
            }
            if !matches!(
                body.as_slice(),
                [GoStmt::Return(Some(GoExpr {
                    kind: GoExprKind::FuncLit(..),
                    ..
                }))]
            ) {
                return None;
            }
            (Some(fun.as_ref().clone()), values.clone())
        };
        operands.push(xs.clone());
        let bound_count = site.bound_count;
        let old_f = f.clone();
        let actual = actual.clone();
        let go = go.to_owned();
        let result = self.with_frozen_values(operands, move |cx, refs| {
            let list = refs.last().unwrap().clone();
            let rebuilt = match outer {
                Some(fun) => GoExpr::new(
                    GoExprKind::Call(Box::new(fun), refs[..refs.len() - 1].to_vec()),
                    old_f.ty.clone(),
                ),
                None => old_f,
            };
            let call = cx.list_hof_typed(hof, &go, rebuilt, list.clone(), &actual);
            let ret = call.ty.clone();
            // Captures exclude the known function symbol, but include every
            // actual already-coerced bound value and the exact list header.
            let boxed = |x: GoExpr| {
                GoExpr::new(
                    GoExprKind::GenericCall(
                        "rt.SpaViewMemoCapture".into(),
                        vec![x.ty.clone()],
                        vec![x],
                    ),
                    GoTy::Any,
                )
            };
            let mut captures = if bound_count == 0 {
                vec![]
            } else {
                refs[1..refs.len() - 1].iter().cloned().map(boxed).collect()
            };
            captures.push(boxed(list));
            let bag = GoExpr::new(
                GoExprKind::SliceLit(GoTy::Any, captures),
                GoTy::Slice(Box::new(GoTy::Any)),
            );
            let build = GoExpr::new(
                GoExprKind::FuncLit(
                    vec![],
                    GoTy::Any,
                    vec![GoStmt::Return(Some(cx.widen(call)))],
                ),
                GoTy::Func(vec![], Box::new(GoTy::Any)),
            );
            let kernel = GoExpr::new(
                GoExprKind::Call(
                    Box::new(GoExpr::new(
                        GoExprKind::Ident("rt.SpaViewMemoRegion".into()),
                        GoTy::Any,
                    )),
                    vec![
                        GoExpr::new(
                            GoExprKind::Ident("sky_view_memo_ctx_private_".into()),
                            GoTy::Any,
                        ),
                        GoExpr::new(GoExprKind::IntLit(site.site as i64), GoTy::Bare(Prim::Int)),
                        cx.widen(bag),
                        build,
                    ],
                ),
                GoTy::Any,
            );
            cx.coerce_if_needed(kernel, &ret)
        });
        self.view_memo_regions += 1;
        Some(result)
    }
}

/// Publish only after alternate lowering AND exact clone ABI checks succeeded.
/// A failed attempt leaves every original function/dispatch table untouched.
pub(super) fn finish(
    plan: &Plan,
    defs: &BTreeMap<DefId, DefEntry>,
    memo_funcs: Vec<GoFuncDecl>,
    funcs: &mut Vec<GoItem>,
    types: &[(String, Vec<GoItem>)],
    regions: usize,
) {
    if regions == 0 {
        return;
    }
    let root = &defs[&plan.root];
    let root_name = top_go_name(&root.module_name, &root.name);
    let route: BTreeSet<_> = plan
        .route
        .iter()
        .map(|d| {
            let e = &defs[d];
            top_go_name(&e.module_name, &e.name)
        })
        .collect();
    let occupied = funcs
        .iter()
        .filter_map(|x| match x {
            GoItem::Func(f) => Some(f.name.clone()),
            GoItem::Var(n, ..) => Some(n.clone()),
            _ => None,
        })
        .collect();
    let Some((cloned, target)) =
        clones::clone_view_route_owned(memo_funcs, &route, &root_name, &occupied)
    else {
        return;
    };
    let Some(tokens) = emitted_type_tokens(types) else {
        return;
    };
    // Preflight only the small existing SPA adapters; do not clone the entire
    // application IR (large real apps already have a strict compiler budget).
    let mut adaptations = Vec::new();
    for item in funcs.iter_mut() {
        let GoItem::Func(f) = item else {
            continue;
        };
        clones::each_expr_mut(&mut f.body, &mut |e| {
            let GoExprKind::StructLit(name, fields) = &e.kind else {
                return;
            };
            if name != "rt.SpaFns" {
                return;
            }
            let Some((_, view)) = fields.iter().find(|(n, _)| n == "View") else {
                return;
            };
            if let Some(adapter) = clones::view_adapter(view, &root_name, &target) {
                adaptations.push((view.clone(), adapter));
            }
        });
    }
    if adaptations.len() != 1 {
        return;
    }
    let (original, adapter) = adaptations.pop().unwrap();
    for item in funcs.iter_mut() {
        let GoItem::Func(f) = item else {
            continue;
        };
        clones::each_expr_mut(&mut f.body, &mut |e| {
            let GoExprKind::StructLit(name, fields) = &mut e.kind else {
                return;
            };
            if name != "rt.SpaFns" || !fields.iter().any(|(n, v)| n == "View" && *v == original) {
                return;
            }
            fields.push(("ViewMemo".into(), adapter.clone()));
            fields.push((
                "ViewMemoTypes".into(),
                GoExpr::new(
                    GoExprKind::SliceLit(GoTy::Any, tokens.clone()),
                    GoTy::Slice(Box::new(GoTy::Any)),
                ),
            ));
        });
    }
    funcs.extend(cloned.into_iter().map(GoItem::Func));
}

/// Authority comes from actual compiler-emitted type IR, never a Go type name
/// or mutable constructor registry. Preserve the existing sealed-token sequence,
/// then append only real IotaEnums in actual emitted declaration order.
fn emitted_type_tokens(types: &[(String, Vec<GoItem>)]) -> Option<Vec<GoExpr>> {
    let mut tokens = Vec::new();
    let token = |factory: &str, name: String| {
        GoExpr::new(
            GoExprKind::GenericCall(factory.into(), vec![GoTy::Named(name, vec![])], vec![]),
            GoTy::Any,
        )
    };
    for (_, group) in types {
        for item in group {
            if let GoItem::Type(parent, GoTypeDef::SealedIface(variants)) = item {
                for (ctor, _, _) in variants {
                    tokens.push(token("rt.SpaViewMemoType", format!("{parent}_{ctor}_V")));
                    if tokens.len() > 4096 {
                        return None;
                    }
                }
            }
        }
    }
    for (_, group) in types {
        for item in group {
            if let GoItem::Type(parent, GoTypeDef::IotaEnum(_)) = item {
                tokens.push(token("rt.SpaViewMemoEnumType", parent.clone()));
                if tokens.len() > 4096 {
                    return None;
                }
            }
        }
    }
    Some(tokens)
}

#[cfg(test)]
mod type_witness_tests {
    use super::*;
    #[test]
    fn enum_witnesses_use_only_actual_iota_ir_after_existing_sealed_tokens() {
        let types = vec![(
            "fixture".into(),
            vec![
                GoItem::Type(
                    "RealEnum".into(),
                    GoTypeDef::IotaEnum(vec!["A".into(), "B".into()]),
                ),
                GoItem::Type(
                    "Variants".into(),
                    GoTypeDef::SealedIface(vec![("V".into(), 0, vec![])]),
                ),
                GoItem::Type(
                    "ForeignAlias".into(),
                    GoTypeDef::RuntimeAlias("foreign.Enum".into()),
                ),
                GoItem::Type(
                    "LooksLikeEnum".into(),
                    GoTypeDef::Alias(GoTy::Bare(Prim::Int)),
                ),
                GoItem::Raw("type Forged int".into()),
            ],
        )];
        let tokens = emitted_type_tokens(&types).unwrap();
        assert_eq!(tokens.len(), 2);
        for (actual, factory, name) in [
            (&tokens[0], "rt.SpaViewMemoType", "Variants_V_V"),
            (&tokens[1], "rt.SpaViewMemoEnumType", "RealEnum"),
        ] {
            assert!(
                matches!(&actual.kind, GoExprKind::GenericCall(f, ts, args) if f==factory && ts==&vec![GoTy::Named(name.into(),vec![])] && args.is_empty())
            );
        }
    }
    #[test]
    fn enum_and_struct_witnesses_share_the_original_combined_limit() {
        let types = |count| {
            vec![(
                "fixture".into(),
                (0..count)
                    .map(|i| GoItem::Type(format!("E{i}"), GoTypeDef::IotaEnum(vec!["A".into()])))
                    .collect(),
            )]
        };
        assert_eq!(emitted_type_tokens(&types(4096)).unwrap().len(), 4096);
        assert!(emitted_type_tokens(&types(4097)).is_none());
        let mut mixed = types(4095);
        mixed[0].1.push(GoItem::Type(
            "TwoVariants".into(),
            GoTypeDef::SealedIface(vec![("A".into(), 0, vec![]), ("B".into(), 1, vec![])]),
        ));
        assert!(
            emitted_type_tokens(&mixed).is_none(),
            "one combined enum/struct budget"
        );
    }
}
