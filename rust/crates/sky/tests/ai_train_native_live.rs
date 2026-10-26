#![cfg_attr(
    not(all(target_os = "macos", target_arch = "aarch64")),
    allow(dead_code, unused_imports)
)]
//! LIVE: a real QLoRA run with the native trainer (`sky-trainer`, MLX through
//! mlx-c) on Apple silicon, from Sky code, under a memory cap.
//!
//! The fixture trains the development base (Qwen2.5 0.5B Instruct, the
//! `mlx-community` 4-bit snapshot) for 24 steps of 1 row of 256 tokens, rank 8
//! on the last 8 layers (q and v), a checkpoint every 8 steps, under
//! `withMemoryCap 2500000000` (the trainer's own guard). After the first
//! checkpoint the test SIGKILLs the trainer's process group; the durable
//! workflow sees the trainer gone without a terminal line, relaunches it from
//! the newest verified checkpoint, and the run finishes at step 24 with a
//! finite eval loss. Once the trainer has exited, the adapter (converted to
//! GGUF by the workflow) is served by the pinned llama-server on the dev
//! model, and evaluated against the base on the same three cases.
//!
//! Memory: the test polls each trainer's phys_footprint itself
//! (`/usr/bin/footprint`) and SIGKILLs the trainer's group above the cap plus
//! 5%. It FAILS when that poll, or any progress line's `peakBytes`, is above
//! 2.5 GB.
//!
//! macOS on Apple silicon only. It needs Go, the native trainer
//! (SKY_AI_TRAINER_BIN, or a pinned bundle in the Std.Ai home), the engine,
//! the dev model and the dev base weights in the Std.Ai home or the network
//! to fetch them; without them it FAILS naming what to install (`live_gate`,
//! `Need::AiTrainNative`), unless SKY_LIVE_TESTS=skip.

use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const DEV_MODEL_SHA256: &str = "7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed";
/// The cap the fixture's job sets (`withMemoryCap`), and the test's bound.
const CAP_BYTES: u64 = 2_500_000_000;
/// The test's own watchdog: the cap plus 5%.
const KILL_BYTES: u64 = CAP_BYTES + CAP_BYTES / 20;

/// A unique suffix for a scratch directory (the clock alone is not unique).
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

/// The native trainer: the operator's SKY_AI_TRAINER_BIN (the executable or
/// its directory) when it answers `--version` as sky-trainer, else a bundle
/// `sky ai provision --train mlx` installed.
fn have_trainer(home: &Path) -> bool {
    if let Ok(b) = std::env::var("SKY_AI_TRAINER_BIN") {
        if !b.is_empty() {
            let mut p = PathBuf::from(b);
            if p.is_dir() {
                p = p.join("sky-trainer");
            }
            return Command::new(&p)
                .arg("--version")
                .output()
                .map(|o| {
                    o.status.success()
                        && String::from_utf8_lossy(&o.stdout).starts_with("sky-trainer")
                })
                .unwrap_or(false);
        }
    }
    has_child(&home.join("trainers/mlx"), "ok.json")
}

fn provisioned(home: &Path) -> bool {
    has_child(&home.join("engines/llama"), "llama-server")
        && home
            .join("models/sha256")
            .join(format!("{DEV_MODEL_SHA256}.gguf"))
            .is_file()
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
    let d = std::env::temp_dir().join(format!("sky-train-native-{tag}-{}", unique_suffix()));
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

/// The fixture's command, with no training or slot variable of the caller's
/// except the AI home, the accelerator and the operator's trainer.
fn app_command(project: &Path, args: &[&str]) -> Command {
    let mut cmd = Command::new(project.join("sky-out/app"));
    cmd.args(args)
        .current_dir(project)
        .env_remove("ENV")
        .env_remove("SKY_TEST_MODE");
    for (k, _) in std::env::vars() {
        if k.starts_with("SKY_AI_")
            && k != "SKY_AI_HOME"
            && k != "SKY_AI_ACCEL"
            && k != "SKY_AI_TRAINER_BIN"
        {
            cmd.env_remove(k);
        }
    }
    cmd
}

fn app(project: &Path, args: &[&str]) -> Output {
    app_command(project, args).output().unwrap()
}

/// Provision what the live tests need, once per test binary, the way a user
/// does: `sky ai provision --train mlx --base dev` installs the pinned
/// sky-trainer bundle and the dev base weights, and `sky ai provision
/// --dev-model` the pinned engine and dev model. Nothing runs when the home
/// already holds them, or when an operator's trainer is given and the rest is
/// there, or when the hosts are not reachable (the gate then reports the
/// missing need). A provision that runs and fails is a failure, never a skip.
fn provision_for_tests(home: &Path) {
    use std::sync::OnceLock;
    static DONE: OnceLock<()> = OnceLock::new();
    DONE.get_or_init(|| {
        if !(reachable("github.com") && reachable("huggingface.co")) {
            return;
        }
        let mut steps: Vec<&[&str]> = Vec::new();
        if !have_trainer(home) || !home.join(DEV_BASE).join("model.safetensors").is_file() {
            steps.push(&["ai", "provision", "--train", "mlx", "--base", "dev"]);
        }
        if !provisioned(home) {
            steps.push(&["ai", "provision", "--dev-model"]);
        }
        for args in steps {
            let out = Command::new(SKY)
                .args(args)
                .current_dir(std::env::temp_dir())
                .env("SKY_AI_HOME", home)
                .env_remove("SKY_AI_FETCH")
                .output()
                .unwrap();
            assert!(
                out.status.success(),
                "sky {}: {}",
                args.join(" "),
                text(&out)
            );
        }
    });
}

fn gate() -> bool {
    if !required(Need::Go, have_go()) {
        return false;
    }
    let home = ai_home();
    provision_for_tests(&home);
    required(
        Need::AiTrainNative,
        have_trainer(&home) && provisioned(&home),
    )
}

/// The run directory of the fixture's job, once it exists.
fn run_dir(work: &Path) -> Option<PathBuf> {
    std::fs::read_dir(work.join("runs"))
        .ok()?
        .flatten()
        .map(|e| e.path())
        .find(|p| p.join("job.json").is_file())
}

/// The current trainer's pid and attempt, from the run's trainer.json.
fn trainer(dir: &Path) -> Option<(i64, i64)> {
    let t: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(dir.join("trainer.json")).ok()?).ok()?;
    Some((t["pid"].as_i64()?, t["attempt"].as_i64()?))
}

fn alive(pid: i64) -> bool {
    Command::new("kill")
        .args(["-0", &pid.to_string()])
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn kill_group(pid: i64) -> bool {
    Command::new("kill")
        .arg("-KILL")
        .arg(format!("-{pid}"))
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// The phys_footprint of a process, as macOS's `footprint` reports it (it
/// counts Metal memory, which RSS misses).
fn footprint(pid: i64) -> Option<u64> {
    let out = Command::new("/usr/bin/footprint")
        .args(["-f", "bytes", "--noCategories", &pid.to_string()])
        .stderr(Stdio::null())
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout);
    let at = s.find("Footprint: ")? + "Footprint: ".len();
    s[at..]
        .split(|c: char| !c.is_ascii_digit())
        .next()?
        .parse()
        .ok()
}

/// The highest `peakBytes` of any progress line in the run's events.
fn reported_peak(dir: &Path) -> u64 {
    std::fs::read_to_string(dir.join("events.jsonl"))
        .unwrap_or_default()
        .lines()
        .filter_map(|l| serde_json::from_str::<serde_json::Value>(l).ok())
        .filter(|v| v["type"] == "progress")
        .filter_map(|v| v["peakBytes"].as_u64())
        .max()
        .unwrap_or(0)
}

/// Kills the last trainer group the test saw when it goes out of scope, on
/// every path: the trainer runs in its own process group, which the driver's
/// group does not include.
struct TrainerGuard(i64);
impl Drop for TrainerGuard {
    fn drop(&mut self) {
        if self.0 > 0 && alive(self.0) {
            kill_group(self.0);
        }
    }
}

#[cfg(all(target_os = "macos", target_arch = "aarch64"))]
#[cfg(unix)]
#[test]
fn native_trainer_trains_under_its_cap_survives_a_kill_serves_and_evaluates() {
    if !gate() {
        return;
    }
    let project = fixture();
    let work = scratch("work");
    let w = work.to_string_lossy().to_string();

    let mut cmd = app_command(&project, &["drive", "mlx", &w]);
    cmd.stdout(Stdio::piped()).stderr(Stdio::piped());
    // The driver and everything in its group end with the test on every
    // path, a panic in the wait below included. Its pipes are drained on
    // threads.
    let mut driver = child_group::ChildGroup::spawn(&mut cmd).unwrap();
    let drain = |r: Option<Box<dyn std::io::Read + Send>>| {
        std::thread::spawn(move || {
            let mut buf = Vec::new();
            if let Some(mut r) = r {
                let _ = r.read_to_end(&mut buf);
            }
            buf
        })
    };
    let stdout = drain(
        driver
            .child()
            .stdout
            .take()
            .map(|r| Box::new(r) as Box<dyn std::io::Read + Send>),
    );
    let stderr = drain(
        driver
            .child()
            .stderr
            .take()
            .map(|r| Box::new(r) as Box<dyn std::io::Read + Send>),
    );

    // Until the driver exits: watch every trainer's footprint, and SIGKILL
    // the first one's group after its first checkpoint (step 8). A first run
    // fetches the base, the engine and the model first.
    let deadline = Instant::now() + Duration::from_secs(2400);
    let mut guard = TrainerGuard(0);
    let mut killed = false;
    let mut polled_peak: u64 = 0;
    let mut over: Option<u64> = None;
    let status = loop {
        if let Some(st) = driver.child().try_wait().expect("poll the driver") {
            break st;
        }
        assert!(
            Instant::now() < deadline,
            "the run did not finish in 40 minutes"
        );
        if let Some(dir) = run_dir(&work) {
            if let Some((pid, attempt)) = trainer(&dir) {
                if pid > 0 && alive(pid) {
                    guard.0 = pid;
                    if let Some(fp) = footprint(pid) {
                        polled_peak = polled_peak.max(fp);
                        if fp > KILL_BYTES {
                            kill_group(pid);
                            over = Some(fp);
                        }
                    }
                    if attempt == 0
                        && !killed
                        && dir.join("checkpoints/0000008/manifest.json").is_file()
                    {
                        assert!(kill_group(pid), "kill the trainer group {pid}");
                        killed = true;
                    }
                }
            }
        }
        std::thread::sleep(Duration::from_millis(200));
    };
    let out = Output {
        status,
        stdout: stdout.join().unwrap(),
        stderr: stderr.join().unwrap(),
    };
    assert!(
        over.is_none(),
        "the test's watchdog killed a trainer at {over:?} bytes (cap {CAP_BYTES})"
    );
    assert!(out.status.success(), "drive: {}", text(&out));
    let a = json(&out);
    eprintln!("ai_train_native_live: {a}");
    assert_eq!(a["backend"], "mlx", "{a}");
    assert_eq!(a["steps"], 24, "{a}");
    let val = a["valLoss"].as_f64().unwrap();
    assert!(val.is_finite() && val > 0.0, "{a}");
    // The trainer finished before the test saw checkpoint 8: the kill had
    // nothing to kill. That is a slow poll, not a pass.
    assert!(
        killed,
        "the trainer was not killed after its first checkpoint: {a}"
    );
    assert_eq!(a["relaunches"], 1, "{a}");
    assert!(a["resumedFrom"].as_i64().unwrap() >= 8, "{a}");

    // Memory: the trainer's own peak (every progress line, both attempts) and
    // the test's poll, each under the cap.
    let dir = run_dir(&work).expect("the run directory");
    let events = std::fs::read_to_string(dir.join("events.jsonl")).unwrap_or_default();
    assert_eq!(
        events.matches("\"toolchain\":\"native\",").count(),
        2,
        "two native trainers said hello:\n{events}"
    );
    let reported = reported_peak(&dir);
    eprintln!(
        "ai_train_native_live: peak footprint {reported} bytes reported, {polled_peak} polled (cap {CAP_BYTES})"
    );
    assert!(reported > 0, "no progress line reported peakBytes");
    assert!(
        reported <= CAP_BYTES,
        "the trainer peaked at {reported} bytes"
    );
    assert!(polled_peak <= CAP_BYTES, "the poll saw {polled_peak} bytes");
    assert!(!dir.join("guard.json").exists(), "the guard fired");

    // The trainer has exited before anything is served.
    let (pid, attempt) = trainer(&dir).expect("trainer.json");
    assert_eq!(attempt, 1, "the relaunch is the last trainer");
    assert!(!alive(pid), "trainer {pid} is still running");
    guard.0 = 0;

    let gguf = a["gguf"].as_str().unwrap().to_string();
    let sha = a["ggufSha256"].as_str().unwrap().to_string();
    let out = app(&project, &["serve", &gguf, &sha]);
    assert!(out.status.success(), "serve: {}", text(&out));
    let answer = json(&out)["answer"].as_str().unwrap().to_string();
    eprintln!("ai_train_native_live: the adapter answered: {answer}");
    assert!(!answer.is_empty());

    let out = app(&project, &["eval", &gguf, &sha, &w]);
    assert!(out.status.success(), "eval: {}", text(&out));
    let e = json(&out);
    eprintln!("ai_train_native_live: base vs adapter: {e}");
    assert_eq!(e["baseCases"], 3, "{e}");
    assert_eq!(e["adapterCases"], 3, "{e}");
    assert_eq!(e["adapterErrors"], 0, "{e}");

    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&work);
}

const DEV_BASE: &str =
    "bases/mlx-community--Qwen2.5-0.5B-Instruct-4bit@a5339a4131f135d0fdc6a5c8b5bbed2753bbe0f3";

/// The pinned engine's directory that holds `name`.
fn engine_tool(home: &Path, name: &str) -> Option<PathBuf> {
    std::fs::read_dir(home.join("engines/llama"))
        .ok()?
        .flatten()
        .map(|e| e.path().join(name))
        .find(|p| p.is_file())
}

/// The tokenize step's tokenizer (Go, from the dev base's tokenizer.json)
/// gives the same ids as the pinned engine's `llama-tokenize` on the dev
/// GGUF, for text that exercises the Qwen2 pre-tokenizer (contractions,
/// digits, whitespace runs, newlines, accents, CJK, emoji, code) and the
/// ChatML special tokens. A mismatch means the trainer learns on ids the
/// serving model reads differently.
#[cfg(all(target_os = "macos", target_arch = "aarch64"))]
#[test]
fn go_tokenizer_matches_llama_tokenize_on_the_dev_model() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let home = ai_home();
    provision_for_tests(&home);
    let gguf = home
        .join("models/sha256")
        .join(format!("{DEV_MODEL_SHA256}.gguf"));
    let tokenize = engine_tool(&home, "llama-tokenize");
    if !required(Need::AiEngine, gguf.is_file() && tokenize.is_some()) {
        return;
    }
    let base = home.join(DEV_BASE);
    if !required(Need::AiTrainNative, base.join("tokenizer.json").is_file()) {
        return;
    }
    let work = scratch("tokens");
    let tool = work.join("sky-train-tool");
    let runtime = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../runtime-go");
    let out = Command::new("go")
        .args(["build", "-o"])
        .arg(&tool)
        .arg("./cmd/sky-train-tool")
        .current_dir(&runtime)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "go build sky-train-tool: {}",
        text(&out)
    );

    let cases = [
        "Hello, world! How are you today?",
        "I'm sure they'll say it's fine, we've done it, you'd agree, don't you?",
        "Order 12345 cost $3.14159, about 1,000,000 units in 2024-10-26.",
        "two  spaces,   three,\ttab,\n\nblank line,\n   indented\n\n\nend",
        "café naïve façade Ångström über",
        "你好，世界！こんにちは。안녕하세요",
        "emoji 👋🏽 🎉🎉 and ✓ marks",
        "fn main() {\n    println!(\"{}\", x[0] + y.len());\n}",
        "<|im_start|>system\nYou are Qwen, created by Alibaba Cloud. You are a helpful assistant.<|im_end|>\n<|im_start|>user\nHow do I get a refund?<|im_end|>\n<|im_start|>assistant\nOpen Orders, then Refund.<|im_end|>",
        "trailing spaces   ",
    ];
    let mut bad = Vec::new();
    for (i, case) in cases.iter().enumerate() {
        let file = work.join(format!("case{i}.txt"));
        std::fs::write(&file, case).unwrap();
        let go = Command::new(&tool)
            .arg("tokens")
            .arg(&base)
            .arg(&file)
            .output()
            .unwrap();
        assert!(go.status.success(), "sky-train-tool tokens: {}", text(&go));
        let go_ids: Vec<i64> = serde_json::from_slice(&go.stdout).unwrap();
        let ll = Command::new(tokenize.as_ref().unwrap())
            .arg("-m")
            .arg(&gguf)
            .arg("-f")
            .arg(&file)
            .args(["--ids", "--log-disable", "--no-bos"])
            .output()
            .unwrap();
        assert!(ll.status.success(), "llama-tokenize: {}", text(&ll));
        let line = String::from_utf8_lossy(&ll.stdout)
            .lines()
            .rev()
            .find(|l| l.trim_start().starts_with('['))
            .unwrap_or("")
            .to_string();
        let ll_ids: Vec<i64> = serde_json::from_str(line.trim())
            .unwrap_or_else(|e| panic!("llama-tokenize output ({e}): {}", text(&ll)));
        if go_ids != ll_ids {
            bad.push(format!(
                "{case:?}\n  go:    {go_ids:?}\n  llama: {ll_ids:?}"
            ));
        }
    }
    eprintln!(
        "ai_train_native_live: {} of {} texts tokenize the same",
        cases.len() - bad.len(),
        cases.len()
    );
    assert!(bad.is_empty(), "token ids differ:\n{}", bad.join("\n"));
    let _ = std::fs::remove_dir_all(&work);
}
