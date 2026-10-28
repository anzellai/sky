//! `Std.Ai.Dataset`, `Std.Ai.Dataset.Scrub` and `Std.Ai.Dataset.Synthetic`
//! end to end.
//!
//! - The three examples (`examples/68-ai-dataset`, `69-ai-scrub`,
//!   `70-ai-synthetic`) pass their own `sky test` suites offline, in test mode
//!   (each commits a `.env.test`). The synthetic example's teacher and judge
//!   are the default local slots: under test mode their calls are answered by
//!   the example's `tests/mocks/` fixtures, so no engine starts and nothing is
//!   downloaded. The AI home is an empty directory with fetching off, and it
//!   must still be empty afterwards.
//! - `sky ai data freeze` freezes a JSONL file into a content-addressed
//!   version: the same rows in another key order and spacing give the same
//!   digest, a line that is not JSON fails naming the line and never its
//!   text, and a bad `--split` is refused.

use std::path::{Path, PathBuf};
use std::process::{Command, Output};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// A unique suffix for a scratch directory. The clock alone is not unique:
/// macOS gives microseconds, so two tests that start together got the SAME
/// directory and shared one fake engine (ai_cli_flow counted another test's
/// launches). The counter makes every name distinct within the process.
fn unique_suffix() -> String {
    use std::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    format!(
        "{}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sky-ai-dataset-{tag}-{}", unique_suffix()));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

/// Copy a project, leaving out build output and anything hidden except the
/// committed `.env.test` (which turns test mode on).
fn copy_project(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name().to_string_lossy().to_string();
        if name == "sky-out"
            || name == ".skycache"
            || (name.starts_with('.') && name != ".env.test")
        {
            continue;
        }
        let path = entry.path();
        if path.is_dir() {
            copy_project(&path, &to.join(&name));
        } else {
            std::fs::copy(&path, to.join(&name)).unwrap();
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

fn run_example_suite(example: &str, suite: &str) {
    let project = scratch(example);
    let home = scratch(&format!("{example}-home"));
    copy_project(&repo_root().join("examples").join(example), &project);
    assert!(
        project.join(".env.test").exists(),
        "{example} must commit a .env.test so `sky test` runs in test mode"
    );
    let out = Command::new(SKY)
        .args(["test", suite])
        .current_dir(&project)
        .env("SKY_AI_HOME", &home)
        .env("SKY_AI_FETCH", "off")
        .env_remove("SKY_AI_LLAMA_BIN")
        .env_remove("SKY_AI_TEACHER_URL")
        .env_remove("SKY_AI_JUDGE_URL")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky test");
    let report = text(&out);
    let leftovers = std::fs::read_dir(&home).map(|d| d.count()).unwrap_or(0);
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);
    assert!(
        out.status.success(),
        "{example}: {suite} must pass offline:\n{report}"
    );
    assert!(
        report.contains("0 failed"),
        "{example}: the runner must report its cases:\n{report}"
    );
    assert_eq!(
        leftovers, 0,
        "{example}: the AI home must stay empty (no engine, no model fetch)"
    );
}

#[test]
fn the_dataset_example_passes_its_suite_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    run_example_suite("68-ai-dataset", "tests/TicketsTest.sky");
}

#[test]
fn the_scrub_example_passes_its_suite_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    run_example_suite("69-ai-scrub", "tests/RedactTest.sky");
}

#[test]
fn the_synthetic_example_generates_from_fixtures_with_no_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    run_example_suite("70-ai-synthetic", "tests/FaqTest.sky");
}

fn freeze(dir: &Path, args: &[&str]) -> Output {
    let mut all = vec!["ai", "data", "freeze"];
    all.extend_from_slice(args);
    Command::new(SKY)
        .args(&all)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky ai data freeze")
}

fn json_field(out: &str, key: &str) -> String {
    let v: serde_json::Value =
        serde_json::from_str(out.trim()).unwrap_or_else(|e| panic!("not JSON ({e}): {out}"));
    match &v[key] {
        serde_json::Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}

#[test]
fn sky_ai_data_freeze_is_content_addressed_and_fails_typed() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("freeze");
    std::fs::write(
        dir.join("a.jsonl"),
        "{\"prompt\":\"p1\",\"completion\":\"c1\"}\n{\"prompt\":\"p2\",\"completion\":\"c2\"}\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("b.jsonl"),
        "{ \"completion\" : \"c1\", \"prompt\" : \"p1\" }\n\n{\"completion\":\"c2\",\"prompt\":\"p2\"}\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("bad.jsonl"),
        "{\"prompt\":\"p1\",\"completion\":\"c1\"}\n{\"prompt\":\"call ada@example.com\"\n",
    )
    .unwrap();

    let a = freeze(
        &dir,
        &[
            "a.jsonl", "--name", "faq", "--key", "prompt", "--seed", "3", "--format", "json",
        ],
    );
    assert!(a.status.success(), "freeze a.jsonl:\n{}", text(&a));
    let b = freeze(
        &dir,
        &[
            "b.jsonl", "--name", "faq", "--key", "prompt", "--seed", "3", "--format", "json",
        ],
    );
    assert!(b.status.success(), "freeze b.jsonl:\n{}", text(&b));
    let (da, db) = (
        json_field(&String::from_utf8_lossy(&a.stdout), "digest"),
        json_field(&String::from_utf8_lossy(&b.stdout), "digest"),
    );
    assert_eq!(da.len(), 64, "a sha256 digest: {da}");
    assert_eq!(da, db, "the same rows must freeze to the same digest");
    assert_eq!(json_field(&String::from_utf8_lossy(&a.stdout), "rows"), "2");
    let data = dir.join(".sky/ai/datasets/faq").join(format!("{da}.jsonl"));
    let manifest = dir.join(".sky/ai/datasets/faq").join(format!("{da}.json"));
    assert!(
        data.exists() && manifest.exists(),
        "data and manifest under .sky/ai/datasets/faq/"
    );
    let bytes = std::fs::read(&data).unwrap();
    let sum = Command::new("shasum")
        .args(["-a", "256"])
        .arg(&data)
        .output()
        .expect("shasum");
    let digest = String::from_utf8_lossy(&sum.stdout)
        .split_whitespace()
        .next()
        .unwrap_or_default()
        .to_string();
    assert_eq!(digest, da, "the digest is the SHA-256 of the data file");
    let body = String::from_utf8(bytes).unwrap();
    assert!(
        body.starts_with("{\"row\":{\"completion\":\"c1\",\"prompt\":\"p1\"},\"split\":\""),
        "canonical rows with sorted keys:\n{body}"
    );

    let bad = freeze(&dir, &["bad.jsonl"]);
    let said = text(&bad);
    assert!(
        !bad.status.success(),
        "a line that is not JSON must fail:\n{said}"
    );
    assert!(
        said.contains("DatasetInvalid") && said.contains("line 2"),
        "names the failure and the line:\n{said}"
    );
    assert!(
        !said.contains("ada@example.com"),
        "never repeats the row's text:\n{said}"
    );

    let split = freeze(&dir, &["a.jsonl", "--split", "80/10"]);
    assert!(
        !split.status.success(),
        "a bad --split must be refused:\n{}",
        text(&split)
    );

    let _ = std::fs::remove_dir_all(&dir);
}
