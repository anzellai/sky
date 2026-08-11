//! The isolation gate (v2 §3.2).
//!
//! # The finding this gate exists to enforce
//!
//! `record_fieldsets` is built over the **whole compilation**
//! (`lower/src/lower.rs:246-266`), keyed on the sorted field-**name** vector, and
//! its own comment at `:256-258` records that *"Two records with identical field
//! names but different field types collide here."* The TEA-Model heuristic then
//! picks the first `(Record, Cmd _)` candidate in a stable `(module, name)` order
//! (`lower.rs:267-278`), and `goty.rs:186-196` resolves **any** strict-subset
//! record whose field names are all in the Model's set to that nominal Model
//! `_R`.
//!
//! So batching is **not semantics-preserving**. Batch N TEA-shaped cases into one
//! compilation unit and N−1 of them resolve their subset records against the
//! wrong Model — the batching optimisation silently destroys the exact defect
//! class the corpus exists to catch.
//!
//! # What the gate does
//!
//! A deterministic sample of `isolation = Batch` cases is run in **three
//! configurations**:
//!
//! 1. **alone** — one compilation unit per case
//! 2. **in-batch** — all of them in one compilation unit, in manifest order
//! 3. **shuffled** — the same batch with the module order perturbed, because the
//!    Model heuristic depends on `(module, name)` order and a batch that only
//!    ever runs in one order cannot notice
//!
//! All three must produce **identical verdicts per case**. A divergence is a FAIL
//! — and it is the only mechanism that will notice when a NEW family starts
//! depending on whole-compilation state.
//!
//! The gate also reports, as evidence rather than as a verdict, what happens when
//! the `isolation = Unit` strata are batched anyway. That is the demonstration
//! that the isolation requirement is real and not a precaution.

use super::gen::{batch_module, Body, GenCase};
use super::runner;
use std::collections::BTreeMap;
use std::path::Path;

/// The sample size. Deterministic, but seeded from the commit sha so the sample
/// ROTATES across commits — a fixed sample would only ever prove the same cases
/// are order-independent.
const SAMPLE: usize = 24;

/// Build one compilation unit containing `bodies`, in the given order, and read
/// back each member's checked value.
///
/// The entry module prints `id<TAB>value` per member, so a batched run yields the
/// same per-case attribution an alone run does. Without that, a batch failure
/// could only be reported against the whole unit — which is how "one case in a
/// batch is wrong" becomes invisible.
fn run_batch(
    sky: &Path,
    dir: &Path,
    members: &[(usize, String, Body)],
) -> Result<BTreeMap<String, String>, String> {
    let src_dir = dir.join("src");
    std::fs::create_dir_all(src_dir.join("Batch")).map_err(|e| e.to_string())?;

    let mut imports = String::new();
    let mut prints = Vec::new();
    for (idx, id, body) in members {
        let (mod_name, mod_src) = batch_module(*idx, body);
        let rel = mod_name.replace('.', "/");
        std::fs::write(src_dir.join(format!("{rel}.sky")), mod_src).map_err(|e| e.to_string())?;
        imports.push_str(&format!("import {mod_name}\n"));
        let leaf = mod_name.rsplit('.').next().unwrap().to_string();
        prints.push(format!("\"{id}\\t\" ++ {leaf}.checkValue"));
    }

    let main = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         {imports}\n\n\
         lines : List String\nlines =\n    [ {} ]\n\n\n\
         main =\n    println (String.join \"\\n\" lines)\n",
        prints.join("\n    , ")
    );
    std::fs::write(src_dir.join("Main.sky"), main).map_err(|e| e.to_string())?;

    let out = runner::build_and_run(sky, dir, &src_dir.join("Main.sky"))?;
    let mut map = BTreeMap::new();
    for line in out.lines() {
        if let Some((id, val)) = line.split_once('\t') {
            map.insert(id.to_string(), val.to_string());
        }
    }
    Ok(map)
}

/// Force-batch the `isolation = unit` families and report whether their verdicts
/// change.
///
/// This is the **evidence that the isolation requirement is real** rather than a
/// precaution. v2 §3.2 asserts, from source, that batching is not
/// semantics-preserving for these families; this runs the experiment. It is also
/// the isolation gate's falsifier: if forcing the forbidden families into one
/// compilation unit changes nothing, then either the `unit` marking is
/// unnecessary or the gate is not actually comparing anything.
pub fn prove_isolation_needed(root: &Path) -> i32 {
    let Some(sky) = runner::sky_binary(root) else {
        eprintln!("corpus.isolation: no sky binary.");
        return 1;
    };

    let all = super::all_cases();
    let forbidden: Vec<GenCase> = all
        .iter()
        .filter(|c| c.isolation == super::gen::Isolation::Unit && c.body.is_some())
        .cloned()
        .collect();

    println!("ISOLATION NECESSITY PROBE — force-batching the `unit` families");
    println!("  v2 §3.2 forbids batching these because `record_fieldsets` is built");
    println!("  over the WHOLE compilation (lower/src/lower.rs:246-266) and its own");
    println!("  comment at :256-258 records that same-name/different-type records");
    println!("  collide there. This runs the experiment rather than asserting it.");
    println!();
    println!("  forbidden-family cases with a batchable body : {}", forbidden.len());
    println!();

    if forbidden.is_empty() {
        println!("PROBE: no batchable bodies among the unit families — nothing to compare.");
        return 1;
    }

    let scratch = runner::scratch_root("isolation-probe");
    let _ = std::fs::remove_dir_all(&scratch);

    let mut alone = BTreeMap::new();
    for (i, c) in forbidden.iter().enumerate() {
        let dir = scratch.join(format!("alone-{i:04}"));
        let v = runner::run_case_capture(&sky, &dir, c).unwrap_or_else(|e| format!("<error: {e}>"));
        alone.insert(c.id.clone(), v);
        let _ = std::fs::remove_dir_all(&dir);
    }

    let members: Vec<(usize, String, Body)> = forbidden
        .iter()
        .enumerate()
        .map(|(i, c)| (i, c.id.clone(), c.body.clone().unwrap()))
        .collect();
    let batched = run_batch(&sky, &scratch.join("batch"), &members).unwrap_or_default();

    let mut diverged = Vec::new();
    for c in &forbidden {
        let a = alone.get(&c.id).cloned().unwrap_or_default();
        let b = batched.get(&c.id).cloned().unwrap_or_else(|| "<missing>".into());
        if a != b {
            diverged.push((c.id.clone(), a, b));
        }
    }
    let _ = std::fs::remove_dir_all(&scratch);

    println!("  diverged when batched : {}/{}", diverged.len(), forbidden.len());
    for (id, a, b) in &diverged {
        println!("    {id}");
        println!("        alone   {a:?}");
        println!("        batched {b:?}");
    }
    println!();
    if diverged.is_empty() {
        println!("PROBE RESULT: batching these families changed NO verdict on this compiler.");
        println!("  The `unit` marking is therefore not currently load-bearing for VALUE");
        println!("  equality — it remains justified by the source-level hazard (a batched");
        println!("  neighbour CAN capture the fieldset/Model selection), but this probe did");
        println!("  not exhibit it. Reported as measured, not as assumed.");
    } else {
        println!("PROBE RESULT: batching CHANGED {} verdict(s). The isolation requirement", diverged.len());
        println!("  is load-bearing and the `unit` marking is doing real work.");
    }
    0
}

pub fn run(root: &Path) -> i32 {
    let Some(sky) = runner::sky_binary(root) else {
        eprintln!("corpus.isolation: no sky binary — build it first. A gate that cannot run has not passed.");
        return 1;
    };

    let all = super::all_cases();
    let batchable: Vec<GenCase> = all
        .iter()
        .filter(|c| c.isolation == super::gen::Isolation::Batch && c.body.is_some())
        .cloned()
        .collect();

    // Rotate the sample with the commit sha, per v2 §3.2.
    let seed = super::commit_seed(root);

    let n = SAMPLE.min(batchable.len());
    let start = if batchable.is_empty() { 0 } else { seed % batchable.len() };
    let members: Vec<(usize, String, Body)> = (0..n)
        .map(|i| {
            let c = &batchable[(start + i) % batchable.len()];
            (i, c.id.clone(), c.body.clone().unwrap())
        })
        .collect();

    println!("CORPUS ISOLATION GATE — v2 §3.2 (alone / in-batch / shuffled)");
    println!("  batchable cases : {}", batchable.len());
    println!("  sample          : {n} (seed offset {start}, rotates with the commit sha)");
    println!();

    let scratch = runner::scratch_root("isolation");
    let _ = std::fs::remove_dir_all(&scratch);

    // ---- 1. alone -------------------------------------------------------
    let mut alone: BTreeMap<String, String> = BTreeMap::new();
    for (i, id, _) in &members {
        let case = batchable
            .iter()
            .find(|c| &c.id == id)
            .expect("member came from batchable");
        let dir = scratch.join(format!("alone-{i:04}"));
        let v = match runner::run_case_capture(&sky, &dir, case) {
            Ok(v) => v,
            Err(e) => {
                println!("  alone {id}: ERROR {e}");
                format!("<error: {e}>")
            }
        };
        alone.insert(id.clone(), v);
        let _ = std::fs::remove_dir_all(&dir);
    }
    println!("  alone      : {} values", alone.len());

    // ---- 2. in-batch ----------------------------------------------------
    let batch_dir = scratch.join("batch");
    let in_batch = match run_batch(&sky, &batch_dir, &members) {
        Ok(m) => m,
        Err(e) => {
            println!("  in-batch   : BUILD FAILED — {e}");
            BTreeMap::new()
        }
    };
    println!("  in-batch   : {} values", in_batch.len());

    // ---- 3. shuffled ----------------------------------------------------
    // Reverse plus a rotation: this perturbs the (module, name) order the Model
    // heuristic depends on, which is the whole point of the third configuration.
    let mut shuffled_members = members.clone();
    shuffled_members.reverse();
    let rot = seed % shuffled_members.len().max(1);
    shuffled_members.rotate_left(rot);
    // Re-index so module names differ from the in-batch run too.
    for (new_idx, m) in shuffled_members.iter_mut().enumerate() {
        m.0 = new_idx;
    }
    let shuf_dir = scratch.join("shuffled");
    let shuffled = match run_batch(&sky, &shuf_dir, &shuffled_members) {
        Ok(m) => m,
        Err(e) => {
            println!("  shuffled   : BUILD FAILED — {e}");
            BTreeMap::new()
        }
    };
    println!("  shuffled   : {} values", shuffled.len());
    println!();

    // ---- compare --------------------------------------------------------
    let mut divergences = Vec::new();
    for (_, id, _) in &members {
        let a = alone.get(id).cloned().unwrap_or_else(|| "<missing>".into());
        let b = in_batch.get(id).cloned().unwrap_or_else(|| "<missing>".into());
        let c = shuffled.get(id).cloned().unwrap_or_else(|| "<missing>".into());
        if a != b || a != c {
            divergences.push((id.clone(), a, b, c));
        }
    }

    let _ = std::fs::remove_dir_all(&scratch);

    if divergences.is_empty() {
        println!(
            "ISOLATION GATE: PASS ({n} cases, identical verdicts alone / in-batch / shuffled)"
        );
        0
    } else {
        println!("  ---- {} DIVERGENCE(S) ----", divergences.len());
        for (id, a, b, c) in &divergences {
            println!("  {id}");
            println!("      alone    {a:?}");
            println!("      in-batch {b:?}");
            println!("      shuffled {c:?}");
        }
        println!();
        println!("ISOLATION GATE: FAIL — a case's verdict depends on its neighbours.");
        println!("  Either the case belongs in an `isolation = unit` family (v2 §3.2),");
        println!("  or a new family has started depending on whole-compilation state.");
        1
    }
}
