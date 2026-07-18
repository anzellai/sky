#![forbid(unsafe_code)]
//! `ty` — HM inference, arena union-find, generalisation, exhaustiveness, the
//! type table (doc 02, doc 06).
//!
//! M0 stub: the interned `Ty` + arena `TyVarId` vocabulary is seeded. The
//! union-find that gives type variables *identity* — "the one real design task"
//! (doc 01, L3) — lands in M3 as a `Vec<TyVarId>` local to the `infer` query,
//! never global (L1).

use base::Name;

/// An arena-allocated type-variable id. Replaces `UF.Point` pointer identity
/// with a plain integer compared by `==` (doc 01, L3).
#[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub struct TyVarId(pub u32);

/// A structural type. Interned in M3; here it is the shape the checker speaks
/// (doc 06). Exhaustive enum — illegal type states are unrepresentable (L6).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Ty {
    /// An unbound inference variable.
    Var(TyVarId),
    /// A nullary/applied nominal constructor, e.g. `Int`, `List a`.
    Con(Name, Vec<Ty>),
    /// A function type `a -> b`.
    Fun(Box<Ty>, Box<Ty>),
    /// The unit type `()`.
    Unit,
}

impl Ty {
    pub fn con(name: &str, args: Vec<Ty>) -> Ty {
        Ty::Con(Name::new(name), args)
    }
}

/// Placeholder inference entry point. M3 replaces this with the real
/// `infer(DefId)` query producing `(types, per-region map, diagnostics)`.
pub fn infer_stub() -> Ty {
    Ty::Unit
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builds_a_function_type() {
        let t = Ty::Fun(Box::new(Ty::con("Int", vec![])), Box::new(Ty::Unit));
        match t {
            Ty::Fun(_, ret) => assert_eq!(*ret, Ty::Unit),
            _ => panic!("expected a function type"),
        }
    }
}
