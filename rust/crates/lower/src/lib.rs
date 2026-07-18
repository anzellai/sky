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

pub use lower::{lower_program, lower_program_cfg, LowerConfig, LowerOutput};
