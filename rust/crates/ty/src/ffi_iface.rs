//! **`[E2013]` — a Sky value passed where a Go interface is required.**
//!
//! A Go-FFI parameter of a non-empty interface type (`io.Writer`,
//! `http.Handler`) is `goi@…` in the pinned signature (surface format 3). The
//! signature gives it an unconstrained parameter type, because HM cannot say
//! "any Go type that implements `io.Writer`": a Go value of another nominal
//! may well implement it (`*os.File`, `*bytes.Buffer`, a `*mux.Router` for
//! `http.Handler`), and the inspector's `implements` map does not cover every
//! pair (it only sees the interfaces of the packages a surface imports, and
//! it does not relate one interface to another).
//!
//! What IS decidable is the Sky side, at every position the pinned signature
//! puts an interface (a parameter, a `List`/`Maybe` element, a tuple component,
//! a callback's result: [`crate::ffi_sig::IfaceSlot`]). A Sky value (a String, an Int, a List,
//! a record, a union, a function) is a Go value of a type the program itself
//! defines, with no method the Go interface could require: it can never
//! implement it. So after solving, an argument whose type is a concrete Sky
//! type is rejected here. An argument that is a Go value (`go@…`) or not yet
//! known (a type variable) is left to the wrapper, which asserts the
//! interface inside its guard (`rt.FfiArg`): a Go value that does not
//! implement it is an `Err` the program handles, never a crash.
//!
//! Call forms checked here: a direct or curried application and a pipe (`w |>
//! Pkg.f x`), with a message that names the call and the interface. A binding
//! passed as a value, bound to a name, or applied through a higher-order
//! function is checked by the unifier instead: `Infer::foreign_ref` gives each
//! interface slot a variable with the `GoValue` bound
//! ([`bound_iface_slots`]), the bound survives generalisation, and a Sky shape
//! meeting it is the same `[E2013]` (`unify::go_value_failure`). A type
//! variable stays accepted: the wrapper's run-time assertion makes a Go value
//! that does not implement the interface an `Err`.

use crate::ffi_sig::IfaceSlot;
use crate::Ty;
use base::Span;
use hir::{Body, Expr, ExprId, Res, SkyDb};
use std::collections::HashMap;

/// One offending argument.
#[derive(Clone, Debug)]
pub struct IfaceFinding {
    pub def_name: String,
    pub span: Option<Span>,
    /// `Pkg.fn` as the program spells the call's target.
    pub call: String,
    /// The Go interface key (`Io.Writer`).
    pub iface: String,
    /// The Sky type given.
    pub given: Ty,
}

#[derive(Default)]
pub struct IfaceScan {
    pub found: Vec<IfaceFinding>,
}

/// Is `t` a concrete Sky type (never a Go value, never undetermined)?
fn is_sky_value_type(t: &Ty) -> bool {
    match t {
        Ty::Var(_) | Ty::Error => false,
        Ty::App(n, _) => !crate::nominal::is_go_type(n.as_str()),
        Ty::Fun(..) | Ty::Record(..) | Ty::Tuple(_) | Ty::Unit => true,
    }
}

/// Every saturated-or-partial application of a Go-FFI function in `body`:
/// `(package, name, arguments in order, the application's expr)`. Flattens
/// curried calls (`(Pkg.f a) b`) and pipes (`w |> Pkg.f a`, `Pkg.f a <| w`).
fn foreign_applications(body: &Body) -> Vec<(String, String, Vec<ExprId>, ExprId)> {
    let mut out = Vec::new();
    for (eid, _) in body.exprs.iter() {
        let mut args: Vec<ExprId> = Vec::new();
        let mut cur = eid;
        // Peel the outermost application layers, innermost arguments first.
        loop {
            match &body.exprs[cur] {
                Expr::Call(callee, xs) => {
                    let mut xs = xs.clone();
                    xs.extend(args);
                    args = xs;
                    cur = *callee;
                }
                Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "|>" && cur == eid => {
                    args.push(*lhs);
                    cur = *rhs;
                }
                Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "<|" && cur == eid => {
                    args.push(*rhs);
                    cur = *lhs;
                }
                _ => break,
            }
        }
        if cur == eid || args.is_empty() {
            continue;
        }
        if let Expr::Var(Res::Foreign { package, name }) = &body.exprs[cur] {
            out.push((
                package.as_str().to_string(),
                name.as_str().to_string(),
                args,
                eid,
            ));
        }
    }
    out
}

/// The interface keys a solved argument type violates at `slot`: each place
/// the slot is a Go interface and the type there is a concrete Sky type.
fn violations(slot: &IfaceSlot, t: &Ty, out: &mut Vec<(String, Ty)>) {
    match (slot, t) {
        (IfaceSlot::None, _) => {}
        (IfaceSlot::Iface(k), t) => {
            if is_sky_value_type(t) {
                out.push((k.clone(), t.clone()));
            }
        }
        (IfaceSlot::Elem(s), Ty::App(_, args)) => {
            if let Some(last) = args.last() {
                violations(s, last, out);
            }
        }
        (IfaceSlot::Tuple(ss), Ty::Tuple(ts)) => {
            for (s, t) in ss.iter().zip(ts) {
                violations(s, t, out);
            }
        }
        (IfaceSlot::CallbackResult(n, s), t) => {
            let mut cur = t;
            for _ in 0..*n {
                match cur {
                    Ty::Fun(_, r) => cur = r,
                    _ => return,
                }
            }
            violations(s, cur, out);
        }
        _ => {}
    }
}

/// Rewrite a Go FFI binding's type so every interface position of its
/// parameters is a variable carrying the `GoValue` bound (`govalue_<n>`, one
/// fresh name per slot). The positions are those [`IfaceSlot`] names; a slot
/// whose type is not a variable (a signature that pinned it) is left as is.
pub(crate) fn bound_iface_slots(ty: &Ty, slots: &[IfaceSlot]) -> Ty {
    fn mark(t: &Ty, slot: &IfaceSlot, n: &mut usize) -> Ty {
        match (slot, t) {
            (IfaceSlot::None, _) => t.clone(),
            (IfaceSlot::Iface(_), Ty::Var(_)) => {
                *n += 1;
                Ty::var(&format!("govalue_{n}"))
            }
            (IfaceSlot::Elem(s), Ty::App(name, args)) if !args.is_empty() => {
                let mut args = args.clone();
                let last = args.len() - 1;
                args[last] = mark(&args[last], s, n);
                Ty::App(name.clone(), args)
            }
            (IfaceSlot::Tuple(ss), Ty::Tuple(ts)) => Ty::Tuple(
                ts.iter()
                    .enumerate()
                    .map(|(i, x)| match ss.get(i) {
                        Some(s) => mark(x, s, n),
                        None => x.clone(),
                    })
                    .collect(),
            ),
            (IfaceSlot::CallbackResult(k, s), t) => {
                fn under(t: &Ty, k: usize, s: &IfaceSlot, n: &mut usize) -> Ty {
                    match t {
                        Ty::Fun(a, b) if k > 0 => {
                            Ty::Fun(a.clone(), Box::new(under(b, k - 1, s, n)))
                        }
                        _ if k == 0 => mark(t, s, n),
                        _ => t.clone(),
                    }
                }
                under(t, *k, s, n)
            }
            _ => t.clone(),
        }
    }
    let mut n = 0;
    let mut cur = ty;
    let mut params = Vec::new();
    for _ in 0..slots.len() {
        match cur {
            Ty::Fun(a, b) => {
                params.push(a.as_ref().clone());
                cur = b;
            }
            _ => return ty.clone(),
        }
    }
    let mut out = cur.clone();
    let marked: Vec<Ty> = params
        .iter()
        .zip(slots)
        .map(|(p, s)| mark(p, s, &mut n))
        .collect();
    for p in marked.into_iter().rev() {
        out = Ty::Fun(Box::new(p), Box::new(out));
    }
    out
}

pub fn scan_body(
    body: &Body,
    expr_ty: &HashMap<ExprId, Ty>,
    sky: &dyn SkyDb,
    def_name: &str,
    out: &mut IfaceScan,
) {
    // An inner layer of a curried application is also an application; keep
    // only the outermost one per argument span.
    for (package, name, args, call) in foreign_applications(body) {
        let Some(sig) = sky.ffi_fn(&package, &name) else {
            continue;
        };
        let Some(slots) = crate::ffi_sig::iface_params(&sig.sky_type, sig.arity) else {
            continue;
        };
        for (arg, slot) in args.iter().zip(slots) {
            let Some(given) = expr_ty.get(arg) else {
                continue;
            };
            let mut bad = Vec::new();
            violations(&slot, given, &mut bad);
            for (iface, given) in bad {
                let span = body.expr_span(*arg).or_else(|| body.expr_span(call));
                if out
                    .found
                    .iter()
                    .any(|f| f.def_name == def_name && f.span == span)
                {
                    continue;
                }
                out.found.push(IfaceFinding {
                    def_name: def_name.to_string(),
                    span,
                    call: format!("{}.{}", crate::nominal::base(&package), name),
                    iface,
                    given,
                });
            }
        }
    }
}

pub fn message(f: &IfaceFinding) -> String {
    format!(
        "`{}` needs a Go value that implements the Go interface `{}`, but it is given a \
         Sky `{}`. Since v0.27.0 a Go interface parameter is checked: a Sky value \
         never implements a Go interface. Fix: pass a Go value that implements it \
         (one a binding returns). see docs/migration/v0.27.md#ffi-go-interface-params",
        f.call,
        crate::nominal::base(&f.iface),
        f.given.render_pretty()
    )
}

pub fn suggestion() -> String {
    "Pass a Go value that implements the interface, such as one a binding of the \
     same package returns."
        .to_string()
}
