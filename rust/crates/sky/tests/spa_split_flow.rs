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

// Each test in this file generates + `go build`s two projects (backend native +
// frontend wasm). Cargo runs the tests in a binary in parallel by default, so
// three of them at once means up to six concurrent `go build`s — which contend
// and time out under load, an intermittent false red (same class as the
// db_cluster flake). Serialize the build-heavy bodies through one lock: the
// generation + assertions are cheap, but only one test compiles at a time.
static BUILD_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

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

fn todos_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-split-todos/src/Main.sky")
}

fn push_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-push-counter/src/Main.sky")
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
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
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

/// A REAL one-project app: the todos app (Model `{ todos : List Todo, draft }`,
/// Msg `DraftChanged String | Add | Toggle Int | Remove Int`, user-defined
/// `todoCodec`/`todoListCodec`). Exercises the generalised generator:
///   * **Msg-arg Req fields** — `Toggle Int` / `Remove Int` put a typed `id :
///     Int` into the request; the backend reconstructs `update (Toggle p.id) m`;
///     the frontend sends `{ id = id }`.
///   * **Non-primitive field codecs** — `todos : List Todo` wires to the user's
///     `todoListCodec`, which (with `Todo` + `todoCodec`) is COPIED into Shared.
#[test]
fn generalises_to_a_real_app_with_msg_args_and_nonprimitive_codecs() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            todos_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on the todos app");

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Msg-arg Req fields: `Toggle Int` → `ToggleReq { id : Int }` + codec ---
    assert!(
        shared.contains("type alias ToggleReq") && shared.contains("id : Int"),
        "ToggleReq must carry the typed Msg arg `id : Int`:\n{shared}"
    );
    assert!(
        shared.contains("toggleReqCodec")
            && shared.contains("Codec.field \"id\" .id Codec.int"),
        "toggleReqCodec must encode the Msg arg with a real codec"
    );
    // Backend RECONSTRUCTS the Msg with the wire arg, not a bare ctor.
    assert!(
        back.contains("update (Toggle p.id) m"),
        "backend must reconstruct `update (Toggle p.id) m`:\n{back}"
    );
    // Frontend SENDS the Msg arg.
    assert!(
        front.contains("Spa.postJson toggleReqCodec toggleRespCodec \"/_rpc/Toggle\" { id = id } AppliedToggle"),
        "frontend must send the Msg arg to the RPC:\n{front}"
    );

    // --- Non-primitive field codecs: `todos : List Todo` → user codec, copied ---
    assert!(
        shared.contains("todos : List Todo"),
        "the response field keeps its surface type `List Todo` (not `List any`):\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"todos\" .todos todoListCodec"),
        "the todos field must wire to the user's `todoListCodec`, not a placeholder"
    );
    // The user's type + codecs are COPIED into Shared (so both projects share one).
    assert!(
        shared.contains("type alias Todo =") && shared.contains("todoCodec =") && shared.contains("todoListCodec ="),
        "Shared must copy the user's Todo type + todoCodec + todoListCodec"
    );
    // …and therefore NOT be re-declared in either project's Main (duplicate def).
    assert!(
        !back.contains("type alias Todo ="),
        "backend Main must NOT re-declare Todo (it comes from Shared)"
    );
    assert!(
        !front.contains("todoListCodec ="),
        "frontend Main must NOT re-declare todoListCodec (it comes from Shared)"
    );

    // --- SECURITY: no server effect / tainted helper in the client ---
    for needle in ["File.", "loadTodos", "saveTodos", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
    }

    // --- Both build (Go-gated). Backend native, frontend wasm. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "todos backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "todos frontend must build to wasm");
    assert!(out.join("frontend/dist/main.wasm").is_file(), "frontend stages dist/main.wasm");

    let _ = std::fs::remove_dir_all(&out);
}

/// Server→client PUSH (SSE): the shared-counter fixture uses `Cmd.publish` +
/// `Sub.subscribeTopic`, so the generator must turn on push mode — a shared
/// broker, publish-interpreting RPC handlers, and the `GET /_sky/sub` SSE
/// endpoint — while the frontend keeps `subscriptions` verbatim and leaks no
/// server effect. Both projects must build. (docs/skyspa/auto-split.md §16.)
#[test]
fn wires_server_to_client_push_when_the_app_uses_publish_and_subscribe_topic() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            push_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed on the push fixture");

    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();

    // --- Backend push wiring: shared broker + interpret + the SSE endpoint. ---
    assert!(
        back.contains("Ffi.kernel \"Spa_newBroker\"") && back.contains("spaBroker ="),
        "backend must construct the shared broker CAF:\n{back}"
    );
    assert!(
        back.contains("Ffi.kernel \"Spa_interpretPublish\""),
        "backend must wire the Cmd-publish interpreter"
    );
    assert!(
        back.contains("spaInterpretPublish spaBroker cmd"),
        "the RPC handler must feed its returned Cmd to the broker (not discard it):\n{back}"
    );
    assert!(
        back.contains("Server.api \"GET /_sky/sub\" subHandler")
            && back.contains("Ffi.kernel \"Spa_streamTopic\""),
        "backend must mount the SSE push endpoint:\n{back}"
    );

    // --- Frontend keeps the subscription verbatim; no server effect leaks. ---
    assert!(
        front.contains("Sub.subscribeTopic \"count\" GotCount"),
        "frontend must keep `subscriptions` (the EventSource client wires it):\n{front}"
    );
    for needle in ["File.", "saveCount", "Db.", "System.", "Cmd.publish"] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
    }
    // The server branch still routes through the RPC boundary.
    assert!(
        front.contains("Spa.postJson") && front.contains("/_rpc/Increment"),
        "frontend must call the RPC boundary for the Increment server branch"
    );

    // --- Both build (Go-gated). Backend native, frontend wasm. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(backend_build.success(), "push backend must build natively");
    assert!(out.join("backend/sky-out/app").is_file(), "backend produces sky-out/app");

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "push frontend must build to wasm");
    assert!(out.join("frontend/dist/main.wasm").is_file(), "frontend stages dist/main.wasm");

    let _ = std::fs::remove_dir_all(&out);
}
