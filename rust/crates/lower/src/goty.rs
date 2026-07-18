//! `sky_ty_to_go` — the single Sky→Go type map (doc 07 §3). Total, returns
//! `GoTy`, never a string. Nominal user types (ADTs / record aliases) resolve
//! through the `TypeEnv` the lowerer builds from the program's type declarations.

use crate::ir::{GoTy, Prim};
use std::collections::HashMap;
use ty::Ty;

/// How a nominal type name renders in Go, plus its kind (for value lowering).
#[derive(Clone, Debug)]
pub struct Nominal {
    /// The Go type name as used in a type position, e.g. `Main_Model_R`,
    /// `Main_Msg`, `Sky_Core_Error_Error`.
    pub go_name: String,
    pub kind: NominalKind,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum NominalKind {
    Record,
    Adt,
    Iota,
}

/// The type environment the Sky→Go map consults: nominal type names + a field-set
/// index that resolves a *structural* record (what inference produces after
/// transparent alias expansion, `ty::sig::World::expand`) back to its named `_R`
/// alias — so records emit + assign as nominal Go types (the positional-ABI
/// contract, doc 08 §_fieldIndex).
#[derive(Default)]
pub struct TypeEnv {
    pub nominal: HashMap<String, Nominal>,
    /// sorted field-name list → the record alias's Go `_R` type name.
    pub record_fieldsets: HashMap<Vec<String>, String>,
}

/// Map a Sky type to its structural Go type.
pub fn sky_ty_to_go(t: &Ty, env: &TypeEnv) -> GoTy {
    match t {
        Ty::App(name, args) => app_to_go(name.as_str(), args, env),
        Ty::Fun(a, b) => {
            // Collapse the curried spine into an N-ary Go func.
            let mut params = vec![sky_ty_to_go(a, env)];
            let mut ret = b.as_ref();
            while let Ty::Fun(x, y) = ret {
                params.push(sky_ty_to_go(x, env));
                ret = y;
            }
            GoTy::Func(params, Box::new(sky_ty_to_go(ret, env)))
        }
        Ty::Tuple(xs) => GoTy::Tuple(xs.iter().map(|x| sky_ty_to_go(x, env)).collect()),
        Ty::Unit => GoTy::Unit,
        // A remaining rigid/flex var → generic erase to `any` (doc 07 §6 class 8).
        Ty::Var(_) => GoTy::Any,
        Ty::Record(fields, _) => {
            // resolve to a nominal `_R` alias when the field-name set matches one.
            let mut names: Vec<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            names.sort();
            if let Some(go_name) = env.record_fieldsets.get(&names) {
                return GoTy::Named(go_name.clone(), vec![]);
            }
            // else: anonymous Go struct. Field names Go-exported (capitalised) to
            // stay consistent with record-literal construction + field access.
            GoTy::Struct(
                fields
                    .iter()
                    .map(|(n, ft)| (base::Name::new(&cap(n.as_str())), sky_ty_to_go(ft, env)))
                    .collect(),
            )
        }
        Ty::Error => GoTy::Any,
    }
}

fn app_to_go(name: &str, args: &[Ty], env: &TypeEnv) -> GoTy {
    let go = |t: &Ty| sky_ty_to_go(t, env);
    match (name, args.len()) {
        ("Int", 0) => GoTy::Bare(Prim::Int),
        ("Float", 0) => GoTy::Bare(Prim::Float),
        ("String", 0) => GoTy::Bare(Prim::Str),
        ("Bool", 0) => GoTy::Bare(Prim::Bool),
        ("Char", 0) => GoTy::Bare(Prim::Rune),
        ("Bytes", 0) => GoTy::Bare(Prim::Bytes),
        // numeric-flex default → int (Sky's `number` defaults to Int).
        ("number", 0) => GoTy::Bare(Prim::Int),
        ("comparable" | "appendable" | "compappend", 0) => GoTy::Any,
        ("List", 1) => GoTy::Slice(Box::new(go(&args[0]))),
        ("Maybe", 1) => GoTy::Named("rt.SkyMaybe".into(), vec![go(&args[0])]),
        ("Result", 2) => GoTy::Named("rt.SkyResult".into(), vec![go(&args[0]), go(&args[1])]),
        ("Task", 2) => GoTy::Named("rt.SkyTask".into(), vec![go(&args[0]), go(&args[1])]),
        ("Dict", 2) => GoTy::Map(Box::new(go(&args[0])), Box::new(go(&args[1]))),
        ("Cmd", _) => GoTy::Any,
        ("Sub", _) => GoTy::Any,
        _ => {
            if let Some(n) = env.nominal.get(name) {
                GoTy::Named(n.go_name.clone(), args.iter().map(go).collect())
            } else {
                // Unknown nominal (FFI type, unmodelled) → erase to any.
                GoTy::Any
            }
        }
    }
}

fn cap(s: &str) -> String {
    let mut cs = s.chars();
    match cs.next() {
        Some(c) => c.to_uppercase().collect::<String>() + cs.as_str(),
        None => String::new(),
    }
}
