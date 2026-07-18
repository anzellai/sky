#![forbid(unsafe_code)]
//! `lower` — the typed lowering IR (Sky-typed → Go-IR): type-directed lowering,
//! TCO, DCE, monomorphisation (doc 02, doc 07, law L9: a typed lowering IR where
//! coercion is the rare, explicit exception rather than a pervasive `rt.Coerce`
//! residual surface).
//!
//! M0 stub: the Go-IR node vocabulary is seeded so `codegen` has a target to
//! print. M4 fills in the real type-directed lowering.

use ty::Ty;

/// A node in the typed Go intermediate representation (doc 07). Every node
/// carries the Sky `Ty` it was lowered from, so coercion is explicit, not
/// implicit (L9). Exhaustive enum — no `_ =>` arms on the compiler's own IR (L6).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum GoIr {
    /// A typed literal.
    IntLit(i64),
    /// A typed local reference.
    Local { name: String, ty: Ty },
    /// A typed call.
    Call { callee: Box<GoIr>, args: Vec<GoIr> },
}

/// Placeholder lowering entry point. M4 replaces this with a real
/// `typed_hir(DefId) -> GoIr` query threaded through `skydb`.
pub fn lower_stub() -> GoIr {
    GoIr::IntLit(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn seeds_a_typed_ir_node() {
        assert_eq!(lower_stub(), GoIr::IntLit(0));
    }
}
