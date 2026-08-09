//! Substrate probes for gates whose subject does not exist yet.
//!
//! P0 builds the harness *before* the code it certifies (§10.1 ordering
//! rationale: "the prior attempt's three false-green gates were all authored
//! after the code they guarded, by the same context that wrote the code").
//! Goals 1–5 therefore have registered gates with no implementable body.
//!
//! The danger is obvious: "not implemented yet" is the most natural place in
//! the whole design to hide a green lie. Three properties stop it.
//!
//! 1. **The probe decides, not the author.** Each pending body names the
//!    concrete paths its phase creates. While none exist, the gate is
//!    `NOT RUN` — and `NOT RUN` renders its goal `UNKNOWN`, never PASS, so no
//!    goal can be closed by leaving gates unwritten.
//! 2. **It is a ratchet.** The moment any of those paths appears, the probe
//!    returns **FAIL** with "substrate present, gate body not implemented".
//!    Landing P1 without writing G2.6 turns the harness red; you cannot ship
//!    the code and leave the gate pending.
//! 3. **It cannot be silently widened.** A pending body reports the exact paths
//!    it probed, and those paths are rendered into `STATUS.md`.
//!
//! A pending gate is never `PASS`. There is no code path here that returns one.

use super::registry::{Ctx, GateOutcome};

/// The shared shape: `NOT RUN` while every probed path is absent; `FAIL` as
/// soon as one appears.
fn probe(ctx: &Ctx, phase: &str, substrate: &[&str]) -> GateOutcome {
    let present: Vec<&str> = substrate
        .iter()
        .copied()
        .filter(|p| ctx.exists(p))
        .collect();

    if present.is_empty() {
        GateOutcome::not_run(format!(
            "{phase} substrate absent (probed: {}) — implement this gate with the phase that creates it",
            substrate.join(", ")
        ))
    } else {
        GateOutcome::fail(
            format!("{phase} substrate has landed but this gate still has no body"),
            present
                .iter()
                .map(|p| {
                    format!(
                        "{p} exists — the gate that certifies it must be implemented before the phase can close"
                    )
                })
                .collect(),
        )
    }
}

pub fn p1_substrate(ctx: &Ctx) -> GateOutcome {
    probe(ctx, "P1", &["runtime-go/bluedb"])
}

pub fn p2_index(ctx: &Ctx) -> GateOutcome {
    probe(
        ctx,
        "P2",
        &["runtime-go/bluedb/index_key.go", "sky-stdlib/Std/Persist.sky"],
    )
}

pub fn p3_isolation(ctx: &Ctx) -> GateOutcome {
    probe(
        ctx,
        "P3",
        &[
            "runtime-go/rt/db_isolation.go",
            "runtime-go/rt/db_registry.go",
        ],
    )
}

pub fn p4_full_api(ctx: &Ctx) -> GateOutcome {
    probe(ctx, "P4", &["docs/skypersist", "runtime-go/persistglue"])
}

pub fn p5_sessions(ctx: &Ctx) -> GateOutcome {
    probe(
        ctx,
        "P5",
        &[
            "runtime-go/rt/live_sessions_collection.go",
            "sky-stdlib/Std/Persist.sky",
        ],
    )
}

pub fn p6_reactivity(ctx: &Ctx) -> GateOutcome {
    probe(
        ctx,
        "P6",
        &["runtime-go/rt/changebus.go", "runtime-go/bluedb/changefeed.go"],
    )
}

pub fn p7_console_read(ctx: &Ctx) -> GateOutcome {
    probe(ctx, "P7", &["runtime-go/rt/consoledata"])
}

pub fn p8_console_write(ctx: &Ctx) -> GateOutcome {
    probe(
        ctx,
        "P8",
        &["runtime-go/rt/consoledata/write.go", "runtime-go/rt/consoledata"],
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bluedb_gates::registry::Tier;
    use std::path::PathBuf;

    fn ctx_at(root: PathBuf) -> Ctx {
        Ctx::new(root, Tier::Fast, false, None)
    }

    #[test]
    fn pending_is_not_run_when_the_substrate_is_absent() {
        let dir = std::env::temp_dir().join(format!("bluedb-pending-a-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let out = probe(&ctx_at(dir.clone()), "PX", &["nope/does-not-exist"]);
        assert!(matches!(out, GateOutcome::NotRun { .. }));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn pending_ratchets_to_fail_the_moment_the_substrate_lands() {
        let dir = std::env::temp_dir().join(format!("bluedb-pending-b-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("landed")).unwrap();
        let out = probe(&ctx_at(dir.clone()), "PX", &["landed"]);
        match out {
            GateOutcome::Fail { findings, .. } => {
                assert_eq!(findings.len(), 1);
            }
            _ => panic!("substrate present must FAIL a pending gate, never NOT RUN or PASS"),
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_pending_gate_can_never_report_pass() {
        // Both branches of `probe` are enumerated above; this asserts the
        // property directly so a future edit that adds a PASS arm fails here.
        let dir = std::env::temp_dir().join(format!("bluedb-pending-c-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        for present in [false, true] {
            if present {
                std::fs::create_dir_all(dir.join("s")).unwrap();
            }
            let out = probe(&ctx_at(dir.clone()), "PX", &["s"]);
            assert!(!matches!(out, GateOutcome::Pass { .. }));
        }
        let _ = std::fs::remove_dir_all(&dir);
    }
}
