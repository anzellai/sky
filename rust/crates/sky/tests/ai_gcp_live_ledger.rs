//! Offline checks of the live Google Cloud tooling's books: the price file's
//! worst cases and the ledger (`scripts/ai/live/`). Nothing here calls Google
//! Cloud; `ai_gcp_live.rs` holds the paid tests, and nextest leaves that
//! binary out by default, so these checks live in their own binary.
//!
//! * Every stage's worst case in `gcp-prices.json` covers the spend its
//!   enforced bound allows (the wrapper's stage window, the cancel wait, the
//!   Cloud Build timeout, at the catalogue's rates) AND every estimate a
//!   measured run of that stage has in the ledger. The budget guard reserves
//!   1.25 times the worst case; a worst case below a real run under-reserves.
//! * An l5 run's ledger line carries the cold-start figures its scenario
//!   measured (`coldStartMs`, `idleColdStartMs`), so the tree records them.

use std::path::{Path, PathBuf};
use std::process::Command;

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn read_json(rel: &str) -> serde_json::Value {
    let path = repo_root().join(rel);
    serde_json::from_str(&std::fs::read_to_string(&path).unwrap())
        .unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

fn ledger() -> Vec<serde_json::Value> {
    std::fs::read_to_string(repo_root().join("scripts/ai/live/gcp-ledger.jsonl"))
        .unwrap()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap())
        .collect()
}

fn rate(prices: &serde_json::Value, key: &str) -> f64 {
    prices["rates"][key]
        .as_str()
        .unwrap_or_else(|| panic!("gcp-prices.json has no rate {key}"))
        .parse()
        .unwrap()
}

fn num(v: &serde_json::Value, what: &str) -> f64 {
    v.as_f64()
        .unwrap_or_else(|| panic!("gcp-prices.json: {what} is missing or not a number"))
}

/// The Cloud Build timeout `sky ai image build` sets on each build
/// (sky-bundled/ai-cloud/src/Main.sky, `timeoutSeconds =`): Cloud Build stops
/// a build there even when the wrapper's process is gone.
fn cloud_build_timeout_in_source() -> f64 {
    let src =
        std::fs::read_to_string(repo_root().join("sky-bundled/ai-cloud/src/Main.sky")).unwrap();
    let at = src
        .find("timeoutSeconds =")
        .expect("sky-bundled/ai-cloud sets the Cloud Build timeout");
    src[at + "timeoutSeconds =".len()..]
        .split_whitespace()
        .next()
        .unwrap()
        .parse()
        .expect("the Cloud Build timeout is a literal number of seconds")
}

/// How many l3 scenarios the paid test runs at once (`let names = [...]` in
/// ai_gcp_live.rs): each is one chain of n2-standard-4 jobs.
fn parallel_l3_scenarios() -> f64 {
    let src =
        std::fs::read_to_string(repo_root().join("rust/crates/sky/tests/ai_gcp_live.rs")).unwrap();
    let at = src.find("let names = [").expect("the l3 scenario list");
    let end = at + src[at..].find(']').unwrap();
    src[at..end].matches("\"l3-").count() as f64
}

#[test]
fn each_stage_worst_case_covers_its_enforced_bound_and_every_measured_run() {
    let prices = read_json("scripts/ai/live/gcp-prices.json");
    let worst = &prices["stageWorstCaseUsd"];
    let mut failures = Vec::new();

    // 1. Every measured run: a stage's resources in one ledger line, summed.
    let mut measured: std::collections::BTreeMap<String, (f64, String)> = Default::default();
    for line in ledger() {
        let mut per_stage: std::collections::BTreeMap<String, f64> = Default::default();
        for r in line["resources"].as_array().unwrap() {
            let stage = r["stage"].as_str().unwrap().to_string();
            *per_stage.entry(stage).or_default() += r["estimateUsd"].as_f64().unwrap_or(0.0);
        }
        for (stage, usd) in per_stage {
            if !stage.starts_with('l') {
                continue; // a manual debug or cleanup line, not a wrapper stage
            }
            let e = measured.entry(stage).or_insert((0.0, String::new()));
            if usd > e.0 {
                *e = (usd, line["run"].as_str().unwrap_or("").to_string());
            }
        }
    }
    for (stage, (usd, run)) in &measured {
        let w = worst[stage.as_str()].as_f64().unwrap_or(0.0);
        if w + 1e-9 < *usd {
            failures.push(format!(
                "{stage}: worst case USD {w} is below run {run}'s estimate USD {usd:.4}"
            ));
        }
    }

    // 2. The enforced bound. A Vertex job's seconds count from its creation
    // (PENDING and provisioning included), and its scheduling.timeout counts
    // from RUNNING, so the timeout alone does not bound them. The wrapper
    // runs each stage under a wall-clock window and then cancels the run's
    // live jobs (waiting at most cancelWaitSeconds) and deletes the Cloud Run
    // service: window + cancel wait bounds every resource a stage makes.
    let limits = &prices["stageLimits"];
    if limits.is_null() {
        failures.push("gcp-prices.json has no stageLimits: no stage has an enforced bound".into());
    } else {
        let cancel = num(
            &limits["cancelWaitSeconds"],
            "stageLimits.cancelWaitSeconds",
        );
        let window = |s: &str| {
            num(
                &limits["stageSeconds"][s],
                &format!("stageLimits.stageSeconds.{s}"),
            ) + cancel
        };
        let n2 = rate(&prices, "vertexN2Standard4PerHour");
        let g2 = rate(&prices, "vertexG2Standard4PerHour");
        let run = rate(&prices, "cloudRunL4InstancePerHour");
        let build = rate(&prices, "cloudBuildPerMinute");
        let build_timeout = num(
            &limits["cloudBuildTimeoutSeconds"],
            "stageLimits.cloudBuildTimeoutSeconds",
        );
        if build_timeout != cloud_build_timeout_in_source() {
            failures.push(format!(
                "stageLimits.cloudBuildTimeoutSeconds {build_timeout} is not the timeout sky ai image build sets ({})",
                cloud_build_timeout_in_source()
            ));
        }
        let bounds: Vec<(&str, f64, String)> = vec![
            ("l0", 0.0, "reads only".into()),
            (
                "l1",
                // the trainer and the tier image, each stopped by Cloud Build
                2.0 * build_timeout / 60.0 * build
                    + num(
                        &limits["imageStorageAllowanceUsd"],
                        "stageLimits.imageStorageAllowanceUsd",
                    ),
                "two Cloud Builds at their timeout, and the image storage".into(),
            ),
            ("l2", 0.0, "a refused plan creates nothing".into()),
            (
                "l3",
                parallel_l3_scenarios() * window("l3") / 3600.0 * n2,
                format!("{} n2 chains for the window", parallel_l3_scenarios()),
            ),
            (
                "l4",
                window("l4") / 3600.0 * g2,
                "one g2 chain for the window".into(),
            ),
            (
                "l5",
                window("l5") / 3600.0 * (n2 + run),
                "the l5-prep n2 chain and the Cloud Run L4 instance for the window".into(),
            ),
        ];
        for (stage, bound, basis) in bounds {
            let w = worst[stage].as_f64().unwrap_or(-1.0);
            if w + 1e-9 < bound {
                failures.push(format!(
                    "{stage}: worst case USD {w} is below its enforced bound USD {bound:.4} ({basis})"
                ));
            }
        }
        // A window must let a run that passed finish: it must not be below
        // the longest resource a passing run of that stage recorded.
        for line in ledger().iter().filter(|l| l["testsPassed"] == true) {
            for r in line["resources"].as_array().unwrap() {
                let stage = r["stage"].as_str().unwrap();
                if let Some(limit) = limits["stageSeconds"][stage].as_f64() {
                    let secs = r["seconds"].as_f64().unwrap_or(0.0);
                    if secs > limit {
                        failures.push(format!(
                            "{stage}: the window {limit} s is shorter than a passing run's {secs} s"
                        ));
                    }
                }
            }
        }
        // The wrapper enforces the windows it is priced on.
        let wrapper = std::fs::read_to_string(repo_root().join("scripts/ai/gcp-live.sh")).unwrap();
        for needle in ["stage-seconds", "cancel-wait", "with_timeout \"$limit\""] {
            if !wrapper.contains(needle) {
                failures.push(format!(
                    "scripts/ai/gcp-live.sh does not enforce the stage windows (no `{needle}`)"
                ));
            }
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

/// The Python the wrapper uses: SKY_PYTHON, else Homebrew's, else python3.
fn python() -> String {
    if let Ok(p) = std::env::var("SKY_PYTHON") {
        if !p.is_empty() {
            return p;
        }
    }
    if Path::new("/opt/homebrew/bin/python3").exists() {
        return "/opt/homebrew/bin/python3".into();
    }
    let ok = Command::new("python3")
        .arg("--version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false);
    assert!(
        ok,
        "this test runs scripts/ai/live/ledger.py: install python3"
    );
    "python3".into()
}

#[test]
fn an_l5_run_appends_its_cold_start_figures_to_the_ledger_line() {
    let dir = std::env::temp_dir().join(format!("sky-gcp-ledger-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let stage_log = dir.join("stages.jsonl");
    let measure_log = dir.join("measurements.jsonl");
    let ledger = dir.join("ledger.jsonl");
    std::fs::write(
        &stage_log,
        "{\"stage\":\"l5\",\"kind\":\"cloud-run-l4\",\"seconds\":10,\"estimateUsd\":0.0032}\n",
    )
    .unwrap();
    // A test line of the l5 scenario's shape (not a measurement).
    std::fs::write(
        &measure_log,
        concat!(
            "{\"scenario\":\"l5\",\"deploySeconds\":11,\"coldStartMs\":1234,",
            "\"idleSeconds\":960,\"idleColdStartMs\":5678,\"hotLoadOk\":20,",
            "\"hotLoadErrors\":[],\"noIdTokenStatus\":401,",
            "\"badTokenStatus\":\"an error naming https://example.invalid/x\",",
            "\"base\":{\"ok\":true,\"text\":\"Yes\"},\"idleCold\":{\"ok\":false,\"error\":\"e\"},",
            "\"models\":{\"ok\":true,\"text\":\"m\"}}\n"
        ),
    )
    .unwrap();
    let out = Command::new(python())
        .arg("-I")
        .arg(repo_root().join("scripts/ai/live/ledger.py"))
        .args(["append", "--ledger"])
        .arg(&ledger)
        .arg("--stage-log")
        .arg(&stage_log)
        .arg("--measure-log")
        .arg(&measure_log)
        .args([
            "--run",
            "sky-live-test",
            "--started",
            "2026-10-14T00:00:00Z",
            "--stages",
            "l0,l1,l5",
            "--worst",
            "1.00",
            "--leak",
            "clean",
            "--project-tag",
            "00000000",
            "--status",
            "0",
            "--sha",
            "0000000",
        ])
        .output()
        .expect("run ledger.py");
    assert!(
        out.status.success(),
        "ledger.py append failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let text = std::fs::read_to_string(&ledger).unwrap();
    let line: serde_json::Value = serde_json::from_str(text.lines().next().unwrap()).unwrap();
    let m = &line["measurements"]["l5"];
    assert_eq!(m["coldStartMs"], 1234, "{line}");
    assert_eq!(m["idleColdStartMs"], 5678, "{line}");
    assert_eq!(m["deploySeconds"], 11, "{line}");
    assert_eq!(m["idleSeconds"], 960, "{line}");
    assert_eq!(m["hotLoadOk"], 20, "{line}");
    assert_eq!(m["hotLoadErrorCount"], 0, "{line}");
    assert_eq!(m["noIdTokenStatus"], 401, "{line}");
    assert_eq!(m["baseOk"], true, "{line}");
    assert_eq!(m["idleColdOk"], false, "{line}");
    // Response texts and error strings stay out: an error can name a URL.
    assert!(m.get("badTokenStatus").is_none(), "{line}");
    assert!(!m.to_string().contains("example.invalid"), "{line}");
    assert!(!m.to_string().contains("\"Yes\""), "{line}");
    assert_eq!(line["estimateUsd"], 0.0032, "{line}");
    assert_eq!(line["testsPassed"], true, "{line}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn every_l5_run_that_reached_the_tier_records_its_cold_start() {
    // A run with Cloud Run time reached the tier, so its scenario measured the
    // cold start; a passing run also measured the idle (scale-from-zero) one.
    let mut failures = Vec::new();
    for line in ledger() {
        let reached = line["resources"]
            .as_array()
            .unwrap()
            .iter()
            .any(|r| r["kind"] == "cloud-run-l4");
        if !reached {
            continue;
        }
        let m = &line["measurements"]["l5"];
        let run = line["run"].as_str().unwrap_or("");
        if !m["coldStartMs"].is_number() && line["note"].is_null() {
            failures.push(format!(
                "{run}: no measurements.l5.coldStartMs (and no note why)"
            ));
        }
        if line["testsPassed"] == true && !m["idleColdStartMs"].is_number() {
            failures.push(format!(
                "{run}: a passing l5 run with no measurements.l5.idleColdStartMs"
            ));
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn the_wrapper_and_the_l5_test_carry_the_measurements_to_the_ledger() {
    let wrapper = std::fs::read_to_string(repo_root().join("scripts/ai/gcp-live.sh")).unwrap();
    assert!(
        wrapper.contains("export SKY_AI_LIVE_MEASURE_LOG")
            && wrapper.contains("BOOKS=\"$ROOT/scripts/ai/live/ledger.py\"")
            && wrapper.contains("\"$BOOKS\" append")
            && wrapper.contains("--measure-log \"$MEASURE_LOG\""),
        "gcp-live.sh exports the measurement log and appends the ledger line with ledger.py"
    );
    let live =
        std::fs::read_to_string(repo_root().join("rust/crates/sky/tests/ai_gcp_live.rs")).unwrap();
    assert!(
        live.contains("SKY_AI_LIVE_MEASURE_LOG"),
        "the l5 test writes its scenario line to the measurement log"
    );
}
