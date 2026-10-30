#![forbid(unsafe_code)]
//! `lower` — the typed lowering IR (Sky-typed → Go-IR): type-directed lowering,
//! DCE, kernel dispatch, ADT/record type-decl emission (doc 02, doc 07, law L9).
//!
//! M4: a working type-directed lowering over the resolved HIR + the `ty`
//! per-expression type table. Coercion is an explicit, justified IR node (doc 07
//! §6) — not a pervasive surface. Server/TUI/Webview runtime backends are out of
//! the M4 CLI-family scope; see the milestone report.

pub mod goty;
pub mod ir;
pub mod kernel;
mod lower;
pub mod shape;

pub use ir::{GoTy, Prim};
pub use lower::{
    lower_program, lower_program_cfg, FfiModInfo, FfiTable, LowerConfig, LowerDiag, LowerOutput,
};

/// Top-level def bodies whose types lowering has inferred, process-wide. It
/// grows with the program a build lowers, never with the size of the stdlib a
/// build merely interns (`tests/types_only_reachable_bodies.rs`). It never
/// changes what the compiler computes.
pub(crate) static BODIES_TYPED: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

/// The current value of the bodies-typed counter (see [`BODIES_TYPED`]).
pub fn bodies_typed() -> u64 {
    BODIES_TYPED.load(std::sync::atomic::Ordering::Relaxed)
}
