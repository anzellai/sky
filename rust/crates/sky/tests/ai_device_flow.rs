//! `Std.Ai.Device` through the compiler, offline, on every commit.
//!
//! - The build refuses a misused device slot: a `Device.provider "x"` with no
//!   server slot `"x"`, a `withDevice` binding with parameters, a computed
//!   slot name.
//! - A web:app build puts the `AiDevice_` kernels in the wasm client and the
//!   slot's registration (`rt.AiDeviceRegister`) in the server, and stages the
//!   worker (and the engines, when installed) with the page's meta.
//! - The built server answers the offer and the allow-listed blobs for the
//!   synthetic model the test writes, and refuses a digest no offer names.
//!
//! The browser runs are `scripts/ai-device-e2e.sh`; the native ones
//! `ai_device_native_flow.rs`.

use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[cfg(unix)]
use child_group::ChildGroup;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn unique_suffix() -> String {
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

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-ai-device-{tag}-{}", unique_suffix()));
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

fn fixture(tag: &str) -> PathBuf {
    let dir = scratch(tag);
    copy_dir(
        &repo_root().join("rust/crates/sky/tests/fixtures/ai-device"),
        &dir,
    );
    dir
}

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

fn sky(dir: &Path, home: &Path, args: &[&str]) -> Output {
    Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .env("SKY_AI_HOME", home)
        .env_remove("SKY_AI_PROBE_MODEL")
        .env_remove("SKY_AI_PROBE_DEVICE_MODEL")
        .env_remove("SKY_AI_PROBE_PLACEMENT")
        .stdin(Stdio::null())
        .output()
        .expect("run sky")
}

fn write_module(dir: &Path, name: &str, src: &str) {
    std::fs::write(dir.join("src").join(format!("{name}.sky")), src).unwrap();
}

#[test]
fn the_build_refuses_a_device_provider_with_no_server_slot() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = fixture("noslot");
    let home = scratch("noslot-home");
    write_module(
        &dir,
        "Assist",
        "module Assist exposing (probeSlot, assistant)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Ai.Endpoint as Endpoint exposing (Endpoint)\n\
         import Std.Ai.Local as Local\n\
         import Std.Ai.Device as Device\n\
         import Std.Ai.Provider exposing (Provider)\n\n\
         probeSlot : Endpoint\n\
         probeSlot =\n    Endpoint.endpoint \"probe\" |> Endpoint.withLocal (Local.model Local.devModel)\n\n\
         assistant : Provider\n\
         assistant =\n    Device.provider \"probe\"\n",
    );
    let out = sky(&dir, &home, &["check", "src/Main.sky"]);
    assert!(
        !out.status.success(),
        "a provider without a server slot built:\n{}",
        text(&out)
    );
    assert!(
        text(&out).contains("Device.provider \"probe\" has no server slot"),
        "{}",
        text(&out)
    );
}

#[test]
fn the_build_refuses_a_device_slot_with_parameters() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = fixture("params");
    let home = scratch("params-home");
    write_module(
        &dir,
        "Assist",
        "module Assist exposing (probeSlot, assistant)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Ai.Endpoint as Endpoint exposing (Endpoint)\n\
         import Std.Ai.Device as Device\n\
         import Std.Ai.Provider exposing (Provider)\n\n\
         probeSlot : Device.Spec -> Endpoint\n\
         probeSlot spec =\n    Endpoint.endpoint \"probe\" |> Endpoint.withDevice spec\n\n\
         assistant : Provider\n\
         assistant =\n    Device.provider \"probe\"\n",
    );
    let out = sky(&dir, &home, &["check", "src/Main.sky"]);
    assert!(!out.status.success(), "{}", text(&out));
    assert!(text(&out).contains("without parameters"), "{}", text(&out));
}

/// The synthetic model: `go run ./cmd/sky-tiny-gguf <dir>` in runtime-go.
fn tiny_model(dir: &Path) -> (String, String) {
    let out = Command::new("go")
        .args(["run", "./cmd/sky-tiny-gguf"])
        .arg(dir)
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .expect("go run sky-tiny-gguf");
    assert!(out.status.success(), "{}", text(&out));
    let v: serde_json::Value =
        serde_json::from_str(String::from_utf8_lossy(&out.stdout).trim()).unwrap();
    (
        v["model"].as_str().unwrap().to_string(),
        v["modelSha256"].as_str().unwrap().to_string(),
    )
}

/// Start the built server and wait for it to listen. The server is held by a
/// `ChildGroup`, so a readiness timeout or a failed request later in the test
/// still ends it (and anything it started) when the guard drops.
#[cfg(unix)]
fn start(bin: &Path, port: u16, env: &[(&str, &str)], home: &Path) -> ChildGroup {
    let mut c = Command::new(bin);
    c.current_dir(bin.parent().unwrap().parent().unwrap())
        .env("PORT", port.to_string())
        .env("SKY_AI_HOME", home)
        .env_remove("SKY_AI_PROBE_PLACEMENT")
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    for (k, v) in env {
        c.env(k, v);
    }
    let mut child = ChildGroup::spawn(&mut c).expect("start the server");
    let stdout = child.child().stdout.take().unwrap();
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines().map_while(Result::ok) {
            if line.to_lowercase().contains("listening") {
                let _ = tx.send(());
            }
        }
    });
    rx.recv_timeout(std::time::Duration::from_secs(30))
        .expect("the server never listened");
    child
}

fn http_get(port: u16, path: &str) -> (u16, Vec<u8>) {
    let mut s = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    write!(s, "GET {path} HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n").unwrap();
    let mut buf = Vec::new();
    s.read_to_end(&mut buf).unwrap();
    let split = buf
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .map(|i| i + 4)
        .unwrap_or(buf.len());
    let head = String::from_utf8_lossy(&buf[..split]).to_string();
    let status = head
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    (status, buf[split..].to_vec())
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

#[cfg(unix)]
#[test]
fn a_web_app_build_splits_the_device_kernels_and_serves_the_offer() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = fixture("webapp");
    let home = scratch("webapp-home");
    let out = sky(
        &dir,
        &home,
        &[
            "build",
            "--target",
            "web:app",
            "--ai-web-engine",
            "none",
            "src/Main.sky",
        ],
    );
    assert!(out.status.success(), "web:app build:\n{}", text(&out));
    let split = dir.join(".skyapp/web-app/.split");
    let frontend = std::fs::read_to_string(split.join("frontend/sky-out/main.go")).unwrap();
    let backend = std::fs::read_to_string(split.join("backend/sky-out/main.go")).unwrap();
    // The partition: the device kernels run in the client, the registration
    // in the server, and the client declares no server slot.
    assert!(
        frontend.contains("rt.AiDevice_offer"),
        "the client has the device kernels"
    );
    assert!(
        !frontend.contains("rt.AiDeviceRegister"),
        "the client registers no slot"
    );
    assert!(
        !frontend.contains("rt.Ai_endpointPost"),
        "the client carries no server slot"
    );
    assert!(
        backend.contains("rt.AiDeviceRegister(\"probe\""),
        "the server registers the slot"
    );
    assert!(!split.join("frontend/sky-out/ai_slots_gen.go").exists());
    // `--ai-web-engine none`: the worker is staged, no engine and no meta.
    let dist = split.join("frontend/dist");
    let names: Vec<String> = std::fs::read_dir(&dist)
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    assert!(
        names
            .iter()
            .any(|n| n.starts_with("sky-ai-worker.") && n.ends_with(".js")),
        "{names:?}"
    );
    assert!(
        !names.iter().any(|n| n.starts_with("sky-ai-cpu.")),
        "{names:?}"
    );
    let index = std::fs::read_to_string(dist.join("index.html")).unwrap();
    assert!(!index.contains("sky-ai-engine"), "{index}");

    // The server: the offer for the synthetic model, its blob, and nothing else.
    let (model, sum) = tiny_model(&scratch("webapp-model"));
    let model_ref = format!("file:{model}#sha256={sum}");
    let port = free_port();
    let mut child = start(
        &split.join("backend/sky-out/app"),
        port,
        &[("SKY_AI_PROBE_MODEL", &model_ref)],
        &home,
    );
    let (st, offer) = http_get(port, "/_sky/ai/device/probe");
    let (blob_st, blob) = http_get(port, &format!("/_sky/ai/blob/{sum}"));
    let (other_st, _) = http_get(port, &format!("/_sky/ai/blob/{}", "ab".repeat(32)));
    let (worker_st, _) = http_get(
        port,
        &format!(
            "/{}",
            names
                .iter()
                .find(|n| n.starts_with("sky-ai-worker."))
                .unwrap()
        ),
    );
    child.stop();
    let offer = String::from_utf8_lossy(&offer).to_string();
    assert_eq!(st, 200, "{offer}");
    let o: serde_json::Value = serde_json::from_str(&offer).unwrap();
    assert_eq!(o["placement"], "auto");
    assert_eq!(o["model"]["sha256"], sum.as_str());
    assert_eq!(o["here"], false);
    assert_eq!(blob_st, 200);
    assert_eq!(blob.len() as u64, std::fs::metadata(&model).unwrap().len());
    assert_eq!(other_st, 404, "a digest no offer names is not served");
    assert_eq!(worker_st, 200, "the worker is served");
}
