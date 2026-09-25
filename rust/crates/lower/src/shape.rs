//! Go-shape passes: IR-to-IR rewrites that change only the SHAPE of the emitted
//! Go, never its meaning, so the Go toolchain compiles it with less memory.
//!
//! # Tail-block flattening
//!
//! Sky's `if`, `case` and `let … in` are expressions; Go's are statements. The
//! lowerer therefore lowers each to a typed IIFE (`GoExprKind::Block`,
//! rendered `func() T { … }()`). When such an expression is the RESULT of a
//! function (or of an enclosing IIFE), the IIFE is `return func() T { … }()`,
//! and an `else if` chain or a `case` whose branches are themselves `case`s
//! nests one IIFE per level.
//!
//! The Go compiler inlines every directly-called closure. Inlining a closure
//! copies its body into the caller, and a nested chain is copied once per
//! enclosing level, each copy with its own closure symbols whose names repeat
//! their parents' names. Measured on go1.26.1: a generated `update` with 344
//! such IIFEs produced 7,871 closure bodies for escape analysis from 492
//! function literals in the source, and the console's 23-deep `else if` tag
//! dispatch produced 50 MB symbol names and a 4.8 GB compile unless inlining
//! was turned off for the package.
//!
//! `return func() T { stmts }()` is exactly `stmts` in place of the `return`
//! when `T` is the enclosing function's result type: every `return` inside the
//! IIFE returns a `T`, and the IIFE body is a terminating statement list (Go
//! rejects it otherwise), so control never falls out of it. The pass splices
//! those statements in, inside a bare `{ … }` scope when they declare locals
//! (so a name cannot collide with one already declared in the enclosing list).
//! It applies recursively, so a whole `if`/`case` tree in tail position becomes
//! one statement tree with no closures at all. An IIFE whose type differs from
//! the enclosing result type (an implicit conversion to an interface, say) is
//! left alone, as is one in any non-tail position (an argument, a `:=`).

use crate::ir::{GoExpr, GoExprKind, GoItem, GoStmt, GoTy};

/// Flatten every tail-position IIFE in the program (see the module docs).
pub fn flatten_tail_blocks(items: &mut [GoItem]) {
    for it in items.iter_mut() {
        match it {
            GoItem::Func(f) => {
                // `func main()` renders with no result type, so a value `return`
                // can never be spliced there; walk it for nested bodies only.
                let ret = (f.name != "main").then(|| f.ret.clone());
                let body = std::mem::take(&mut f.body);
                f.body = stmts(body, ret.as_ref());
            }
            GoItem::Var(_, _, Some(e)) => expr(e),
            GoItem::Init(ss) => {
                let body = std::mem::take(ss);
                *ss = stmts(body, None);
            }
            GoItem::Var(_, _, None) | GoItem::Type(_, _) | GoItem::Raw(_) => {}
        }
    }
}

/// Rewrite a statement list whose `return`s produce `ret` (`None`: the list
/// is not a value-returning body, so no `return` is spliced).
fn stmts(body: Vec<GoStmt>, ret: Option<&GoTy>) -> Vec<GoStmt> {
    let mut out = Vec::with_capacity(body.len());
    for s in body {
        stmt(s, ret, &mut out);
    }
    out
}

/// True when the list declares a name in ITS OWN scope (nested blocks have
/// their own scopes and do not count).
fn declares(body: &[GoStmt]) -> bool {
    body.iter()
        .any(|s| matches!(s, GoStmt::Short(_, _) | GoStmt::VarDecl(_, _)))
}

fn stmt(s: GoStmt, ret: Option<&GoTy>, out: &mut Vec<GoStmt>) {
    match s {
        GoStmt::Return(Some(e)) => match (e.kind, ret) {
            (GoExprKind::Block(inner), Some(r)) if e.ty == *r => {
                let inner = stmts(inner, ret);
                if declares(&inner) {
                    out.push(GoStmt::Scope(inner));
                } else {
                    out.extend(inner);
                }
            }
            (kind, _) => {
                let mut e = GoExpr::new(kind, e.ty);
                expr(&mut e);
                out.push(GoStmt::Return(Some(e)));
            }
        },
        GoStmt::Expr(mut e) => {
            expr(&mut e);
            out.push(GoStmt::Expr(e));
        }
        GoStmt::Short(n, mut e) => {
            expr(&mut e);
            out.push(GoStmt::Short(n, e));
        }
        GoStmt::Discard(mut e) => {
            expr(&mut e);
            out.push(GoStmt::Discard(e));
        }
        GoStmt::AssignField(mut base, f, mut val) => {
            expr(&mut base);
            expr(&mut val);
            out.push(GoStmt::AssignField(base, f, val));
        }
        GoStmt::Assign(n, mut e) => {
            expr(&mut e);
            out.push(GoStmt::Assign(n, e));
        }
        GoStmt::Loop(b) => out.push(GoStmt::Loop(stmts(b, ret))),
        GoStmt::If(mut c, t, e) => {
            expr(&mut c);
            out.push(GoStmt::If(c, stmts(t, ret), stmts(e, ret)));
        }
        GoStmt::IfTypeAssert {
            binder,
            ok,
            mut subj,
            ty,
            then,
        } => {
            expr(&mut subj);
            out.push(GoStmt::IfTypeAssert {
                binder,
                ok,
                subj,
                ty,
                then: stmts(then, ret),
            });
        }
        GoStmt::Scope(b) => out.push(GoStmt::Scope(stmts(b, ret))),
        s @ (GoStmt::VarDecl(_, _)
        | GoStmt::Continue
        | GoStmt::Return(None)
        | GoStmt::Comment(_)) => out.push(s),
    }
}

/// Walk an expression and flatten the bodies of the closures and IIFEs inside
/// it, each against its OWN result type.
fn expr(e: &mut GoExpr) {
    match &mut e.kind {
        GoExprKind::FuncLit(_, ret, body) => {
            let ret = ret.clone();
            let b = std::mem::take(body);
            *body = stmts(b, Some(&ret));
        }
        GoExprKind::Block(body) => {
            let b = std::mem::take(body);
            *body = stmts(b, Some(&e.ty));
        }
        GoExprKind::Call(f, args) => {
            expr(f);
            args.iter_mut().for_each(expr);
        }
        GoExprKind::GenericCall(_, _, args) | GoExprKind::SliceLit(_, args) => {
            args.iter_mut().for_each(expr)
        }
        GoExprKind::StructLit(_, fields) => fields.iter_mut().for_each(|(_, v)| expr(v)),
        GoExprKind::Selector(b, _) | GoExprKind::TypeAssert(b, _) | GoExprKind::Widen(b) => expr(b),
        GoExprKind::Index(b, i) => {
            expr(b);
            expr(i);
        }
        GoExprKind::Binary(_, l, r) => {
            expr(l);
            expr(r);
        }
        GoExprKind::Coerce { inner, .. } => expr(inner),
        GoExprKind::Ident(_)
        | GoExprKind::IntLit(_)
        | GoExprKind::FloatLit(_)
        | GoExprKind::StrLit(_)
        | GoExprKind::BoolLit(_)
        | GoExprKind::Nil => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ir::{GoBin, GoFuncDecl, GoParam, Prim};

    fn int() -> GoTy {
        GoTy::Bare(Prim::Int)
    }
    fn id(n: &str, t: GoTy) -> GoExpr {
        GoExpr::new(GoExprKind::Ident(n.into()), t)
    }
    fn lit(n: i64) -> GoExpr {
        GoExpr::new(GoExprKind::IntLit(n), int())
    }
    fn cond() -> GoExpr {
        GoExpr::new(
            GoExprKind::Binary(GoBin::Eq, Box::new(id("x", int())), Box::new(lit(0))),
            GoTy::Bare(Prim::Bool),
        )
    }
    fn block(ss: Vec<GoStmt>, t: GoTy) -> GoExpr {
        GoExpr::new(GoExprKind::Block(ss), t)
    }
    /// `if x == 0 { return a } else { return <rest> }` as a typed IIFE.
    fn if_block(a: GoExpr, rest: GoExpr) -> GoExpr {
        block(
            vec![GoStmt::If(
                cond(),
                vec![GoStmt::Return(Some(a))],
                vec![GoStmt::Return(Some(rest))],
            )],
            int(),
        )
    }
    fn func(ret: GoTy, body: Vec<GoStmt>) -> GoItem {
        GoItem::Func(GoFuncDecl {
            name: "F".into(),
            type_params: vec![],
            params: vec![GoParam {
                name: "x".into(),
                ty: int(),
            }],
            ret,
            body,
            doc: None,
        })
    }
    fn body(it: &GoItem) -> &Vec<GoStmt> {
        match it {
            GoItem::Func(f) => &f.body,
            _ => unreachable!(),
        }
    }
    fn count_blocks_s(ss: &[GoStmt]) -> usize {
        ss.iter()
            .map(|s| match s {
                GoStmt::Return(Some(e)) | GoStmt::Expr(e) | GoStmt::Short(_, e) => count_blocks(e),
                GoStmt::If(c, t, e) => count_blocks(c) + count_blocks_s(t) + count_blocks_s(e),
                GoStmt::Scope(b) | GoStmt::Loop(b) => count_blocks_s(b),
                _ => 0,
            })
            .sum()
    }
    fn count_blocks(e: &GoExpr) -> usize {
        match &e.kind {
            GoExprKind::Block(ss) => 1 + count_blocks_s(ss),
            GoExprKind::FuncLit(_, _, ss) => count_blocks_s(ss),
            GoExprKind::Call(f, a) => count_blocks(f) + a.iter().map(count_blocks).sum::<usize>(),
            _ => 0,
        }
    }

    #[test]
    fn a_nested_else_if_chain_in_tail_position_has_no_iife_left() {
        // if … else if … else if … else — 30 levels, each an IIFE in the
        // enclosing else branch (the shape `Std.Ui`'s tag dispatch lowers to).
        let mut e = lit(-1);
        for i in 0..30 {
            e = if_block(lit(i), e);
        }
        let mut items = vec![func(int(), vec![GoStmt::Return(Some(e))])];
        assert_eq!(count_blocks_s(body(&items[0])), 30);
        flatten_tail_blocks(&mut items);
        assert_eq!(count_blocks_s(body(&items[0])), 0, "{:?}", body(&items[0]));
    }

    #[test]
    fn a_block_that_declares_keeps_its_own_scope() {
        let b = block(
            vec![
                GoStmt::Short("_subj".into(), id("x", int())),
                GoStmt::Return(Some(id("_subj", int()))),
            ],
            int(),
        );
        let mut items = vec![func(int(), vec![GoStmt::Return(Some(b))])];
        flatten_tail_blocks(&mut items);
        match &body(&items[0])[..] {
            [GoStmt::Scope(inner)] => assert!(matches!(inner[0], GoStmt::Short(_, _))),
            other => panic!("expected one scope, got {other:?}"),
        }
    }

    #[test]
    fn a_block_of_another_type_or_outside_tail_position_is_kept() {
        // Result type differs (`any` function, `int` IIFE): Go converts the IIFE
        // value to the interface at the `return`; splicing could change an
        // untyped constant's dynamic type, so it stays.
        let mut items = vec![func(
            GoTy::Any,
            vec![GoStmt::Return(Some(if_block(lit(1), lit(2))))],
        )];
        flatten_tail_blocks(&mut items);
        assert_eq!(count_blocks_s(body(&items[0])), 1);
        // Non-tail: `v := func() int {…}()` keeps its IIFE, but the IIFE's own
        // tail chain is flattened into it.
        let inner = if_block(lit(1), if_block(lit(2), lit(3)));
        let mut items = vec![func(
            int(),
            vec![
                GoStmt::Short("v".into(), inner),
                GoStmt::Return(Some(id("v", int()))),
            ],
        )];
        flatten_tail_blocks(&mut items);
        assert_eq!(count_blocks_s(body(&items[0])), 1);
    }

    #[test]
    fn a_closure_body_is_flattened_against_its_own_result_type() {
        let lam = GoExpr::new(
            GoExprKind::FuncLit(
                vec![],
                int(),
                vec![GoStmt::Return(Some(if_block(lit(1), lit(2))))],
            ),
            GoTy::Func(vec![], Box::new(int())),
        );
        let mut items = vec![func(
            GoTy::Any,
            vec![GoStmt::Return(Some(GoExpr::new(
                GoExprKind::Widen(Box::new(lam)),
                GoTy::Any,
            )))],
        )];
        flatten_tail_blocks(&mut items);
        let s = format!("{:?}", body(&items[0]));
        assert!(!s.contains("Block"), "{s}");
    }
}
