//! `sky doc --diagram wire` over real Sky.Spa projects.
//!
//! Read-only: [`project::diagram::analyze_wire`] reuses
//! [`project::spa_partition::analyze`] — the SAME per-branch read-set / write-set
//! the auto-split derives — so it never type-checks beyond the shared source
//! load, and never lowers, `go build`s, or writes. It runs in well under a
//! second (no timeout wrapper needed; nothing here compiles Go).
//!
//! Coverage:
//!   * `spa-writeset-narrow` (a `case msg of` Sky.Spa app with SERVER branches)
//!     under `--target web:app` — the md table carries `POST /_rpc/<Msg>`
//!     endpoint rows with Request and Response columns, including a narrowed
//!     write-set (`SaveNarrow` → `{log, n, tag}`) and a whole-model branch.
//!   * `62-app-notes` under `--target web:app` (a `Std.App` inline-effect shape,
//!     the auto-split input) — degrades gracefully: `is_spa` is true but no
//!     per-branch endpoints are surfaced, so the report is `limited` with a note
//!     rather than an error.
//!   * `65-metadata-service` (a plain Sky.Http service) — NOT a Spa client, so
//!     `wire` prints the explanatory note and no table, and does not error.

use project::diagram::{analyze_wire, render_wire, Format};
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
fn spa_app_has_rpc_endpoint_rows_with_request_and_response() {
    let root = repo_root();
    let dir = root.join("rust/crates/sky/tests/fixtures/spa-writeset-narrow");
    // Force the wasm-client target: `wire` charts the `/_rpc` contract a Sky.Spa
    // client calls, so the diagram only renders for a wasm-client target.
    let r = analyze_wire(&root, &dir, None, Some("web:app"))
        .unwrap_or_else(|e| panic!("analyze_wire failed: {e}"));

    assert!(r.is_spa, "web:app is a Sky.Spa wasm-client target");
    assert!(
        !r.endpoints.is_empty(),
        "this fixture has SERVER `update` branches surfaced as `/_rpc` endpoints; \
         got none (limited={})",
        r.limited
    );

    let md = render_wire(&r, Format::Md);
    // The header row names the four columns.
    assert!(
        md.contains("| Endpoint | Request (reads + args) | Response (writes) | Effects |"),
        "{md}"
    );
    // At least one `/_rpc/` endpoint row is rendered.
    assert!(md.contains("| POST /_rpc/"), "{md}");
    // `SaveNarrow` narrows its write-set to `{log, n, tag}` (excluding `extra`) —
    // the RESPONSE column proves the diagram surfaces the derived contract, not
    // an over-approximation.
    assert!(
        md.contains("| POST /_rpc/SaveNarrow |"),
        "expected a SaveNarrow endpoint row: {md}"
    );
    let save = r
        .endpoints
        .iter()
        .find(|e| e.msg == "SaveNarrow")
        .expect("SaveNarrow endpoint");
    assert_eq!(save.response, "{log, n, tag}", "narrowed write-set");
    assert!(
        !save.response.contains("extra"),
        "`extra` is untouched, must not ride the response: {}",
        save.response
    );

    // PlantUML form is a data-flow diagram: an endpoint process per crossing.
    let puml = render_wire(&r, Format::Puml);
    assert!(puml.starts_with("@startuml"), "{puml}");
    assert!(
        puml.contains("rectangle \"Client · untrusted\" <<boundary>>"),
        "{puml}"
    );
    assert!(
        puml.contains("rectangle \"POST /_rpc/SaveNarrow\""),
        "{puml}"
    );
    assert!(puml.contains("resp {log, n, tag}"), "{puml}");

    // SVG form is a well-formed sequence diagram.
    let svg = render_wire(&r, Format::Svg);
    assert!(
        svg.trim_start().starts_with("<svg") && svg.trim_end().ends_with("</svg>"),
        "{svg}"
    );
}

#[test]
fn std_app_inline_effect_shape_degrades_gracefully_not_an_error() {
    let root = repo_root();
    let dir = root.join("examples/62-app-notes");
    // `62-app-notes` is a `Std.App` inline-effect app: its update branches are
    // not surfaced the way a `case msg of` Sky.Spa app's are, so per-branch wire
    // extraction is limited. `wire` must say so, not error.
    let r = analyze_wire(&root, &dir, None, Some("web:app"))
        .unwrap_or_else(|e| panic!("analyze_wire failed: {e}"));

    assert!(r.is_spa, "web:app is a Sky.Spa wasm-client target");
    if r.endpoints.is_empty() {
        assert!(
            r.limited,
            "no endpoints ⇒ the report must be marked limited"
        );
        let md = render_wire(&r, Format::Md);
        assert!(
            md.contains("limited"),
            "expected a limited-extraction note: {md}"
        );
    }
}

#[test]
fn metadata_service_charts_its_http_endpoint_map() {
    let root = repo_root();
    let dir = root.join("examples/65-metadata-service");
    // No `--target`, no `[app] target` pin → a plain Sky.Http service. It has no
    // `/_rpc` contract, but it DOES register routes, so `wire` charts the HTTP
    // endpoint map recovered from the resolved HIR.
    let r = analyze_wire(&root, &dir, None, None)
        .unwrap_or_else(|e| panic!("analyze_wire failed: {e}"));

    assert!(!r.is_spa, "a plain Http service is not a Sky.Spa client");
    assert!(
        r.endpoints.is_empty(),
        "a non-Spa app has no /_rpc endpoints"
    );
    assert!(
        !r.http_endpoints.is_empty(),
        "expected a Sky.Http.Server route map; got none"
    );
    // The `/` route with a bare-def handler resolves its name.
    assert!(
        r.http_endpoints
            .iter()
            .any(|e| e.method == "GET" && e.path == "/" && e.handler == "handleRoot"),
        "expected GET / -> handleRoot; got {:?}",
        r.http_endpoints
    );

    let md = render_wire(&r, Format::Md);
    // The HTTP endpoint table, not the /_rpc table.
    assert!(md.contains("| Method | Path | Handler |"), "{md}");
    assert!(md.contains("| GET | / | handleRoot |"), "{md}");
    assert!(
        !md.contains("| Endpoint |"),
        "no /_rpc table for an HTTP app:\n{md}"
    );

    // PlantUML + SVG both render the endpoint map as a DFD.
    let puml = render_wire(&r, Format::Puml);
    assert!(puml.contains("rectangle \"GET /\""), "{puml}");
    let svg = render_wire(&r, Format::Svg);
    assert!(svg.trim_start().starts_with("<svg"), "{svg}");
    assert!(svg.contains("Endpoints (HTTP)"), "{svg}");
}
