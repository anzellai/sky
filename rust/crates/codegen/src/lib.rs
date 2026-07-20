#![forbid(unsafe_code)]
//! `codegen` — deterministic Go source emission from the typed Go-IR (doc 08).
//! One job: render. Never derives a type, resolves a name, or inserts a coercion
//! — those are finished in `lower`. `HashMap`-free: every collection walked here
//! arrives already ordered (L4).

use lower::ir::{
    GoBin, GoExpr, GoExprKind, GoFuncDecl, GoItem, GoParam, GoStmt, GoTy, GoTypeDef, Prim,
};

/// A thin append-only buffer (doc 08 §1). One growing `String`; O(n) total.
struct Writer {
    buf: String,
    indent: u32,
}

impl Writer {
    fn new() -> Self {
        Writer {
            buf: String::with_capacity(4096),
            indent: 0,
        }
    }
    fn pad(&mut self) {
        for _ in 0..self.indent {
            self.buf.push('\t');
        }
    }
    fn line(&mut self, s: &str) {
        self.pad();
        self.buf.push_str(s);
        self.buf.push('\n');
    }
    fn nl(&mut self) {
        self.buf.push('\n');
    }
}

/// Emit a full `main.go` for a program: package clause, `rt` import, then the
/// items in the order the lowerer produced them (DCE-filtered, deterministic).
///
/// `console_needed` — when true (the program imports `Std.Live.*` or
/// `Sky.Http.Server.*`, so its runtime auto-mounts `/_sky/console`), a blank
/// `_ "sky-app/rt/console_app"` import is added so the inline dev console's
/// `init()` cfg-provider registration links into the binary. The build driver
/// materialises `rt/console_app` for exactly these programs. False → the single
/// `import rt "sky-app/rt"` line is emitted byte-for-byte as before (a CLI /
/// Tui / Webview binary never links the console stack). Mirrors the oracle's
/// `consoleNeededFromImports` gate on `collectGoImports`.
pub fn emit_program(items: &[GoItem], console_needed: bool) -> String {
    let mut w = Writer::new();
    w.line("package main");
    w.nl();
    if console_needed {
        w.line("import (");
        w.line("\trt \"sky-app/rt\"");
        w.line("\t_ \"sky-app/rt/console_app\"");
        w.line(")");
    } else {
        w.line("import rt \"sky-app/rt\"");
    }
    w.nl();
    w.line("var _ = rt.AsInt");
    w.nl();
    for it in items {
        emit_item(&mut w, it);
        w.nl();
    }
    w.buf
}

fn emit_item(w: &mut Writer, it: &GoItem) {
    match it {
        GoItem::Func(f) => emit_func(w, f),
        GoItem::Type(name, def) => emit_type(w, name, def),
        GoItem::Var(name, ty, init) => {
            let mut s = format!("var {name} ");
            s.push_str(&render_ty(ty));
            if let Some(e) = init {
                s.push_str(" = ");
                s.push_str(&render_expr(e));
            }
            w.line(&s);
        }
        GoItem::Init(stmts) => {
            w.line("func init() {");
            w.indent += 1;
            for st in stmts {
                emit_stmt(w, st);
            }
            w.indent -= 1;
            w.line("}");
        }
        GoItem::Raw(s) => {
            w.line(s);
        }
    }
}

fn emit_func(w: &mut Writer, f: &GoFuncDecl) {
    if let Some(doc) = &f.doc {
        for l in doc.lines() {
            w.line(&format!("// {l}"));
        }
    }
    let mut sig = format!("func {}", f.name);
    if !f.type_params.is_empty() {
        sig.push('[');
        let tps: Vec<String> = f
            .type_params
            .iter()
            .map(|(n, c)| format!("{n} {}", render_ty(c)))
            .collect();
        sig.push_str(&tps.join(", "));
        sig.push(']');
    }
    sig.push('(');
    sig.push_str(&render_params(&f.params));
    sig.push(')');
    // `func main()` has no return type; everything else renders its return.
    if f.name != "main" {
        sig.push(' ');
        sig.push_str(&render_ty(&f.ret));
    }
    sig.push_str(" {");
    w.line(&sig);
    w.indent += 1;
    for st in &f.body {
        emit_stmt(w, st);
    }
    w.indent -= 1;
    w.line("}");
}

fn render_params(params: &[GoParam]) -> String {
    params
        .iter()
        .map(|p| format!("{} {}", p.name, render_ty(&p.ty)))
        .collect::<Vec<_>>()
        .join(", ")
}

fn emit_type(w: &mut Writer, name: &str, def: &GoTypeDef) {
    match def {
        GoTypeDef::AdtAlias => {
            w.line(&format!("type {name} = rt.SkyADT"));
        }
        GoTypeDef::SealedIface(variants) => {
            // The sealed interface every variant satisfies.
            w.line(&format!("type {name} interface {{"));
            w.indent += 1;
            w.line("SkyVariantTag() int");
            w.line("SkyVariantName() string");
            w.indent -= 1;
            w.line("}");
            // One concrete struct per variant + its two marker methods.
            for (ctor, tag, fields) in variants {
                let vstruct = format!("{name}_{ctor}_V");
                if fields.is_empty() {
                    w.line(&format!("type {vstruct} struct {{}}"));
                } else {
                    let fs: Vec<String> = fields
                        .iter()
                        .enumerate()
                        .map(|(i, t)| format!("V{i} {}", render_ty(t)))
                        .collect();
                    w.line(&format!("type {vstruct} struct {{ {} }}", fs.join("; ")));
                }
                w.line(&format!(
                    "func ({vstruct}) SkyVariantTag() int {{ return {tag} }}"
                ));
                w.line(&format!(
                    "func ({vstruct}) SkyVariantName() string {{ return \"{ctor}\" }}"
                ));
            }
        }
        GoTypeDef::IotaEnum(variants) => {
            w.line(&format!("type {name} = int"));
            w.line("const (");
            w.indent += 1;
            for (i, v) in variants.iter().enumerate() {
                if i == 0 {
                    w.line(&format!("{name}_{v} {name} = iota"));
                } else {
                    w.line(&format!("{name}_{v}"));
                }
            }
            w.indent -= 1;
            w.line(")");
        }
        GoTypeDef::Struct(fields) => {
            let fs: Vec<String> = fields
                .iter()
                .map(|(n, t)| format!("{n} {}", render_ty(t)))
                .collect();
            w.line(&format!("type {name} struct {{ {} }}", fs.join("; ")));
        }
        GoTypeDef::Alias(t) => {
            w.line(&format!("type {name} = {}", render_ty(t)));
        }
    }
}

// ---- statements ----------------------------------------------------------

fn emit_stmt(w: &mut Writer, st: &GoStmt) {
    match st {
        GoStmt::Expr(e) => w.line(&render_expr(e)),
        GoStmt::Short(name, e) => w.line(&format!("{name} := {}", render_expr(e))),
        GoStmt::Discard(e) => w.line(&format!("_ = {}", render_expr(e))),
        GoStmt::AssignField(base, field, val) => {
            w.line(&format!("{}.{} = {}", render_expr(base), field, render_expr(val)))
        }
        GoStmt::Return(None) => w.line("return"),
        GoStmt::Return(Some(e)) => w.line(&format!("return {}", render_expr(e))),
        GoStmt::Comment(c) => w.line(&format!("// {c}")),
        GoStmt::IfTypeAssert { binder, ok, subj, ty, then } => {
            w.line(&format!(
                "if {binder}, {ok} := {}.({}); {ok} {{",
                render_expr(subj),
                render_ty(ty)
            ));
            w.indent += 1;
            for s in then {
                emit_stmt(w, s);
            }
            w.indent -= 1;
            w.line("}");
        }
        GoStmt::If(cond, then, els) => {
            w.line(&format!("if {} {{", render_expr(cond)));
            w.indent += 1;
            for s in then {
                emit_stmt(w, s);
            }
            w.indent -= 1;
            if els.is_empty() {
                w.line("}");
            } else {
                w.line("} else {");
                w.indent += 1;
                for s in els {
                    emit_stmt(w, s);
                }
                w.indent -= 1;
                w.line("}");
            }
        }
    }
}

// ---- expressions ---------------------------------------------------------

/// Render an expression to a single-line string (statements own newlines).
pub fn render_expr(e: &GoExpr) -> String {
    match &e.kind {
        GoExprKind::Ident(n) => n.clone(),
        GoExprKind::IntLit(n) => n.to_string(),
        GoExprKind::FloatLit(f) => {
            let s = format!("{f}");
            if s.contains('.') || s.contains('e') {
                s
            } else {
                format!("{s}.0")
            }
        }
        GoExprKind::StrLit(s) => format!("\"{}\"", escape_go(s)),
        GoExprKind::BoolLit(b) => b.to_string(),
        GoExprKind::Nil => "nil".to_string(),
        GoExprKind::Call(f, args) => {
            let rendered: Vec<String> = args.iter().map(render_expr).collect();
            format!("{}({})", render_expr(f), rendered.join(", "))
        }
        GoExprKind::GenericCall(f, targs, args) => {
            let ts: Vec<String> = targs.iter().map(render_ty).collect();
            let rendered: Vec<String> = args.iter().map(render_expr).collect();
            format!("{}[{}]({})", f, ts.join(", "), rendered.join(", "))
        }
        GoExprKind::Selector(base, field) => format!("{}.{}", render_expr(base), field),
        GoExprKind::TypeAssert(base, ty) => format!("{}.({})", render_expr(base), render_ty(ty)),
        GoExprKind::Index(base, i) => format!("{}[{}]", render_expr(base), render_expr(i)),
        GoExprKind::SliceLit(elem, xs) => {
            let rendered: Vec<String> = xs.iter().map(render_expr).collect();
            format!("[]{}{{{}}}", render_ty(elem), rendered.join(", "))
        }
        GoExprKind::StructLit(name, fields) => {
            let fs: Vec<String> = fields
                .iter()
                .map(|(n, v)| format!("{n}: {}", render_expr(v)))
                .collect();
            format!("{name}{{{}}}", fs.join(", "))
        }
        GoExprKind::FuncLit(params, ret, body) => {
            let mut s = format!("func({}) {}", render_params(params), render_ty(ret));
            s.push_str(" { ");
            s.push_str(&render_stmts_inline(body));
            s.push_str(" }");
            s
        }
        GoExprKind::Binary(op, l, r) => {
            format!("({} {} {})", render_expr(l), render_bin(*op), render_expr(r))
        }
        GoExprKind::Block(stmts) => {
            let ret = render_ty(&e.ty);
            format!("func() {ret} {{ {} }}()", render_stmts_inline(stmts))
        }
        GoExprKind::Coerce {
            inner,
            from,
            to,
            reason,
        } => {
            if from == to {
                return render_expr(inner);
            }
            let comment = format!("/* {} */ ", reason.comment());
            let call = match to {
                GoTy::Named(n, args) if n == "rt.SkyTask" && args.len() == 2 => format!(
                    "rt.TaskCoerceT[{}, {}]({})",
                    render_ty(&args[0]),
                    render_ty(&args[1]),
                    render_expr(inner)
                ),
                GoTy::Named(n, args) if n == "rt.SkyMaybe" && args.len() == 1 => {
                    format!("rt.MaybeCoerce[{}]({})", render_ty(&args[0]), render_expr(inner))
                }
                GoTy::Named(n, args) if n == "rt.SkyResult" && args.len() == 2 => format!(
                    "rt.ResultCoerce[{}, {}]({})",
                    render_ty(&args[0]),
                    render_ty(&args[1]),
                    render_expr(inner)
                ),
                GoTy::Slice(t) => {
                    format!("rt.AsListT[{}]({})", render_ty(t), render_expr(inner))
                }
                // A Sky `Dict k v` is `map[string]V` at runtime; narrow an `any`
                // (`rt.Dict_empty()`, an untyped kernel return) via `rt.AsMapT[V]`,
                // which REBUILDS the value-coerced `map[string]V` (matching the
                // oracle). `rt.Coerce[map[…]…]` would assert the exact Go map type
                // and panic (Go map types are invariant).
                GoTy::Map(_, v) => {
                    format!("rt.AsMapT[{}]({})", render_ty(v), render_expr(inner))
                }
                GoTy::Bare(Prim::Str) => format!("rt.AsString({})", render_expr(inner)),
                GoTy::Bare(Prim::Int) => format!("rt.AsInt({})", render_expr(inner)),
                GoTy::Bare(Prim::Bool) => format!("rt.AsBool({})", render_expr(inner)),
                GoTy::Bare(Prim::Float) => format!("rt.AsFloat({})", render_expr(inner)),
                other => format!("rt.Coerce[{}]({})", render_ty(other), render_expr(inner)),
            };
            format!("{comment}{call}")
        }
        GoExprKind::Widen(inner) => format!("any({})", render_expr(inner)),
    }
}

fn render_stmts_inline(body: &[GoStmt]) -> String {
    let mut parts: Vec<String> = Vec::new();
    for s in body {
        match s {
            GoStmt::Expr(e) => parts.push(render_expr(e)),
            GoStmt::Short(n, e) => parts.push(format!("{n} := {}", render_expr(e))),
            GoStmt::Discard(e) => parts.push(format!("_ = {}", render_expr(e))),
            GoStmt::AssignField(base, field, val) => {
                parts.push(format!("{}.{} = {}", render_expr(base), field, render_expr(val)))
            }
            GoStmt::Return(None) => parts.push("return".to_string()),
            GoStmt::Return(Some(e)) => parts.push(format!("return {}", render_expr(e))),
            GoStmt::Comment(c) => parts.push(format!("/* {c} */")),
            GoStmt::If(cond, then, els) => {
                let mut s = format!("if {} {{ {} }}", render_expr(cond), render_stmts_inline(then));
                if !els.is_empty() {
                    s.push_str(&format!(" else {{ {} }}", render_stmts_inline(els)));
                }
                parts.push(s);
            }
            GoStmt::IfTypeAssert { binder, ok, subj, ty, then } => {
                parts.push(format!(
                    "if {binder}, {ok} := {}.({}); {ok} {{ {} }}",
                    render_expr(subj),
                    render_ty(ty),
                    render_stmts_inline(then)
                ));
            }
        }
    }
    parts.join("; ")
}

fn render_bin(op: GoBin) -> &'static str {
    match op {
        GoBin::Add => "+",
        GoBin::Sub => "-",
        GoBin::Mul => "*",
        GoBin::Eq => "==",
        GoBin::Ne => "!=",
        GoBin::Lt => "<",
        GoBin::Gt => ">",
        GoBin::Le => "<=",
        GoBin::Ge => ">=",
        GoBin::And => "&&",
        GoBin::Or => "||",
    }
}

// ---- types ---------------------------------------------------------------

/// The single place a `GoTy` becomes text (doc 08 §2, `emit_ty`).
pub fn render_ty(t: &GoTy) -> String {
    match t {
        GoTy::Bare(p) => p.go_name().to_string(),
        GoTy::Unit => "struct{}".to_string(),
        GoTy::Any => "any".to_string(),
        GoTy::Named(n, args) if args.is_empty() => n.clone(),
        GoTy::Named(n, args) => {
            let a: Vec<String> = args.iter().map(render_ty).collect();
            format!("{n}[{}]", a.join(", "))
        }
        GoTy::Slice(t) => format!("[]{}", render_ty(t)),
        GoTy::Map(k, v) => format!("map[{}]{}", render_ty(k), render_ty(v)),
        GoTy::Func(ps, r) => {
            let a: Vec<String> = ps.iter().map(render_ty).collect();
            format!("func({}) {}", a.join(", "), render_ty(r))
        }
        GoTy::Tuple(xs) => render_tuple_ty(xs),
        GoTy::TyVar(n) => n.clone(),
        GoTy::Struct(fs) => {
            let parts: Vec<String> = fs
                .iter()
                .map(|(n, t)| format!("{} {}", n.as_str(), render_ty(t)))
                .collect();
            format!("struct{{ {} }}", parts.join("; "))
        }
    }
}

fn render_tuple_ty(xs: &[GoTy]) -> String {
    // Tuples render with erased `any` element types — the runtime standardises
    // on `rt.T2[any,any]` / `rt.T3[…]` (`SkyTuple2`) and its reflection paths
    // assert that shape. Concrete element types survive on the GoTy only for
    // pattern-bind coercion. (Mirror: lower::render_goty.)
    match xs.len() {
        // Runtime has typed structs `rt.T2`..`rt.T9`; arity ≥10 is the
        // slice-backed `rt.SkyTupleN`. (Must match `lower::lower_tuple`'s
        // construct + pattern-access split at the same 9/10 boundary.)
        2..=9 => {
            let n = xs.len();
            let a: Vec<String> = xs.iter().map(|_| "any".to_string()).collect();
            format!("rt.T{n}[{}]", a.join(", "))
        }
        _ => "rt.SkyTupleN".to_string(),
    }
}

/// Port of `Builder.escapeGo` (doc 08 §4): Go strings are UTF-8; printable
/// Unicode passes through, C0 controls escape.
fn escape_go(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_call() {
        let e = GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(
                    GoExprKind::Ident("rt.Log_println".into()),
                    GoTy::Any,
                )),
                vec![GoExpr::new(
                    GoExprKind::StrLit("hi".into()),
                    GoTy::Bare(Prim::Str),
                )],
            ),
            GoTy::Any,
        );
        assert_eq!(render_expr(&e), "rt.Log_println(\"hi\")");
    }
}
