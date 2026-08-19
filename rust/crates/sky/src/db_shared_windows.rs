//! Windows stub for the shared host cluster (`sky db provision --shared`).
//!
//! The real module (`db_shared.rs`, compiled only on unix) provisions **one
//! shared PostgreSQL cluster per host, one role per app** and enforces the P6
//! cross-tenant boundary by speaking the PostgreSQL wire protocol over a
//! **unix domain socket** (`crate::pg_wire`). `std::os::unix` does not exist on
//! Windows, so the feature cannot be built there.
//!
//! This stub exists so the CLI still compiles and *dispatches* on Windows:
//! `sky db provision --shared` reaches [`cmd_shared`] exactly as it does on
//! unix, and gets a clear error instead of a missing symbol.

use std::process::ExitCode;

/// Refuse `sky db provision --shared` on Windows with a clear message.
///
/// Mirrors the unix [`crate::db_shared::cmd_shared`] signature so the single
/// caller (`db_provision::cmd_provision`) compiles unchanged on both platforms.
pub fn cmd_shared(_args: &[String]) -> ExitCode {
    eprintln!(
        "sky db provision --shared: the shared host cluster is not supported on \
         Windows (it enforces the cross-tenant boundary over a unix domain \
         socket). Provision the shared cluster on a unix host."
    );
    ExitCode::FAILURE
}
