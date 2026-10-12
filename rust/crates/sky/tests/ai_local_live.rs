//! LIVE: the real managed engine with zero configuration.
//!
//! A compiled Sky program whose code names only `Local.devModel` gets a chat
//! completion and an embedding through two `Std.Ai.Endpoint` slots, with no
//! `SKY_AI_*` variable set: the runtime fetches the pinned `llama-server`
//! build and the pinned Qwen2.5 0.5B model (both verified by SHA-256) into the
//! Std.Ai home, plans the fit, starts two engines on 127.0.0.1, waits for
//! them, answers, and stops them with the program.
//!
//! It needs the engine and model in the Std.Ai home, or the network to fetch
//! them. Without either it FAILS naming `sky ai provision --dev-model`
//! (`live_gate`, `Need::AiEngine`); CI provides them from a cache keyed on
//! `runtime-go/rt/ai_pins.go` (`.github/workflows/rust-ci.yml`, job
//! `ai-local-live`). It also needs Go, to build the program.

use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// The pinned dev model's digest (runtime-go/rt/ai_pins.go).
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

/// The Std.Ai home the runtime uses with nothing configured: `$SKY_AI_HOME`,
/// else `$SKY_HOME/ai`, else the user cache directory.
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

fn provisioned(home: &Path) -> bool {
    let engines = home.join("engines/llama");
    let engine = std::fs::read_dir(&engines)
        .map(|rd| {
            rd.flatten()
                .any(|e| e.path().join("llama-server").is_file())
        })
        .unwrap_or(false);
    let model = home
        .join("models/sha256")
        .join(format!("{DEV_MODEL_SHA256}.gguf"))
        .is_file();
    engine && model
}

fn reachable(host: &str) -> bool {
    format!("{host}:443")
        .to_socket_addrs()
        .ok()
        .and_then(|mut a| a.next())
        .map(|addr| TcpStream::connect_timeout(&addr, Duration::from_secs(5)).is_ok())
        .unwrap_or(false)
}

#[test]
fn zero_config_endpoint_chats_and_embeds_on_the_real_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = ai_home();
    let available = provisioned(&home) || (reachable("github.com") && reachable("huggingface.co"));
    if !required(Need::AiEngine, available) {
        return;
    }
    let project = std::env::temp_dir().join(format!("sky-ai-live-{}", std::process::id()));
    copy_dir(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-endpoint"),
        &project,
    );
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&project)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "sky build: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );

    // Zero configuration: no SKY_AI_* variable reaches the program.
    let mut app = Command::new(project.join("sky-out/app"));
    app.current_dir(&project).env_remove("ENV");
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_") {
            app.env_remove(k);
        }
    }
    let first_run = !provisioned(&home);
    let started = Instant::now();
    let out = app.output().unwrap();
    let took = started.elapsed();
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    eprintln!(
        "ai_local_live: {} run took {:.1}s\n{stdout}",
        if first_run {
            "first (fetching)"
        } else {
            "cached"
        },
        took.as_secs_f64()
    );
    assert!(out.status.success(), "app failed: {stdout}{stderr}");

    let chat = stdout
        .lines()
        .find_map(|l| l.strip_prefix("chat="))
        .unwrap_or_else(|| panic!("no chat line: {stdout}"));
    assert!(!chat.trim().is_empty(), "an empty answer: {stdout}");
    // Qwen2.5 0.5B embeds into 896 dimensions.
    assert!(stdout.contains("embed=2 dims=896"), "{stdout}");
    assert!(
        stdout.contains("describe chat mode=local source(model)=code"),
        "{stdout}"
    );
    assert!(
        provisioned(&home),
        "the engine and model are not in {}",
        home.display()
    );

    // The engines stopped with the program: no entry it held is still live.
    let reg = std::fs::read_to_string(home.join("engines.json")).unwrap_or_default();
    let ps = Command::new(SKY)
        .args(["ai", "ps", "--format", "json"])
        .output()
        .unwrap();
    let running: serde_json::Value =
        serde_json::from_slice(&ps.stdout).unwrap_or(serde_json::Value::Null);
    let held_by_app = running
        .as_array()
        .map(|a| {
            a.iter()
                .filter(|e| e["slot"] == "embed" || e["slot"] == "chat")
                .count()
        })
        .unwrap_or(0);
    assert_eq!(held_by_app, 0, "engines outlived the program: {reg}");
    let _ = std::fs::remove_dir_all(&project);
}

/// The flagship's pipeline on the real engine and model: `sky ai eval
/// support` runs the suite on the chat slot, calibrates the judge (the judge
/// slot, the same pinned model) and gates, all with no `SKY_AI_*` variable.
/// A 0.5B model may or may not pass the suite, so the test asserts the
/// lifecycle, not the model: one JSON outcome whose verdict the exit code
/// carries (0 when it passed, 1 when it failed), the JUnit file, a calibrated
/// judge, and no engine left running when the program ends.
#[test]
fn the_flagship_pipeline_evaluates_on_the_real_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = ai_home();
    let available = provisioned(&home) || (reachable("github.com") && reachable("huggingface.co"));
    if !required(Need::AiEngine, available) {
        return;
    }
    let project = std::env::temp_dir().join(format!("sky-ai-live-desk-{}", std::process::id()));
    let data = std::env::temp_dir().join(format!("sky-ai-live-desk-data-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&project);
    std::fs::create_dir_all(&data).unwrap();
    let flagship =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/72-ai-support-desk");
    for part in ["src", "sky.toml"] {
        let from = flagship.join(part);
        if from.is_dir() {
            copy_dir(&from, &project.join(part));
        } else {
            std::fs::create_dir_all(&project).unwrap();
            std::fs::copy(&from, project.join(part)).unwrap();
        }
    }
    let mut eval = Command::new(SKY);
    eval.args(["ai", "eval", "support", "--format", "json"])
        .current_dir(&project)
        .env_remove("ENV")
        .env("SKY_DB_PATH", data.join("app.db"))
        .env(
            "SKY_AI_REGISTRY",
            format!("file://{}", data.join("store").display()),
        );
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_") && k != "SKY_AI_REGISTRY" && k != "SKY_AI_HOME" {
            eval.env_remove(k);
        }
    }
    let child = eval
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    // `sky ai` replaces itself with the pipeline program: this pid holds the
    // engines it starts.
    let holder = child.id();
    let out = child.wait_with_output().unwrap();
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    let stderr = String::from_utf8_lossy(&out.stderr).into_owned();
    eprintln!("ai_local_live: sky ai eval support\n{stdout}");
    let outcome: serde_json::Value = serde_json::from_str(stdout.trim())
        .unwrap_or_else(|e| panic!("not one JSON outcome ({e}):\n{stdout}{stderr}"));
    let verdict = outcome["verdict"].as_str().unwrap_or("").to_string();
    assert!(verdict == "passed" || verdict == "failed", "{outcome}");
    assert_eq!(
        out.status.code(),
        Some(if verdict == "passed" { 0 } else { 1 }),
        "the exit code carries the verdict: {stderr}"
    );
    assert_eq!(outcome["candidate"], "slot:chat", "{outcome}");
    assert!(
        outcome["markdown"]
            .as_str()
            .unwrap_or("")
            .contains("calibration"),
        "the judge was calibrated first: {outcome}"
    );
    assert!(project.join(".sky/ai/eval/support/junit.xml").is_file());

    let ps = Command::new(SKY)
        .args(["ai", "ps", "--format", "json"])
        .output()
        .unwrap();
    let running: serde_json::Value =
        serde_json::from_slice(&ps.stdout).unwrap_or(serde_json::Value::Null);
    let held = running
        .as_array()
        .map(|a| {
            a.iter()
                .filter(|e| e["holderPid"] == holder && e["state"] == "running")
                .count()
        })
        .unwrap_or(0);
    assert_eq!(held, 0, "engines outlived the pipeline: {running}");
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&data);
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
