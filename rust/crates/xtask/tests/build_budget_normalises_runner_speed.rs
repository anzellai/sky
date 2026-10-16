//! The build-speed budget (`scripts/bench-build.sh --budget`) compares a
//! build's wall time as a RATIO to a fixed reference workload measured on the
//! same machine, and that comparison passes a slow runner and fails a slow Sky.
//!
//! # Why this exists
//!
//! ubuntu-latest is not one machine. The same cold `app50-web` build took
//! 95-115 CPU-s on some runners and 155-177 on others (rust-ci runs 38764872817
//! to 38892647386), and every scenario moved together. Against an absolute wall
//! budget, `build-perf` failed 2 of 8 runs on main and 4 of 7 on a branch with
//! no build-speed change at all. A gate that fails on the runner draw is
//! ignored, and an ignored gate does not catch the regression it exists for.
//!
//! # What this asserts
//!
//! It drives the REAL script in its `--report` mode (it compares a finished
//! report and builds nothing), against fixture reports derived from one budget:
//!
//! - the seed itself passes;
//! - a runner 1.6x slower (every wall time AND the reference x 1.6) passes;
//! - a Sky 1.6x slower on the same runner (one key's wall x 1.6, reference
//!   unchanged) FAILS, naming the key;
//! - the same slowdown on the slow runner FAILS;
//! - a report without a reference FAILS (no absolute fallback);
//! - a budget without ratios FAILS, and so does a reference workload that is not
//!   the one the budget was seeded against.
//!
//! The first two are the property the runner-speed fix needs; the rest prove
//! the gate still fails on a real regression (the falsifier).

use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::{json, Value};

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
        .canonicalize()
        .expect("canonicalize repo root")
}

const PKGS: &str = "net/http encoding/json text/template";
const KEYS: &[(&str, f64)] = &[
    ("app50-web/cold", 30.0),
    ("app50-web/edit", 1.8),
    ("app50-web/newdir", 1.2),
    ("app50-web/warm", 0.4),
];
const REF_S: f64 = 12.0;

fn budget(pkgs: &str, with_ratio: bool) -> Value {
    let mut b = serde_json::Map::new();
    for (k, w) in KEYS {
        let mut e = json!({ "wall_s": w, "peak_rss_bytes": 600_000_000u64 });
        if with_ratio {
            e["wall_ratio"] = json!(w / REF_S);
        }
        b.insert((*k).to_string(), e);
    }
    json!({
        "factor": 1.5,
        "slack_s": 2,
        "slack_bytes": 67_108_864u64,
        "platforms": { "linux-x86_64": {
            "reps": 3,
            "reference": { "packages": pkgs, "wall_s": REF_S },
            "budget": Value::Object(b),
        }}
    })
}

/// A report whose walls are the budget's x `speed` (x `slow` for `slow_key`)
/// and whose reference is `REF_S` x `ref_speed` (absent when `None`).
fn report(speed: f64, ref_speed: Option<f64>, slow_key: Option<(&str, f64)>) -> Value {
    let mut medians = serde_json::Map::new();
    for (k, w) in KEYS {
        let extra = match slow_key {
            Some((sk, f)) if sk == *k => f,
            _ => 1.0,
        };
        medians.insert(
            (*k).to_string(),
            json!({ "wall_s": w * speed * extra, "cpu_s": w * speed * extra * 3.0,
                    "peak_rss_bytes": 600_000_000u64 }),
        );
    }
    let mut r = json!({
        "machine": { "platform": "linux-x86_64", "cpus": 4 },
        "reps": 3,
        "medians": Value::Object(medians),
        "runs": [],
    });
    if let Some(rs) = ref_speed {
        r["reference"] = json!({ "packages": PKGS, "wall_s": REF_S * rs, "cpu_s": REF_S * rs * 3.0 });
    }
    r
}

/// Run the comparison; return (passed, stdout + stderr).
fn compare(dir: &Path, name: &str, report: &Value, budget: &Value) -> (bool, String) {
    let rp = dir.join(format!("{name}.report.json"));
    let bp = dir.join(format!("{name}.budget.json"));
    std::fs::write(&rp, serde_json::to_vec_pretty(report).unwrap()).unwrap();
    std::fs::write(&bp, serde_json::to_vec_pretty(budget).unwrap()).unwrap();
    let out = Command::new("bash")
        .arg("scripts/bench-build.sh")
        .arg("--report")
        .arg(&rp)
        .arg("--budget")
        .arg(&bp)
        .arg("--budget-subset")
        .current_dir(repo_root())
        .output()
        .expect("run scripts/bench-build.sh (bash on PATH?)");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    (out.status.success(), text)
}

fn scratch() -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-build-budget-test-{}", std::process::id()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

#[test]
fn a_slow_runner_passes_and_a_slow_sky_fails() {
    let dir = scratch();
    let b = budget(PKGS, true);

    let (ok, out) = compare(&dir, "seed", &report(1.0, Some(1.0), None), &b);
    assert!(ok, "the seed itself must pass:\n{out}");
    assert!(
        out.contains("PASS"),
        "a pass must say so (anti-vacuity):\n{out}"
    );

    let (ok, out) = compare(&dir, "slow-runner", &report(1.6, Some(1.6), None), &b);
    assert!(
        ok,
        "a runner 1.6x slower for every workload, the reference included, is the \
         runner draw, not a regression; it must pass:\n{out}"
    );

    let (ok, out) = compare(
        &dir,
        "slow-sky",
        &report(1.0, Some(1.0), Some(("app50-web/cold", 1.6))),
        &b,
    );
    assert!(
        !ok,
        "a cold build 1.6x slower on an unchanged runner is a regression; it must fail:\n{out}"
    );
    assert!(
        out.contains("OVER app50-web/cold"),
        "the failure must name the key:\n{out}"
    );

    let (ok, out) = compare(
        &dir,
        "slow-sky-on-slow-runner",
        &report(1.6, Some(1.6), Some(("app50-web/cold", 1.6))),
        &b,
    );
    assert!(
        !ok,
        "a regression on a slow runner must still fail:\n{out}"
    );
    assert!(out.contains("OVER app50-web/cold"), "{out}");

    // The absolute slack still guards sub-second builds against jitter: a warm
    // build 3x its budget is 1.2 s, within the 2 s floor.
    let (ok, out) = compare(
        &dir,
        "warm-jitter",
        &report(1.0, Some(1.0), Some(("app50-web/warm", 3.0))),
        &b,
    );
    assert!(ok, "a sub-second build within the slack must pass:\n{out}");

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn the_comparison_refuses_what_it_cannot_normalise() {
    let dir = scratch().join("refuse");
    std::fs::create_dir_all(&dir).unwrap();

    let (ok, out) = compare(&dir, "no-ref", &report(1.0, None, None), &budget(PKGS, true));
    assert!(
        !ok && out.contains("NO-REFERENCE"),
        "a report without a reference workload must fail, never fall back to an \
         absolute comparison:\n{out}"
    );

    let (ok, out) = compare(&dir, "no-ratio", &report(1.0, Some(1.0), None), &budget(PKGS, false));
    assert!(
        !ok && out.contains("NO-RATIO"),
        "a budget without ratios must fail until it is re-seeded:\n{out}"
    );

    let (ok, out) = compare(
        &dir,
        "other-ref",
        &report(1.0, Some(1.0), None),
        &budget("net/http", true),
    );
    assert!(
        !ok && out.contains("REFERENCE-MISMATCH"),
        "a budget seeded against another reference workload must fail:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

/// The committed budget is in ratio form for the CI platform, against the
/// reference workload the script measures today. A budget that lost its ratios
/// would fail every build-perf run with NO-RATIO; this says so before CI does.
#[test]
fn the_committed_linux_budget_is_in_ratio_form() {
    let root = repo_root();
    let text = std::fs::read_to_string(root.join("docs/perf/build-budget.json"))
        .expect("read docs/perf/build-budget.json");
    let b: Value = serde_json::from_str(&text).expect("the budget parses");
    let script = std::fs::read_to_string(root.join("scripts/bench-build.sh")).unwrap();
    let pkgs = script
        .lines()
        .find_map(|l| l.trim().strip_prefix("REFERENCE_PKGS=\""))
        .and_then(|r| r.split('"').next())
        .expect("bench-build.sh sets REFERENCE_PKGS");
    let p = &b["platforms"]["linux-x86_64"];
    assert_eq!(
        p["reference"]["packages"].as_str(),
        Some(pkgs),
        "the linux-x86_64 budget was seeded against another reference workload"
    );
    assert!(p["reference"]["wall_s"].as_f64().unwrap_or(0.0) > 0.0);
    let keys = p["budget"].as_object().expect("budget keys");
    assert!(keys.len() >= 8, "only {} budgeted keys", keys.len());
    for (k, v) in keys {
        assert!(
            v["wall_ratio"].as_f64().unwrap_or(0.0) > 0.0,
            "linux-x86_64 key {k} has no wall_ratio"
        );
    }
}
