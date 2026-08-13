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

/// P1's ENGINE substrate — the commit path — as distinct from the key format.
///
/// P1 lands in stages: Stage 1 is the irreversible key format (`comparer.go`,
/// `keys.go`, `hlc.go`) and Stage 2 is the engine hub (`committer.go`,
/// `pebble_engine.go`, `txn.go`, …). A gate that certifies *durability on ack*
/// or the *crash corpus* is asking a question about the commit path, and there
/// is no commit path until `committer.go` exists — `bluedb/` containing three
/// pure encoding files cannot answer it either way.
///
/// Probing the directory therefore made G2.6/G2.9a demand a body for substrate
/// that had not landed, which is a false trigger of the same shape the P6 probe
/// had: it forces the phase to either write a gate against nothing, or to skip
/// it. Both roads end at a gate nobody trusts.
///
/// `committer.go` is the right marker because it IS the thing under test: it
/// owns the single-writer goroutine, group commit, and the `Apply(pebble.Sync)`
/// that arm (a) of G2.9a is about. `p1_engine_probe_ratchets_on_the_committer`
/// proves the trigger still fires.
pub fn p1_engine(ctx: &Ctx) -> GateOutcome {
    probe(ctx, "P1 (engine)", &["runtime-go/bluedb/committer.go"])
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

/// P6's substrate is `rt/changebus.go` — and ONLY that.
///
/// `runtime-go/bluedb/changefeed.go` used to be probed here too, and it is the
/// wrong marker: it is L1 substrate, not reactivity. `committer.go:144,152`
/// call `hasChangeSubs` / `emitChangeBatch` directly, so the engine does not
/// compile without it — verified by dropping the file, which yields
/// `undefined: ChangeBatch, changeFeedSub`. It therefore lands in **P1**, and
/// its presence says nothing whatsoever about whether P6's reactivity exists.
///
/// This is a NARROWING of a false trigger, not a widening of an exemption, and
/// the distinction is the one this module's header is about. The probe still
/// fires on P6's real substrate: `changebus.go` is net-new in P6 (§10.1 —
/// "`ChangeBus` **local + postgres**"), exists on no branch today, and cannot
/// be created by P1. `pending_probe_fires_when_p6_substrate_appears` proves the
/// ratchet still works by creating that file and asserting FAIL.
///
/// The alternative — leaving the probe as-is — would have made P1 red for a
/// file P1 is required to port, and the only ways out of that are to skip the
/// gate or to weaken it later under pressure. A probe that cries wolf gets
/// disabled; that is how the false-green class starts.
pub fn p6_reactivity(ctx: &Ctx) -> GateOutcome {
    probe(ctx, "P6", &["runtime-go/rt/changebus.go"])
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

    /// The P6 probe was narrowed to drop `bluedb/changefeed.go` (L1 substrate
    /// the committer calls, so P1 must port it). This proves the narrowing did
    /// NOT disarm the ratchet: P6's real substrate still flips the gate to FAIL.
    ///
    /// Without this, "the probe was mis-specified" is exactly the sentence that
    /// turns a ratchet into a rubber stamp — so the claim is measured, not
    /// argued.
    #[test]
    fn p6_probe_still_ratchets_on_its_real_substrate() {
        let dir = std::env::temp_dir().join(format!("bluedb-pending-p6-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();

        // The engine file P1 legitimately ports must NOT trip P6.
        std::fs::create_dir_all(dir.join("runtime-go/bluedb")).unwrap();
        std::fs::write(dir.join("runtime-go/bluedb/changefeed.go"), "package bluedb\n").unwrap();
        assert!(
            matches!(p6_reactivity(&ctx_at(dir.clone())), GateOutcome::NotRun { .. }),
            "porting the L1 changefeed must leave P6 NOT RUN — it is not P6 substrate"
        );

        // P6's own substrate must still trip it.
        std::fs::create_dir_all(dir.join("runtime-go/rt")).unwrap();
        std::fs::write(dir.join("runtime-go/rt/changebus.go"), "package rt\n").unwrap();
        assert!(
            matches!(p6_reactivity(&ctx_at(dir.clone())), GateOutcome::Fail { .. }),
            "P6 substrate present must FAIL while the gate body is unwritten"
        );

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Stage 1 (the key format) must NOT trip the engine gates; Stage 2 must.
    #[test]
    fn p1_engine_probe_ratchets_on_the_committer() {
        let dir = std::env::temp_dir().join(format!("bluedb-pending-eng-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("runtime-go/bluedb")).unwrap();

        // Stage 1: key format only. Durability gates have nothing to certify.
        for f in ["comparer.go", "keys.go", "hlc.go"] {
            std::fs::write(dir.join("runtime-go/bluedb").join(f), "package bluedb\n").unwrap();
        }
        assert!(
            matches!(p1_engine(&ctx_at(dir.clone())), GateOutcome::NotRun { .. }),
            "the key format alone must not demand a durability gate body"
        );
        // …while the directory probe DOES fire, which is what G0.3 keys on.
        assert!(matches!(
            p1_substrate(&ctx_at(dir.clone())),
            GateOutcome::Fail { .. }
        ));

        // Stage 2: the commit path lands. Now the durability gates are owed.
        std::fs::write(
            dir.join("runtime-go/bluedb/committer.go"),
            "package bluedb\n",
        )
        .unwrap();
        assert!(
            matches!(p1_engine(&ctx_at(dir.clone())), GateOutcome::Fail { .. }),
            "committer.go present must FAIL while the durability gate has no body"
        );

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
