//! Acceptance test for `sky spa-split` (Phase B3/B4 of the Sky.Spa auto-split).
//!
//! Runs the source-to-source GENERATOR on the crafted skeleton fixture under
//! `tests/fixtures/spa-split` (Model {n,log}; Msg Bump|Persist; server helper
//! `saveN` with a File effect; `Bump` pure, `Persist` inline File effect →
//! read-set {n}, write-set {log}) and asserts:
//!
//!   1. The generator emits the three-tree split (shared / backend / frontend).
//!   2. SECURITY: no server-tainted value/function (`saveN`, `File.`, `Db.`,
//!      `System.`) leaks into the frontend source.
//!   3. Both projects BUILD — the backend natively and the frontend to wasm
//!      (`--target web`). Gated on the Go toolchain via `live_gate`.
//!
//! The build legs need Go + the wasm target, so they gate through `live_gate`;
//! the generation + leak-check legs need only the in-repo stdlib and always run.

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-split/src/Main.sky")
}

fn scratch() -> PathBuf {
    let uniq = format!(
        "sky-spasplit-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    std::env::temp_dir().join(uniq)
}

#[test]
fn generates_a_buildable_split_with_no_server_leak_into_the_client() {
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    // 1. Generate.
    let status = Command::new(SKY)
        .args([
            "spa-split",
            fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    // The three-tree split exists.
    for rel in [
        "shared/Shared.sky",
        "backend/src/Main.sky",
        "backend/src/Shared.sky",
        "backend/sky.toml",
        "frontend/src/Main.sky",
        "frontend/src/Shared.sky",
        "frontend/sky.toml",
    ] {
        assert!(out.join(rel).is_file(), "generator must write {rel}");
    }

    // 2. SECURITY — the frontend source must not contain any server-tainted
    // value or effect kernel. This is the spine of the split.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();
    for needle in ["File.", "saveN", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
    }
    // The backend, by contrast, MUST carry the effect (it runs it server-side).
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(back.contains("saveN"), "backend must keep the server effect saveN");
    assert!(
        back.contains("Server.api \"POST /_rpc/Persist\""),
        "backend must expose the generated RPC endpoint"
    );
    // The frontend must reach the effect through the typed RPC boundary instead.
    assert!(
        front.contains("Spa.postJson") && front.contains("/_rpc/Persist"),
        "frontend must call the RPC boundary for the server branch"
    );

    // 3. Both projects build (Go-gated). Backend native, frontend wasm.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "backend must build natively");
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend build must produce sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "frontend must build to wasm");
    assert!(
        out.join("frontend/dist/main.wasm").is_file(),
        "frontend build must stage dist/main.wasm"
    );
    assert!(
        out.join("frontend/dist/index.html").is_file(),
        "frontend build must stage dist/index.html"
    );

    let _ = std::fs::remove_dir_all(&out);
}
