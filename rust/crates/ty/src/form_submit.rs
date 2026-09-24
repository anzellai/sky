//! **`[E2010]` — the form-submit handler check.**
//!
//! `onSubmit` keeps a deliberately permissive signature in all three places it
//! is exposed:
//!
//! * `Std.Html.Events.onSubmit : a -> Attribute msg`
//! * `Std.Ui.onSubmit : a -> Attribute b`
//! * `Std.Ui.Events.onSubmit` (the same shape)
//!
//! The argument is either a plain Msg (dispatched as is) or a handler that the
//! runtime calls with the submitted form. `a` is free because HM cannot say
//! "a Msg OR a record -> Msg". That freedom let programs through that can never
//! work: `onSubmit 42` built and panicked on submit, a `String -> Msg` handler
//! received the text `map[x:ex]`, and a record field the form cannot fill (a
//! `List`, a nested record) was zero-filled.
//!
//! What every client really delivers (Sky.Live `__skyExtractArgs`, Sky.Spa
//! `spaFormData`, the terminal form) is a map from control name to a STRING
//! value, with an unchecked checkbox left out. The runtime decodes that map
//! strictly into the handler's record (`runtime-go/rt/form_decode.go`): a
//! String field takes the text, an Int / Float field parses it, a Bool field is
//! True for a checked box and False when it is absent, and a `Maybe` of those is
//! `Nothing` when the control is absent or empty. So the argument must be one of:
//!
//! 1. a Msg value (not a function), or
//! 2. a one-argument function `R -> Msg` whose `R` is a record of
//!    form-decodable fields (String, Int, Float, Bool, `Maybe` of those), or
//!    `Dict String String` (the raw field map).
//!
//! Anything else is rejected here, at the call site, with `[E2010]`.
//!
//! # Fail-open on what the checker cannot see
//!
//! A type variable anywhere this check reads (a polymorphic helper's `msg`, a
//! lambda `\_ -> Save` whose parameter is never constrained, a field of unknown
//! type) is ACCEPTED. The runtime decoder is the backstop there, and it fails
//! loudly (a classified decode error), never with a zero value. Over-rejecting
//! a working program is worse than the runtime error it would prevent.

use crate::Ty;
use base::Span;
use hir::{Body, Expr, ExprId, Res, SkyDb};
use std::collections::HashMap;

/// Is `(module, func)` one of the `onSubmit` builders?
fn is_on_submit(module: &str, func: &str) -> bool {
    func == "onSubmit" && matches!(module, "Std.Html.Events" | "Std.Ui" | "Std.Ui.Events")
}

/// Resolve a callee to `(module, func)` when it names a def or kernel.
pub(crate) fn callee_name(callee: &Expr, sky: &dyn SkyDb) -> Option<(String, String)> {
    match callee {
        Expr::Var(Res::Def(def)) => {
            let loc = sky.def_loc(*def)?;
            Some((
                sky.module_name(loc.module).to_string(),
                loc.name.as_str().to_string(),
            ))
        }
        Expr::Var(Res::Kernel { module, func }) => {
            Some((module.as_str().to_string(), func.as_str().to_string()))
        }
        _ => None,
    }
}

/// Every `(callee, first-arg, whole-call)` application in `body`: a direct
/// call `f x …`, and the pipes `x |> f` / `f <| x`.
pub(crate) fn applications(body: &Body) -> Vec<(ExprId, Vec<ExprId>, ExprId)> {
    let mut out = Vec::new();
    for (eid, expr) in body.exprs.iter() {
        match expr {
            Expr::Call(callee, args) => out.push((*callee, args.clone(), eid)),
            Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "|>" => {
                out.push((*rhs, vec![*lhs], eid));
            }
            Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "<|" => {
                out.push((*lhs, vec![*rhs], eid));
            }
            _ => {}
        }
    }
    out
}

/// Does `t` contain no type variable (and no error sentinel)?
pub(crate) fn is_ground(t: &Ty) -> bool {
    match t {
        Ty::Var(_) | Ty::Error => false,
        Ty::Unit => true,
        Ty::Fun(a, b) => is_ground(a) && is_ground(b),
        Ty::App(_, xs) | Ty::Tuple(xs) => xs.iter().all(is_ground),
        Ty::Record(fs, ext) => ext.is_none() && fs.iter().all(|(_, t)| is_ground(t)),
    }
}

/// Could `a` and `b` be the same type? A variable matches anything, so this
/// only says "no" when both sides are concrete at the point they differ.
pub(crate) fn compatible(a: &Ty, b: &Ty) -> bool {
    match (a, b) {
        (Ty::Var(_), _) | (_, Ty::Var(_)) | (Ty::Error, _) | (_, Ty::Error) => true,
        (Ty::Unit, Ty::Unit) => true,
        (Ty::Fun(a1, b1), Ty::Fun(a2, b2)) => compatible(a1, a2) && compatible(b1, b2),
        (Ty::App(n1, x1), Ty::App(n2, x2)) => {
            (n1 == n2 || crate::nominal::base(n1.as_str()) == crate::nominal::base(n2.as_str()))
                && x1.len() == x2.len()
                && x1.iter().zip(x2).all(|(p, q)| compatible(p, q))
        }
        (Ty::Tuple(x1), Ty::Tuple(x2)) => {
            x1.len() == x2.len() && x1.iter().zip(x2).all(|(p, q)| compatible(p, q))
        }
        (Ty::Record(f1, e1), Ty::Record(f2, e2)) => {
            let m2: HashMap<&str, &Ty> = f2.iter().map(|(n, t)| (n.as_str(), t)).collect();
            let m1: HashMap<&str, &Ty> = f1.iter().map(|(n, t)| (n.as_str(), t)).collect();
            for (n, t) in f1 {
                match m2.get(n.as_str()) {
                    Some(u) if !compatible(t, u) => return false,
                    None if e2.is_none() => return false,
                    _ => {}
                }
            }
            for (n, _) in f2 {
                if !m1.contains_key(n.as_str()) && e1.is_none() {
                    return false;
                }
            }
            true
        }
        _ => false,
    }
}

fn prim(t: &Ty) -> Option<&str> {
    match t {
        Ty::App(n, xs) if xs.is_empty() => Some(crate::nominal::base(n.as_str())),
        _ => None,
    }
}

/// Is `t` a value a form control can fill? `None` = yes (or unknown); `Some`
/// = the reason it cannot.
fn field_problem(t: &Ty) -> Option<String> {
    if matches!(t, Ty::Var(_) | Ty::Error) {
        return None;
    }
    if let Some(p) = prim(t) {
        if matches!(p, "String" | "Int" | "Float" | "Bool") {
            return None;
        }
    }
    if let Ty::App(n, xs) = t {
        if crate::nominal::base(n.as_str()) == "Maybe" && xs.len() == 1 {
            let inner = &xs[0];
            if matches!(inner, Ty::Var(_) | Ty::Error) {
                return None;
            }
            if let Some(p) = prim(inner) {
                if matches!(p, "String" | "Int" | "Float" | "Bool") {
                    return None;
                }
            }
        }
    }
    Some(t.render_pretty())
}

/// Why this `onSubmit` argument can never work, if it cannot.
fn problem(arg: &Ty, msg: Option<&Ty>) -> Option<String> {
    match arg {
        Ty::Var(_) | Ty::Error => None,
        Ty::Fun(param, result) => {
            if matches!(result.as_ref(), Ty::Fun(..)) {
                return Some(format!(
                    "the handler `{}` takes more than one argument, but a form submit \
                     calls it with one value (the form's fields)",
                    arg.render_pretty()
                ));
            }
            if let Some(m) = msg {
                if !compatible(result, m) {
                    return Some(format!(
                        "the handler returns `{}`, but this attribute's message type is `{}`",
                        result.render_pretty(),
                        m.render_pretty()
                    ));
                }
            }
            match param.as_ref() {
                Ty::Var(_) | Ty::Error => None,
                Ty::Record(fields, _) => {
                    let bad: Vec<String> = fields
                        .iter()
                        .filter_map(|(n, t)| {
                            field_problem(t).map(|r| format!("`{}` : {r}", n.as_str()))
                        })
                        .collect();
                    if bad.is_empty() {
                        None
                    } else {
                        Some(format!(
                            "a form fills each record field from the control with the same \
                             name, as a String, Int, Float, Bool or a Maybe of those. These \
                             fields cannot be filled from a form: {}",
                            bad.join(", ")
                        ))
                    }
                }
                Ty::App(n, xs)
                    if crate::nominal::base(n.as_str()) == "Dict"
                        && xs.len() == 2
                        && xs.iter().all(|x| {
                            matches!(x, Ty::Var(_) | Ty::Error) || prim(x) == Some("String")
                        }) =>
                {
                    None
                }
                other => Some(format!(
                    "the handler takes `{}`, but a form submit delivers a record of the \
                     form's fields (e.g. `{{ email : String, age : Int }}`), or a \
                     `Dict String String` of them",
                    other.render_pretty()
                )),
            }
        }
        _ => {
            if !is_ground(arg) {
                return None;
            }
            match msg {
                Some(m) if is_ground(m) => {
                    if compatible(arg, m) {
                        None
                    } else {
                        Some(format!(
                            "`{}` is not this attribute's message type `{}`",
                            arg.render_pretty(),
                            m.render_pretty()
                        ))
                    }
                }
                // The attribute's message type is not pinned here (onSubmit's
                // result is decoupled from its argument). A message is a custom
                // type, so a primitive, record, tuple or list can never be one.
                _ => {
                    let not_msg = match arg {
                        Ty::Record(..) | Ty::Tuple(..) | Ty::Unit => true,
                        Ty::App(n, _) => matches!(
                            crate::nominal::base(n.as_str()),
                            "Int" | "Float" | "String" | "Bool" | "Char" | "List" | "Dict"
                        ),
                        _ => false,
                    };
                    not_msg.then(|| {
                        format!(
                            "`{}` is not a message: pass a Msg, or a function from the \
                             form's record to a Msg",
                            arg.render_pretty()
                        )
                    })
                }
            }
        }
    }
}

/// One offending `onSubmit` call.
#[derive(Clone, Debug)]
pub struct FormSubmitFinding {
    pub def_name: String,
    pub span: Option<Span>,
    pub reason: String,
}

#[derive(Default)]
pub struct FormSubmitScan {
    pub found: Vec<FormSubmitFinding>,
}

/// Scan one body for `onSubmit` applications whose argument cannot work.
pub fn scan_body(
    body: &Body,
    expr_ty: &HashMap<ExprId, Ty>,
    sky: &dyn SkyDb,
    def_name: &str,
    out: &mut FormSubmitScan,
) {
    for (callee, args, call) in applications(body) {
        let Some((module, func)) = callee_name(&body.exprs[callee], sky) else {
            continue;
        };
        if !is_on_submit(&module, &func) {
            continue;
        }
        let Some(&arg) = args.first() else { continue };
        let Some(arg_ty) = expr_ty.get(&arg) else {
            continue;
        };
        // The whole application's type is `Attribute m`; `m` is the message.
        let msg = match expr_ty.get(&call) {
            Some(Ty::App(_, xs)) if args.len() == 1 => xs.last(),
            _ => None,
        };
        if let Some(reason) = problem(arg_ty, msg) {
            let span = body.expr_span(arg).or_else(|| body.expr_span(call));
            if !out
                .found
                .iter()
                .any(|f| f.def_name == def_name && f.span == span)
            {
                out.found.push(FormSubmitFinding {
                    def_name: def_name.to_string(),
                    span,
                    reason,
                });
            }
        }
    }
}

pub fn message(reason: &str) -> String {
    format!(
        "this `onSubmit` handler can never receive a form submit: {reason}. A form \
         submit delivers the named controls' values as text, which the runtime \
         decodes into the handler's record."
    )
}

pub fn suggestion() -> String {
    "pass a Msg (`onSubmit Save`), or a constructor that takes a record of String / \
     Int / Float / Bool / Maybe fields named like the form's inputs (`onSubmit \
     SignIn` with `SignIn : { email : String, age : Int } -> Msg`)."
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::Name;

    fn int() -> Ty {
        Ty::app("Int", vec![])
    }
    fn string() -> Ty {
        Ty::app("String", vec![])
    }
    fn msg() -> Ty {
        Ty::app("Main.Msg", vec![])
    }
    fn rec(fs: Vec<(&str, Ty)>) -> Ty {
        Ty::Record(
            fs.into_iter().map(|(n, t)| (Name::new(n), t)).collect(),
            None,
        )
    }
    fn fun(a: Ty, b: Ty) -> Ty {
        Ty::Fun(Box::new(a), Box::new(b))
    }

    #[test]
    fn plain_msg_and_typed_record_are_accepted() {
        assert!(problem(&msg(), Some(&msg())).is_none());
        let r = rec(vec![
            ("title", string()),
            ("age", int()),
            ("agree", Ty::app("Bool", vec![])),
            ("note", Ty::app("Maybe", vec![string()])),
        ]);
        assert!(problem(&fun(r, msg()), Some(&msg())).is_none());
        let dict = Ty::app("Dict", vec![string(), string()]);
        assert!(problem(&fun(dict, msg()), Some(&msg())).is_none());
    }

    #[test]
    fn a_number_or_a_string_handler_is_rejected() {
        assert!(problem(&int(), Some(&msg())).is_some());
        assert!(problem(&int(), None).is_some());
        assert!(problem(&fun(string(), msg()), Some(&msg())).is_some());
    }

    #[test]
    fn a_non_form_field_is_rejected() {
        let r = rec(vec![("tags", Ty::app("List", vec![string()]))]);
        assert!(problem(&fun(r, msg()), Some(&msg())).is_some());
    }

    #[test]
    fn unknowns_are_accepted() {
        assert!(problem(&Ty::var("t3"), Some(&msg())).is_none());
        assert!(problem(&fun(Ty::var("t4"), msg()), None).is_none());
        let r = rec(vec![("x", Ty::var("t5"))]);
        assert!(problem(&fun(r, msg()), None).is_none());
    }
}
