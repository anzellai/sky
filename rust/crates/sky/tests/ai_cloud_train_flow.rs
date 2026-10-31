//! `Std.Ai.Train`'s Vertex backend, offline, on every commit.
//!
//! Two layers:
//!
//! * the workflow suite `fixtures/ai-cloud/tests/CloudWorkflowTest.sky` runs
//!   under `sky test` with SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid: the
//!   runtime's built-in emulator answers every Google Cloud call, in process.
//!   It proves a run end to end, exactly-once launch after a lost journal row,
//!   a relaunch from a checkpoint, cancel, the budget refusal and the budget
//!   stop, a stockout, a quota of 0, the region rule and the settings checks;
//! * the CLI against the emulator as a separate server
//!   (`runtime-go/rt/testdata/fakegcp`): `sky ai train --backend vertex`
//!   trains, registers and cleans up; `sky ai cloud ls` sees nothing left;
//!   `sky ai teardown --run` is idempotent.

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn unique() -> String {
    use std::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    format!(
        "{}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-ai-cloud-{tag}-{}", unique()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let s = name.to_string_lossy();
        if s == "sky-out" || s == ".skyapp" || s == ".sky" || s.starts_with(".env") {
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

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[test]
fn the_vertex_workflow_suite_passes_against_the_in_process_emulator() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("suite");
    copy_dir(
        &repo_root().join("rust/crates/sky/tests/fixtures/ai-cloud"),
        &dir,
    );
    // The committed tree cannot carry a fixture's .env.test (gitignored), so
    // the flow writes the test-mode marker: the emulator's host.
    let creds = credentials(&dir);
    std::fs::write(
        dir.join(".env.test"),
        format!(
            "SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid\nSKY_AI_TEST_CREDS={}\n",
            creds.display()
        ),
    )
    .unwrap();
    let out = Command::new(SKY)
        .args(["test", "tests/CloudWorkflowTest.sky"])
        .current_dir(&dir)
        .env_remove("GOOGLE_APPLICATION_CREDENTIALS")
        .env_remove("SKY_AI_TRAIN_BACKEND")
        .env_remove("SKY_AI_TRAIN_PROJECT")
        .env_remove("SKY_AI_TRAIN_STAGING")
        .env_remove("SKY_AI_TRAIN_IMAGE")
        .output()
        .expect("sky test");
    let t = text(&out);
    assert!(out.status.success(), "the workflow suite:\n{t}");
    assert!(t.contains("17 passed, 0 failed"), "every case ran:\n{t}");

    let out = Command::new(SKY)
        .args(["test", "tests/GoogleAuthTest.sky"])
        .current_dir(&dir)
        .env_remove("GOOGLE_APPLICATION_CREDENTIALS")
        .output()
        .expect("sky test");
    let t = text(&out);
    assert!(out.status.success(), "the credential suite:\n{t}");
    assert!(t.contains("1 passed, 0 failed"), "every case ran:\n{t}");
}

/// The credential files GoogleAuthTest.sky reads: a service-account key
/// (with an RSA key made for this run, so no key is in the repository), a
/// user's own login, and a workload identity federation credential whose
/// subject token is a file. Every endpoint is the emulator's.
fn credentials(dir: &Path) -> PathBuf {
    let creds = dir.join("creds");
    std::fs::create_dir_all(&creds).unwrap();
    let key = creds.join("key.pem");
    let out = Command::new("openssl")
        .args([
            "genpkey",
            "-algorithm",
            "RSA",
            "-pkeyopt",
            "rsa_keygen_bits:2048",
            "-out",
        ])
        .arg(&key)
        .output()
        .expect("openssl (install it: the credential suite signs with a key it makes)");
    assert!(out.status.success(), "openssl genpkey:\n{}", text(&out));
    let pem = std::fs::read_to_string(&key).unwrap();
    let sa = serde_json::json!({
        "type": "service_account", "client_email": "sky-test@p.iam.gserviceaccount.com",
        "private_key": pem, "token_uri": "https://oauth2.googleapis.com/token",
    });
    std::fs::write(creds.join("sa.json"), sa.to_string()).unwrap();
    std::fs::write(
        creds.join("user.json"),
        r#"{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}"#,
    )
    .unwrap();
    std::fs::write(
        creds.join("subject.txt"),
        "a-subject-token-from-the-ci-provider",
    )
    .unwrap();
    let ext = serde_json::json!({
        "type": "external_account",
        "audience": "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/github",
        "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
        "token_url": "http://sky-gcp.invalid/sts/v1/token",
        "service_account_impersonation_url": "http://sky-gcp.invalid/iamcredentials/v1/projects/-/serviceAccounts/sky-test@p.iam.gserviceaccount.com:generateAccessToken",
        "credential_source": { "file": creds.join("subject.txt").display().to_string() },
    });
    std::fs::write(creds.join("external.json"), ext.to_string()).unwrap();
    creds
}

fn http_get(addr: &str, path: &str) -> String {
    let mut s = TcpStream::connect(addr).expect("connect");
    s.set_read_timeout(Some(Duration::from_secs(10))).unwrap();
    write!(
        s,
        "GET {path} HTTP/1.1\r\nHost: {addr}\r\nConnection: close\r\n\r\n"
    )
    .unwrap();
    let mut buf = String::new();
    let _ = s.read_to_string(&mut buf);
    buf.split_once("\r\n\r\n")
        .map(|(_, b)| b.to_string())
        .unwrap_or_default()
}

#[cfg(unix)]
#[test]
fn sky_ai_train_runs_on_vertex_through_the_cli() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("cli");
    let bin = dir.join("fakegcp");
    let out = Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg("./rt/testdata/fakegcp")
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .unwrap();
    assert!(out.status.success(), "go build fakegcp:\n{}", text(&out));
    let port_file = dir.join("port");
    // The fake Google Cloud server and anything it starts end with the
    // test on every path, a panic included.
    let _gcp = child_group::ChildGroup::spawn(
        Command::new(&bin)
            .arg("-port-file")
            .arg(&port_file)
            .stdout(Stdio::null())
            .stderr(Stdio::null()),
    )
    .unwrap();
    let start = Instant::now();
    let addr = loop {
        if let Ok(a) = std::fs::read_to_string(&port_file) {
            if !a.is_empty() {
                break a;
            }
        }
        assert!(
            start.elapsed() < Duration::from_secs(10),
            "fakegcp did not start"
        );
        std::thread::sleep(Duration::from_millis(20));
    };
    std::fs::copy(
        repo_root().join("examples/73-ai-train/data/support.jsonl"),
        dir.join("support.jsonl"),
    )
    .unwrap();
    let sky = |args: &[&str]| {
        Command::new(SKY)
            .arg("ai")
            .args(args)
            .current_dir(&dir)
            .env("SKY_AI_HOME", dir.join("home"))
            .env("SKY_AI_GCP_ENDPOINT", format!("http://{addr}"))
            .env("SKY_TEST_MODE", "1")
            .env_remove("GOOGLE_APPLICATION_CREDENTIALS")
            .env_remove("SKY_AI_TRAIN_BACKEND")
            .env_remove("SKY_DB_PATH")
            .env_remove("DATABASE_URL")
            .output()
            .expect("sky ai")
    };
    let out = sky(&[
        "data",
        "freeze",
        "support.jsonl",
        "--name",
        "support",
        "--out",
        ".sky/ai/datasets",
        "--split",
        "70/30/0",
    ]);
    assert!(out.status.success(), "freeze:\n{}", text(&out));
    let out = sky(&[
        "train",
        "--data",
        ".sky/ai/datasets/support",
        "--backend",
        "vertex",
        "--steps",
        "6",
        "--rank",
        "2",
        "--layers",
        "1",
        "--checkpoint-every",
        "2",
        "--project",
        "p",
        "--region",
        "us-central1",
        "--machine",
        "n2-standard-4",
        "--staging",
        "gs://bkt/sky",
        "--budget",
        "USD 2.00",
        "--image",
        "r/sky-ai-trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
        "--price",
        "n2-standard-4@us-central1=0.28",
        "--price-book",
        "2026-10-12",
        "--label",
        "team=support",
        "--register",
        "--db",
        ".sky/ai/train.db",
        "--format",
        "json",
    ]);
    let t = text(&out);
    assert!(out.status.success(), "sky ai train --backend vertex:\n{t}");
    let v: serde_json::Value = serde_json::from_str(
        String::from_utf8_lossy(&out.stdout)
            .trim()
            .lines()
            .last()
            .unwrap(),
    )
    .unwrap();
    assert_eq!(v["backend"], "vertex", "{v}");
    assert_eq!(v["steps"], 6, "{v}");
    let stats: serde_json::Value = serde_json::from_str(&http_get(&addr, "/_fake/stats")).unwrap();
    assert_eq!(stats["creates"], 1, "one job: {stats}");
    assert_eq!(
        stats["jobs"].as_array().map(|a| a.len()),
        Some(0),
        "the job record is deleted: {stats}"
    );
    let objects = http_get(&addr, "/_fake/objects?prefix=bkt/sky/runs/");
    assert!(
        objects.contains("\"objects\":null") || objects.contains("\"objects\":[]"),
        "the run's prefix is empty: {objects}"
    );

    let out = sky(&["cloud", "ls", "--project", "p", "--region", "us-central1"]);
    assert!(text(&out).contains("nothing Sky made"), "{}", text(&out));
    for _ in 0..2 {
        let out = sky(&[
            "teardown",
            "--run",
            "sky-0000",
            "--staging",
            "gs://bkt/sky",
            "--project",
            "p",
            "--region",
            "us-central1",
        ]);
        assert!(
            out.status.success(),
            "teardown --run is idempotent:\n{}",
            text(&out)
        );
    }
}
