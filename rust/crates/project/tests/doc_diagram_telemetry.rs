//! `sky doc --diagram telemetry` over real example projects.
//!
//! Read-only: [`project::diagram::analyze_telemetry`] loads the source db and
//! walks the resolved HIR — it never type-checks, lowers, `go build`s, or writes,
//! so these run in well under a second (no timeout wrapper needed; nothing here
//! compiles Go).
//!
//! Coverage:
//!   * `07-todo-cli` uses `Std.Log` (`Log.info` / `Log.infoWith` / `Log.errorWith`
//!     with string-literal messages) — the md table lists each call with its
//!     event and the `structured logs` sink.
//!   * `01-hello-world` logs a single line — its lone call site appears.

use project::diagram::{analyze_telemetry, render_telemetry, Format, Sink};
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

#[test]
fn todo_cli_lists_its_log_call_sites_with_sinks() {
    let root = repo_root();
    let dir = root.join("examples/07-todo-cli");
    let r = analyze_telemetry(&root, &dir, None, None)
        .unwrap_or_else(|e| panic!("analyze_telemetry failed: {e}"));

    // Every call site this example writes is a Std.Log call → the logs sink.
    assert!(
        !r.calls.is_empty(),
        "expected Std.Log call sites in 07-todo-cli"
    );
    assert!(
        r.calls.iter().all(|c| c.sink == Sink::Logs),
        "07-todo-cli only logs; got {:?}",
        r.calls.iter().map(|c| (&c.call, c.sink)).collect::<Vec<_>>()
    );

    // A specific string-literal event is captured verbatim.
    assert!(
        r.calls
            .iter()
            .any(|c| c.call == "Log.info" && c.event == "Sky TODO - A simple todo manager"),
        "expected the Log.info startup line; got {:?}",
        r.calls.iter().map(|c| (&c.call, &c.event)).collect::<Vec<_>>()
    );
    // `*With` variants: the message is the first string literal, the props follow.
    assert!(
        r.calls
            .iter()
            .any(|c| c.call == "Log.errorWith" && c.event == "todo-cli"),
        "expected Log.errorWith with its message; got {:?}",
        r.calls.iter().map(|c| (&c.call, &c.event)).collect::<Vec<_>>()
    );

    // The md table lists the call with its full sink description.
    let md = render_telemetry(&r, Format::Md);
    assert!(md.contains("| Module | Call | Event | Sink |"), "{md}");
    assert!(
        md.contains(
            "| Main | Log.info | Sky TODO - A simple todo manager | structured logs (console; OTel when OTEL_EXPORTER_OTLP_ENDPOINT set) |"
        ),
        "{md}"
    );
    // The server-side note is present.
    assert!(md.contains("effect kernels") && md.contains("/_rpc"), "{md}");

    // The PlantUML form draws a module → logs-sink edge.
    let puml = render_telemetry(&r, Format::Puml);
    assert!(puml.starts_with("@startuml"), "{puml}");
    assert!(puml.contains("queue \"Logs\" as sink_logs"), "{puml}");
    assert!(puml.contains("m_Main --> sink_logs :"), "{puml}");

    // The SVG form is well-formed.
    let svg = render_telemetry(&r, Format::Svg);
    assert!(svg.trim_start().starts_with("<svg") && svg.trim_end().ends_with("</svg>"), "{svg}");
    assert!(svg.contains("Logs"), "{svg}");
}

#[test]
fn hello_world_logs_one_line() {
    let root = repo_root();
    let dir = root.join("examples/01-hello-world");
    let r = analyze_telemetry(&root, &dir, None, None)
        .unwrap_or_else(|e| panic!("analyze_telemetry failed: {e}"));

    // The example is `println "Hello from Sky!"` (Std.Log.println, exposed).
    assert!(
        r.calls
            .iter()
            .any(|c| c.call == "Log.println" && c.event == "Hello from Sky!"),
        "expected the hello-world println line; got {:?}",
        r.calls.iter().map(|c| (&c.call, &c.event)).collect::<Vec<_>>()
    );
}
