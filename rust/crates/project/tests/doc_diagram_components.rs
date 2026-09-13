//! `sky doc --diagram components` over real example projects.
//!
//! Read-only: [`project::diagram::analyze_components`] loads the source db and
//! walks the resolved HIR — it never type-checks, lowers, `go build`s, or writes,
//! so these run in well under a second (no timeout wrapper needed; nothing here
//! compiles Go).
//!
//! Coverage:
//!   * `65-metadata-service` (HTTP → SQL read → JSON, non-Spa) — a single-lane
//!     diagram with the Database capability + a module → Database edge.
//!   * `62-app-notes` (Std.App inline effects, the auto-split input) rendered as
//!     a Sky.Spa client (`--target web:app`) — both `Client` and `Server`
//!     subgraphs with the `/_rpc` boundary between them.

use project::diagram::{analyze_components, render_components, Format};
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
fn metadata_service_is_single_lane_with_a_database_edge() {
    let root = repo_root();
    let dir = root.join("examples/65-metadata-service");
    let g = analyze_components(&root, &dir, None, None)
        .unwrap_or_else(|e| panic!("analyze_components failed: {e}"));

    // A read-backed HTTP service touches the Database.
    assert!(
        g.capabilities
            .contains(&project::diagram::Capability::Database),
        "expected a Database capability; got {:?}",
        g.capabilities.iter().map(|c| c.label()).collect::<Vec<_>>()
    );
    assert!(!g.is_spa, "a plain Http service is not a Sky.Spa app");

    let out = render_components(&g, Format::Puml);
    assert!(out.starts_with("@startuml"), "{out}");
    // the Database capability node (a distinct database shape)
    assert!(out.contains("database \"Database\" as cap_db"), "{out}");
    // at least one module → Database edge
    assert!(out.contains("--> cap_db"), "{out}");
    // single-lane: no RPC boundary
    assert!(!out.contains("interface \"/_rpc\""), "{out}");

    // md format carries the module → capability table.
    let md = render_components(&g, Format::Md);
    assert!(md.contains("| Module | Capabilities |"), "{md}");
    assert!(md.contains("Database"), "{md}");

    // svg format is a well-formed SVG.
    let svg = render_components(&g, Format::Svg);
    assert!(svg.trim_start().starts_with("<svg"), "{svg}");
    assert!(svg.trim_end().ends_with("</svg>"), "{svg}");
}

#[test]
fn app_notes_as_a_spa_client_has_both_lanes_and_the_rpc_boundary() {
    let root = repo_root();
    let dir = root.join("examples/62-app-notes");
    // Force the wasm-client target (this project is the auto-split INPUT; its
    // default `sky build` is Live, but `--target web:app` builds the Sky.Spa
    // client, which is the shape this diagram charts).
    let g = analyze_components(&root, &dir, None, Some("web:app"))
        .unwrap_or_else(|e| panic!("analyze_components failed: {e}"));

    assert!(g.is_spa, "web:app is a Sky.Spa wasm-client target");
    assert!(
        g.capabilities
            .contains(&project::diagram::Capability::Database),
        "app-notes persists notes through Std.Db; got {:?}",
        g.capabilities.iter().map(|c| c.label()).collect::<Vec<_>>()
    );

    let out = render_components(&g, Format::Puml);
    assert!(out.contains("package \"Client · wasm\""), "{out}");
    assert!(out.contains("package \"Server · effects\""), "{out}");
    assert!(out.contains("interface \"/_rpc\" as rpc"), "{out}");
    assert!(out.contains("rpc --> cap_db"), "{out}");
    // a client module reaches an effect and crosses /_rpc
    assert!(out.contains("--> rpc"), "{out}");

    // svg carries the two package lanes + the /_rpc node.
    let svg = render_components(&g, Format::Svg);
    assert!(svg.trim_start().starts_with("<svg"), "{svg}");
    assert!(svg.contains("Client · wasm") && svg.contains("/_rpc"), "{svg}");
}
