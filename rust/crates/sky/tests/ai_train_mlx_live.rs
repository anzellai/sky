#![cfg_attr(
    not(all(target_os = "macos", target_arch = "aarch64")),
    allow(dead_code, unused_imports)
)]
//! LIVE: a real QLoRA run with MLX on Apple silicon, from Sky code.
//!
//! The fixture trains the development base (Qwen2.5 0.5B Instruct, the
//! `mlx-community` 4-bit snapshot) for 12 steps of 2 rows of 256 tokens, rank
//! 4 on the last 4 layers, a checkpoint every 4 steps. After the first
//! checkpoint the test SIGKILLs the trainer's process group; the durable
//! workflow sees the trainer gone without a terminal line, relaunches it from
//! the newest verified checkpoint, and the run finishes at step 12 with a
//! finite eval loss. The adapter is converted to GGUF, served by the managed
//! llama-server on the dev model, and evaluated against the base on the same
//! three cases.
//!
//! macOS on Apple silicon only. It needs Go, the engine, the dev model, the
//! Mlx toolchain and the dev base weights in the Std.Ai home, or the network to
//! fetch them (about 700 MB); without them it FAILS naming `sky ai provision
//! --train mlx --base dev` (`live_gate`, `Need::AiTrainMlx`). On a CI runner
//! without Metal, `SKY_AI_ACCEL=cpu` runs MLX on the CPU. CI runs it in
//! `.github/workflows/rust-ci.yml`, job `ai-train-mlx-live`.

use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const DEV_MODEL_SHA256: &str = "7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed";

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
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
        && has_child(&home.join("toolchains/mlx"), "ok.json")
        && home
            .join("bases/mlx-community--Qwen2.5-0.5B-Instruct-4bit@a5339a4131f135d0fdc6a5c8b5bbed2753bbe0f3/model.safetensors")
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
    let d = std::env::temp_dir().join(format!(
        "sky-train-mlx-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
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
    let available = provisioned(&ai_home())
        || (reachable("github.com") && reachable("pypi.org") && reachable("huggingface.co"));
    required(Need::AiTrainMlx, available)
}

/// The run directory of the fixture's MLX job, once it exists.
fn run_dir(work: &Path) -> Option<PathBuf> {
    std::fs::read_dir(work.join("runs"))
        .ok()?
        .flatten()
        .map(|e| e.path())
        .find(|p| p.join("job.json").is_file())
}

#[cfg(all(target_os = "macos", target_arch = "aarch64"))]
#[test]
fn mlx_trains_survives_a_killed_trainer_serves_and_evaluates() {
    if !gate() {
        return;
    }
    let project = fixture();
    let work = scratch("work");
    let w = work.to_string_lossy().to_string();

    let mut cmd = Command::new(project.join("sky-out/app"));
    cmd.args(["drive", "mlx", &w])
        .current_dir(&project)
        .env_remove("ENV")
        .env_remove("SKY_TEST_MODE")
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_") && k != "SKY_AI_HOME" && k != "SKY_AI_ACCEL" {
            cmd.env_remove(k);
        }
    }
    let driver = cmd.spawn().unwrap();

    // Wait for the first checkpoint (a first run fetches ~700 MB first), then
    // SIGKILL the trainer's whole process group.
    let deadline = Instant::now() + Duration::from_secs(1800);
    let mut killed = false;
    while Instant::now() < deadline {
        if let Some(dir) = run_dir(&work) {
            if dir.join("checkpoints/0000004/manifest.json").is_file() {
                let t: serde_json::Value = serde_json::from_str(
                    &std::fs::read_to_string(dir.join("trainer.json")).unwrap(),
                )
                .unwrap();
                let pid = t["pid"].as_i64().unwrap();
                if t["attempt"] == 0 && pid > 0 {
                    let st = Command::new("kill")
                        .arg("-KILL")
                        .arg(format!("-{pid}"))
                        .status()
                        .unwrap();
                    assert!(st.success(), "kill the trainer group {pid}");
                    killed = true;
                }
                break;
            }
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let out = driver.wait_with_output().unwrap();
    assert!(out.status.success(), "drive: {}", text(&out));
    let a = json(&out);
    eprintln!("ai_train_mlx_live: {a}");
    assert_eq!(a["backend"], "mlx", "{a}");
    assert_eq!(a["steps"], 12, "{a}");
    let val = a["valLoss"].as_f64().unwrap();
    assert!(val.is_finite() && val > 0.0, "{a}");
    if killed {
        assert_eq!(a["relaunches"], 1, "{a}");
        assert!(a["resumedFrom"].as_i64().unwrap() >= 4, "{a}");
    } else {
        // The trainer finished before the test saw checkpoint 4: the kill had
        // nothing to kill. That is a slow test machine, not a pass.
        panic!("the trainer was not killed after its first checkpoint: {a}");
    }

    let gguf = a["gguf"].as_str().unwrap().to_string();
    let sha = a["ggufSha256"].as_str().unwrap().to_string();
    let out = app(&project, &["serve", &gguf, &sha]);
    assert!(out.status.success(), "serve: {}", text(&out));
    let answer = json(&out)["answer"].as_str().unwrap().to_string();
    eprintln!("ai_train_mlx_live: the adapter answered: {answer}");
    assert!(!answer.is_empty());

    let out = app(&project, &["eval", &gguf, &sha, &w]);
    assert!(out.status.success(), "eval: {}", text(&out));
    let e = json(&out);
    eprintln!("ai_train_mlx_live: base vs adapter: {e}");
    assert_eq!(e["baseCases"], 3, "{e}");
    assert_eq!(e["adapterCases"], 3, "{e}");
    assert_eq!(e["adapterErrors"], 0, "{e}");

    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&work);
}
