//! Bind lines in `let` (`p <- e`): the resolver's split into segments
//! (docs/design/let-bind-lines.md §2, §4).
//!
//! `let { seg1 ; p <- e ; seg2 } in body` resolves to
//! `Let { seg1, Call(Var(BindAndThen), [Lambda([p], Let { seg2, body }), e]) }`.

use base::FileId;
use hir::{resolve, Body, Expr, Pattern, Res, ResolveResult, SourceDb};

fn resolve_main(src: &str) -> ResolveResult {
    let mut db = SourceDb::new();
    db.add_module("Main", syntax::parse(src, FileId(0)));
    resolve(&db, db.module_by_name("Main").expect("Main registered"))
}

fn module(body: &str) -> String {
    format!("module Main exposing (main)\n\n\n{body}\n\n\nmain =\n    0\n")
}

fn body_of<'a>(r: &'a ResolveResult, name: &str) -> &'a Body {
    let def = r
        .top_defs
        .iter()
        .find(|t| t.name.as_str() == name)
        .expect("def")
        .def;
    &r.bodies[&def]
}

fn messages(r: &ResolveResult, code: &str) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == code)
        .map(|d| d.message.clone())
        .collect()
}

fn no_errors(r: &ResolveResult) {
    let errs: Vec<_> = r
        .diagnostics
        .iter()
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect();
    assert!(errs.is_empty(), "{errs:?}");
}

/// The source byte offset of the `n`th `<-` in `src`.
fn larrow_at(src: &str, n: usize) -> u32 {
    src.match_indices("<-").nth(n).expect("larrow").0 as u32
}

#[test]
fn one_bind_line_splits_the_block_into_two_segments() {
    let src =
        module("go t =\n    let\n        a = 1\n        b <- t a\n        c = b\n    in\n    c");
    let r = resolve_main(&src);
    no_errors(&r);
    let body = body_of(&r, "go");
    let root = body.root.expect("root");
    let Expr::Let { defs, body: call } = &body.exprs[root] else {
        panic!("root is not a Let: {:?}", body.exprs[root]);
    };
    assert_eq!(defs.len(), 1, "seg1 is `a = 1` only");
    assert_eq!(defs[0].binders[0].0.as_str(), "a");
    let Expr::Call(f, args) = &body.exprs[*call] else {
        panic!("let body is not the bind Call: {:?}", body.exprs[*call]);
    };
    assert!(matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)));
    assert_eq!(args.len(), 2);
    let Expr::Lambda { params, body: cont } = &body.exprs[args[0]] else {
        panic!("first argument is not the continuation lambda");
    };
    assert_eq!(params.len(), 1);
    assert!(matches!(body.pats[params[0]], Pattern::Var(..)));
    let Expr::Let {
        defs: seg2,
        body: inner,
    } = &body.exprs[*cont]
    else {
        panic!("continuation body is not Let{{seg2, body}}");
    };
    assert_eq!(seg2.len(), 1);
    assert_eq!(seg2[0].binders[0].0.as_str(), "c");
    assert!(matches!(body.exprs[*inner], Expr::Var(Res::Local(_))));
    // `e` is the bound expression `t a`.
    assert!(matches!(body.exprs[args[1]], Expr::Call(..)));

    // Spans: the placeholder carries the `<-` token; the synthesised Call,
    // Lambda and inner Let carry none; the pattern keeps its own.
    let at = larrow_at(&src, 0);
    let sp = body.expr_span(*f).expect("placeholder span");
    assert_eq!(sp.range, (at, at + 2));
    assert!(
        body.expr_span(*call).is_none(),
        "synthesised Call has a span"
    );
    assert!(
        body.expr_span(args[0]).is_none(),
        "synthesised Lambda has a span"
    );
    assert!(
        body.expr_span(*cont).is_none(),
        "synthesised Let has a span"
    );
    let ps = body.pat_span(params[0]).expect("pattern span");
    assert_eq!(src[ps.range.0 as usize..ps.range.1 as usize].trim(), "b");

    // The LSP index records the bind line.
    assert_eq!(r.bind_occs.len(), 1);
    assert_eq!(r.bind_occs[0].span.range.0, at);
    assert_eq!(r.bind_occs[0].call, *call);
}

#[test]
fn a_block_whose_last_line_is_a_bind_line_continues_into_the_in_body() {
    let r = resolve_main(&module("go t =\n    let\n        x <- t\n    in\n    x"));
    no_errors(&r);
    let body = body_of(&r, "go");
    let Expr::Let { defs, body: call } = &body.exprs[body.root.unwrap()] else {
        panic!("root is not a Let");
    };
    assert!(defs.is_empty());
    let Expr::Call(_, args) = &body.exprs[*call] else {
        panic!("not a Call")
    };
    let Expr::Lambda { body: cont, .. } = &body.exprs[args[0]] else {
        panic!("not a Lambda")
    };
    // No empty Let wrapper: the continuation is the `in` body itself.
    assert!(matches!(body.exprs[*cont], Expr::Var(Res::Local(_))));
}

#[test]
fn two_bind_lines_nest() {
    let r = resolve_main(&module(
        "go t u =\n    let\n        a <- t\n        b <- u a\n    in\n    b",
    ));
    no_errors(&r);
    assert_eq!(r.bind_occs.len(), 2);
}

#[test]
fn every_irrefutable_pattern_shape_binds_its_names() {
    for line in [
        "_ <- t",
        "() <- t",
        "( a, b ) <- t",
        "{ a, b } <- t",
        "(( a, b ) as pair) <- t",
    ] {
        let r = resolve_main(&module(&format!(
            "go t =\n    let\n        {line}\n    in\n    0"
        )));
        no_errors(&r);
        assert_eq!(r.bind_occs.len(), 1, "{line}");
    }
    let r = resolve_main(&module(
        "go t =\n    let\n        ( a, b ) <- t\n        c = a + b\n    in\n    c",
    ));
    no_errors(&r);
}

#[test]
fn the_bound_expression_does_not_see_its_own_pattern() {
    let r = resolve_main(&module("go f =\n    let\n        x <- f x\n    in\n    x"));
    let got = messages(&r, "E1001");
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(got[0].contains("Undefined name: x"), "{}", got[0]);
}

#[test]
fn the_bound_expression_sees_the_segment_before_it() {
    let r = resolve_main(&module(
        "go f =\n    let\n        k = 1\n        x <- f k\n    in\n    x",
    ));
    no_errors(&r);
}

#[test]
fn a_reference_across_the_split_names_the_bind_line() {
    let r = resolve_main(&module(
        "go t =\n    let\n        a = c + 1\n        b <- t\n        c = b\n    in\n    a",
    ));
    let got = messages(&r, "E1001");
    assert!(
        got.iter().any(|m| m.contains("Undefined name: c")
            && m.contains("a bind line splits the block: move this definition above `b <- …`")),
        "{got:?}"
    );
    // The bound expression may not read a later name either.
    let r = resolve_main(&module(
        "go f =\n    let\n        b <- f c\n        c = 1\n    in\n    b",
    ));
    let got = messages(&r, "E1001");
    assert!(
        got.iter()
            .any(|m| m.contains("Undefined name: c") && m.contains("`b <- …`")),
        "{got:?}"
    );
}

#[test]
fn each_segment_is_still_one_recursive_group() {
    // Forward reference and mutual recursion inside one segment, as in a
    // `let` with no bind line.
    let r = resolve_main(&module(
        "go t =\n    let\n        x <- t\n        isEven n =\n            if n == 0 then\n                True\n\n            else\n                isOdd (n - 1)\n\n        isOdd n =\n            if n == 0 then\n                False\n\n            else\n                isEven (n - 1)\n        y = z\n        z = x\n    in\n    isEven y",
    ));
    no_errors(&r);
}

#[test]
fn a_local_andthen_is_not_a_name_lookup() {
    // `BindAndThen` is not resolved by name: a local `andThen` does not
    // change what the bind line resolves to.
    let r = resolve_main(&module(
        "go t =\n    let\n        andThen f m =\n            m\n        x <- t\n    in\n    x",
    ));
    no_errors(&r);
    let body = body_of(&r, "go");
    let call = r.bind_occs[0].call;
    let Expr::Call(f, _) = &body.exprs[call] else {
        panic!("not a Call")
    };
    assert!(matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)));
}
