//! The paid live tests of Std.Ai's Google Cloud backends.
//!
//! Every test here is `#[ignore]` and needs `SKY_AI_GCP_LIVE=1`, which only
//! `scripts/ai/gcp-live.sh` sets: the wrapper checks the ledger's room under
//! the spending cap, creates the run's bucket, repositories, secret and
//! images, exports their names, runs these tests one at a time, and ALWAYS
//! tears everything down and checks for leaks. Run with `--ignored` and
//! without the wrapper, a test fails naming it (`live_gate`, `Need::GcpLive`).
//!
//! Each stage runs one scenario of the fixture program
//! (`fixtures/ai-gcp-live`, built once into the wrapper's work directory) and
//! checks the JSON line it prints. A stage the wrapper was not asked for
//! (`--stages`) returns without asserting and says so.
//!
//!   l2  plan with USD 0.01 is BudgetExceeded, and no job exists
//!   l3  Fake jobs on n2-standard-4: exactly-once launch after a lost journal
//!       row, a relaunch from a checkpoint, a cancel, the hard runtime ceiling
//!   l4  a real Unsloth QLoRA run on one L4 (g2-standard-4)
//!   l5  the GPU tier on Cloud Run: cold start, two adapters on one GPU, a
//!       promotion hot-loaded under load, the ID-token and service-token layers
//!
//! Every stage appends what it ran (machine, seconds, the estimate at the
//! catalogue's rate) to the wrapper's stage log, which becomes the ledger line.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::OnceLock;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

/// Whether this stage runs: the wrapper set SKY_AI_GCP_LIVE=1 (else the
/// live gate fails the test, naming the wrapper) and asked for the stage.
fn stage(name: &str) -> bool {
    let live = std::env::var("SKY_AI_GCP_LIVE").as_deref() == Ok("1");
    if !required(Need::GcpLive, live) {
        return false;
    }
    let stages = std::env::var("SKY_AI_LIVE_STAGES").unwrap_or_default();
    if stages.split(',').any(|s| s.trim() == name) {
        true
    } else {
        let _ = writeln!(
            std::io::stderr(),
            "ai_gcp_live: stage {name} was not asked for (--stages {stages}); not run"
        );
        false
    }
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        if name.to_string_lossy() == "sky-out" {
            continue;
        }
        let dest = to.join(&name);
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

/// The fixture program, built once per run in the wrapper's work directory
/// (its database and run directories are shared by the stages: l5 serves the
/// adapters l3 and l4 registered).
fn program() -> &'static (PathBuf, PathBuf) {
    static BUILT: OnceLock<(PathBuf, PathBuf)> = OnceLock::new();
    BUILT.get_or_init(|| {
        let work = PathBuf::from(
            std::env::var("SKY_AI_LIVE_STAGE_LOG").expect("SKY_AI_LIVE_STAGE_LOG from the wrapper"),
        )
        .parent()
        .unwrap()
        .join("fixture");
        copy_dir(
            &repo_root().join("rust/crates/sky/tests/fixtures/ai-gcp-live"),
            &work,
        );
        let out = Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&work)
            .output()
            .expect("sky build");
        assert!(
            out.status.success(),
            "the live fixture does not build:\n{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        (work.join("sky-out/app"), work)
    })
}

/// Run one scenario and return the JSON lines it printed.
fn scenario(name: &str, extra: &[(&str, String)]) -> Vec<serde_json::Value> {
    let (bin, dir) = program();
    let mut cmd = Command::new(bin);
    cmd.arg(name).current_dir(dir).stdin(Stdio::null());
    // A scenario's own database, run directory and registry prefix: the l3
    // scenarios run at once.
    // l5-prep registers the versions l5 serves, so the two share one world.
    let world = if name.starts_with("l5") { "l5" } else { name };
    cmd.env("SKY_AI_LIVE_DIR", format!(".sky/live/{world}"));
    if let Ok(reg) = std::env::var("SKY_AI_LIVE_REGISTRY") {
        cmd.env("SKY_AI_LIVE_REGISTRY", format!("{reg}/{world}"));
    }
    for (k, v) in extra {
        cmd.env(k, v);
    }
    let out = cmd.output().expect("run the live fixture");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let lines: Vec<serde_json::Value> = stdout
        .lines()
        .filter_map(|l| serde_json::from_str(l.trim()).ok())
        .collect();
    assert!(
        !lines.is_empty(),
        "scenario {name} printed no result (exit {:?}):\n{}{}",
        out.status.code(),
        stdout,
        String::from_utf8_lossy(&out.stderr)
    );
    for l in &lines {
        eprintln!("ai_gcp_live: {name}: {l}");
    }
    lines
}

fn rate(key: &str) -> f64 {
    let prices: serde_json::Value = serde_json::from_str(
        &std::fs::read_to_string(repo_root().join("scripts/ai/live/gcp-prices.json")).unwrap(),
    )
    .unwrap();
    prices["rates"][key].as_str().unwrap().parse().unwrap()
}

/// Append what a stage ran to the wrapper's stage log (the ledger line).
fn record(stage: &str, kind: &str, machine: &str, seconds: f64, per_hour: f64) {
    let path = std::env::var("SKY_AI_LIVE_STAGE_LOG").unwrap();
    let line = serde_json::json!({
        "stage": stage, "kind": kind, "machine": machine, "region": std::env::var("SKY_AI_TRAIN_REGION").unwrap_or_default(),
        "seconds": seconds.round(), "ratePerHour": per_hour, "priceBookVersion": "2026-10-12",
        "estimateUsd": ((seconds / 3600.0 * per_hour) * 10000.0).round() / 10000.0,
    });
    let mut f = std::fs::OpenOptions::new().append(true).open(path).unwrap();
    writeln!(f, "{line}").unwrap();
}

fn training(stage: &str, line: &serde_json::Value) {
    let machine = line["machine"].as_str().unwrap_or("");
    let per_hour = if machine.starts_with("g2") {
        rate("vertexG2Standard4PerHour")
    } else {
        rate("vertexN2Standard4PerHour")
    };
    record(
        stage,
        "vertex-custom-job",
        machine,
        line["seconds"].as_f64().unwrap_or(0.0),
        per_hour,
    );
}

#[test]
#[ignore = "paid: run through scripts/ai/gcp-live.sh"]
fn gcp_live_l2_a_budget_below_the_estimate_launches_nothing() {
    if !stage("l2") {
        return;
    }
    let out = scenario("l2", &[]);
    let r = &out[0];
    assert_eq!(r["code"], "BudgetExceeded", "{r}");
    assert_eq!(r["jobs"], 0, "a refused plan creates no job: {r}");
}

#[test]
#[ignore = "paid: run through scripts/ai/gcp-live.sh"]
fn gcp_live_l3_fake_jobs_exactly_once_relaunch_cancel_budget() {
    if !stage("l3") {
        return;
    }
    // The four scenarios run at once: four n2-standard-4 jobs.
    let names = ["l3-once", "l3-crash", "l3-cancel", "l3-budget"];
    let handles: Vec<_> = names
        .iter()
        .map(|n| {
            let n = n.to_string();
            std::thread::spawn(move || (n.clone(), scenario(&n, &[])))
        })
        .collect();
    let mut failures = Vec::new();
    for h in handles {
        let (name, lines) = h.join().expect("scenario thread");
        let last = lines.last().unwrap().clone();
        training("l3", &last);
        let check = match name.as_str() {
            "l3-once" => {
                let attempt0 = lines
                    .iter()
                    .find(|l| l["scenario"] == "l3-once-attempt-0")
                    .cloned()
                    .unwrap_or_default();
                (
                    attempt0["jobs"] == 1 && last["ok"] == true && last["steps"] == 20,
                    "one job for attempt 0 and the run finished",
                )
            }
            "l3-crash" => (
                last["ok"] == true
                    && last["relaunches"] == 1
                    && last["resumedFrom"] == 5
                    && last["steps"] == 12,
                "relaunched once from the checkpoint at step 5",
            ),
            "l3-cancel" => (last["code"] == "TrainingCancelled", "TrainingCancelled"),
            // Vertex stopped the job at its 3-minute ceiling: one job, no
            // relaunch, and its runtime (create to end, provisioning
            // included) far below the 27 minutes the steps need.
            _ => (
                last["code"] == "TrainingFailed"
                    && last["jobs"] == 1
                    && last["seconds"].as_i64().unwrap_or(i64::MAX) < 20 * 60,
                "TrainingFailed after the 3-minute ceiling, one job",
            ),
        };
        if !check.0 {
            failures.push(format!("{name}: expected {}: {last}", check.1));
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
#[ignore = "paid: run through scripts/ai/gcp-live.sh"]
fn gcp_live_l4_unsloth_qlora_on_one_l4() {
    if !stage("l4") {
        return;
    }
    let out = scenario("l4", &[]);
    let r = out.last().unwrap();
    training("l4", r);
    assert_eq!(r["ok"], true, "the Unsloth run: {r}");
    assert_eq!(r["steps"], 30, "{r}");
    assert!(
        r["registered"]
            .as_str()
            .unwrap_or("")
            .starts_with("support@v"),
        "{r}"
    );
}

/// The tier's service token, read from Secret Manager into memory and handed
/// to the fixture's environment; never printed.
fn tier_token() -> String {
    let secret = std::env::var("SKY_AI_LIVE_SECRET").unwrap();
    let project = std::env::var("SKY_AI_SERVE_PROJECT").unwrap();
    let out = Command::new("gcloud")
        .args([
            "secrets",
            "versions",
            "access",
            "latest",
            "--secret",
            &secret,
            "--project",
            &project,
        ])
        .stderr(Stdio::null())
        .output()
        .expect("gcloud secrets versions access");
    assert!(out.status.success(), "cannot read the tier's token secret");
    String::from_utf8(out.stdout).unwrap().trim().to_string()
}

fn active_account() -> String {
    let out = Command::new("gcloud")
        .args(["config", "list", "--format=value(core.account)"])
        .output()
        .expect("gcloud config list");
    String::from_utf8(out.stdout).unwrap().trim().to_string()
}

#[test]
#[ignore = "paid: run through scripts/ai/gcp-live.sh"]
fn gcp_live_l5_the_gpu_tier_on_cloud_run() {
    if !stage("l5") {
        return;
    }
    // The versions the tier serves: two small Fake runs on CPU Vertex jobs,
    // billed as Vertex jobs, not as Cloud Run time.
    let prep = scenario("l5-prep", &[]);
    for l in &prep {
        if l["scenario"]
            .as_str()
            .unwrap_or("")
            .starts_with("l5-prep-v")
        {
            training("l5", l);
            assert_eq!(l["ok"], true, "an l5 version trains and registers: {l}");
        }
    }
    let registered: Vec<&str> = prep
        .iter()
        .filter_map(|l| l["registered"].as_str())
        .collect();
    assert_eq!(
        registered,
        vec!["support@v1", "support@v2"],
        "l5-prep registers support v1 and v2"
    );
    let t0 = std::time::Instant::now();
    let out = scenario(
        "l5",
        &[
            ("SKY_AI_LIVE_TOKEN", tier_token()),
            ("SKY_AI_LIVE_INVOKER", active_account()),
        ],
    );
    let r = out.last().unwrap();
    // The service exists from the deploy until the wrapper deletes it; bill
    // the whole scenario at the instance's rate (an upper bound: it scales to
    // zero when idle).
    record(
        "l5",
        "cloud-run-l4",
        "1 L4, 5 vCPU, 20 GiB",
        t0.elapsed().as_secs_f64(),
        rate("cloudRunL4InstancePerHour"),
    );
    assert_eq!(r["base"]["ok"], true, "the base answers: {r}");
    assert_eq!(
        r["idleCold"]["ok"], true,
        "the base answers again after the idle wait (scale from zero): {r}"
    );
    assert_eq!(r["v1"]["ok"], true, "support-v1 answers: {r}");
    assert_eq!(
        r["v2"]["ok"], true,
        "support-v2 answers on the same GPU: {r}"
    );
    let models = r["models"]["text"].as_str().unwrap_or("");
    assert!(
        models.contains("support-v1") && models.contains("support-v2"),
        "/v1/models lists both: {r}"
    );
    assert_eq!(
        r["hotLoadOk"], 20,
        "the promotion is hot-loaded with no failed request: {r}"
    );
    assert_eq!(
        r["noIdTokenStatus"], 401,
        "Cloud Run answers 401 to a call without a valid ID token: {r}"
    );
    assert_eq!(
        r["badTokenStatus"], 401,
        "the tier refuses a wrong service token: {r}"
    );
}
