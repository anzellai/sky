//! `Std.Ai.Serve` and the cloud verbs, offline, on every commit.
//!
//! The GPU tier (`sky ai serve`, the bundled program sky-bundled/ai-cloud)
//! runs against `runtime-go/rt/testdata/fakevllm`, a stand-in for vLLM's
//! OpenAI server with runtime LoRA loading, and reads a local registry store
//! the fixture program `fixtures/ai-serve` fills. Test mode keeps the base's
//! one-gigabyte download out of the test (the tier stages a stand-in); the
//! routing, the service token, multi-LoRA, on-demand loading and the hot-load
//! of a promotion are the real code. The deployment verbs run against
//! `runtime-go/rt/testdata/fakegcp`, the runtime's Google Cloud emulator.

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
#[cfg(unix)]
use child_group::ChildGroup;
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
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("sky-ai-serve-{tag}-{}", unique()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

/// Build one of runtime-go/rt/testdata's Go programs into `dir`.
fn go_build(name: &str, dir: &Path) -> PathBuf {
    let bin = dir.join(name);
    let out = Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg(format!("./rt/testdata/{name}"))
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .expect("go build");
    assert!(out.status.success(), "go build {name}:\n{}", text(&out));
    bin
}

/// Start a testdata server and return it with its address. The server and
/// anything it starts end with the returned guard, on every path (the
/// readiness panic below included).
#[cfg(unix)]
fn start_server(bin: &Path, dir: &Path, args: &[&str]) -> (ChildGroup, String) {
    let port_file = dir.join(format!(
        "{}.port",
        bin.file_name().unwrap().to_string_lossy()
    ));
    let child = ChildGroup::spawn(
        Command::new(bin)
            .arg("-port-file")
            .arg(&port_file)
            .args(args)
            .stdout(Stdio::null())
            .stderr(Stdio::null()),
    )
    .expect("start the server");
    let start = Instant::now();
    while start.elapsed() < Duration::from_secs(10) {
        if let Ok(addr) = std::fs::read_to_string(&port_file) {
            if !addr.is_empty() {
                return (child, addr);
            }
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    panic!("{} did not start", bin.display());
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// One HTTP/1.1 request; returns the status and the body.
fn http(addr: &str, method: &str, path: &str, token: Option<&str>, body: &str) -> (u16, String) {
    let Ok(mut s) = TcpStream::connect(addr) else {
        return (0, String::new());
    };
    s.set_read_timeout(Some(Duration::from_secs(20))).unwrap();
    let auth = token
        .map(|t| format!("Authorization: Bearer {t}\r\n"))
        .unwrap_or_default();
    let _ = write!(
        s,
        "{method} {path} HTTP/1.1\r\nHost: {addr}\r\n{auth}Content-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    let mut buf = String::new();
    let _ = s.read_to_string(&mut buf);
    let status = buf
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    let body = buf
        .split_once("\r\n\r\n")
        .map(|(_, b)| b.to_string())
        .unwrap_or_default();
    (status, body)
}

fn chat(addr: &str, token: &str, model: &str) -> (u16, String) {
    http(
        addr,
        "POST",
        "/v1/chat/completions",
        Some(token),
        &format!(r#"{{"model":"{model}","messages":[{{"role":"user","content":"hi"}}]}}"#),
    )
}

/// The registry fixture, built once per test into its own copy.
fn fixture(dir: &Path) -> PathBuf {
    let src = repo_root().join("rust/crates/sky/tests/fixtures/ai-serve");
    let project = dir.join("fixture");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::copy(src.join("sky.toml"), project.join("sky.toml")).unwrap();
    std::fs::copy(src.join("src/Main.sky"), project.join("src/Main.sky")).unwrap();
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&project)
        .output()
        .expect("sky build");
    assert!(out.status.success(), "the fixture builds:\n{}", text(&out));
    project.join("sky-out/app")
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[cfg(unix)]
#[test]
fn the_tier_serves_two_adapters_and_hot_loads_a_promotion() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("tier");
    let app = fixture(&dir);
    let reg = dir.join("reg");
    let out = Command::new(&app)
        .arg("register")
        .arg(&reg)
        .output()
        .unwrap();
    assert!(out.status.success(), "register:\n{}", text(&out));

    let fakevllm = go_build("fakevllm", &dir);
    let (_engine, engine_addr) = start_server(
        &fakevllm,
        &dir,
        &["-base", "qwen2.5-0.5b-instruct", "-delay-ms", "100"],
    );

    let port = free_port();
    let addr = format!("127.0.0.1:{port}");
    let _tier = ChildGroup::spawn(
        Command::new(SKY)
            .args([
                "ai",
                "serve",
                "--adapters",
                "support@qa",
                "--max-adapters",
                "3",
            ])
            .arg("--registry")
            .arg(format!("file://{}", reg.join("store").display()))
            .arg("--bind")
            .arg(&addr)
            .arg("--engine-url")
            .arg(format!("http://{engine_addr}"))
            .arg("--models")
            .arg(dir.join("models"))
            .current_dir(&dir)
            .env("SKY_AI_HOME", dir.join("home"))
            .env("SKY_AI_SERVE_TOKEN", "flow-token")
            .env("SKY_AI_SERVE_POLL_S", "1")
            .env("SKY_TEST_MODE", "1")
            .stdout(Stdio::null())
            .stderr(Stdio::null()),
    )
    .expect("sky ai serve");

    let start = Instant::now();
    loop {
        let (st, _) = http(&addr, "GET", "/health", None, "");
        if st == 200 {
            break;
        }
        assert!(
            start.elapsed() < Duration::from_secs(240),
            "the tier never became ready (last status {st})"
        );
        std::thread::sleep(Duration::from_millis(200));
    }

    let (st, body) = chat(&addr, "flow-token", "");
    assert!(
        st == 200 && body.contains("echo:qwen2.5-0.5b-instruct"),
        "the base: {st} {body}"
    );
    let (st, body) = chat(&addr, "flow-token", "support-v1");
    assert!(
        st == 200 && body.contains("echo:support-v1"),
        "the alias's version: {st} {body}"
    );
    let (st, body) = chat(&addr, "wrong", "support-v1");
    assert!(
        st == 401 && !body.contains("flow-token"),
        "a wrong token: {st} {body}"
    );
    let (st, _) = chat(&addr, "flow-token", "support-v9");
    assert_eq!(st, 404, "a version the registry does not hold");

    // The promotion, while requests run: qa moves to v2 after the fifth.
    let mut failures = 0;
    // The promotion comes after the last request for v1: a request for v1
    // after the drain would load it again on demand (a version no alias pins
    // is served while someone asks for it), and the check below is that the
    // tier drops the version the alias left. The Go test
    // TestAiTierHotLoadsAPromotionWithoutDroppingARequest covers a promotion
    // with v1 requests still in flight.
    for i in 0..20 {
        if i == 10 {
            let out = Command::new(&app)
                .arg("promote")
                .arg(&reg)
                .arg("2")
                .output()
                .unwrap();
            assert!(out.status.success(), "promote:\n{}", text(&out));
        }
        let model = if i < 10 { "support-v1" } else { "support-v2" };
        let (st, _) = chat(&addr, "flow-token", model);
        if st != 200 {
            failures += 1;
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    assert_eq!(failures, 0, "no request fails across the promotion");

    // The tier follows the alias: v2 resident, v1 unloaded once idle.
    let start = Instant::now();
    loop {
        let (_, stats) = http(&engine_addr, "GET", "/_fake/stats", None, "");
        let v: serde_json::Value = serde_json::from_str(&stats).unwrap_or_default();
        let loaded = v["loaded"].to_string();
        if loaded.contains("support-v2") && !loaded.contains("support-v1") {
            assert_eq!(v["broken"], 0, "no request saw its adapter vanish: {stats}");
            break;
        }
        assert!(
            start.elapsed() < Duration::from_secs(30),
            "v1 was not drained and unloaded: {stats}"
        );
        std::thread::sleep(Duration::from_millis(250));
    }
    let (st, metrics) = http(&addr, "GET", "/metrics", None, "");
    assert!(
        st == 200 && metrics.contains("sky_ai_tier_resident_adapters"),
        "{metrics}"
    );
}

#[cfg(unix)]
#[test]
fn deploy_dry_run_teardown_and_ls_against_the_emulator() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("deploy");
    let fakegcp = go_build("fakegcp", &dir);
    let (_gcp, gcp_addr) = start_server(&fakegcp, &dir, &[]);
    let ep = format!("http://{gcp_addr}");
    let image = "us-central1-docker.pkg.dev/p/sky/sky-ai-tier@sha256:2222222222222222222222222222222222222222222222222222222222222222";
    let sky = |args: &[&str]| {
        Command::new(SKY)
            .arg("ai")
            .args(args)
            .current_dir(&dir)
            .env("SKY_AI_GCP_ENDPOINT", &ep)
            .env_remove("GOOGLE_APPLICATION_CREDENTIALS")
            .env_remove("SKY_AI_SERVE_PROJECT")
            .env_remove("SKY_AI_SERVE_REGION")
            .env_remove("SKY_AI_SERVE_IMAGE")
            .output()
            .expect("sky ai")
    };
    let common = [
        "--service",
        "support-tier",
        "--project",
        "p",
        "--region",
        "us-central1",
        "--tier-image",
        image,
        "--adapters",
        "support@prod",
        "--token-secret",
        "support-token",
    ];

    let mut args = vec!["deploy", "--dry-run", "--format", "json"];
    args.extend_from_slice(&common);
    let out = sky(&args);
    assert!(out.status.success(), "dry run:\n{}", text(&out));
    let v: serde_json::Value = serde_json::from_str(
        String::from_utf8_lossy(&out.stdout)
            .trim()
            .lines()
            .last()
            .unwrap(),
    )
    .unwrap();
    let spec = &v["spec"];
    assert_eq!(spec["template"]["scaling"]["minInstanceCount"], 0);
    assert_eq!(
        spec["template"]["containers"][1]["resources"]["limits"]["nvidia.com/gpu"],
        "1"
    );
    assert_eq!(spec["labels"]["sky-managed"], "true");
    let (_, stats) = http(&gcp_addr, "GET", "/_fake/stats", None, "");
    assert!(
        stats.contains("\"serviceCreates\":0"),
        "a dry run creates nothing: {stats}"
    );

    let mut args = vec![
        "deploy",
        "--invoker",
        "serviceAccount:app@p.iam.gserviceaccount.com",
    ];
    args.extend_from_slice(&common);
    let out = sky(&args);
    assert!(out.status.success(), "deploy:\n{}", text(&out));
    assert!(
        text(&out).contains("https://support-tier-fake.a.run.app"),
        "{}",
        text(&out)
    );
    // A second deploy updates the service in place (guarded by its etag).
    let out = sky(&args);
    assert!(out.status.success(), "redeploy:\n{}", text(&out));
    let (_, stats) = http(&gcp_addr, "GET", "/_fake/stats", None, "");
    assert!(
        stats.contains("\"serviceCreates\":1") && stats.contains("\"servicePatches\":1"),
        "{stats}"
    );
    assert!(
        stats.contains("roles/run.invoker"),
        "the invoker is granted: {stats}"
    );

    let out = sky(&["cloud", "ls", "--project", "p", "--region", "us-central1"]);
    assert!(
        text(&out).contains("run-service  support-tier"),
        "{}",
        text(&out)
    );

    // `teardown --all-sky --label k=v` deletes only what carries the label: a
    // live run in a shared project must not delete another Sky service. A
    // second Sky service, made by "another run", carries live-run=r1.
    let other = r#"{"labels":{"sky-managed":"true","live-run":"r1"},"template":{"nodeSelector":{"accelerator":"nvidia-l4"},"containers":[
{"name":"tier","image":"r/t@sha256:01","ports":[{"containerPort":8080}]},
{"name":"engine","image":"r/v@sha256:02","resources":{"limits":{"cpu":"4","memory":"16Gi","nvidia.com/gpu":"1"}}}]}}"#;
    let (st, body) = http(
        &gcp_addr,
        "POST",
        "/run/v2/projects/p/locations/us-central1/services?serviceId=other-tier",
        Some("fake"),
        other,
    );
    assert_eq!(st, 200, "create the other service: {body}");
    let out = sky(&[
        "teardown",
        "--all-sky",
        "--label",
        "live-run",
        "--project",
        "p",
        "--region",
        "us-central1",
    ]);
    assert!(
        !out.status.success() && text(&out).contains("key=value"),
        "a malformed label is refused, not ignored:\n{}",
        text(&out)
    );
    let out = sky(&[
        "teardown",
        "--all-sky",
        "--label",
        "live-run=r1",
        "--project",
        "p",
        "--region",
        "us-central1",
    ]);
    assert!(out.status.success(), "teardown --label:\n{}", text(&out));
    let out = sky(&["cloud", "ls", "--project", "p", "--region", "us-central1"]);
    assert!(
        text(&out).contains("run-service  support-tier") && !text(&out).contains("other-tier"),
        "only the labelled service is deleted:\n{}",
        text(&out)
    );

    for _ in 0..2 {
        let out = sky(&[
            "teardown",
            "--service",
            "support-tier",
            "--project",
            "p",
            "--region",
            "us-central1",
        ]);
        assert!(
            out.status.success(),
            "teardown is idempotent:\n{}",
            text(&out)
        );
    }
    let out = sky(&["cloud", "ls", "--project", "p", "--region", "us-central1"]);
    assert!(text(&out).contains("nothing Sky made"), "{}", text(&out));
}
