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
/// `ci-green` is the single required status check: branch protection keys on it,
/// and it asserts every job in its `needs` succeeded. A job that is NOT in that
/// list still runs and still shows a red X on the run — but it does not block a
/// merge, because the required check went green without it.
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
