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
    Assignment, Axis, Stratum, COLLIDER, COLLISION, EDGE, ERASURE, IMPORT_SHAPE, INNER, POSITION,
    SHADOW,
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
        // Family S. `nominal` is the happy path that always worked; moving to
        // an empty / boundary / unicode / failure input is what breaks a
        // surface, so `edge` is the axis and `nominal` neutralises it.
        "stdlib_edge" => (EDGE, "nominal"),
        // NOT `import_shape` — see the module docstring and
        // [`witness_exemption`]: import syntax is erased by name resolution, so
        // no emit-shape witness for it can exist, on this stratum or any other.
        // `shadow` is the axis that does reach the compiler, and its values
        // produce different programs AND different values.
        "stdlib_import" => (SHADOW, "none"),
        other => panic!("no axis-under-test declared for stratum {other:?}"),
    }
}

/// Strata for which emitted-Go equality is the CORRECT outcome, so the
/// emit-shape witness does not apply.
///
/// v2 §5.5: *"Exemptions are explicit, counted, and owned."* This is the list,
/// and every entry states why. It is deliberately a function returning a reason
/// rather than a silent skip — an exempt case is REPORTED, not hidden, and the
/// coverage claim for that stratum is correspondingly weaker.
///
/// # `import_shape`
///
/// Discovered by this gate, 2026-08-10: all 20 `import_shape` cases emit
/// **byte-identical Go** across `plain` / `aliased` /
/// `alias_not_last_segment` / `exposing_list` / `exposing_all`. That is the
/// compiler being RIGHT — import syntax is erased by name resolution, and two
/// spellings of the same import must produce the same program.
///
/// So the emit-shape witness cannot apply here. **The honest consequence is that
/// this stratum's cases do not currently witness their axis by any mechanism**,
/// and the reason is a real weakness in the generator rather than a property of
/// the compiler: the `collision` axis is **inert**. Its non-`none` values add an
/// unrelated local binding (`answer2`, `label2`) that collides with nothing, so
/// no case ever creates the name conflict #164 was actually about.
///
/// These cases still carry a genuine class-V value assertion (the imported
/// `answer` must read back as 42, which does prove the import resolved to the
/// right symbol). They must NOT be claimed as covering the #164 defect class
/// until the `collision` axis actually collides — see
/// `docs/ci-test-phase-4-results.md` §4.
///
/// **Family S's `stdlib_import` stratum is the repair**, and it is a separate
/// stratum rather than a rewrite of this one because the two record different
/// things: this one keeps the historical shape (a generated helper module
/// graph), the new one collides against REAL stdlib names — `String.length` vs
/// `List.length` vs a local `length` — exactly as v2 §3.1 requires. Its
/// axis-under-test is `shadow`, not `import_shape`, so it is WITNESSED rather
/// than exempt; the exemption below is a property of import syntax and applies
/// to any stratum that tries to make `import_shape` its subject. On its first
/// run that stratum found a live defect: two modules that both
/// `exposing (..)` the same name resolve to the LAST import, silently
/// (`gen::blocked_reason`).
fn witness_exemption(stratum: &str) -> Option<&'static str> {
    match stratum {
        "import_shape" => Some(
            "import syntax is erased by name resolution — identical Go is the \
             CORRECT outcome; the collision axis is inert and must be \
             strengthened before this stratum can claim to cover #164",
        ),
        _ => None,
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
    let mut exempt: Vec<(&'static str, usize, &'static str)> = Vec::new();
    for s in super::axes::STRATA {
        let (axis, neutral) = axis_under_test(s);
        let varied: Vec<Assignment> = super::axes::full_cross(s)
            .into_iter()
            .filter(|a| a.get(axis) != neutral)
            .collect();
        if let Some(reason) = witness_exemption(s.name) {
            exempt.push((s.name, varied.len(), reason));
            continue;
        }
        for a in varied {
            let case = gen::build(s, &a);
            // A case that expects a REJECTION has no emitted Go to fingerprint
            // once it starts being rejected — its witness is the diagnostic
            // (`Witness::Diagnostic`), not the emit shape. Including it here
            // would make the gate report a spurious FAIL on the day the defect
            // it pins is fixed, which is the worst possible time for a gate to
            // cry wolf.
            if matches!(case.expect, gen::Expect::Reject { .. }) {
                continue;
            }
            let neutralised = a.clone().with(axis, neutral);
            candidates.push((case, neutralised, axis));
        }
    }
    candidates.sort_by(|x, y| x.0.id.cmp(&y.0.id));

    let seed = super::commit_seed(root);
    let n = SHARD.min(candidates.len());
    let start = if candidates.is_empty() { 0 } else { seed % candidates.len() };

    println!("CORPUS WITNESS GATE — v2 §4.4 (does each case witness its own axis?)");
    println!("  candidates : {}", candidates.len());
    println!("  shard      : {n} (offset {start}, rotates with the commit sha)");
    if !exempt.is_empty() {
        // Counted and named, never silent (v2 §5.5). An exempt stratum's
        // coverage claim is weaker, and this is where that is said out loud.
        let total: usize = exempt.iter().map(|(_, n, _)| n).sum();
        println!("  EXEMPT     : {total} case(s) across {} stratum/strata —", exempt.len());
        for (name, n, reason) in &exempt {
            println!("      {name} ({n} cases): {reason}");
        }
    }
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
