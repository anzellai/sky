//! `sky ai` and `Std.Ai.Endpoint` end to end, against a FAKE engine.
//!
//! The fake (`runtime-go/rt/testdata/fakellama`) answers the launch
//! arguments, `/health`, `/v1/chat/completions` and `/v1/embeddings` the way
//! `llama-server` does, so these tests prove the CLI verbs and a compiled
//! Sky program's slots without a model. The real engine and model are the
//! live test `ai_local_live.rs`.
//!
//! The operator path is used throughout: `SKY_AI_LLAMA_BIN` points at the
//! fake engine and `SKY_AI_<SLOT>_MODEL` at a small GGUF file, and
//! `SKY_AI_HOME` keeps every test's engines, keys and registry apart.

use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

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

fn temp_dir(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "sky-ai-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// The fake engine as `<dir>/llama-server`, plus a small model and its
/// reference string.
struct Fake {
    dir: PathBuf,
    home: PathBuf,
    model_ref: String,
}

fn fake_engine() -> Fake {
    let dir = temp_dir("engine");
    let bin = dir.join("llama-server");
    let out = Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg("./rt/testdata/fakellama")
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .expect("spawn go build");
    assert!(
        out.status.success(),
        "go build fakellama: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let model = dir.join("tiny.gguf");
    let out = Command::new(&bin)
        .arg("--write-test-model")
        .arg(&model)
        .output()
        .expect("write test model");
    assert!(out.status.success());
    let sum = String::from_utf8_lossy(&out.stdout).trim().to_string();
    assert_eq!(sum.len(), 64, "digest {sum:?}");
    Fake {
        model_ref: format!("file:{}#sha256={sum}", model.display()),
        home: temp_dir("home"),
        dir,
    }
}

impl Drop for Fake {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
        let _ = std::fs::remove_dir_all(&self.home);
    }
}

impl Fake {
    fn sky(&self, cwd: &Path) -> Command {
        let mut c = Command::new(SKY);
        c.current_dir(cwd)
            .env("SKY_AI_HOME", &self.home)
            .env("SKY_AI_LLAMA_BIN", &self.dir)
            .env_remove("ENV")
            .stdin(Stdio::null());
        c
    }
}

fn launches(fake: &Fake) -> usize {
    std::fs::read_to_string(fake.dir.join("launches.log"))
        .map(|s| s.lines().count())
        .unwrap_or(0)
}

#[test]
fn sky_ai_refuses_a_bad_verb_shape_without_building_anything() {
    let outs = [
        Command::new(SKY).arg("ai").output().unwrap(),
        Command::new(SKY).args(["ai", "train"]).output().unwrap(),
        Command::new(SKY)
            .args(["ai", "pull", "only-one"])
            .output()
            .unwrap(),
        Command::new(SKY).args(["ai", "start"]).output().unwrap(),
        // The pipeline verbs (src/Ai.sky) are shape-checked before anything
        // is built too.
        Command::new(SKY).args(["ai", "eval"]).output().unwrap(),
        Command::new(SKY)
            .args(["ai", "eval", "--candidate", "support-lora@v2"])
            .output()
            .unwrap(),
        Command::new(SKY).args(["ai", "run"]).output().unwrap(),
        Command::new(SKY)
            .args(["ai", "members", "extra"])
            .output()
            .unwrap(),
        Command::new(SKY)
            .args(["ai", "data", "freeze"])
            .output()
            .unwrap(),
    ];
    for out in outs {
        assert_eq!(out.status.code(), Some(2));
        let err = String::from_utf8_lossy(&out.stderr);
        assert!(err.contains("usage: sky ai"), "{err}");
    }
}

#[test]
fn sky_ai_start_ps_stop_drive_an_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let fake = fake_engine();
    let cwd = temp_dir("cwd");

    // provision with an operator build checks it and fetches nothing.
    let out = fake.sky(&cwd).args(["ai", "provision"]).output().unwrap();
    let text =
        String::from_utf8_lossy(&out.stdout).into_owned() + &String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "provision: {text}");
    assert!(text.contains("llama-server custom"), "provision: {text}");
    let out = fake
        .sky(&cwd)
        .args(["ai", "provision", "--variant", "cuda12"])
        .output()
        .unwrap();
    let text =
        String::from_utf8_lossy(&out.stdout).into_owned() + &String::from_utf8_lossy(&out.stderr);
    assert!(!out.status.success(), "a missing variant succeeded: {text}");
    assert!(text.contains("no pinned cuda12 build"), "{text}");

    // start holds the engine in the foreground.
    let mut start = fake
        .sky(&cwd)
        .args(["ai", "start", "chat", "--model", &fake.model_ref])
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit())
        .spawn()
        .unwrap();
    let mut line = String::new();
    let mut reader = BufReader::new(start.stdout.take().unwrap());
    let deadline = Instant::now() + Duration::from_secs(600);
    while !line.contains("ready") {
        line.clear();
        if reader.read_line(&mut line).unwrap() == 0 || Instant::now() > deadline {
            let _ = start.kill();
            panic!("sky ai start never reported ready");
        }
    }
    assert!(line.contains("slot chat ready (local"), "{line}");

    let out = fake
        .sky(&cwd)
        .args(["ai", "ps", "--format", "json"])
        .output()
        .unwrap();
    let ps: serde_json::Value = serde_json::from_slice(&out.stdout)
        .unwrap_or_else(|e| panic!("ps json: {e}: {}", String::from_utf8_lossy(&out.stdout)));
    let engines = ps.as_array().unwrap();
    assert_eq!(engines.len(), 1, "{ps}");
    assert_eq!(engines[0]["slot"], "chat");
    assert_eq!(engines[0]["state"], "running");
    assert_eq!(engines[0]["holderPid"], start.id());
    let engine_pid = engines[0]["pid"].as_u64().unwrap();

    let out = fake
        .sky(&cwd)
        .args(["ai", "stop", "chat"])
        .output()
        .unwrap();
    assert!(
        String::from_utf8_lossy(&out.stdout).contains("stopped 1 engine(s)"),
        "{}",
        String::from_utf8_lossy(&out.stdout)
    );
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if let Some(_status) = start.try_wait().unwrap() {
            break;
        }
        if Instant::now() > deadline {
            let _ = start.kill();
            panic!("sky ai start did not exit after sky ai stop");
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    assert!(
        !pid_alive(engine_pid as i32),
        "the engine outlived sky ai stop"
    );
    let out = fake.sky(&cwd).args(["ai", "ps"]).output().unwrap();
    assert!(String::from_utf8_lossy(&out.stdout).contains("no engines are running"));
    assert_eq!(launches(&fake), 1);
    let _ = std::fs::remove_dir_all(&cwd);
}

#[test]
fn endpoint_slots_run_a_compiled_program_on_the_managed_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let fake = fake_engine();
    let project = temp_dir("project");
    copy_dir(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-endpoint"),
        &project,
    );
    let out = fake
        .sky(&project)
        .args(["build", "src/Main.sky"])
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "sky build: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let busy = busy_server();
    let out = Command::new(project.join("sky-out/app"))
        .current_dir(&project)
        .env("SKY_AI_HOME", &fake.home)
        .env("SKY_AI_LLAMA_BIN", &fake.dir)
        .env("SKY_AI_CHAT_MODEL", &fake.model_ref)
        .env("SKY_AI_EMBED_MODEL", &fake.model_ref)
        .env("SKY_AI_BUSY_URL", format!("http://{busy}/v1"))
        .env_remove("ENV")
        .output()
        .unwrap();
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "app: {stdout}{}",
        String::from_utf8_lossy(&out.stderr)
    );
    // The options reached the engine: seed, max tokens, temperature 0.
    assert!(
        stdout.contains(
            "chat=fake[local]: What colour is the sky on a clear day? seed=7 max=16 temp=0"
        ),
        "{stdout}"
    );
    assert!(stdout.contains("embed=2 dims=4"), "{stdout}");
    // The operator's MODEL beat the code's model.
    assert!(
        stdout.contains("describe chat mode=local source(model)=env"),
        "{stdout}"
    );
    assert!(
        stdout.contains("ghost=InvalidInput: Std.Ai NotConfigured")
            && stdout.contains("SKY_AI_GHOST_URL"),
        "{stdout}"
    );
    // The typed view (Std.Ai.Failure, v0.28.0): the same failure read as a
    // typed value, with the generic Error helpers. A remote 429 with
    // Retry-After is Overloaded, carries the delay and the status, and is
    // retryable; NotConfigured names the variable and is not.
    assert!(
        stdout.contains(
            "ghostTyped=NotConfigured setting=SKY_AI_GHOST_URL domain=ai retryable=False"
        ),
        "{stdout}"
    );
    assert!(
        stdout.contains("busy=Overloaded retryAfterMs=2000 status=429 domain=ai retryable=True"),
        "{stdout}"
    );
    // Two slots, two engines (chat and an embeddings engine), both stopped
    // with the program.
    assert_eq!(launches(&fake), 2);
    let reg = std::fs::read_to_string(fake.home.join("engines.json")).unwrap_or_default();
    assert!(
        !reg.contains("\"pid\""),
        "engines outlived the program: {reg}"
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// A server that answers every request with HTTP 429 and `Retry-After: 2`,
/// as an overloaded OpenAI-compatible gateway does. Returns its address.
fn busy_server() -> String {
    use std::io::{Read, Write};
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut s) = stream else { continue };
            let mut buf = [0u8; 8192];
            let _ = s.read(&mut buf);
            let _ = s.write_all(
                b"HTTP/1.1 429 Too Many Requests\r\nRetry-After: 2\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
            );
        }
    });
    addr
}

#[cfg(unix)]
fn pid_alive(pid: i32) -> bool {
    nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), None).is_ok()
}

#[cfg(not(unix))]
fn pid_alive(_pid: i32) -> bool {
    false
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
