//! LIVE: `Std.Ai.Train` with a real trainer process, and no Python.
//!
//! The Fake backend trains in the runtime itself: the fixture app starts
//! itself again as the trainer (`--sky-ai-fake-trainer`). The first trainer is
//! made to die after step 3; the durable workflow relaunches it from the newest
//! verified checkpoint and the run finishes. The adapter is converted to GGUF
//! and loaded by the real llama-server on the development model, and the same
//! adapter registered as trained on another base is refused (AdapterMismatch).
//!
//! It needs Go, the engine and the dev model in the Std.Ai home, or the
//! network to fetch them. Without them it FAILS naming `sky ai provision
//! --dev-model` (`live_gate`, `Need::AiTrainToolchain`). CI runs it in
//! `.github/workflows/rust-ci.yml`, job `ai-train-live`, with the Std.Ai home
//! cached on `runtime-go/rt/ai_pins.go`. The Unsloth driver's Python suites
//! run in `runtime-go/rt/ai_python_suites_live_test.go` (`go test -tags
//! ailive`) in the same job.

use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::Duration;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const DEV_MODEL_SHA256: &str = "7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed";

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
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn ai_home() -> PathBuf {
    if let Ok(h) = std::env::var("SKY_AI_HOME") {
        if !h.is_empty() {
            return PathBuf::from(h);
        }
    }
    if let Ok(h) = std::env::var("SKY_HOME") {
        if !h.is_empty() {
            return PathBuf::from(h).join("ai");
        }
    }
    let home = PathBuf::from(std::env::var("HOME").unwrap_or_else(|_| ".".into()));
    if cfg!(target_os = "macos") {
        return home.join("Library/Caches/sky/ai");
    }
    match std::env::var("XDG_CACHE_HOME") {
        Ok(x) if !x.is_empty() => PathBuf::from(x).join("sky/ai"),
        _ => home.join(".cache/sky/ai"),
    }
}

fn has_child(dir: &Path, rel: &str) -> bool {
    std::fs::read_dir(dir)
        .map(|rd| rd.flatten().any(|e| e.path().join(rel).exists()))
        .unwrap_or(false)
}

fn provisioned(home: &Path) -> bool {
    has_child(&home.join("engines/llama"), "llama-server")
        && home
            .join("models/sha256")
            .join(format!("{DEV_MODEL_SHA256}.gguf"))
            .is_file()
}

fn reachable(host: &str) -> bool {
    format!("{host}:443")
        .to_socket_addrs()
        .ok()
        .and_then(|mut a| a.next())
        .map(|addr| TcpStream::connect_timeout(&addr, Duration::from_secs(5)).is_ok())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-train-live-{tag}-{}", unique_suffix()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
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

fn json(out: &Output) -> serde_json::Value {
    let stdout = String::from_utf8_lossy(&out.stdout);
    let line = stdout.trim().lines().last().unwrap_or("");
    serde_json::from_str(line).unwrap_or_else(|e| panic!("not JSON ({e}):\n{}", text(out)))
}

/// Build the fixture project into a fresh directory and return it.
fn fixture() -> PathBuf {
    let project = scratch("project");
    copy_dir(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-train"),
        &project,
    );
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&project)
        .output()
        .unwrap();
    assert!(out.status.success(), "sky build: {}", text(&out));
    project
}

/// Run the fixture with no training or slot variable of the caller's.
fn app(project: &Path, args: &[&str]) -> Output {
    let mut cmd = Command::new(project.join("sky-out/app"));
    cmd.args(args)
        .current_dir(project)
        .env_remove("ENV")
        .env_remove("SKY_TEST_MODE");
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_") && k != "SKY_AI_HOME" && k != "SKY_AI_ACCEL" {
            cmd.env_remove(k);
        }
    }
    cmd.output().unwrap()
}

fn gate() -> bool {
    if !required(Need::Go, have_go()) {
        return false;
    }
    let available =
        provisioned(&ai_home()) || (reachable("github.com") && reachable("huggingface.co"));
    required(Need::AiTrainToolchain, available)
}

#[test]
fn fake_trains_in_its_own_process_relaunches_and_serves_on_the_real_engine() {
    if !gate() {
        return;
    }
    let project = fixture();
    let work = scratch("work");
    let w = work.to_string_lossy().to_string();

    let out = app(&project, &["drive", "fake", &w]);
    assert!(out.status.success(), "drive: {}", text(&out));
    let a = json(&out);
    assert_eq!(a["backend"], "fake", "{a}");
    assert_eq!(a["steps"], 10, "{a}");
    assert_eq!(
        a["relaunches"], 1,
        "the first trainer died after step 3: {a}"
    );
    assert_eq!(
        a["resumedFrom"], 2,
        "the relaunch resumes from checkpoint 2: {a}"
    );
    assert!(
        a["toolchain"]
            .as_str()
            .unwrap()
            .starts_with("fake@builtin-"),
        "the built-in Fake trainer, no Python toolchain: {a}"
    );
    let run_dir = work.join("runs").join(a["run"].as_str().unwrap());
    let events = std::fs::read_to_string(run_dir.join("events.jsonl")).unwrap();
    assert_eq!(events.matches("\"type\":\"hello\"").count(), 2, "{events}");
    for line in events.lines() {
        let v: serde_json::Value = serde_json::from_str(line).expect("only protocol lines");
        assert_eq!(v["v"], 1, "{line}");
    }
    let trainer: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(run_dir.join("trainer.json")).unwrap())
            .unwrap();
    assert_eq!(trainer["simulated"], false, "a real trainer ran: {trainer}");
    // The trainer was the app itself, started again: its hello names the
    // built-in trainer.
    assert!(
        events.contains("\"sky-fake-trainer\""),
        "the built-in Fake trainer: {events}"
    );

    // The GGUF adapter loads in the real llama-server on the dev model.
    let gguf = a["gguf"].as_str().unwrap().to_string();
    let sha = a["ggufSha256"].as_str().unwrap().to_string();
    assert!(Path::new(&gguf).is_file());
    let out = app(&project, &["serve", &gguf, &sha]);
    assert!(out.status.success(), "serve: {}", text(&out));
    assert!(
        !json(&out)["answer"].as_str().unwrap().is_empty(),
        "{}",
        text(&out)
    );

    // Registered as trained on another base, it is refused before it loads.
    let out = app(&project, &["mismatch", &gguf, &sha, &w]);
    assert!(out.status.success(), "mismatch: {}", text(&out));
    assert_eq!(json(&out)["outcome"], "AdapterMismatch", "{}", text(&out));

    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&work);
}

/// The native trainer's contract (scripts/ai/trainer/test-trainer.sh: the
/// gradient check, the falling loss, kill and resume to the same adapter,
/// cancel, the memory guard, quantised bases, the GGUF conversion, the
/// runtime's command line and the chunked loss) on the synthetic tiny model,
/// on the CPU, with the trainer a user gets: the operator's
/// SKY_AI_TRAINER_BIN, else the pinned bundle `sky ai provision --train mlx`
/// installs. CI's linux job takes it from the bundle workflow until the
/// bundles are pinned (.github/actions/sky-trainer).
#[test]
fn the_native_trainer_passes_its_contract_on_the_tiny_model() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let trainer = match std::env::var("SKY_AI_TRAINER_BIN") {
        Ok(b) if !b.trim().is_empty() => Some(PathBuf::from(b)),
        _ => {
            let out = Command::new(SKY)
                .args(["ai", "provision", "--train", "mlx", "--format", "json"])
                .current_dir(scratch("provision-trainer"))
                .output()
                .unwrap();
            if out.status.success() {
                json(&out)["trainer"].as_str().map(PathBuf::from)
            } else {
                None
            }
        }
    };
    if !required(
        Need::AiTrainer,
        trainer.as_ref().is_some_and(|t| t.is_file()),
    ) {
        return;
    }
    let trainer = trainer.unwrap();
    let work = scratch("trainer-contract");
    let out = Command::new("bash")
        .arg(repo_root().join("scripts/ai/trainer/test-trainer.sh"))
        .arg("--trainer")
        .arg(&trainer)
        .args(["--device", "cpu", "--work"])
        .arg(&work)
        .output()
        .unwrap();
    let report = text(&out);
    assert!(
        out.status.success(),
        "test-trainer.sh with {}:\n{report}",
        trainer.display()
    );
    assert!(report.contains("test-trainer: all passed"), "{report}");
    let _ = std::fs::remove_dir_all(&work);
}

/// The native trainer's memory guard SIGKILLs the trainer over its cap,
/// writing guard.json first and its error line after. The durable workflow
/// ends the run with `ModelTooLarge` and never relaunches it into the same
/// cap. A stand-in `sky-trainer` (SKY_AI_TRAINER_BIN) leaves the worst case:
/// guard.json and a SIGKILL, no error line. The rest of the workflow is real:
/// the pinned dev base, the tokenize step, the shape and the fit plan.
#[cfg(all(target_os = "macos", target_arch = "aarch64"))]
#[test]
fn a_guard_kill_ends_model_too_large_without_a_relaunch() {
    use std::os::unix::fs::PermissionsExt;
    if !gate() {
        return;
    }
    let project = fixture();
    let work = scratch("guard");
    let bin = work.join("sky-trainer");
    std::fs::write(
        &bin,
        r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo 'sky-trainer 0.1.0 (mlx-c)'; exit 0; fi
run=""
while [ $# -gt 0 ]; do if [ "$1" = "--run" ]; then run="$2"; fi; shift; done
echo '{"attempt":0,"backend":"mlx","resumeFrom":0,"toolchain":"native","totalSteps":12,"type":"hello","v":1}'
printf '{"capBytes":2500000000,"code":"ModelTooLarge","peakBytes":2600000000}\n' > "$run/guard.json"
kill -9 $$
"#,
    )
    .unwrap();
    std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
    let w = work.to_string_lossy().to_string();
    let mut cmd = Command::new(project.join("sky-out/app"));
    cmd.args(["drive", "mlx", &w])
        .current_dir(&project)
        .env_remove("ENV")
        .env_remove("SKY_TEST_MODE");
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_") && k != "SKY_AI_HOME" {
            cmd.env_remove(k);
        }
    }
    let out = cmd.env("SKY_AI_TRAINER_BIN", &bin).output().unwrap();
    let t = text(&out);
    assert!(!out.status.success(), "the run must fail: {t}");
    assert!(t.contains("ModelTooLarge"), "{t}");
    let runs: Vec<_> = std::fs::read_dir(work.join("runs"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.join("events.jsonl").is_file())
        .collect();
    assert_eq!(runs.len(), 1, "{runs:?}");
    let events = std::fs::read_to_string(runs[0].join("events.jsonl")).unwrap();
    assert_eq!(
        events.matches("\"type\":\"hello\"").count(),
        1,
        "no relaunch after the guard's kill:\n{events}"
    );
    let result = std::fs::read_to_string(runs[0].join("result.json")).unwrap();
    assert!(result.contains("ModelTooLarge"), "{result}");
    assert!(
        result.contains("2600000000"),
        "the peak it reached: {result}"
    );
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&work);
}
