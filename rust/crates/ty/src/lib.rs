#![forbid(unsafe_code)]
//! `ty` — HM inference, arena union-find, generalisation, exhaustiveness, the
//! per-def type table (doc 02, doc 06).
//!
//! M3. The interned/canonical [`Ty`] is what flows on schemes, what a region/def
//! maps to, and what read-back produces; [`TyVarId`] (in [`unify`]) is the
//! transient union-find variable used only during one inference run. The four
//! load-bearing soundness behaviours from `Sky.Type.*` are reproduced: the
//! wildcard-`any` per-occurrence gate ([`is_polymorphic`]), records + row
//! polymorphism ([`unify::UnionFind::unify`]), the FFI interface-satisfaction
//! axiom (folded into nominal `App` unify + the implements map in [`sig`]), and
//! exhaustiveness ([`exhaustive`]).

use base::Name;

mod check;
mod db;
mod exhaustive;
mod infer;
mod sig;
mod unify;

pub use check::{check_modules, BodyTypes, CheckOutput, DefType, TypeErrorKind, Typer};
pub use db::{compute_body_types, TyDb};
pub use sig::{
    record_alias_fields, update_base_defs, variant_arg_types, variant_arg_types_qualified, World,
};
pub use unify::{SuperType, UnionFind};

/// An arena-allocated type-variable id. Replaces `UF.Point` pointer identity
/// with a plain integer compared by `==` (doc 06, L3).
#[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub struct TyVarId(pub u32);

/// A canonical / read-back type — what flows on schemes and out of inference.
/// Mirrors `Sky.AST.Canonical.Type`. `Var("any")` is the per-occurrence
/// wildcard (see [`is_polymorphic`]).
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
pub enum Ty {
    /// A quantifier / rigid type-variable *name* at the canonical level.
    Var(Name),
    /// `a -> b`.
    Fun(Box<Ty>, Box<Ty>),
    /// Nominal application `Name a b …` (home folded into the name for
    /// accept-parity).
    App(Name, Vec<Ty>),
    /// Row-polymorphic record: sorted fields + optional extension var name.
    Record(Vec<(Name, Ty)>, Option<Name>),
    Tuple(Vec<Ty>),
    Unit,
    /// L7 error-recovery sentinel. Unifies with anything, suppresses cascades.
    Error,
}

impl Ty {
    pub fn app(name: &str, args: Vec<Ty>) -> Ty {
        Ty::App(Name::new(name), args)
    }
    pub fn var(name: &str) -> Ty {
        Ty::Var(Name::new(name))
    }

    /// Free type-variable names, in first-seen order (deterministic, L4).
    pub fn free_vars(&self) -> Vec<Name> {
        let mut out = Vec::new();
        self.free_vars_into(&mut out);
        out
    }

    fn free_vars_into(&self, out: &mut Vec<Name>) {
        match self {
            Ty::Var(n) => {
                if !out.contains(n) {
                    out.push(n.clone());
                }
            }
            Ty::Fun(a, b) => {
                a.free_vars_into(out);
                b.free_vars_into(out);
            }
            Ty::App(_, args) | Ty::Tuple(args) => {
                for a in args {
                    a.free_vars_into(out);
                }
            }
            Ty::Record(fields, ext) => {
                for (_, t) in fields {
                    t.free_vars_into(out);
                }
                if let Some(e) = ext {
                    if !out.contains(e) {
                        out.push(e.clone());
                    }
                }
            }
            Ty::Unit | Ty::Error => {}
        }
    }

    /// A compact rendering for spot-checks / hover (not the emission form).
    pub fn render(&self) -> String {
        match self {
            Ty::Var(n) => n.as_str().to_string(),
            Ty::Unit => "()".to_string(),
            Ty::Error => "?".to_string(),
            Ty::Fun(a, b) => {
                let lhs = match a.as_ref() {
                    Ty::Fun(_, _) => format!("({})", a.render()),
                    _ => a.render(),
                };
                format!("{lhs} -> {}", b.render())
            }
            Ty::App(n, args) if args.is_empty() => n.as_str().to_string(),
            Ty::App(n, args) => {
                let parts: Vec<String> = args
                    .iter()
                    .map(|a| match a {
                        Ty::App(_, xs) if !xs.is_empty() => format!("({})", a.render()),
                        Ty::Fun(_, _) => format!("({})", a.render()),
                        _ => a.render(),
                    })
                    .collect();
                format!("{} {}", n.as_str(), parts.join(" "))
            }
            Ty::Tuple(xs) => {
                let parts: Vec<String> = xs.iter().map(Ty::render).collect();
                format!("( {} )", parts.join(", "))
            }
            Ty::Record(fields, ext) => {
                let parts: Vec<String> = fields
                    .iter()
                    .map(|(n, t)| format!("{} : {}", n.as_str(), t.render()))
                    .collect();
                match ext {
                    Some(e) => format!("{{ {} | {} }}", e.as_str(), parts.join(", ")),
                    None => format!("{{ {} }}", parts.join(", ")),
                }
            }
        }
    }
}

/// A HM type scheme: quantified vars + body. Generalisation is via annotations
/// (Sky does not rank-generalise), so `vars` are the annotation's free vars
/// minus `"any"` (doc 06 §"Generalisation & instantiation").
///
/// `PartialEq`/`Eq` are load-bearing for salsa **backdating** of the
/// `type_world` tracked query (Stage D-2): a body-only edit re-executes
/// `World::build` but the resulting schemes are value-equal, so salsa backdates
/// the world and dependent `infer(DefId)` queries validate from memo instead of
/// re-executing. See `skydb::type_world_query`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Scheme {
    pub vars: Vec<Name>,
    pub ty: Ty,
}

impl Scheme {
    /// A monomorphic scheme (no quantifiers).
    pub fn mono(ty: Ty) -> Self {
        Scheme {
            vars: Vec::new(),
            ty,
        }
    }

    /// Generalise a type over its free vars (except `"any"`).
    pub fn generalize(ty: Ty) -> Self {
        let vars = ty
            .free_vars()
            .into_iter()
            .filter(|v| v.as_str() != "any")
            .collect();
        Scheme { vars, ty }
    }
}

/// The one true polymorphism predicate (doc 06 §"Wildcard-`any`, per
/// occurrence"). Do NOT replace with `!free.is_empty()`: a `Cfg -> msg` whose
/// only free var is `any` is **not** polymorphic — treating it as polymorphic
/// diverges body↔caller vars under per-call-site re-instantiation and accepts
/// wrong return types.
pub fn is_polymorphic(free: &[Name]) -> bool {
    free.iter().any(|n| n.as_str() != "any")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wildcard_any_gate() {
        // StrictHmArityGateSpec wa-a / wp-a.
        assert!(!is_polymorphic(&[Name::new("any")]));
        assert!(is_polymorphic(&[Name::new("msg")]));
        assert!(!is_polymorphic(&[]));
        assert!(is_polymorphic(&[Name::new("any"), Name::new("a")]));
    }

    #[test]
    fn free_vars_dedup_ordered() {
        let t = Ty::Fun(
            Box::new(Ty::var("a")),
            Box::new(Ty::app("List", vec![Ty::var("b"), Ty::var("a")])),
        );
        let fvs: Vec<String> = t
            .free_vars()
            .iter()
            .map(|n| n.as_str().to_string())
            .collect();
        assert_eq!(fvs, vec!["a", "b"]);
    }

    #[test]
    fn renders_function_type() {
        let t = Ty::Fun(Box::new(Ty::app("Int", vec![])), Box::new(Ty::Unit));
        assert_eq!(t.render(), "Int -> ()");
    }
}
