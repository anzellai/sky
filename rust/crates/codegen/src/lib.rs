#![forbid(unsafe_code)]
//! `codegen` — deterministic Go source emission from the Go-IR; the runtime
//! ABI/interface (doc 02, doc 08). Emission-order determinism (L4) is a
//! crate-local invariant here: no `HashMap` iteration ever reaches output; walk
//! `BTreeMap` / `IndexMap` / interned-id order only.
//!
//! M0 stub: a trivial emitter proves the `lower::GoIr` → `String` shape. M4
//! fills in byte-identical-to-oracle Go emission on the frozen subset.

use lower::GoIr;

/// Emit Go source for a Go-IR node. M0 handles only the seed literal; the match
/// is exhaustive over the current IR (L6) so extending `GoIr` forces this to
/// grow deliberately.
pub fn emit(node: &GoIr) -> String {
    match node {
        GoIr::IntLit(n) => n.to_string(),
        GoIr::Local { name, .. } => name.clone(),
        GoIr::Call { callee, args } => {
            let rendered: Vec<String> = args.iter().map(emit).collect();
            format!("{}({})", emit(callee), rendered.join(", "))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn emits_a_literal() {
        assert_eq!(emit(&GoIr::IntLit(42)), "42");
    }
}
