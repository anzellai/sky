//! Every GitHub workflow must be parseable YAML.
//!
//! This exists because it was not checked, and the consequence was total: a
//! step name containing `: ` —
//!
//! ```yaml
//! - name: Gate — lsp (Neovim editor-parity: 17 symbol-class + 32 corpus cases)
//! ```
//!
//! is invalid inside an unquoted plain scalar. GitHub could not parse
//! `rust-ci.yml`, so the run died in 0 s with "workflow file issue" and EVERY
//! job — corpus, macOS determinism, lsp-fuzz, postgres, the budget assert —
//! was skipped. A compiler change reached `main` with zero CI verification, and
//! the failure looked like a red X rather than like nothing having run.
//!
//! `xtask ci-scan` could not catch it: it reads workflows LINE BY LINE to
//! extract gate names and never parses the document. So the cycle whose premise
//! is "a gate that cannot fail is worse than no gate" ended with a CI
//! definition that could not RUN, guarded by nothing.
//!
//! Parsing is the whole assertion. A workflow that parses can still be wrong;
//! a workflow that does not parse cannot check anything at all.

use std::path::PathBuf;

fn workflows() -> Vec<PathBuf> {
    let dir = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../../.github/workflows"));
    let mut out: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("cannot read {}: {e}", dir.display()))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| matches!(p.extension().and_then(|x| x.to_str()), Some("yml" | "yaml")))
        .collect();
    out.sort();
    assert!(!out.is_empty(), "no workflows found — the glob is wrong, not the repo");
    out
}

#[test]
fn every_workflow_is_parseable_yaml() {
    let mut bad = Vec::new();
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        if let Err(e) = serde_yaml::from_str::<serde_yaml::Value>(&text) {
            bad.push(format!("{}: {e}", path.display()));
        }
    }
    assert!(
        bad.is_empty(),
        "workflow(s) are not valid YAML — GitHub will refuse the whole file and \
         run NOTHING:\n  {}\nA `:` followed by a space inside an unquoted step \
         name is the usual cause; wrap the value in single quotes.",
        bad.join("\n  ")
    );
}

/// A parseable workflow must still have the shape GitHub requires, or it parses
/// and then does nothing useful.
#[test]
fn every_workflow_declares_jobs_with_steps() {
    for path in workflows() {
        let text = std::fs::read_to_string(&path).expect("read workflow");
        let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("parsed above");
        let jobs = doc.get("jobs").unwrap_or_else(|| panic!("{}: no `jobs`", path.display()));
        let map = jobs
            .as_mapping()
            .unwrap_or_else(|| panic!("{}: `jobs` is not a mapping", path.display()));
        assert!(!map.is_empty(), "{}: `jobs` is empty", path.display());
        for (name, job) in map {
            let name = name.as_str().unwrap_or("<non-string>");
            // A job either runs steps or delegates to a reusable workflow.
            let has_steps = job.get("steps").and_then(|s| s.as_sequence()).is_some_and(|s| !s.is_empty());
            let has_uses = job.get("uses").is_some();
            assert!(
                has_steps || has_uses,
                "{}: job `{name}` has neither `steps` nor `uses`",
                path.display()
            );
        }
    }
}

/// The fan-in must actually fan in.
///
/// `ci-green` asserts every job in its `needs` succeeded, and is the check
/// INTENDED to be the single required one. A job that is NOT in that list still
/// runs and still shows a red X on the run, but `ci-green` goes green without
/// it — so once promoted, that job stops blocking merges.
///
/// STATE OF THE WORLD, 2026-08-13, because an earlier version of this docstring
/// asserted the opposite as fact and a Judge caught it: `main` currently has
/// **no required status checks at all** (`GET /branches/main/protection` returns
/// `required_status_checks: null`; PR review IS required). `rust-ci.yml`'s own
/// header describes ci-green as an ADDITIONAL check pending promotion, which is
/// step 2 of that rollout and lives in repo settings, outside this tree.
///
/// So today this test protects a property that is not yet load-bearing. That is
/// the right time to add it — the alternative is discovering on promotion day
/// that the list drifted for months — but the claim "in `needs` ⇒
/// merge-blocking" is FALSE until required checks are enabled, and saying
/// otherwise would be the same kind of unverified assertion this file exists to
/// stop.
///
/// So the `needs` list is load-bearing, and it is hand-maintained. Adding a job
/// and forgetting the one-line `needs` entry produces a gate that reports and
/// enforces nothing, which is indistinguishable from the failure this whole
/// cycle was opened to remove.
///
/// This is the general form of a hole found on 2026-08-12: the harness `T2`
/// tier — 383 behavioural assertions including the `Dict` key×operation
/// crossing built after #174 escaped — was in no workflow at all. Wiring it in
/// fixed that instance; this test is what stops the next one, one layer up.
#[test]
fn ci_green_needs_every_other_job_in_its_workflow() {
    let path = PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../.github/workflows/rust-ci.yml"
    ));
    let text = std::fs::read_to_string(&path).expect("read rust-ci.yml");
    let doc: serde_yaml::Value = serde_yaml::from_str(&text).expect("rust-ci.yml parses");
    let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("`jobs` mapping");

    let all: Vec<String> = jobs
        .keys()
        .filter_map(|k| k.as_str())
        .filter(|n| *n != "ci-green")
        .map(str::to_string)
        .collect();
    assert!(
        all.len() > 5,
        "only {} job(s) found besides ci-green — the parse is wrong, and a test \
         that inspects nothing passes silently",
        all.len()
    );

    let needs = jobs
        .get(serde_yaml::Value::from("ci-green"))
        .and_then(|g| g.get("needs"))
        .expect("ci-green declares `needs`");
    let declared: Vec<String> = match needs {
        serde_yaml::Value::Sequence(s) => {
            s.iter().filter_map(|v| v.as_str()).map(str::to_string).collect()
        }
        serde_yaml::Value::String(s) => vec![s.clone()],
        other => panic!("ci-green `needs` is neither a list nor a string: {other:?}"),
    };

    let missing: Vec<&String> = all.iter().filter(|j| !declared.contains(j)).collect();
    assert!(
        missing.is_empty(),
        "job(s) in rust-ci.yml are NOT in `ci-green.needs`, so they do not block a \
         merge — the required check can go green while they are red:\n  {}\n\
         Add each to the `needs:` list.",
        missing
            .iter()
            .map(|s| s.as_str())
            .collect::<Vec<_>>()
            .join("\n  ")
    );

    // The inverse: a `needs` entry naming a job that no longer exists makes the
    // whole fan-in job fail to schedule, which branch protection sees as a
    // missing check rather than a failure.
    let phantom: Vec<&String> = declared.iter().filter(|d| !all.contains(d)).collect();
    assert!(
        phantom.is_empty(),
        "ci-green `needs` names job(s) that do not exist in rust-ci.yml:\n  {}",
        phantom
            .iter()
            .map(|s| s.as_str())
            .collect::<Vec<_>>()
            .join("\n  ")
    );
}

/// Every harness TIER that has gates must be invoked by something.
///
/// This is the general form of a defect found three times in one cycle. Gate
/// selection is exact tier equality (`harness/mod.rs`: `g.tier ==
/// o.tier.unwrap_or(Tier::T1)`), so `--tier t1` runs T1 and nothing else. A gate
/// registered at any other tier is therefore invisible unless some workflow or
/// script names that tier explicitly.
///
/// What that cost, in order of discovery:
///   * **T2** — 383 behavioural assertions, including the `Dict` key ×
///     access-shape crossing built after #174's `Dict.foldl` panic reached a
///     release. Executed by nothing.
///   * **T3** — `apps-ledger-postgres`, `apps-dispatch-postgres`, `apps-fleet`.
///     Executed by nothing.
///   * **T4** — `apps-ffi-scale`, the pre-release FFI-scale benchmark whose
///     entire purpose is to run at release. Executed by nothing.
///
/// Each was registered, budgeted, documented, and dead. The registry even
/// records falsifying mutations proving some of them CAN fail — proofs against
/// gates nothing invoked.
///
/// A tier nobody runs is indistinguishable from a tier that does not exist, so
/// this asserts the invocation exists rather than trusting that someone
/// remembered. It deliberately checks BOTH workflows and scripts: a tier run
/// only by `preflight-tag.sh` is weaker than one in CI, but it is not dead, and
/// this test is about deadness.
#[test]
fn every_harness_tier_with_gates_is_invoked_somewhere() {
    let repo = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));

    // Which tiers the registry actually assigns to gates.
    let registry = std::fs::read_to_string(repo.join("rust/crates/xtask/src/harness/registry.rs"))
        .expect("read registry.rs");
    let mut needed: Vec<&str> = Vec::new();
    for (marker, flag) in [
        ("Tier::T1", "t1"),
        ("Tier::T2", "t2"),
        ("Tier::T3", "t3"),
        ("Tier::T4", "t4"),
    ] {
        if registry.contains(marker) {
            needed.push(flag);
        }
    }
    assert!(
        needed.len() >= 3,
        "found only {} tier(s) in registry.rs — the scan is wrong, and a test \
         that inspects nothing passes silently",
        needed.len()
    );

    // Everything that could invoke one.
    let mut haystack = String::new();
    for dir in ["\u{2e}github/workflows", "scripts"] {
        let d = repo.join(dir.replace('\u{2e}', "."));
        let Ok(rd) = std::fs::read_dir(&d) else { continue };
        for e in rd.filter_map(|e| e.ok()) {
            let p = e.path();
            if p.is_file() {
                if let Ok(t) = std::fs::read_to_string(&p) {
                    haystack.push_str(&t);
                    haystack.push('\n');
                }
            }
        }
    }
    assert!(
        haystack.contains("--tier"),
        "no `--tier` invocation found in any workflow or script — the file scan \
         is broken, not the repo"
    );

    let dead: Vec<&str> = needed
        .iter()
        .copied()
        .filter(|t| !haystack.contains(&format!("--tier {t}")))
        .collect();
    assert!(
        dead.is_empty(),
        "harness tier(s) have registered gates but are invoked by NO workflow \
         and NO script: {}\n\n\
         Gate selection is exact tier equality, so those gates run nowhere — \
         registered, budgeted, and dead. Add a `harness --tier <t>` invocation \
         to a workflow (or, for a pre-release tier, to release.yml), or move the \
         gates to a tier that runs.",
        dead.join(", ")
    );
}
