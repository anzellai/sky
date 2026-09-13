//! Smoke coverage for the `sky doc` verb paths.
//!
//! `sky doc`'s rendering helpers (project::render_module / render_doc_site) are
//! unit-tested inside the `project` crate, but the CLI verb paths had no
//! end-to-end coverage. Two cases here:
//!   (a) `sky doc <Module>` — terminal render → exit 0 + the module's real
//!       signatures in the output.
//!   (b) `sky doc --serve --port N` — build + spawn the bundled doc server,
//!       poll the port, GET `/` + `/api/symbols.json`, assert HTTP 200 with the
//!       expected content, then tear the whole process group down.
//!
//! The serve case needs a `go` toolchain (it compiles the bundled Sky.Http.Server
//! doc app). When go is absent it early-returns with a note. Every spawned
//! server is bounded by a poll deadline and killed via its process group so no
//! orphan survives the test.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::Command;
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch_project(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-doc-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"doc-smoke\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\n\nmain : Task Error ()\nmain =\n    Task.succeed ()\n",
    )
    .unwrap();
    dir
}

#[test]
fn doc_module_prints_signatures() {
    let dir = scratch_project("module");
    let out = Command::new(SKY)
        .args(["doc", "Sky.Core.List"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "sky doc Sky.Core.List failed:\n{stdout}{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        stdout.contains("Sky.Core.List"),
        "doc output missing the module header:\n{stdout}"
    );
    // A couple of real signatures the module documents — pins that the terminal
    // renderer actually emitted typed signatures, not just the header.
    assert!(
        stdout.contains("map : (a -> b) -> List a -> List b"),
        "doc output missing `map` signature:\n{stdout}"
    );
    assert!(
        stdout.contains("filter :"),
        "doc output missing `filter` signature:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Minimal HTTP/1.0 GET over a raw TcpStream (avoids a curl / reqwest
/// dependency). Returns (status_code, body) or None if the connection failed.
fn http_get(port: u16, path: &str) -> Option<(u16, String)> {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).ok()?;
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .ok()?;
    let req = format!(
        "GET {path} HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(req.as_bytes()).ok()?;
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).ok()?;
    let text = String::from_utf8_lossy(&raw).into_owned();
    let status = text
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|c| c.parse::<u16>().ok())?;
    let body = text
        .split_once("\r\n\r\n")
        .map(|(_, b)| b.to_string())
        .unwrap_or_default();
    Some((status, body))
}

/// SIGKILL the child's entire process group (the bundled server is a grandchild
/// of the spawned `sky` process; they share the group we created).
fn kill_group(child: &std::process::Child) {
    #[cfg(unix)]
    {
        use nix::sys::signal::{killpg, Signal};
        use nix::unistd::Pid;
        let _ = killpg(Pid::from_raw(child.id() as i32), Signal::SIGTERM);
        std::thread::sleep(Duration::from_millis(300));
        let _ = killpg(Pid::from_raw(child.id() as i32), Signal::SIGKILL);
    }
}

#[test]
fn doc_serve_answers_http_200() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch_project("serve");
    // A port unlikely to collide (derived from pid, high range).
    let port: u16 = 20000 + (std::process::id() % 20000) as u16;

    let mut cmd = Command::new(SKY);
    cmd.args(["doc", "--serve", "--port", &port.to_string()])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped());
    #[cfg(unix)]
    {
        // New process group so we can reap the whole server tree.
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    let mut child = cmd.spawn().expect("spawn sky doc --serve");

    // Poll the port until it answers 200 (or the deadline elapses). First run may
    // build the bundled Sky.Http.Server doc app, so the window is generous.
    let deadline = Instant::now() + Duration::from_secs(180);
    let mut got: Option<(u16, String)> = None;
    while Instant::now() < deadline {
        // If the server crashed early, stop waiting.
        if let Ok(Some(status)) = child.try_wait() {
            kill_group(&child);
            panic!("doc --serve exited early with {status:?} before binding port {port}");
        }
        if let Some((code, body)) = http_get(port, "/") {
            if code == 200 {
                got = Some((code, body));
                break;
            }
        }
        std::thread::sleep(Duration::from_millis(500));
    }

    let index = match got {
        Some((code, body)) => {
            assert_eq!(code, 200, "index did not return 200");
            body
        }
        None => {
            kill_group(&child);
            let _ = std::fs::remove_dir_all(&dir);
            panic!("doc --serve never answered 200 on port {port} within 180s");
        }
    };

    // The index is the doc SPA shell; assert it renders the doc site.
    assert!(
        index.contains("Sky API docs") || index.contains("Sky.Core"),
        "index HTML missing expected doc content:\n{}",
        &index[..index.len().min(600)]
    );

    // The symbols index must be served and carry stdlib entries.
    let symbols = http_get(port, "/api/symbols.json");
    if let Some((code, body)) = symbols {
        assert_eq!(code, 200, "/api/symbols.json did not return 200");
        assert!(
            body.contains("Sky.Core.List") && body.contains("\"module\""),
            "symbols.json missing expected entries:\n{}",
            &body[..body.len().min(400)]
        );
    } else {
        kill_group(&child);
        let _ = std::fs::remove_dir_all(&dir);
        panic!("/api/symbols.json connection failed");
    }

    kill_group(&child);
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky doc --diagram wire --target web:app` on an `App.app` (`Std.App`) app
/// must chart the RPC contract, even though those branches exist only in the
/// SYNTHESISED `Std.Spa` entry the build derives (not the raw `App.app` entry).
/// The diagram stages that synthesised project the same way the build stages it
/// and analyses THAT. Before the fix this printed the "inline-effect shape" note
/// and an empty table. The fixture's `Save` branch runs a server effect
/// (`System.getenv`), so it becomes `POST /_rpc/Save`.
#[test]
fn doc_diagram_wire_on_std_app_web_charts_rpc() {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/diagram-app-web");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "wire", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram wire");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "sky doc --diagram wire failed:\n{stdout}{stderr}"
    );
    // At least one `/_rpc/` row — the proof the synthesis path was analysed. The
    // raw `App.app` entry has no `Std.Spa` `main`, so without staging the
    // synthesised project this table would be empty.
    assert!(
        stdout.contains("POST /_rpc/Save"),
        "wire table missing the synthesised /_rpc/Save endpoint:\n{stdout}"
    );
    // The "inline-effect shape" fallback note must NOT appear — the branches
    // were recovered.
    assert!(
        !stdout.contains("inline-effect shape"),
        "wire diagram fell back to the Std.App inline-effect note:\n{stdout}"
    );
    // The display label names the user's project, not the staged scratch dir.
    assert!(
        !stdout.contains(".skyapp"),
        "wire diagram leaked the staged scratch dir into its output:\n{stdout}"
    );
    // The staging scratch tree is cleaned up (no `.skyapp` residue in the
    // committed fixture).
    assert!(
        !fixture.join(".skyapp").exists(),
        "staged `.skyapp` scratch dir was not cleaned up"
    );
}

/// `--diagram components --target web:app` on the same `Std.App` app must still
/// render the Client / Server lanes over the `/_rpc` boundary.
#[test]
fn doc_diagram_components_on_std_app_web_renders_lanes() {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/diagram-app-web");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "sky doc --diagram components failed:\n{stdout}{}",
        String::from_utf8_lossy(&out.stderr)
    );
    // Default format is PlantUML: a C4 container view with the Browser and
    // Server trust-boundary zones and the `/_rpc` crossing between them.
    assert!(stdout.starts_with("@startuml"), "not a PlantUML doc:\n{stdout}");
    assert!(stdout.contains("rectangle \"Browser · untrusted\" <<boundary>>"), "no Browser zone:\n{stdout}");
    assert!(stdout.contains("rectangle \"Server · trusted\" <<boundary>>"), "no Server zone:\n{stdout}");
    assert!(stdout.contains("/_rpc"), "no /_rpc crossing:\n{stdout}");
    // The title names the app (`sky.toml` `name`), not a machine-local file path:
    // a diagram is a shared artefact, and a path is noise (and leaks a layout).
    assert!(
        stdout.contains("— diagram-app-web-fixture") && !stdout.contains(&*fixture.to_string_lossy()),
        "diagram title should use the sky.toml app name, not a file path:\n{stdout}"
    );
    assert!(
        !fixture.join(".skyapp").exists(),
        "staged `.skyapp` scratch dir was not cleaned up"
    );
}

/// `sky doc --diagram telemetry` on an app with no telemetry / analytics /
/// logging call site is NOT an error: it prints the "no call sites" line and
/// exits 0. The `diagram-app-web` fixture only reads env (`System.getenv`), so
/// it has no Log / Analytics call. Also verifies the staging scratch tree is
/// cleaned up on the telemetry arm (run under `--target web:app`).
#[test]
fn doc_diagram_telemetry_no_sites_exits_zero() {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/diagram-app-web");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "telemetry", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram telemetry");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "sky doc --diagram telemetry failed:\n{stdout}{stderr}"
    );
    assert!(
        stdout.contains("No telemetry, analytics, or logging call sites found."),
        "expected the no-sites message:\n{stdout}"
    );
    assert!(
        !fixture.join(".skyapp").exists(),
        "staged `.skyapp` scratch dir was not cleaned up"
    );
}

/// `sky doc --diagram journey` on a real multi-page Sky.Live app (examples/
/// 19-skyforum: a `Page` union `HomePage | PostPage Int | LoginPage`, a Model
/// `currentPage : Page`, and an `update` that reroutes to `LoginPage` /
/// `HomePage`). It must name at least two pages and at least one action, with no
/// go build and no staging (the bare Live target does not synthesise a client).
#[test]
fn doc_diagram_journey_on_skyforum_lists_pages_and_actions() {
    let project = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/19-skyforum");
    // A committed example — a missing one is a real failure, not a skip (a silent
    // skip here is exactly what the live-tests meta-gate forbids).
    assert!(
        project.join("src/State.sky").exists(),
        "examples/19-skyforum is a committed example and must be present at {project:?}"
    );
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "journey", "--format", "md"])
        .current_dir(&project)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram journey");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "sky doc --diagram journey failed:\n{stdout}{stderr}"
    );
    // The page set (>= 2 pages), recovered from the `Page` union.
    assert!(stdout.contains("## Pages"), "no Pages section:\n{stdout}");
    assert!(stdout.contains("| HomePage |"), "missing HomePage:\n{stdout}");
    assert!(stdout.contains("| LoginPage |"), "missing LoginPage:\n{stdout}");
    // The action inventory (>= 1 action), with the new typed columns, and a
    // recovered navigation target.
    assert!(stdout.contains("## Actions"), "no Actions section:\n{stdout}");
    assert!(
        stdout.contains("| Action | Kind | Effects | Navigates to |"),
        "actions table must carry the Kind + Effects columns:\n{stdout}"
    );
    assert!(stdout.contains("| Navigate |"), "missing Navigate action:\n{stdout}");
    // UpvotePost reroutes to LoginPage; the Live app has no /_rpc, so its Kind is
    // effectful (server-side) or pure, never a `server (SSE)` string.
    let upvote = stdout
        .lines()
        .find(|l| l.starts_with("| UpvotePost |"))
        .unwrap_or_else(|| panic!("missing UpvotePost row:\n{stdout}"));
    assert!(
        upvote.contains("LoginPage") && !upvote.contains("/_rpc"),
        "UpvotePost should reroute to LoginPage without a /_rpc round-trip (Live):\n{upvote}"
    );
    // A bare Live target never stages a client, so no `.skyapp` scratch tree.
    assert!(
        !project.join(".skyapp/diagram").exists(),
        "journey left a staged `.skyapp/diagram` scratch dir"
    );
}

/// `sky doc --diagram wire --target web:app` on a `Std.App` app that registers a
/// raw `App.api` webhook must chart it in a dedicated **HTTP endpoints** section
/// BESIDE the `/_rpc` contract, marked CSRF-exempt — the Stripe-webhook shape a
/// plain `/_rpc` table used to miss.
#[test]
fn doc_diagram_wire_charts_the_app_api_webhook() {
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/diagram-webhook");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "wire", "--target", "web:app", "--format", "md"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram wire");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "wire failed:\n{stdout}{stderr}");
    // The /_rpc contract is still charted (the effectful `Save` branch).
    assert!(stdout.contains("POST /_rpc/Save"), "missing /_rpc/Save:\n{stdout}");
    // The raw `App.api` webhook is charted in its own HTTP-endpoints section.
    assert!(
        stdout.contains("## HTTP endpoints (raw `App.api`, beside /_rpc)"),
        "missing the HTTP endpoints section:\n{stdout}"
    );
    assert!(
        stdout.contains("| POST | /webhooks/stripe | Main.handleWebhook | raw api · CSRF-exempt |"),
        "missing the CSRF-exempt webhook row:\n{stdout}"
    );
    // Default puml carries the same depth: the webhook endpoint + a per-endpoint
    // effects note (Save reaches System via the bootId CAF).
    let puml = Command::new(SKY)
        .args(["doc", "--diagram", "wire", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram wire puml");
    let puml = String::from_utf8_lossy(&puml.stdout);
    assert!(puml.starts_with("@startuml"), "not puml:\n{puml}");
    assert!(puml.contains("/webhooks/stripe"), "webhook missing from puml:\n{puml}");
    assert!(
        puml.contains("note right of ep") && puml.contains("effects:"),
        "puml missing per-endpoint effects note:\n{puml}"
    );
    assert!(!fixture.join(".skyapp").exists(), "staged scratch not cleaned up");
}

/// `sky doc --diagram journey --target web:app` on the same app must split the
/// non-navigating actions into distinct **Effectful** and **Pure** sections
/// (`Save` is effectful via `System.getenv`; `Inc` is pure).
#[test]
fn doc_diagram_journey_splits_effectful_and_pure() {
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/diagram-webhook");
    let md = Command::new(SKY)
        .args(["doc", "--diagram", "journey", "--target", "web:app", "--format", "md"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram journey");
    let stdout = String::from_utf8_lossy(&md.stdout);
    assert!(md.status.success(), "journey failed:\n{stdout}");
    assert!(stdout.contains("| Action | Kind | Effects | Navigates to |"), "{stdout}");
    assert!(
        stdout.contains("| Save | effectful (server · /_rpc) |"),
        "Save must be effectful (server · /_rpc):\n{stdout}"
    );
    assert!(stdout.contains("| Inc | pure |"), "Inc must be pure:\n{stdout}");
    // The SVG carries the two labelled sections.
    let svg = Command::new(SKY)
        .args(["doc", "--diagram", "journey", "--target", "web:app", "--format", "svg"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram journey svg");
    let svg = String::from_utf8_lossy(&svg.stdout);
    assert!(svg.contains("Effectful actions"), "no Effectful section:\n{svg}");
    assert!(svg.contains("Pure actions"), "no Pure section:\n{svg}");
    // Default puml carries the same split as two floating notes.
    let puml = Command::new(SKY)
        .args(["doc", "--diagram", "journey", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram journey puml");
    let puml = String::from_utf8_lossy(&puml.stdout);
    assert!(puml.contains("note as effectful_note"), "no Effectful note in puml:\n{puml}");
    assert!(puml.contains("<b>Effectful actions"), "no Effectful heading in puml:\n{puml}");
    assert!(puml.contains("note as pure_note"), "no Pure note in puml:\n{puml}");
    assert!(!fixture.join(".skyapp").exists(), "staged scratch not cleaned up");
}

/// `sky doc --diagram components --target web:app` on the same app must list the
/// real `Std.Db` table name inside the Database container.
#[test]
fn doc_diagram_components_lists_db_table_names() {
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/diagram-webhook");
    let md = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--target", "web:app", "--format", "md"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components");
    let stdout = String::from_utf8_lossy(&md.stdout);
    assert!(md.status.success(), "components failed:\n{stdout}");
    assert!(
        stdout.contains("Database tables (1): widgets."),
        "the Database container must list the real table name:\n{stdout}"
    );
    let svg = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--target", "web:app", "--format", "svg"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components svg");
    let svg = String::from_utf8_lossy(&svg.stdout);
    assert!(svg.contains(">widgets<"), "table name inside the Database store:\n{svg}");
    // Default puml carries the table name in the Database node label + the
    // effectful count on the /_rpc crossing.
    let puml = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--target", "web:app"])
        .current_dir(&fixture)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components puml");
    let puml = String::from_utf8_lossy(&puml.stdout);
    assert!(
        puml.contains("database \"Database\\nwidgets\""),
        "puml Database node must list the table:\n{puml}"
    );
    assert!(puml.contains("effectful"), "puml /_rpc edge must carry the effectful count:\n{puml}");
    assert!(!fixture.join(".skyapp").exists(), "staged scratch not cleaned up");
}

/// `--format mermaid` is retired: the CLI exits non-zero with a message naming
/// the shipped formats. No diagram is produced.
#[test]
fn doc_diagram_mermaid_format_is_retired() {
    let project = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/19-skyforum");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--format", "mermaid"])
        .current_dir(&project)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram");
    assert!(!out.status.success(), "retired format must exit non-zero");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("mermaid was retired") && stderr.contains("puml"),
        "expected the retired-mermaid message:\n{stderr}"
    );
    assert!(String::from_utf8_lossy(&out.stdout).is_empty(), "no diagram should be printed");
}

/// `--format svg` produces a self-contained, well-formed SVG on stdout.
#[test]
fn doc_diagram_components_svg_is_wellformed() {
    let project = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/19-skyforum");
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--format", "svg"])
        .current_dir(&project)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components --format svg");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(out.status.success(), "svg render failed:\n{}", String::from_utf8_lossy(&out.stderr));
    assert!(stdout.trim_start().starts_with("<svg"), "not an SVG:\n{}", &stdout[..stdout.len().min(200)]);
    assert!(stdout.trim_end().ends_with("</svg>"), "SVG not closed");
    assert!(stdout.contains("<rect"), "SVG has no nodes");
}

/// `--out <path>` writes the diagram to a file instead of stdout; stdout stays
/// empty.
#[test]
fn doc_diagram_out_writes_a_file() {
    let project = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/19-skyforum");
    let out_path = std::env::temp_dir().join(format!("sky-diagram-{}.puml", std::process::id()));
    let _ = std::fs::remove_file(&out_path);
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "components", "--out"])
        .arg(&out_path)
        .current_dir(&project)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram components --out");
    assert!(out.status.success(), "--out failed:\n{}", String::from_utf8_lossy(&out.stderr));
    assert!(String::from_utf8_lossy(&out.stdout).is_empty(), "stdout must be empty with --out");
    let written = std::fs::read_to_string(&out_path).expect("--out file exists");
    assert!(written.starts_with("@startuml"), "file is not a PlantUML doc:\n{written}");
    let _ = std::fs::remove_file(&out_path);
}

/// `wire` on an API-only Sky.Http.Server app (no Sky.Spa client) charts the HTTP
/// endpoint map recovered from the resolved HIR: method + path + handler.
#[test]
fn doc_diagram_wire_on_http_server_charts_endpoint_map() {
    let project = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../examples/15-http-server");
    assert!(
        project.join("src/Main.sky").exists(),
        "examples/15-http-server is a committed example and must be present"
    );
    let out = Command::new(SKY)
        .args(["doc", "--diagram", "wire", "--format", "md"])
        .current_dir(&project)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky doc --diagram wire");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "wire on http-server failed:\n{stdout}{stderr}");
    assert!(
        stdout.contains("| Method | Path | Handler / page | Kind |"),
        "no endpoint map:\n{stdout}"
    );
    assert!(stdout.contains("| GET | / | handleHome | http |"), "missing GET / route:\n{stdout}");
    assert!(
        stdout.contains("| POST | /api/echo | handleEcho | http |"),
        "missing POST route:\n{stdout}"
    );
    // It is not a Spa app, so there is no /_rpc table.
    assert!(!stdout.contains("| Endpoint |"), "an HTTP app has no /_rpc table:\n{stdout}");
}
