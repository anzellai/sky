//! `Std.Ai.Device` in the native apps: the probe fixture
//! (`tests/fixtures/ai-device`) reads the slot's status, prepares it (a
//! download from its backend, checked by SHA-256), asks one question on the
//! device's own llama.cpp and reports each step through its backend as
//! `SKY-PROBE` lines:
//!
//! - the iOS simulator (SkyLlama.xcframework, the CPU slice);
//! - a running Android emulator (`libskyllama.so`; the CI runner boots an
//!   x86_64 one, a Mac an arm64 one);
//! - the macOS desktop app (the managed llama-server).
//!
//! The model is the synthetic Qwen2 model (`go run ./cmd/sky-tiny-gguf`),
//! through the operator variable `SKY_AI_PROBE_MODEL`, so no 430 MB download
//! is needed. Each test needs its engine installed (`sky ai provision
//! --device ios|android`) and fails naming the command otherwise.

use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::sync::{Arc, Mutex};

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
#[cfg(unix)]
use child_group::ChildGroup;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

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

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-ai-native-{tag}-{}", unique_suffix()));
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

fn have(cmd: &str, arg: &str) -> bool {
    Command::new(cmd)
        .arg(arg)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// The engine of a target, installed by `sky ai provision --device <t>`.
fn engine_installed(target: &str) -> bool {
    let out = Command::new(SKY)
        .args(["ai", "provision", "--device", target, "--check"])
        .output();
    out.map(|o| o.status.success()).unwrap_or(false)
}

/// The synthetic model's reference: `file:<path>#sha256=<hex>`.
fn tiny_model() -> String {
    let dir = scratch("model");
    let out = Command::new("go")
        .args(["run", "./cmd/sky-tiny-gguf"])
        .arg(&dir)
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .expect("go run sky-tiny-gguf");
    assert!(out.status.success(), "{}", text(&out));
    let v: serde_json::Value =
        serde_json::from_str(String::from_utf8_lossy(&out.stdout).trim()).unwrap();
    format!(
        "file:{}#sha256={}",
        v["model"].as_str().unwrap(),
        v["modelSha256"].as_str().unwrap()
    )
}

/// Build the probe for a target, with the build's PORT as the address the
/// app loads.
fn build(target: &str, port: u16) -> PathBuf {
    let dir = scratch(&target.replace(':', "-"));
    copy_dir(
        &repo_root().join("rust/crates/sky/tests/fixtures/ai-device"),
        &dir,
    );
    let mut cmd = Command::new(SKY);
    cmd.args(["build", "--target", target, "src/Main.sky"])
        .current_dir(&dir)
        .env("PORT", port.to_string());
    // The iOS build runs Xcode's tools. The other builds keep the caller's
    // toolchain: Xcode's SDK headers under another C compiler break cgo.
    if target.starts_with("mobile:ios") {
        cmd.env(
            "DEVELOPER_DIR",
            "/Applications/Xcode.app/Contents/Developer",
        );
    }
    let out = cmd.output().expect("sky build");
    assert!(out.status.success(), "{target} build:\n{}", text(&out));
    dir
}

/// The probe's backend, its output kept. The backend and anything it starts
/// end with the guard on every path, the readiness assertion included.
#[cfg(unix)]
struct Backend {
    _child: ChildGroup,
    lines: Arc<Mutex<Vec<String>>>,
}

#[cfg(unix)]
impl Backend {
    fn start(split: &Path, port: u16, model: &str) -> Backend {
        let mut child = ChildGroup::spawn(
            Command::new(split.join("backend/sky-out/app"))
                .current_dir(split.join("backend"))
                .env("PORT", port.to_string())
                .env("SKY_HOST", "0.0.0.0")
                .env("SKY_AI_PROBE_MODEL", model)
                .stdout(Stdio::piped())
                .stderr(Stdio::piped()),
        )
        .expect("start the backend");
        let lines = Arc::new(Mutex::new(Vec::new()));
        for stream in [
            Box::new(child.child().stdout.take().unwrap()) as Box<dyn std::io::Read + Send>,
            Box::new(child.child().stderr.take().unwrap()),
        ] {
            let l = lines.clone();
            std::thread::spawn(move || {
                for line in BufReader::new(stream).lines().map_while(Result::ok) {
                    l.lock().unwrap().push(line);
                }
            });
        }
        let b = Backend {
            _child: child,
            lines,
        };
        assert!(b.wait_for("listening", 30), "the backend never listened");
        b
    }

    fn wait_for(&self, needle: &str, secs: u64) -> bool {
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(secs);
        while std::time::Instant::now() < deadline {
            if self
                .lines
                .lock()
                .unwrap()
                .iter()
                .any(|l| l.to_lowercase().contains(&needle.to_lowercase()))
            {
                return true;
            }
            std::thread::sleep(std::time::Duration::from_millis(250));
        }
        false
    }

    /// The probe's reports: `key -> value`, the first report of each key (a
    /// page the shell loads again reports again, from the state the first
    /// load left). Every report line is printed, as the run's evidence.
    fn reports(&self) -> std::collections::BTreeMap<String, String> {
        let mut out = std::collections::BTreeMap::new();
        for l in self.lines.lock().unwrap().iter() {
            if let Some(rest) = l.split("SKY-PROBE ").nth(1) {
                eprintln!("SKY-PROBE {rest}");
                if let Some((k, v)) = rest.split_once('=') {
                    out.entry(k.to_string())
                        .or_insert_with(|| v.trim().to_string());
                }
            }
        }
        out
    }
}

fn assert_on_device(r: &std::collections::BTreeMap<String, String>, what: &str) {
    // The reports are the run's evidence: printed with `--nocapture`.
    eprintln!("{what}: {r:?}");
    assert_eq!(
        r.get("status").map(String::as_str),
        Some("not-downloaded:native"),
        "{what}: {r:?}"
    );
    assert_eq!(
        r.get("prepared").map(String::as_str),
        Some("ready:native"),
        "{what}: {r:?}"
    );
    let answer = r.get("answer").cloned().unwrap_or_default();
    assert!(
        !answer.is_empty() && !answer.starts_with("err:"),
        "{what}: {r:?}"
    );
    // Provider.stream: the same answer, in more than one piece.
    let stream = r.get("stream").cloned().unwrap_or_default();
    let pieces: usize = stream
        .strip_suffix(":same")
        .and_then(|n| n.parse().ok())
        .unwrap_or(0);
    assert!(pieces > 1, "{what}: not streamed piece by piece: {r:?}");
}

fn simctl(args: &[&str]) -> Output {
    Command::new("/usr/bin/xcrun")
        .arg("simctl")
        .args(args)
        .env(
            "DEVELOPER_DIR",
            "/Applications/Xcode.app/Contents/Developer",
        )
        .output()
        .expect("xcrun simctl")
}

/// An available iPhone simulator, booted; `true` when this test booted it.
fn boot_iphone() -> Option<(String, bool)> {
    let list = simctl(&["list", "devices", "available", "-j"]);
    let json: serde_json::Value = serde_json::from_slice(&list.stdout).ok()?;
    let devices: Vec<serde_json::Value> = json["devices"]
        .as_object()
        .into_iter()
        .flatten()
        .filter(|(rt, _)| rt.contains("iOS"))
        .flat_map(|(_, devs)| devs.as_array().cloned().unwrap_or_default())
        .filter(|d| d["name"].as_str().unwrap_or("").starts_with("iPhone"))
        .collect();
    if let Some(d) = devices.iter().find(|d| d["state"] == "Booted") {
        return Some((d["udid"].as_str()?.to_string(), false));
    }
    let udid = devices.first()?["udid"].as_str()?.to_string();
    let booted_here = simctl(&["boot", &udid]).status.success();
    let _ = simctl(&["bootstatus", &udid, "-b"]);
    Some((udid, booted_here))
}

#[cfg(target_os = "macos")]
#[test]
#[ignore = "native: needs Go, Xcode, an iOS simulator and `sky ai provision --device ios` (release gate-ai-device-native)"]
fn ios_simulator_runs_the_slot_on_the_device() {
    if !required(Need::Go, have("go", "version"))
        || !required(Need::Xcode, have("/usr/bin/xcodebuild", "-version"))
    {
        return;
    }
    assert!(
        engine_installed("ios"),
        "the iOS engine is not installed: run `sky ai provision --device ios`"
    );
    let port = free_port();
    let dir = build("mobile:ios", port);
    let split = dir.join(".skyapp/mobile-ios/.split");
    let app = std::fs::read_dir(split.join("frontend/sky-out/ios/build"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .find(|p| p.extension().is_some_and(|x| x == "app"))
        .expect("no .app");
    let model = tiny_model();
    let backend = Backend::start(&split, port, &model);
    let (udid, booted_here) = boot_iphone().expect("no available iPhone simulator");
    let _ = simctl(&["uninstall", &udid, "com.example.deviceprobe"]);
    let install = simctl(&["install", &udid, app.to_str().unwrap()]);
    let launch = simctl(&["launch", &udid, "com.example.deviceprobe"]);
    let answered = backend.wait_for("SKY-PROBE stream=", 600);
    let _ = simctl(&["terminate", &udid, "com.example.deviceprobe"]);
    let _ = simctl(&["uninstall", &udid, "com.example.deviceprobe"]);
    if booted_here {
        let _ = simctl(&["shutdown", &udid]);
    }
    assert!(
        install.status.success() && launch.status.success(),
        "{}{}",
        text(&install),
        text(&launch)
    );
    assert!(answered, "no answer: {:?}", backend.reports());
    assert_on_device(&backend.reports(), "iOS simulator");
}

fn adb() -> PathBuf {
    let sdk = std::env::var("ANDROID_HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|_| {
            PathBuf::from(std::env::var("HOME").unwrap_or_default()).join("Library/Android/sdk")
        });
    sdk.join("platform-tools/adb")
}

#[cfg(unix)]
#[test]
#[ignore = "native: needs Go, the Android SDK, a running emulator and `sky ai provision --device android` (release gate-ai-device-android)"]
fn android_emulator_runs_the_slot_on_the_device() {
    if !required(Need::Go, have("go", "version")) {
        return;
    }
    let devices = Command::new(adb()).arg("devices").output();
    let running = devices
        .as_ref()
        .map(|o| {
            String::from_utf8_lossy(&o.stdout)
                .lines()
                .any(|l| l.starts_with("emulator-") && l.ends_with("device"))
        })
        .unwrap_or(false);
    if !required(Need::AndroidEmulator, running) {
        return;
    }
    assert!(
        engine_installed("android"),
        "the Android engine is not installed: run `sky ai provision --device android`"
    );
    let port = free_port();
    let dir = build("mobile:android", port);
    let split = dir.join(".skyapp/mobile-android/.split");
    let apk = std::fs::read_dir(split.join("frontend/sky-out/android/build"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .find(|p| {
            p.extension().is_some_and(|x| x == "apk")
                && !p.ends_with("base.apk")
                && !p.ends_with("aligned.apk")
        })
        .expect("no .apk");
    let model = tiny_model();
    let backend = Backend::start(&split, port, &model);
    // A fresh install: no model, no adapter and no progress of an earlier run.
    let _ = Command::new(adb())
        .args(["uninstall", "com.example.deviceprobe"])
        .output();
    let install = Command::new(adb())
        .args(["install", "-r"])
        .arg(&apk)
        .output()
        .unwrap();
    let launch = Command::new(adb())
        .args([
            "shell",
            "am",
            "start",
            "-n",
            "com.example.deviceprobe/.MainActivity",
        ])
        .output()
        .unwrap();
    let answered = backend.wait_for("SKY-PROBE stream=", 900);
    let _ = Command::new(adb())
        .args(["uninstall", "com.example.deviceprobe"])
        .output();
    assert!(
        install.status.success() && launch.status.success(),
        "{}{}",
        text(&install),
        text(&launch)
    );
    assert!(answered, "no answer: {:?}", backend.reports());
    assert_on_device(&backend.reports(), "Android emulator");
}

#[cfg(target_os = "macos")]
#[test]
#[ignore = "native: needs Go, Xcode and a macOS desktop session (release gate-ai-device-native)"]
fn desktop_mac_runs_the_slot_on_the_managed_engine() {
    if !required(Need::Go, have("go", "version"))
        || !required(Need::Xcode, have("/usr/bin/xcodebuild", "-version"))
    {
        return;
    }
    let port = free_port();
    let dir = build("desktop:mac", port);
    let split = dir.join(".skyapp/desktop-mac/.split");
    let bin = std::fs::read_dir(split.join("frontend/sky-out/desktop"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .find(|p| p.is_file())
        .expect("no desktop binary");
    let model = tiny_model();
    let backend = Backend::start(&split, port, &model);
    let home = scratch("desktop-home");
    let mut app = ChildGroup::spawn(
        Command::new(&bin)
            .env("SKY_APP_URL", format!("http://127.0.0.1:{port}/"))
            .env("SKY_AI_HOME", &home)
            .stdout(Stdio::null())
            .stderr(Stdio::null()),
    )
    .expect("start the desktop app");
    let answered = backend.wait_for("SKY-PROBE stream=", 600);
    // SIGTERM: the app stops the engine it started on its way out. A SIGKILL
    // would leave the engine running until the next Sky program reaps it.
    let _ = Command::new("kill")
        .args(["-TERM", &app.id().to_string()])
        .status();
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
    while std::time::Instant::now() < deadline && app.child().try_wait().ok().flatten().is_none() {
        std::thread::sleep(std::time::Duration::from_millis(200));
    }
    app.kill();
    assert!(answered, "no answer: {:?}", backend.reports());
    assert_on_device(&backend.reports(), "desktop:mac");
}
