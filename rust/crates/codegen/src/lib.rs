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
    // L10a: whole-binary gob-type registration. A session's Model is persisted
    // via gob; a concrete type that only ever lives in an `any`-typed Model field
    // (nil at init) is invisible to the boot-time walk of the init VALUE, and
    // gob's name→type registry is process-local — so after a restart the new
    // process never registered it and decode fails, silently dropping the session
    // to memory. Listing every non-generic record struct + ADT variant struct
    // here gives EVERY process (encoder and, after a restart, decoder) that
    // registration at boot. Generic structs (`Foo_R[T]`) are skipped — they can't
    // be zero-valued without type args, and their concrete instantiations are
    // reachable from the Model's static type (covered by the boot walk). Sorted +
    // deduped for byte-stable (repro-safe) output. See rt.RegisterSkyGobTypes.
    let mut gob_types: Vec<String> = Vec::new();
    for it in items {
        if let GoItem::Type(name, def) = it {
            if name.contains('[') {
                continue; // generic — can't zero-value
            }
            match def {
                GoTypeDef::Struct(_) => gob_types.push(name.clone()),
                GoTypeDef::SealedIface(variants) => {
                    for (ctor, _tag, _fields) in variants {
                        gob_types.push(format!("{name}_{ctor}_V"));
                    }
                }
                _ => {}
            }
        }
    }
    gob_types.sort();
    gob_types.dedup();
    if !gob_types.is_empty() {
        let list: Vec<String> = gob_types.iter().map(|n| format!("{n}{{}}")).collect();
        w.line(&format!(
            "func init() {{ rt.RegisterSkyGobTypes([]any{{{}}}) }}",
            list.join(", ")
        ));
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
        GoTypeDef::RuntimeAlias(rt_ty) => {
            w.line(&format!("type {name} = {rt_ty}"));
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
            // Register the ordinal↔name mapping so Codec.auto can store this
            // enum as a readable name rather than its ordinal int.
            let names: Vec<String> = variants.iter().map(|v| format!("\"{v}\"")).collect();
            w.line(&format!(
                "func init() {{ rt.RegisterEnum(\"{name}\", []string{{{}}}) }}",
                names.join(", ")
            ));
        }
        GoTypeDef::Struct(fields) => {
            // Emit a `sky:"<field>,<declaredType>"` tag per field. The declared
            // Go type survives here even when it is an enum alias to `int` that
            // reflection would otherwise resolve away — this is the metadata
            // `Std.Codec.auto` reads to recover a field's true Sky type.
            let fs: Vec<String> = fields
                .iter()
                .map(|(n, t)| {
                    let ty = render_ty(t);
                    let sky_name = sky_field_name(n);
                    format!("{n} {ty} `sky:\"{sky_name},{ty}\"`")
                })
                .collect();
            w.line(&format!("type {name} struct {{ {} }}", fs.join("; ")));
        }
        GoTypeDef::Alias(t) => {
            w.line(&format!("type {name} = {}", render_ty(t)));
        }
    }
}

/// The Sky (camelCase) field name for a Go (PascalCase) struct field: lowercase
/// the first character. `PriceMinor` -> `priceMinor`, `Id` -> `id`.
fn sky_field_name(go_name: &str) -> String {
    let mut chars = go_name.chars();
    match chars.next() {
        Some(first) => first.to_lowercase().chain(chars).collect(),
        None => String::new(),
    }
}

// ---- statements ----------------------------------------------------------

fn emit_stmt(w: &mut Writer, st: &GoStmt) {
    match st {
        GoStmt::Expr(e) => w.line(&render_expr(e)),
        GoStmt::Short(name, e) => w.line(&format!("{name} := {}", render_expr(e))),
        GoStmt::Discard(e) => w.line(&format!("_ = {}", render_expr(e))),
        GoStmt::AssignField(base, field, val) => w.line(&format!(
            "{}.{} = {}",
            render_expr(base),
            field,
            render_expr(val)
        )),
        GoStmt::Assign(name, e) => w.line(&format!("{name} = {}", render_expr(e))),
        GoStmt::VarDecl(name, ty) => w.line(&format!("var {name} {}", render_ty(ty))),
        GoStmt::Loop(body) => {
            w.line("for {");
            w.indent += 1;
            for s in body {
                emit_stmt(w, s);
            }
            w.indent -= 1;
            w.line("}");
        }
        GoStmt::Continue => w.line("continue"),
        GoStmt::Return(None) => w.line("return"),
        GoStmt::Return(Some(e)) => w.line(&format!("return {}", render_expr(e))),
        GoStmt::Comment(c) => w.line(&format!("// {c}")),
        GoStmt::IfTypeAssert {
            binder,
            ok,
            subj,
            ty,
            then,
        } => {
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
            format!(
                "({} {} {})",
                render_expr(l),
                render_bin(*op),
                render_expr(r)
            )
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
            let call = narrow_call(to, &render_expr(inner));
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
            GoStmt::AssignField(base, field, val) => parts.push(format!(
                "{}.{} = {}",
                render_expr(base),
                field,
                render_expr(val)
            )),
            GoStmt::Assign(n, e) => parts.push(format!("{n} = {}", render_expr(e))),
            GoStmt::VarDecl(n, ty) => parts.push(format!("var {n} {}", render_ty(ty))),
            GoStmt::Loop(body) => parts.push(format!("for {{ {} }}", render_stmts_inline(body))),
            GoStmt::Continue => parts.push("continue".to_string()),
            GoStmt::Return(None) => parts.push("return".to_string()),
            GoStmt::Return(Some(e)) => parts.push(format!("return {}", render_expr(e))),
            GoStmt::Comment(c) => parts.push(format!("/* {c} */")),
            GoStmt::If(cond, then, els) => {
                let mut s = format!(
                    "if {} {{ {} }}",
                    render_expr(cond),
                    render_stmts_inline(then)
                );
                if !els.is_empty() {
                    s.push_str(&format!(" else {{ {} }}", render_stmts_inline(els)));
                }
                parts.push(s);
            }
            GoStmt::IfTypeAssert {
                binder,
                ok,
                subj,
                ty,
                then,
            } => {
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
    // Typed-tuple codegen: each element renders to
    // its concrete Go type, so `(String, Int)` emits `rt.T2[string, int]`. A
    // `GoTy::Any` element renders to `"any"` (via `render_ty`), so a floor /
    // type-var position stays `any` — partial typing, e.g. `rt.T2[any, int]`.
    // The runtime reflection sites (fst/snd/Dict.fromList) were hardened in
    // Phase 0 to accept these distinct nominal instantiations. (Mirror:
    // lower::render_goty.)
    match xs.len() {
        // Runtime has typed structs `rt.T2`..`rt.T9`; arity ≥10 is the
        // slice-backed `rt.SkyTupleN`. (Must match `lower::lower_tuple`'s
        // construct + pattern-access split at the same 9/10 boundary.)
        2..=9 => {
            let n = xs.len();
            let a: Vec<String> = xs.iter().map(render_ty).collect();
            format!("rt.T{n}[{}]", a.join(", "))
        }
        _ => "rt.SkyTupleN".to_string(),
    }
}

/// Render a reflection-free narrow of an `any`-typed `inner` expression to the
/// Go type `to`, choosing the typed fast-path helper per shape so emitted Go
/// carries no `reflect.Value` for shapes whose element/field types are
/// statically known here. Recurses into a struct target's fields. Falls back to
/// the reflect helper `rt.Coerce[T]` only for shapes with no typed
/// decomposition (and, inside the struct arm, only when the boxed source is
/// neither the canonical all-`any` form nor already the target).
fn narrow_call(to: &GoTy, inner: &str) -> String {
    match to {
        GoTy::Named(n, args) if n == "rt.SkyTask" && args.len() == 2 => format!(
            "rt.TaskCoerceT[{}, {}]({})",
            render_ty(&args[0]),
            render_ty(&args[1]),
            inner
        ),
        GoTy::Named(n, args) if n == "rt.SkyMaybe" && args.len() == 1 => {
            format!("rt.MaybeCoerce[{}]({})", render_ty(&args[0]), inner)
        }
        GoTy::Named(n, args) if n == "rt.SkyResult" && args.len() == 2 => {
            // A `Result e a` narrow reconstructs the Result by typed assertion
            // (`rt.ResultCoerceOk`) and narrows the Ok value with the
            // reflection-free narrow for `a` (e.g. `rt.AsListT[x]` for `List x`)
            // instead of reflect-narrowing the whole Result. The err type stays
            // `e`. An `a == any` payload needs no narrow — keep plain
            // `rt.ResultCoerce`, whose `SkyResult[e, any]` fast path is already
            // reflection-free.
            match &args[1] {
                GoTy::Any => format!(
                    "rt.ResultCoerce[{}, {}]({})",
                    render_ty(&args[0]),
                    render_ty(&args[1]),
                    inner
                ),
                ok => format!(
                    "rt.ResultCoerceOk[{}, {}]({}, func(_v any) {} {{ return {} }})",
                    render_ty(&args[0]),
                    render_ty(&args[1]),
                    inner,
                    render_ty(ok),
                    narrow_call(ok, "_v"),
                ),
            }
        }
        GoTy::Slice(t) => format!("rt.AsListT[{}]({})", render_ty(t), inner),
        // A Sky `Dict k v` is `map[string]V` at runtime; narrow via `rt.AsMapT`,
        // which REBUILDS the value-coerced `map[string]V` (Go map types are
        // invariant, so `rt.Coerce[map[…]…]` would assert the exact type + panic).
        GoTy::Map(_, v) => format!("rt.AsMapT[{}]({})", render_ty(v), inner),
        GoTy::Bare(Prim::Str) => format!("rt.AsString({})", inner),
        GoTy::Bare(Prim::Int) => format!("rt.AsInt({})", inner),
        GoTy::Bare(Prim::Bool) => format!("rt.AsBool({})", inner),
        GoTy::Bare(Prim::Float) => format!("rt.AsFloat({})", inner),
        GoTy::Func(ps, r) if ps.len() == 1 => {
            // Narrow to a 1-arg typed func target (a codec's `EncFields func(any)
            // []T`, a decoder, …) reflection-free. Two boxed source shapes occur
            // and both are handled by assertion, never `reflect.MakeFunc`:
            //   * the exact target func type (a func value boxed into `any` keeps
            //     its concrete Go func type) → return it directly;
            //   * the canonical boxed `func(any) any` (from `Ctx::widen`) → wrap
            //     it in an adapter that calls it and narrows the result to `R`.
            // `rt.Coerce` on a func target calls `adaptFuncValue` /
            // `reflect.MakeFunc` (unimplemented under TinyGo); it stays only as a
            // last-resort fallback for a genuinely divergent shape.
            let tgt = render_ty(to);
            let p0 = render_ty(&ps[0]);
            let rty = render_ty(r);
            let narrowed = narrow_call(r, "_g(any(_a0))");
            // `_s` binds through an explicit `any(...)` conversion: a boxed source
            // reaches here as `any`, but a point-free monomorphic value keeps its
            // CONCRETE Go func type (`rt.Basics_identity[any]` is `func(any) any`,
            // not an interface), and `_s.(T)` on a non-interface is a Go compile
            // error. The conversion is a no-op when `inner` is already `any`.
            format!(
                "func() {tgt} {{ _s := any({inner}); if _f, _ok := _s.({tgt}); _ok {{ return _f }}; if _g, _ok := _s.(func(any) any); _ok {{ return func(_a0 {p0}) {rty} {{ return {narrowed} }} }}; return rt.Coerce[{tgt}](_s) }}()"
            )
        }
        GoTy::Func(ps, r) if ps.len() >= 2 => {
            // A MULTI-arg func target (`func(A,B,C) R`, e.g. `Result.map3`'s
            // uncurried callback slot). A function VALUE reaches here boxed as the
            // canonical CURRIED `func(any) any` nest (`Ctx::widen` /
            // `lower_ctor_value`: `func(_p0)(func(_p1)(func(_p2)…))`), but the slot
            // is a FLAT N-ary Go func — the arity mismatch the reflect
            // `adaptFuncValueWithCapture` used to (mis-)bridge. Uncurry it
            // STATICALLY here — the target arity is known — so map3 calling
            // `v_0(a,b,c)` threads all N args through the curried source
            // reflection-free:
            //   func(_a0 A,_a1 B,_a2 C) R {
            //     return narrow_R( _c(any(_a0)).(func(any)any)(any(_a1)).(func(any)any)(any(_a2)) ) }
            // The exact-shape assertion still short-circuits an already-flat
            // source; reflect stays only as the last-resort divergent-shape path.
            let tgt = render_ty(to);
            let n = ps.len();
            let params = ps
                .iter()
                .enumerate()
                .map(|(i, p)| format!("_a{i} {}", render_ty(p)))
                .collect::<Vec<_>>()
                .join(", ");
            let mut app = String::from("_c");
            for i in 0..n {
                if i == 0 {
                    app = format!("{app}(any(_a0))");
                } else {
                    app = format!("({app}).(func(any) any)(any(_a{i}))");
                }
            }
            let rty = render_ty(r);
            let narrowed = narrow_call(r, &app);
            format!(
                "func() {tgt} {{ _s := any({inner}); if _f, _ok := _s.({tgt}); _ok {{ return _f }}; if _c, _ok := _s.(func(any) any); _ok {{ return func({params}) {rty} {{ return {narrowed} }} }}; return rt.Coerce[{tgt}](_s) }}()"
            )
        }
        GoTy::Func(_, _) => {
            // 0-arg func target (`func() T`): a curried nest never applies (only
            // arity ≥ 1 boxes), so a direct assertion recovers the exact-shape
            // boxed source reflection-free; reflect fallback only for a divergent
            // shape. `any(...)`: a concrete-typed func source is not an interface,
            // so assert through an explicit box. No-op when `inner` is already
            // `any`.
            let tgt = render_ty(to);
            format!(
                "func() {tgt} {{ if _f, _ok := (any({})).({tgt}); _ok {{ return _f }}; return rt.Coerce[{tgt}]({}) }}()",
                inner, inner
            )
        }
        GoTy::Struct(fs) if !fs.is_empty() => {
            // A structural (anonymous) record target — e.g. the applicative
            // `Std.Codec` record `{Dec, Enc, Shp}` pulled from its ADT bag. Every
            // anonymous record boxed into `any` is emitted as the canonical
            // all-`any` struct with field names SORTED (lower::lower_record's
            // all-`any` fallback), so narrow it with a reflection-free comma-ok
            // assertion to that shape, then rebuild the typed target field-by-
            // field (each field re-narrowed by this same function). Falls back to
            // the reflect `rt.Coerce` for any other boxed shape, so correctness
            // is preserved universally while the common codec/record path carries
            // no `reflect.Value`. The reflect form is unimplemented under TinyGo;
            // this closes it there.
            let tgt = render_ty(to);
            let mut sorted: Vec<&str> = fs.iter().map(|(n, _)| n.as_str()).collect();
            sorted.sort();
            let src_fields = sorted
                .iter()
                .map(|n| format!("{n} any"))
                .collect::<Vec<_>>()
                .join("; ");
            let assigns = fs
                .iter()
                .map(|(n, t)| {
                    let f = n.as_str();
                    format!("{f}: {}", narrow_call(t, &format!("_m.{f}")))
                })
                .collect::<Vec<_>>()
                .join(", ");
            format!(
                "func(_s any) {tgt} {{ if _m, _ok := _s.(struct{{ {src_fields} }}); _ok {{ return {tgt}{{{assigns}}} }}; return rt.Coerce[{tgt}](_s) }}({inner})"
            )
        }
        other => format!("rt.Coerce[{}]({})", render_ty(other), inner),
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

    // Regression (de-reflection 1430e4c0): a point-free MONOMORPHIC value keeps
    // its CONCRETE Go func type (`rt.Basics_identity[any]` is `func(any) any`,
    // not an interface), so the func-narrow adapter must bind `_s` through an
    // explicit `any(...)` — `_s.(T)` on a non-interface fails `go build`
    // ("invalid operation: _s ... is not an interface").
    #[test]
    fn narrow_to_func_boxes_source_through_any() {
        let one = GoTy::Func(vec![GoTy::Any], Box::new(GoTy::Any));
        let g1 = narrow_call(&one, "rt.Basics_identity[any]");
        assert!(
            g1.contains("_s := any(rt.Basics_identity[any])"),
            "1-arg func narrow must box _s through any(): {g1}"
        );
        // 0-arg target: no curried nest applies, but still box before asserting.
        let zero = GoTy::Func(vec![], Box::new(GoTy::Bare(Prim::Str)));
        let g0 = narrow_call(&zero, "src");
        assert!(
            g0.contains("(any(src))."),
            "0-arg func narrow must box through any(): {g0}"
        );
    }

    // Regression (de-reflection 1430e4c0, surfaced by 06-json `Result.map3`): a
    // function VALUE reaches a MULTI-arg callback slot boxed CURRIED
    // (`func(any) any` nest), but the slot is a flat N-ary Go func. The narrow
    // must UNCURRY statically at the target arity (reflection-free) — otherwise
    // the reflect fallback mis-adapts and map3's `v_0(a,b,c)` yields a leftover
    // `func(any) any` that panics `rt.Coerce[Profile]`.
    #[test]
    fn narrow_to_multiarg_func_uncurries_boxed_source() {
        let to = GoTy::Func(
            vec![GoTy::Any, GoTy::Any, GoTy::Any],
            Box::new(GoTy::Any),
        );
        let g = narrow_call(&to, "boxedCtor");
        assert!(
            g.contains("_s.(func(any, any, any) any)"),
            "exact-shape short-circuit for an already-flat source missing: {g}"
        );
        assert!(
            g.contains("func(_a0 any, _a1 any, _a2 any) any"),
            "uncurry closure at the target arity missing: {g}"
        );
        assert!(
            g.contains(".(func(any) any)(any(_a1))")
                && g.contains(".(func(any) any)(any(_a2))"),
            "curried-application chain (apply each arg through the nest) missing: {g}"
        );
        assert!(
            !g.contains("MakeFunc") && !g.contains("reflect."),
            "the uncurry must be reflection-free: {g}"
        );
    }
}
