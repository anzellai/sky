//! Isolated typed-GoIR clone kernel. NOT activated by names or user options.
//! Caller must first prove HIR ownership, exact stdlib provenance, callback
//! purity and result membership. Original functions are NEVER mutated.
use crate::ir::*;
use std::collections::{BTreeMap, BTreeSet};

const CTX: &str = "sky_view_memo_ctx_private_";
fn ident(name: &str, ty: GoTy) -> GoExpr {
    GoExpr::new(GoExprKind::Ident(name.into()), ty)
}
fn function_ty(f: &GoFuncDecl) -> GoTy {
    GoTy::Func(
        f.params.iter().map(|p| p.ty.clone()).collect(),
        Box::new(f.ret.clone()),
    )
}

/// Route = minimal ancestor closure from root to admitted region owners.
/// Generic/curried/aliased actual ABI, name collisions, latent function refs,
/// source/coerce-generated signature disagreement => no capability.
/// Names come from authenticated DefId->GoName, NOT guessed module spelling.
#[cfg(test)]
pub fn clone_view_route(
    originals: &[GoFuncDecl],
    route: &BTreeSet<String>,
    root: &str,
    occupied: &BTreeSet<String>,
) -> Option<(Vec<GoFuncDecl>, GoExpr)> {
    if !route.contains(root) {
        return None;
    }
    let by_name: BTreeMap<_, _> = originals.iter().map(|f| (f.name.clone(), f)).collect();
    let mut replacements = BTreeMap::new();
    for name in route {
        let f = *by_name.get(name)?;
        if !f.type_params.is_empty() || f.params.iter().any(|p| p.name == CTX) {
            return None;
        }
        let new_name = format!("{name}_sky_view_memo_private_");
        if occupied.contains(&new_name) || route.contains(&new_name) {
            return None;
        }
        replacements.insert(name.clone(), (new_name, function_ty(f)));
    }
    let mut out = Vec::new();
    for name in route {
        let mut f = (*by_name.get(name)?).clone();
        for s in &mut f.body {
            rewrite_stmt(s, &replacements, false)?;
        }
        f.name = replacements[name].0.clone();
        f.params.push(GoParam {
            name: CTX.into(),
            ty: GoTy::Any,
        });
        out.push(f);
    }
    let f = *by_name.get(root)?;
    if f.params.len() != 1 {
        return None;
    }
    // Return the typed cloned target, NOT a guessed any->model adapter.
    // Integrating lower must reuse spa_adapter's actual TypeAssert/EnumOf and
    // curried handling to construct func(model any, ctx any) any.
    let target_ty = GoTy::Func(
        vec![f.params[0].ty.clone(), GoTy::Any],
        Box::new(f.ret.clone()),
    );
    Some((out, ident(&replacements[root].0, target_ty)))
}

/// Consume only OWNED alternate-lowering scratch. Reference functions must stay
/// in the caller's separate original vector. Failure discards all scratch;
/// nothing is published until the caller's unchanged adapter preflight passes.
/// Moving each body avoids retaining a third recursively cloned route tree.
pub fn clone_view_route_owned(
    scratch: Vec<GoFuncDecl>,
    route: &BTreeSet<String>,
    root: &str,
    occupied: &BTreeSet<String>,
) -> Option<(Vec<GoFuncDecl>, GoExpr)> {
    if !route.contains(root) {
        return None;
    }
    let mut by_name: BTreeMap<_, _> = scratch.into_iter().map(|f| (f.name.clone(), f)).collect();
    let mut replacements = BTreeMap::new();
    for name in route {
        let f = by_name.get(name)?;
        if !f.type_params.is_empty() || f.params.iter().any(|p| p.name == CTX) {
            return None;
        }
        let new_name = format!("{name}_sky_view_memo_private_");
        if occupied.contains(&new_name) || route.contains(&new_name) {
            return None;
        }
        replacements.insert(name.clone(), (new_name, function_ty(f)));
    }
    let f = by_name.get(root)?;
    if f.params.len() != 1 {
        return None;
    }
    // Preserve the exact typed target; spa_adapter still builds the any ABI.
    let target_ty = GoTy::Func(
        vec![f.params[0].ty.clone(), GoTy::Any],
        Box::new(f.ret.clone()),
    );
    let mut out = Vec::with_capacity(route.len());
    for name in route {
        let mut f = by_name.remove(name)?;
        for s in &mut f.body {
            rewrite_stmt(s, &replacements, false)?;
        }
        f.name = replacements[name].0.clone();
        f.params.push(GoParam {
            name: CTX.into(),
            ty: GoTy::Any,
        });
        out.push(f);
    }
    Some((out, ident(&replacements[root].0, target_ty)))
}

fn rewrite_stmt(s: &mut GoStmt, r: &BTreeMap<String, (String, GoTy)>, adapter: bool) -> Option<()> {
    base::ensure_stack(|| {
        match s {
            GoStmt::Expr(e) | GoStmt::Short(_, e) | GoStmt::Discard(e) | GoStmt::Assign(_, e) => {
                rewrite_expr(e, r, adapter)?
            }
            GoStmt::AssignField(a, _, b) => {
                rewrite_expr(a, r, adapter)?;
                rewrite_expr(b, r, adapter)?;
            }
            GoStmt::Return(Some(e)) => rewrite_expr(e, r, adapter)?,
            GoStmt::Loop(xs) | GoStmt::Scope(xs) => {
                for s in xs {
                    rewrite_stmt(s, r, adapter)?;
                }
            }
            GoStmt::If(e, a, b) => {
                rewrite_expr(e, r, adapter)?;
                for s in a.iter_mut().chain(b) {
                    rewrite_stmt(s, r, adapter)?;
                }
            }
            GoStmt::IfTypeAssert { subj, then, .. } => {
                rewrite_expr(subj, r, adapter)?;
                for s in then {
                    rewrite_stmt(s, r, adapter)?;
                }
            }
            _ => {}
        }
        Some(())
    })
}
fn rewrite_expr(e: &mut GoExpr, r: &BTreeMap<String, (String, GoTy)>, adapter: bool) -> Option<()> {
    base::ensure_stack(|| {
        match &mut e.kind {
            GoExprKind::Call(f, args) => {
                // Handle original callee before walking children; its bare Ident
                // would otherwise be correctly rejected as a function escape.
                if let GoExprKind::Ident(n) = &f.kind {
                    if let Some((new_name, actual)) = r.get(n) {
                        let GoTy::Func(ps, ret) = actual else {
                            return None;
                        };
                        if f.ty != *actual
                            || ps.len() != args.len()
                            || (e.ty != **ret && !(adapter && e.ty == GoTy::Any))
                            || ps.iter().zip(args.iter()).any(|(p, a)| *p != a.ty)
                        {
                            return None;
                        }
                        for a in args.iter_mut() {
                            rewrite_expr(a, r, adapter)?;
                        }
                        let mut ps = ps.clone();
                        ps.push(GoTy::Any);
                        f.kind = GoExprKind::Ident(new_name.clone());
                        f.ty = GoTy::Func(ps, ret.clone());
                        args.push(ident(CTX, GoTy::Any));
                        return Some(());
                    }
                }
                rewrite_expr(f, r, adapter)?;
                for a in args {
                    rewrite_expr(a, r, adapter)?;
                }
            }
            GoExprKind::Ident(n) if r.contains_key(n) => return None,
            GoExprKind::GenericCall(n, _, args) => {
                if r.contains_key(n) {
                    return None;
                }
                for a in args {
                    rewrite_expr(a, r, adapter)?;
                }
            }
            GoExprKind::FuncLit(_, _, stmts) => {
                // Any direct selected reference in a latent closure denies the
                // route. Do not silently capture ctx in user event closures.
                if adapter {
                    for s in stmts {
                        rewrite_stmt(s, r, true)?;
                    }
                } else if contains_route(stmts, r) {
                    return None;
                }
            }
            GoExprKind::Block(stmts) => {
                for s in stmts {
                    rewrite_stmt(s, r, adapter)?;
                }
            }
            GoExprKind::Selector(e, _)
            | GoExprKind::TypeAssert(e, _)
            | GoExprKind::Coerce { inner: e, .. }
            | GoExprKind::Widen(e) => rewrite_expr(e, r, adapter)?,
            GoExprKind::Index(a, b) | GoExprKind::Binary(_, a, b) => {
                rewrite_expr(a, r, adapter)?;
                rewrite_expr(b, r, adapter)?;
            }
            GoExprKind::SliceLit(_, xs) => {
                for x in xs {
                    rewrite_expr(x, r, adapter)?;
                }
            }
            GoExprKind::StructLit(_, xs) => {
                for (_, x) in xs {
                    rewrite_expr(x, r, adapter)?;
                }
            }
            _ => {}
        }
        Some(())
    })
}
fn contains_route(stmts: &[GoStmt], r: &BTreeMap<String, (String, GoTy)>) -> bool {
    // Read-only iterative inventory. Never clone a whole latent function body
    // merely to ask whether it mentions a selected callee; that could multiply
    // clone work and inherit unprotected statement-clone recursion.
    enum Node<'a> {
        Expr(&'a GoExpr),
        Stmt(&'a GoStmt),
    }
    let mut todo: Vec<_> = stmts.iter().map(Node::Stmt).collect();
    while let Some(node) = todo.pop() {
        match node {
            Node::Expr(e) => match &e.kind {
                GoExprKind::Ident(n) if r.contains_key(n) => return true,
                GoExprKind::GenericCall(n, _, _) if r.contains_key(n) => return true,
                GoExprKind::Call(f, xs) => {
                    todo.push(Node::Expr(f));
                    todo.extend(xs.iter().map(Node::Expr));
                }
                GoExprKind::GenericCall(_, _, xs) | GoExprKind::SliceLit(_, xs) => {
                    todo.extend(xs.iter().map(Node::Expr))
                }
                GoExprKind::StructLit(_, xs) => todo.extend(xs.iter().map(|(_, x)| Node::Expr(x))),
                GoExprKind::Selector(x, _)
                | GoExprKind::TypeAssert(x, _)
                | GoExprKind::Widen(x)
                | GoExprKind::Coerce { inner: x, .. } => todo.push(Node::Expr(x)),
                GoExprKind::Index(a, b) | GoExprKind::Binary(_, a, b) => {
                    todo.push(Node::Expr(a));
                    todo.push(Node::Expr(b));
                }
                GoExprKind::FuncLit(_, _, ss) | GoExprKind::Block(ss) => {
                    todo.extend(ss.iter().map(Node::Stmt))
                }
                _ => {}
            },
            Node::Stmt(s) => match s {
                GoStmt::Expr(e)
                | GoStmt::Short(_, e)
                | GoStmt::Discard(e)
                | GoStmt::Assign(_, e)
                | GoStmt::Return(Some(e)) => todo.push(Node::Expr(e)),
                GoStmt::AssignField(a, _, b) => {
                    todo.push(Node::Expr(a));
                    todo.push(Node::Expr(b));
                }
                GoStmt::If(e, a, b) => {
                    todo.push(Node::Expr(e));
                    todo.extend(a.iter().chain(b).map(Node::Stmt));
                }
                GoStmt::IfTypeAssert { subj, then, .. } => {
                    todo.push(Node::Expr(subj));
                    todo.extend(then.iter().map(Node::Stmt));
                }
                GoStmt::Loop(ss) | GoStmt::Scope(ss) => todo.extend(ss.iter().map(Node::Stmt)),
                _ => {}
            },
        }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    fn f(n: &str, body: GoExpr) -> GoFuncDecl {
        GoFuncDecl {
            name: n.into(),
            type_params: vec![],
            params: vec![GoParam {
                name: "m".into(),
                ty: GoTy::Any,
            }],
            ret: GoTy::Any,
            body: vec![GoStmt::Return(Some(body))],
            doc: None,
        }
    }
    fn call(n: &str) -> GoExpr {
        GoExpr::new(
            GoExprKind::Call(
                Box::new(ident(n, GoTy::Func(vec![GoTy::Any], Box::new(GoTy::Any)))),
                vec![ident("m", GoTy::Any)],
            ),
            GoTy::Any,
        )
    }
    #[test]
    fn clones_exact_abi_and_keeps_originals_identical() {
        let originals = vec![
            f("view", call("editor")),
            f("editor", ident("m", GoTy::Any)),
        ];
        let frozen = originals.clone();
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        let (clones, adapter) = clone_view_route(&originals, &route, "view", &route).unwrap();
        assert_eq!(originals, frozen);
        assert_eq!(clones.len(), 2);
        assert!(clones.iter().all(|f| f.params.last().unwrap().name == CTX));
        assert_eq!(
            adapter.ty,
            GoTy::Func(vec![GoTy::Any, GoTy::Any], Box::new(GoTy::Any))
        );
    }
    #[test]
    fn denies_function_escape_and_abi_mismatch() {
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        for e in [
            ident("editor", GoTy::Any),
            GoExpr::new(
                GoExprKind::Call(
                    Box::new(ident("editor", GoTy::Any)),
                    vec![ident("m", GoTy::Any)],
                ),
                GoTy::Any,
            ),
        ] {
            let originals = vec![f("view", e), f("editor", ident("m", GoTy::Any))];
            assert!(clone_view_route(&originals, &route, "view", &route).is_none());
        }
    }
    #[test]
    fn denies_latent_callback_even_with_matching_actual_abi() {
        let latent = GoExpr::new(
            GoExprKind::FuncLit(
                vec![],
                GoTy::Any,
                vec![GoStmt::Return(Some(call("editor")))],
            ),
            GoTy::Any,
        );
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        assert!(clone_view_route(
            &[f("view", latent), f("editor", ident("m", GoTy::Any))],
            &route,
            "view",
            &route
        )
        .is_none());
    }
    #[test]
    fn owned_route_matches_borrowed_and_moves_body_allocations() {
        let originals = vec![
            f("view", call("editor")),
            f("editor", ident("m", GoTy::Any)),
            f("unused", ident("m", GoTy::Any)),
        ];
        let frozen = originals.clone();
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        let expected = clone_view_route(&originals, &route, "view", &route).unwrap();
        let scratch = originals.clone();
        let bodies: BTreeMap<_, _> = scratch
            .iter()
            .map(|f| (f.name.clone(), f.body.as_ptr()))
            .collect();
        let actual = clone_view_route_owned(scratch, &route, "view", &route).unwrap();
        assert_eq!(actual, expected);
        for clone in &actual.0 {
            let original = clone.name.strip_suffix("_sky_view_memo_private_").unwrap();
            // Pointer comparison only: no dereference after ownership transfer.
            // This would fail if the consumed route were recursively cloned.
            assert_eq!(clone.body.as_ptr(), bodies[original]);
        }
        assert_eq!(originals, frozen);
    }

    #[test]
    fn owned_failure_after_partial_rewrite_conserves_reference_functions() {
        let mut bad_call = call("editor");
        bad_call.ty = GoTy::Unit;
        let originals = vec![f("view", bad_call), f("editor", ident("m", GoTy::Any))];
        let frozen = originals.clone();
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        // editor sorts first and is mutated before view fails its return ABI.
        assert!(clone_view_route_owned(originals.clone(), &route, "view", &route).is_none());
        assert!(clone_view_route(&originals, &route, "view", &route).is_none());
        assert_eq!(originals, frozen);
    }

    #[test]
    fn owned_preflight_rejections_match_borrowed_without_mutating_reference() {
        let route = BTreeSet::from(["view".into(), "editor".into()]);
        let baseline = vec![
            f("view", call("editor")),
            f("editor", ident("m", GoTy::Any)),
        ];
        for case in 0..5 {
            let mut originals = baseline.clone();
            let mut selected = route.clone();
            let mut occupied = route.clone();
            match case {
                0 => {
                    selected.remove("view");
                }
                1 => {
                    originals.pop();
                }
                2 => {
                    originals[0].params.push(GoParam {
                        name: "extra".into(),
                        ty: GoTy::Any,
                    });
                }
                3 => {
                    occupied.insert("view_sky_view_memo_private_".into());
                }
                4 => {
                    originals[1].params[0].name = CTX.into();
                }
                _ => unreachable!(),
            }
            let frozen = originals.clone();
            assert!(clone_view_route(&originals, &selected, "view", &occupied).is_none());
            assert!(
                clone_view_route_owned(originals.clone(), &selected, "view", &occupied).is_none()
            );
            assert_eq!(originals, frozen);
        }
    }
}

/// Start from the EXISTING compiler SPA adapter, preserving its original model
/// assertion/EnumOf and actual call coercions. Only this compiler-owned adapter
/// permits traversal into generated eta wrappers; source clones do not.
pub(super) fn view_adapter(original: &GoExpr, root: &str, target: &GoExpr) -> Option<GoExpr> {
    let GoTy::Func(inputs, ret) = &target.ty else {
        return None;
    };
    if inputs.len() != 2 || inputs[1] != GoTy::Any {
        return None;
    }
    let GoExprKind::Ident(new) = &target.kind else {
        return None;
    };
    let actual = GoTy::Func(vec![inputs[0].clone()], ret.clone());
    let replacements = BTreeMap::from([(root.to_owned(), (new.clone(), actual))]);
    let mut copy = original.clone();
    let GoExprKind::FuncLit(params, out, body) = &mut copy.kind else {
        return None;
    };
    if params.len() != 1
        || params[0].ty != GoTy::Any
        || *out != GoTy::Any
        || !contains_route(body, &replacements)
    {
        return None;
    }
    for s in body {
        rewrite_stmt(s, &replacements, true)?;
    }
    params.push(GoParam {
        name: CTX.into(),
        ty: GoTy::Any,
    });
    copy.ty = GoTy::Func(vec![GoTy::Any, GoTy::Any], Box::new(GoTy::Any));
    Some(copy)
}

/// Structural IR visitor shared by final capability publication; no Raw text
/// rewriting, generated symbols or source expression strings.
pub(super) fn each_expr_mut(stmts: &mut [GoStmt], f: &mut impl FnMut(&mut GoExpr)) {
    base::ensure_stack(|| {
        fn expr(e: &mut GoExpr, f: &mut impl FnMut(&mut GoExpr)) {
            base::ensure_stack(|| {
                match &mut e.kind {
                    GoExprKind::Call(h, xs) => {
                        expr(h, f);
                        for x in xs {
                            expr(x, f);
                        }
                    }
                    GoExprKind::GenericCall(_, _, xs) | GoExprKind::SliceLit(_, xs) => {
                        for x in xs {
                            expr(x, f);
                        }
                    }
                    GoExprKind::StructLit(_, xs) => {
                        for (_, x) in xs {
                            expr(x, f);
                        }
                    }
                    GoExprKind::Selector(x, _)
                    | GoExprKind::TypeAssert(x, _)
                    | GoExprKind::Widen(x)
                    | GoExprKind::Coerce { inner: x, .. } => expr(x, f),
                    GoExprKind::Index(a, b) | GoExprKind::Binary(_, a, b) => {
                        expr(a, f);
                        expr(b, f);
                    }
                    GoExprKind::FuncLit(_, _, ss) | GoExprKind::Block(ss) => each_expr_mut(ss, f),
                    _ => {}
                }
                f(e);
            })
        }
        for s in stmts {
            match s {
                GoStmt::Expr(e)
                | GoStmt::Short(_, e)
                | GoStmt::Discard(e)
                | GoStmt::Assign(_, e)
                | GoStmt::Return(Some(e)) => expr(e, f),
                GoStmt::AssignField(a, _, b) => {
                    expr(a, f);
                    expr(b, f);
                }
                GoStmt::If(e, a, b) => {
                    expr(e, f);
                    each_expr_mut(a, f);
                    each_expr_mut(b, f);
                }
                GoStmt::IfTypeAssert { subj, then, .. } => {
                    expr(subj, f);
                    each_expr_mut(then, f);
                }
                GoStmt::Loop(ss) | GoStmt::Scope(ss) => each_expr_mut(ss, f),
                _ => {}
            }
        }
    })
}

#[cfg(test)]
mod adapter_tests {
    use super::*;
    #[test]
    fn preserves_actual_original_enum_model_dispatch_inside_compiler_adapter() {
        let model = GoTy::Named("Main_Model".into(), vec![]);
        let result = GoTy::Named("Std_Html_Html".into(), vec![]);
        let original_fn = GoTy::Func(vec![model.clone()], Box::new(result.clone()));
        let actual = GoExpr::new(
            GoExprKind::GenericCall(
                "rt.EnumOf".into(),
                vec![model.clone()],
                vec![ident("a0", GoTy::Any)],
            ),
            model.clone(),
        );
        let original_call = GoExpr::new(
            GoExprKind::Call(
                Box::new(ident("Main_view", original_fn)),
                vec![actual.clone()],
            ),
            GoTy::Any,
        );
        let original = GoExpr::new(
            GoExprKind::FuncLit(
                vec![GoParam {
                    name: "a0".into(),
                    ty: GoTy::Any,
                }],
                GoTy::Any,
                vec![GoStmt::Return(Some(original_call))],
            ),
            GoTy::Func(vec![GoTy::Any], Box::new(GoTy::Any)),
        );
        let frozen = original.clone();
        let target = ident(
            "Main_view_sky_view_memo_private_",
            GoTy::Func(vec![model, GoTy::Any], Box::new(result)),
        );
        let mut adapter = view_adapter(&original, "Main_view", &target).unwrap();
        assert_eq!(original, frozen);
        let GoExprKind::FuncLit(ps, _, ss) = &mut adapter.kind else {
            panic!()
        };
        assert_eq!(ps.len(), 2);
        let mut enum_args = 0;
        each_expr_mut(ss, &mut |e| {
            if *e == actual {
                enum_args += 1;
            }
        });
        assert_eq!(enum_args, 1, "reuse original EnumOf coercion exactly once");
    }
}

#[cfg(test)]
mod deep_stack_tests {
    use super::*;
    #[test]
    fn visits_and_rewrites_twenty_thousand_appends_on_small_worker_stack() {
        std::thread::Builder::new()
            .stack_size(2 * 1024 * 1024)
            .spawn(|| {
                let mut tree =
                    GoExpr::new(GoExprKind::StrLit(String::new()), GoTy::Bare(Prim::Str));
                for _ in 0..20_000 {
                    tree = GoExpr::new(
                        GoExprKind::Binary(
                            GoBin::Add,
                            Box::new(tree),
                            Box::new(GoExpr::new(
                                GoExprKind::StrLit("x".into()),
                                GoTy::Bare(Prim::Str),
                            )),
                        ),
                        GoTy::Bare(Prim::Str),
                    );
                }
                let mut body = vec![GoStmt::Return(Some(tree))];
                let empty = BTreeMap::new();
                assert!(!contains_route(&body, &empty));
                let mut visited = 0;
                each_expr_mut(&mut body, &mut |_| visited += 1);
                assert_eq!(visited, 40_001);
                for stmt in &mut body {
                    assert!(rewrite_stmt(stmt, &empty, false).is_some());
                }
                // The selected root's own deep body must clone/rewrite safely too.
                let original = GoFuncDecl {
                    name: "view".into(),
                    type_params: vec![],
                    params: vec![GoParam {
                        name: "m".into(),
                        ty: GoTy::Any,
                    }],
                    ret: GoTy::Any,
                    body,
                    doc: None,
                };
                let route = BTreeSet::from(["view".into()]);
                let (cloned, _) = clone_view_route(&[original], &route, "view", &route).unwrap();
                assert_eq!(cloned.len(), 1);
            })
            .unwrap()
            .join()
            .unwrap();
    }
}
