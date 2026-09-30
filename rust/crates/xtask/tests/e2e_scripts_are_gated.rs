//! Rules that keep the release workflow honest about what it runs.
//!
//! - Every `scripts/*-e2e.sh` runs in a release gating job, or is named in
//!   [`EXEMPT`] with the reason. `scripts/nav-e2e.sh` (Std.Nav in a real
//!   browser) and `scripts/example-e2e.sh` (the `examples/*/e2e.json`
//!   contracts) ran in no workflow, while the CHANGELOG and the coverage ledger
//!   cited them as coverage (G-1, G-9). A hand list of required scripts could
//!   not notice a new script; this rule does.
//! - An e2e script checks its prerequisites with `require_tool`, never a bare
//!   `command -v … || exit 1`, so `SKY_LIVE_TESTS=skip` means the same thing
//!   everywhere (G-19).
//! - A harness step's gate budgets fit inside its job's `timeout-minutes`, so a
//!   slow gate ends as a harness verdict, not a cancelled job (G-18).
//! - The release job signs a build-provenance attestation for every published
//!   asset, and holds the permissions that needs (F-9).

use std::path::PathBuf;

/// Scripts that are not run by a release gating job, each with the reason.
/// Empty today: every e2e script is gated.
const EXEMPT: &[(&str, &str)] = &[];

/// Minutes a gate job spends outside the harness (checkout, toolchain and
/// cache restore, the compiler build, cache save).
const SETUP_MINUTES: u64 = 15;

fn root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn release() -> serde_yaml::Value {
    let text = std::fs::read_to_string(root().join(".github/workflows/release.yml"))
        .expect("read release.yml");
    serde_yaml::from_str(&text).expect("release.yml parses")
}

/// Every `run:` (and action `script:`) of the enabled `gate-*` jobs.
fn gate_runs(doc: &serde_yaml::Value) -> Vec<(String, String)> {
    let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("jobs");
    let mut out = Vec::new();
    for (k, job) in jobs {
        let Some(name) = k.as_str() else { continue };
        if !name.starts_with("gate-") {
            continue;
        }
        if job.get("if").and_then(|v| v.as_bool()) == Some(false) {
            continue;
        }
        for step in job
            .get("steps")
            .and_then(|s| s.as_sequence())
            .into_iter()
            .flatten()
        {
            if step.get("continue-on-error").and_then(|v| v.as_bool()) == Some(true) {
                continue;
            }
            for key in ["run"] {
                if let Some(r) = step.get(key).and_then(|r| r.as_str()) {
                    out.push((name.to_string(), r.to_string()));
                }
            }
            if let Some(r) = step
                .get("with")
                .and_then(|w| w.get("script"))
                .and_then(|r| r.as_str())
            {
                out.push((name.to_string(), r.to_string()));
            }
        }
    }
    out
}

fn e2e_scripts() -> Vec<String> {
    let mut out: Vec<String> = std::fs::read_dir(root().join("scripts"))
        .expect("read scripts/")
        .filter_map(|e| e.ok())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .filter(|n| n.ends_with("-e2e.sh"))
        .collect();
    out.sort();
    assert!(out.len() >= 10, "found only {} e2e scripts", out.len());
    out
}

#[test]
fn every_e2e_script_runs_in_a_release_gate_or_declares_an_exemption() {
    let doc = release();
    let runs = gate_runs(&doc);
    let needs: Vec<String> = doc
        .get("jobs")
        .and_then(|j| j.get("release"))
        .and_then(|r| r.get("needs"))
        .and_then(|n| n.as_sequence())
        .expect("release: needs:")
        .iter()
        .filter_map(|v| v.as_str().map(str::to_string))
        .collect();
    let mut missing = Vec::new();
    for script in e2e_scripts() {
        let call = format!("scripts/{script}");
        let gated = runs.iter().any(|(job, r)| {
            needs.iter().any(|n| n == job)
                && r.lines().any(|l| {
                    let l = l.trim();
                    !l.starts_with('#') && l.contains(&call)
                })
        });
        let exempt = EXEMPT
            .iter()
            .any(|(n, why)| *n == script && !why.trim().is_empty());
        if !gated && !exempt {
            missing.push(script);
        }
    }
    assert!(
        missing.is_empty(),
        "e2e script(s) that no release gating job runs: {missing:?}. Add each to a \
         gate-* job in release.yml (and the nightly), or to EXEMPT with the reason."
    );
    for (n, _) in EXEMPT {
        assert!(
            root().join("scripts").join(n).is_file(),
            "EXEMPT names a script that does not exist: {n}"
        );
    }
}

#[test]
fn e2e_scripts_check_prerequisites_with_require_tool() {
    let mut bad = Vec::new();
    for script in e2e_scripts() {
        let text = std::fs::read_to_string(root().join("scripts").join(&script)).unwrap();
        for (i, line) in text.lines().enumerate() {
            let l = line.trim();
            if l.starts_with('#') {
                continue;
            }
            if l.contains("command -v") && l.contains("||") && l.contains("exit") {
                bad.push(format!("{script}:{}: {l}", i + 1));
            }
        }
    }
    assert!(
        bad.is_empty(),
        "prerequisite checks that bypass require_tool (SKY_LIVE_TESTS=skip does not \
         apply to them):\n  {}",
        bad.join("\n  ")
    );
}

/// The `budget_s` of every registered harness gate.
fn budgets() -> Vec<(String, u64)> {
    let text = std::fs::read_to_string(root().join("rust/crates/xtask/src/harness/registry.rs"))
        .expect("read the registry");
    let mut out = Vec::new();
    let mut name: Option<String> = None;
    for line in text.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("name: \"") {
            name = rest.split('"').next().map(str::to_string);
        } else if let Some(rest) = t.strip_prefix("budget_s: ") {
            let digits: String = rest
                .chars()
                .take_while(|c| c.is_ascii_digit() || *c == '_')
                .filter(|c| *c != '_')
                .collect();
            if let (Some(n), Ok(b)) = (name.take(), digits.parse::<u64>()) {
                out.push((n, b));
            }
        }
    }
    assert!(out.len() > 30, "registry parse found {} budgets", out.len());
    out
}

#[test]
fn every_harness_shard_fits_inside_its_job_timeout() {
    let doc = release();
    let budgets = budgets();
    let jobs = doc.get("jobs").and_then(|j| j.as_mapping()).expect("jobs");
    let mut over = Vec::new();
    for (k, job) in jobs {
        let Some(name) = k.as_str() else { continue };
        let Some(timeout) = job.get("timeout-minutes").and_then(|t| t.as_u64()) else {
            continue;
        };
        let mut sum = 0u64;
        for step in job
            .get("steps")
            .and_then(|s| s.as_sequence())
            .into_iter()
            .flatten()
        {
            let Some(run) = step.get("run").and_then(|r| r.as_str()) else {
                continue;
            };
            for line in run
                .lines()
                .filter(|l| l.contains(" harness ") && !l.contains("--verify-falsifiers"))
            {
                let toks: Vec<&str> = line.split_whitespace().collect();
                let Some(i) = toks.iter().position(|t| *t == "--only") else {
                    continue;
                };
                for gate in toks[i + 1].split(',') {
                    let b = budgets
                        .iter()
                        .find(|(n, _)| n == gate)
                        .map(|(_, b)| *b)
                        .unwrap_or_else(|| panic!("{name}: --only names unknown gate {gate}"));
                    sum += b;
                }
            }
        }
        if sum == 0 {
            continue;
        }
        let need = sum.div_ceil(60) + SETUP_MINUTES;
        if timeout < need {
            over.push(format!(
                "{name}: timeout-minutes {timeout}, but its gates budget {} min + {SETUP_MINUTES} \
                 min setup = {need}",
                sum.div_ceil(60)
            ));
        }
    }
    assert!(
        over.is_empty(),
        "a slow gate would end as a cancelled job, not a harness verdict:\n  {}",
        over.join("\n  ")
    );
}

#[test]
fn the_release_job_attests_the_published_assets() {
    let doc = release();
    let job = doc
        .get("jobs")
        .and_then(|j| j.get("release"))
        .expect("release job");
    let perms = job.get("permissions").expect("release job permissions");
    for (k, v) in [
        ("contents", "write"),
        ("id-token", "write"),
        ("attestations", "write"),
    ] {
        assert_eq!(
            perms.get(k).and_then(|x| x.as_str()),
            Some(v),
            "release job needs `{k}: {v}`"
        );
    }
    let steps = job
        .get("steps")
        .and_then(|s| s.as_sequence())
        .expect("steps");
    let pos = |pred: &dyn Fn(&serde_yaml::Value) -> bool| steps.iter().position(|s| pred(s));
    let sums = pos(&|s| {
        s.get("run")
            .and_then(|r| r.as_str())
            .is_some_and(|r| r.contains("> checksums.txt"))
    })
    .expect("a step writes checksums.txt");
    let attest = pos(&|s| {
        s.get("uses")
            .and_then(|u| u.as_str())
            .is_some_and(|u| u.starts_with("actions/attest-build-provenance@"))
    })
    .expect("a step attests build provenance");
    let publish = pos(&|s| {
        s.get("uses")
            .and_then(|u| u.as_str())
            .is_some_and(|u| u.starts_with("softprops/action-gh-release@"))
    })
    .expect("a step publishes the release");
    assert!(
        sums < attest && attest < publish,
        "checksums, attest, publish"
    );
    let subject = steps[attest]
        .get("with")
        .and_then(|w| w.get("subject-path"))
        .and_then(|p| p.as_str())
        .unwrap_or("");
    assert_eq!(subject, "release/*", "every published asset is attested");
}
