//! The falsifier runner — "does this gate's assertion actually bite?"
//!
//! A gate that cannot fail is worse than no gate, because it consumes the
//! budget of a real one while certifying nothing. The audit behind this mandate
//! found eleven of them at once. So every gate declares a mutation, and this
//! runner proves the mutation makes it red.
//!
//! The protocol, per gate × mutation:
//!
//! ```text
//!   1. baseline run          → must PASS, else INCONCLUSIVE
//!                              ("both sides failing proves nothing", v2 §4.2)
//!   2. apply the mutation    → exact-once textual replacement, guarded
//!   3. run again             → FAIL ⇒ PROVEN, PASS ⇒ VACUOUS
//!   4. revert                → unconditional, including on panic
//! ```
//!
//! Two properties the BlueDB precedent lacks and that are load-bearing here:
//!
//! * **The mutation probe is timed out.** The precedent runs `cargo build` with
//!   no timeout at all. Here the mutated run is supervised by the same
//!   `killpg`-backed budget as the baseline.
//! * **The revert is guaranteed.** [`Patch`] restores in `Drop`, so a panic
//!   between apply and revert cannot leave a mutated source in the tree.

use super::child::{result_path, run_gate_in_child};
use super::registry::{Expect, Gate, Mutation, MutationKind};
use std::path::{Path, PathBuf};
use std::time::Duration;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Falsified {
    /// The gate went red under its mutation. The assertion is live.
    Proven,
    /// The gate stayed green under its mutation. For a normal gate this is a
    /// **defect**; for the canary it is the **required** answer.
    Vacuous,
    /// Nothing could be concluded — the baseline was not green, the mutation
    /// could not be applied, or the run could not be supervised.
    ///
    /// Deliberately distinct from both: "I could not tell" must never be
    /// rounded to "proven", and rounding it to "vacuous" would blame the gate
    /// for the harness's own inability to run.
    Inconclusive(String),
}

impl Falsified {
    pub fn label(&self) -> &'static str {
        match self {
            Falsified::Proven => "PROVEN",
            Falsified::Vacuous => "VACUOUS",
            Falsified::Inconclusive(_) => "INCONCLUSIVE",
        }
    }
}

pub struct FalsifyReport {
    pub gate: &'static str,
    pub mutation: &'static str,
    pub outcome: Falsified,
    /// Did the observed outcome match the gate's declared [`Expect`]?
    ///
    /// For every gate but the canary this is `outcome == Proven`. For the
    /// canary it is `outcome == Vacuous` — the one place where a *passing*
    /// gate is the success signal, and where `PROVEN` means the harness itself
    /// is broken.
    pub as_declared: bool,
    pub detail: String,
}

/// A textual mutation applied to the working tree, reverted on drop.
#[derive(Debug)]
struct Patch {
    path: PathBuf,
    original: String,
    applied: bool,
}

impl Patch {
    /// Apply an exact-once replacement.
    ///
    /// Refuses when the pattern is absent (the mutation has rotted — this is
    /// how "7 of 48 verified" happens) or occurs more than once (the mutation
    /// is ambiguous and might perturb something other than the axis under
    /// test).
    fn apply(root: &Path, rel: &str, from: &str, to: &str) -> Result<Patch, String> {
        let path = root.join(rel);
        let original = std::fs::read_to_string(&path)
            .map_err(|e| format!("cannot read mutation target {rel}: {e}"))?;
        let hits = original.matches(from).count();
        if hits != 1 {
            return Err(format!(
                "mutation pattern {from:?} occurs {hits}x in {rel} (must be exactly 1)"
            ));
        }
        let mutated = original.replacen(from, to, 1);
        std::fs::write(&path, &mutated)
            .map_err(|e| format!("cannot write mutation to {rel}: {e}"))?;
        Ok(Patch {
            path,
            original,
            applied: true,
        })
    }

    fn revert(&mut self) {
        if self.applied {
            let _ = std::fs::write(&self.path, &self.original);
            self.applied = false;
        }
    }
}

impl Drop for Patch {
    fn drop(&mut self) {
        // Unconditional. A panic between apply and revert must not leave a
        // mutated source behind for the next gate — or for the developer.
        self.revert();
    }
}

pub struct FalsifyOpts {
    pub exe: PathBuf,
    pub repo_root: PathBuf,
    pub scratch: PathBuf,
}

/// Verify one gate's declared mutations.
pub fn verify_gate(gate: &'static Gate, opts: &FalsifyOpts, generation: &mut u64) -> Vec<FalsifyReport> {
    let budget = Duration::from_secs(gate.budget_s);
    let mut out = Vec::new();

    // ---- step 1: the baseline must be green -------------------------------
    //
    // "Both sides failing identically proves nothing" (v2 §4.2) generalises: a
    // gate that is already red tells us nothing about whether the MUTATION made
    // it red. Refusing here is what stops a broken tree from manufacturing a
    // wall of false PROVENs.
    *generation += 1;
    let base_gen = *generation;
    let base = run_gate_in_child(
        &opts.exe,
        &opts.repo_root,
        gate.name,
        base_gen,
        budget,
        &result_path(&opts.scratch, gate.name, base_gen),
    );
    let base_green = !base.timed_out
        && base
            .result
            .as_ref()
            .map(|r| r.passed && r.assertions > 0)
            .unwrap_or(false);

    if !base_green {
        let why = if base.timed_out {
            format!("baseline exceeded its {}s budget", gate.budget_s)
        } else {
            match &base.result {
                Some(r) => format!("baseline is red: {}", r.detail),
                None => "baseline produced no result".to_string(),
            }
        };
        for m in gate.mutations.as_slice() {
            out.push(FalsifyReport {
                gate: gate.name,
                mutation: m.id,
                outcome: Falsified::Inconclusive(why.clone()),
                as_declared: false,
                detail: why.clone(),
            });
        }
        return out;
    }

    // ---- steps 2-4: one mutation at a time --------------------------------
    for m in gate.mutations.as_slice() {
        *generation += 1;
        out.push(verify_one(gate, m, opts, *generation, budget));
    }
    out
}

fn verify_one(
    gate: &'static Gate,
    m: &'static Mutation,
    opts: &FalsifyOpts,
    generation: u64,
    budget: Duration,
) -> FalsifyReport {
    // The patch guard lives for exactly the mutated run.
    let _patch = match m.kind {
        MutationKind::ReplaceOnce { path, from, to } => {
            match Patch::apply(&opts.repo_root, path, from, to) {
                Ok(p) => Some(p),
                Err(e) => {
                    return FalsifyReport {
                        gate: gate.name,
                        mutation: m.id,
                        outcome: Falsified::Inconclusive(e.clone()),
                        as_declared: false,
                        detail: e,
                    };
                }
            }
        }
        // The canary. Nothing is written; the tree is byte-identical.
        MutationKind::NoOp => None,
    };

    let run = run_gate_in_child(
        &opts.exe,
        &opts.repo_root,
        gate.name,
        generation,
        budget,
        &result_path(&opts.scratch, gate.name, generation),
    );

    // A mutated run that times out IS red — the mutation broke it badly enough
    // to hang. That is a genuine falsification, and it is bounded, unlike the
    // precedent's untimed mutation probe.
    let went_red = run.timed_out
        || run
            .result
            .as_ref()
            .map(|r| !r.passed || r.assertions == 0)
            .unwrap_or(true);

    let outcome = if went_red {
        Falsified::Proven
    } else {
        Falsified::Vacuous
    };

    let as_declared = match gate.expect {
        Expect::Falsifiable => outcome == Falsified::Proven,
        Expect::Vacuous => outcome == Falsified::Vacuous,
    };

    let detail = match (&outcome, gate.expect) {
        (Falsified::Proven, Expect::Falsifiable) => run
            .result
            .as_ref()
            .map(|r| format!("went red as declared: {}", r.detail))
            .unwrap_or_else(|| "went red (no result — timed out or crashed)".into()),
        (Falsified::Vacuous, Expect::Vacuous) => {
            "stayed green under a no-op patch, as a correct runner must".into()
        }
        (Falsified::Vacuous, Expect::Falsifiable) => {
            "STAYED GREEN under its mutation — this gate asserts nothing about the axis \
             it claims to cover"
                .into()
        }
        (Falsified::Proven, Expect::Vacuous) => {
            "CANARY WENT RED under a NO-OP patch — the harness is broken: it is reporting \
             falsification for a change that was never made (patch applied in the wrong \
             tree, or the verdict is not being read from this run)"
                .into()
        }
        (Falsified::Inconclusive(w), _) => w.clone(),
    };

    FalsifyReport {
        gate: gate.name,
        mutation: m.id,
        outcome,
        as_declared,
        detail,
    }
    // `_patch` drops here → the tree is restored before the next gate runs.
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-falsify-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_patch_reverts_on_drop() {
        let root = tmp("revert");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        {
            let _p = Patch::apply(&root, "f.txt", "world", "mutant").unwrap();
            assert_eq!(
                std::fs::read_to_string(root.join("f.txt")).unwrap(),
                "hello mutant"
            );
        }
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world",
            "the tree must be restored when the guard drops"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn a_patch_reverts_even_when_the_scope_panics() {
        let root = tmp("panic");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        let r = root.clone();
        let _ = std::panic::catch_unwind(move || {
            let _p = Patch::apply(&r, "f.txt", "world", "mutant").unwrap();
            panic!("simulated failure mid-mutation");
        });
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world",
            "a panic between apply and revert must not leave a mutated tree"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn a_mutation_whose_pattern_is_missing_is_refused() {
        // The "7 of 48 verified" failure mode: a literal gets reworded and the
        // mutation silently stops mutating. Refusing beats reporting VACUOUS.
        let root = tmp("missing");
        std::fs::write(root.join("f.txt"), "hello world").unwrap();
        let e = Patch::apply(&root, "f.txt", "absent", "x").unwrap_err();
        assert!(e.contains("occurs 0x"), "{e}");
        assert_eq!(
            std::fs::read_to_string(root.join("f.txt")).unwrap(),
            "hello world"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn an_ambiguous_mutation_is_refused() {
        let root = tmp("ambiguous");
        std::fs::write(root.join("f.txt"), "a a").unwrap();
        let e = Patch::apply(&root, "f.txt", "a", "b").unwrap_err();
        assert!(e.contains("occurs 2x"), "{e}");
        let _ = std::fs::remove_dir_all(&root);
    }
}
