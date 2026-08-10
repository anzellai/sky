//! The axis-witness gate (v2 §4.4).
//!
//! # Why a coverage percentage is unfalsifiable without this
//!
//! > A case that varies an axis but asserts something independent of that axis
//! > does not cover the axis; it only spends budget.
//!
//! A generator can emit 10,000 cases across an axis and, if every one of them
//! asserts something the axis cannot influence, "this axis is covered" is a claim
//! with no way to be shown false. The witness requirement is what turns it into
//! one.
//!
//! # Value is the oracle; emitted Go is the witness
//!
//! For most axes here the VALUE is deliberately axis-invariant — that is the
//! property under test. Moving a record update from bare position into a tuple
//! must NOT change what it computes; #166 was a bug precisely because it did. So
//! the generator-constructed value (`gen.rs`) is the **correctness oracle**, and
//! it cannot also be the axis witness.
//!
//! The **emitted Go** is the witness. If a case at `position = in_tuple` emits
//! byte-identical Go to the same case at `position = bare`, then the axis did not
//! reach the compiler at all and the case is not testing what it claims. That is
//! a FAIL, by name, for that case.
//!
//! This is the same distinction v2 §4.4 draws between "a *different* expected
//! value" and "a *different* emit-shape fingerprint" — for this corpus it is
//! always the latter.

use super::axes::{
    Assignment, Axis, Stratum, COLLIDER, COLLISION, ERASURE, IMPORT_SHAPE, INNER, POSITION,
};
use super::gen;
use super::runner;
use std::path::Path;

/// How many cases the witness gate proves per run.
///
/// v2 §4.3: per-item self-falsification doubles the work, so behavioural items
/// run as a **rotating deterministic shard** per push and all of them in T3. Each
/// witness check is two `sky build`s, so this is `2 × SHARD × c_u`.
const SHARD: usize = 16;

/// The axis each stratum is *about*, and the value that neutralises it.
///
/// The neutral value is the shape the defect did NOT occur in — the simple case
/// that always worked. #166's `position` neutral is `bare`, because bare record
/// update was never broken; putting it in a tuple is what broke it.
fn axis_under_test(s: &Stratum) -> (Axis, &'static str) {
    match s.name {
        "record_update" => (POSITION, "bare"),
        "destructure" => (ERASURE, "direct"),
        "type_nesting" => (INNER, "none"),
        "import_shape" => (IMPORT_SHAPE, "plain"),
        "fieldset_collision" => (COLLISION, "none"),
        // Neutralising the COLLIDER — renaming the fields so they collide with
        // nothing in the stdlib — is what makes the otherwise-identical program
        // correct, so that is the axis this stratum is about.
        "fieldset_ctor" => (COLLIDER, "local"),
        other => panic!("no axis-under-test declared for stratum {other:?}"),
    }
}

/// Build a case and return a fingerprint of the Go it emitted.
///
/// The fingerprint is the emitted Go with whitespace collapsed — stable enough
/// that formatting noise does not manufacture a false witness, specific enough
/// that a changed struct, field order, or coercion shows up.
fn emit_fingerprint(sky: &Path, dir: &Path, case: &gen::GenCase) -> Result<String, String> {
    let src = dir.join("src");
    std::fs::create_dir_all(&src).map_err(|e| e.to_string())?;
    for (name, source) in &case.modules {
        let rel: std::path::PathBuf =
            name.split('.').collect::<std::path::PathBuf>().with_extension("sky");
        let path = src.join(&rel);
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p).map_err(|e| e.to_string())?;
        }
        std::fs::write(&path, source).map_err(|e| e.to_string())?;
    }
    let entry_rel: std::path::PathBuf = case
        .entry
        .split('.')
        .collect::<std::path::PathBuf>()
        .with_extension("sky");
    runner::build_only(sky, dir, &src.join(entry_rel))?;

    let go = dir.join("sky-out").join("main.go");
    let text = std::fs::read_to_string(&go)
        .map_err(|e| format!("no emitted Go at {}: {e}", go.display()))?;
    Ok(text.split_whitespace().collect::<Vec<_>>().join(" "))
}

pub fn run(root: &Path) -> i32 {
    let Some(sky) = runner::sky_binary(root) else {
        eprintln!("corpus.witness: no sky binary — a gate that cannot run has not passed.");
        return 1;
    };

    // Candidates are cases NOT already sitting at the neutral value — a case at
    // the neutral value IS the baseline and has nothing to witness against.
    let mut candidates: Vec<(gen::GenCase, Assignment, Axis)> = Vec::new();
    for s in super::axes::STRATA {
        let (axis, neutral) = axis_under_test(s);
        for a in super::axes::full_cross(s) {
            if a.get(axis) == neutral {
                continue;
            }
            let neutralised = a.clone().with(axis, neutral);
            candidates.push((gen::build(s, &a), neutralised, axis));
        }
    }
    candidates.sort_by(|x, y| x.0.id.cmp(&y.0.id));

    let seed = super::commit_seed(root);
    let n = SHARD.min(candidates.len());
    let start = if candidates.is_empty() { 0 } else { seed % candidates.len() };

    println!("CORPUS WITNESS GATE — v2 §4.4 (does each case witness its own axis?)");
    println!("  candidates : {}", candidates.len());
    println!("  shard      : {n} (offset {start}, rotates with the commit sha)");
    println!();

    let scratch = runner::scratch_root("witness");
    let _ = std::fs::remove_dir_all(&scratch);

    let mut failures = Vec::new();
    let mut proven = 0usize;
    for k in 0..n {
        let (case, neutralised, axis) = &candidates[(start + k) % candidates.len()];
        let stratum = super::axes::STRATA
            .iter()
            .find(|s| s.name == case.stratum)
            .expect("stratum exists");
        let baseline = gen::build(stratum, neutralised);

        let d1 = scratch.join(format!("w{k:03}-varied"));
        let d2 = scratch.join(format!("w{k:03}-neutral"));
        let fa = emit_fingerprint(&sky, &d1, case);
        let fb = emit_fingerprint(&sky, &d2, &baseline);
        let _ = std::fs::remove_dir_all(&d1);
        let _ = std::fs::remove_dir_all(&d2);

        match (fa, fb) {
            (Ok(a), Ok(b)) => {
                if a == b {
                    failures.push(format!(
                        "{}  [{} = {} vs neutral {}] emits BYTE-IDENTICAL Go — the axis never reached the compiler",
                        case.id,
                        axis.name,
                        case.axes.get(*axis),
                        neutralised.get(*axis),
                    ));
                } else {
                    proven += 1;
                }
            }
            (Err(e), _) | (_, Err(e)) => {
                failures.push(format!("{}  build failed: {e}", case.id));
            }
        }
    }

    let _ = std::fs::remove_dir_all(&scratch);

    println!("  witnessed  : {proven}/{n}");
    if failures.is_empty() {
        println!();
        println!("WITNESS GATE: PASS ({n} cases each emit different Go from their axis-neutralised twin)");
        0
    } else {
        println!();
        println!("  ---- {} NOT WITNESSED ----", failures.len());
        for f in &failures {
            println!("  {f}");
        }
        println!();
        println!("WITNESS GATE: FAIL — a case that does not witness its axis does not");
        println!("  cover it, and must not be counted toward the coverage number (v2 §4.4).");
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every stratum must declare an axis-under-test, and its neutral value must
    /// be a real value of that axis.
    ///
    /// This test exists because adding the `fieldset_ctor` stratum without an
    /// entry in [`axis_under_test`] made the witness gate PANIC at run time. The
    /// harness caught it (the gate body panicked → the baseline was red → the
    /// falsifier reported INCONCLUSIVE rather than a false PROVEN), which is the
    /// harness behaving correctly — but a missing table entry is a `cargo test`
    /// failure, not a gate-runtime discovery.
    #[test]
    fn every_stratum_declares_an_axis_under_test_with_a_valid_neutral() {
        for s in super::super::axes::STRATA {
            let (axis, neutral) = axis_under_test(s);
            assert!(
                s.axes.iter().any(|a| a.name == axis.name),
                "stratum {}'s axis-under-test {:?} is not one of its own axes",
                s.name,
                axis.name
            );
            assert!(
                axis.values.contains(&neutral),
                "stratum {}'s neutral {neutral:?} is not a value of axis {:?}",
                s.name,
                axis.name
            );
        }
    }
}
