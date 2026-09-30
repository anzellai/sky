//! **`[E2012]` — a Sky value passed where a Go interface is required.**
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
//! What IS decidable is the Sky side. A Sky value (a String, an Int, a List,
//! a record, a union, a function) is a Go value of a type the program itself
//! defines, with no method the Go interface could require: it can never
//! implement it. So after solving, an argument whose type is a concrete Sky
//! type is rejected here. An argument that is a Go value (`go@…`) or not yet
//! known (a type variable) is left to the wrapper, which asserts the
//! interface inside its guard (`rt.FfiArg`): a Go value that does not
//! implement it is an `Err` the program handles, never a crash.

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

pub fn scan_body(
    body: &Body,
    expr_ty: &HashMap<ExprId, Ty>,
    sky: &dyn SkyDb,
    def_name: &str,
    out: &mut IfaceScan,
) {
    for (callee, args, call) in crate::form_submit::applications(body) {
        let Expr::Var(Res::Foreign { package, name }) = &body.exprs[callee] else {
            continue;
        };
        let Some(sig) = sky.ffi_fn(package.as_str(), name.as_str()) else {
            continue;
        };
        let Some(slots) = crate::ffi_sig::iface_params(&sig.sky_type, sig.arity) else {
            continue;
        };
        for (arg, slot) in args.iter().zip(slots) {
            let Some(iface) = slot else { continue };
            let Some(given) = expr_ty.get(arg) else {
                continue;
            };
            if !is_sky_value_type(given) {
                continue;
            }
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
                call: format!(
                    "{}.{}",
                    crate::nominal::base(package.as_str()),
                    name.as_str()
                ),
                iface,
                given: given.clone(),
            });
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
