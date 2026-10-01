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

fn multimodule_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-multimodule/src/Main.sky")
}

fn mixed_codec_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-mixed-codec/src/Main.sky")
}

fn error_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-error-wire/src/Main.sky")
}

fn msg_with_wire_types_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-msg-with-wire-types/src/Main.sky")
}

fn union_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-union-wire/src/Main.sky")
}

fn auto_record_codec_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-auto-record-codec/src/Main.sky")
}

fn bare_adt_wire_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-bare-adt-wire/src/Main.sky")
}

fn ssr_multimodule_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-multimodule")
}

fn clientonly_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-clientonly/src/Main.sky")
}

fn clientnative_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-clientnative/src/Main.sky")
}

fn explicit_rpc_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-split-explicit-rpc/src/Main.sky")
}

fn server_chain_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-server-chain/src/Main.sky")
}

fn guard_wrapper_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-guard-wrapper/src/Main.sky")
}

fn client_result_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-client-result/src/Main.sky")
}

fn guarded_chain_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-guarded-chain/src/Main.sky")
}

fn multihop_chain_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-multihop-chain/src/Main.sky")
}

fn derived_read_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-derived-read/src/Main.sky")
}

/// The wasm bundle is content-hashed (main.<hash>.wasm), so check for that shape
/// rather than a fixed `main.wasm`.
fn dist_has_wasm(dist: &std::path::Path) -> bool {
    std::fs::read_dir(dist)
        .map(|rd| {
            rd.filter_map(|e| e.ok()).any(|e| {
                let n = e.file_name().to_string_lossy().into_owned();
                n.starts_with("main.") && n.ends_with(".wasm")
            })
        })
        .unwrap_or(false)
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

/// Recursively copy `src` → `dst`, skipping generated build dirs (so a committed
/// fixture's stray local `.skyapp`/`sky-out`/`dist` never rides along). Used by
/// the `--target web:app` tests, which build INTO the project dir and so must
/// run on a scratch copy, never the checked-in tree.
fn copy_tree(src: &std::path::Path, dst: &std::path::Path) {
    std::fs::create_dir_all(dst).unwrap();
    for entry in std::fs::read_dir(src).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name();
        let n = name.to_string_lossy();
        if matches!(
            n.as_ref(),
            ".skyapp" | ".split" | "sky-out" | "sky-out-rust" | ".skycache" | ".skydeps" | "dist"
        ) {
            continue;
        }
        let from = entry.path();
        let to = dst.join(&name);
        if from.is_dir() {
            copy_tree(&from, &to);
        } else {
            std::fs::copy(&from, &to).unwrap();
        }
    }
}

fn web_config_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-web-config")
}

/// Write a minimal `sky.toml` + a Std.App `App.app` `src/Main.sky` from `main_sky`
/// into a fresh scratch project dir. Used by the BUG-2/BUG-3 `--target web:app`
/// synthesis tests, which need a specific `App.app` shape rather than a committed
/// fixture.
fn scratch_std_app(name: &str, main_sky: &str) -> PathBuf {
    let dir = scratch();
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"{name}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), main_sky).unwrap();
    dir
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
    assert!(
        back.contains("saveN"),
        "backend must keep the server effect saveN"
    );
    assert!(
        back.contains("Server.rpc \"POST /_rpc/Persist\""),
        "backend must expose the generated RPC endpoint"
    );
    // The frontend must reach the effect through the typed RPC boundary instead.
    assert!(
        front.contains("Spa.rpc") && front.contains("/_rpc/Persist"),
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
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );
    assert!(
        out.join("frontend/dist/index.html").is_file(),
        "frontend build must stage dist/index.html"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Read-set completeness for a model-DERIVED value threaded into a helper.
///
/// Regression for a silent-wrong-answer bug found in shop-app prod: a SERVER
/// branch `SetRegion` computed shipping through
/// `recomputeTotals (clear { model | region = r })`, where `recomputeTotals` reads
/// `model.basket`. That read of `basket` is reachable ONLY through the helper
/// chain (never a direct `model.basket` in the branch), and the read-set analysis
/// DROPPED it — so the generated RPC request carried only the Msg arg, the server
/// rebuilt the model from a fresh `init ()` (empty basket), and shipping came back
/// `0` for every region (`shippingForBasket []` is 0). No panic, just a wrong
/// value — the class Sky must never produce.
///
/// The fixture mirrors it minimally: `SetScale k` runs
/// `recompute (clear { model | scale = k })`, and `recompute` reads `m.n` while
/// doing a pure-typed server read (`System.getenvOr`). The fix
/// (`spa_partition::collect_reads`, the `model_write_shape` arm) over-approximates
/// the read-set to the WHOLE model whenever a model-derived value flows into a
/// callee, so `SetScaleReq` carries every model field — `n` included — and the
/// handler reconstructs the client's real model instead of `init ()`.
#[test]
fn derived_model_threaded_into_helper_keeps_the_read_in_the_request() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            derived_read_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    // Three branches read `n` ONLY through a helper, via three different shapes the
    // read-set analysis must all fail CLOSED on (send the whole model):
    //   * SetScale    — model-derived value passed DIRECTLY to the reading helper
    //   * SetScaleLet — the model-derived value is `let`-bound first (alias)
    //   * SetScaleVia — the model flows through a 2-arg helper (`stamp k model`)
    // For each, the generated `<Msg>Req` must carry the whole model (n/scale/log),
    // or the server reruns the branch against a fresh `init ()` → wrong answer.
    for req in ["SetScaleReq", "SetScaleLetReq", "SetScaleViaReq"] {
        assert!(
            shared.contains(&format!("type alias {req}")),
            "{req} must exist (branch must be a SERVER RPC):\n{shared}"
        );
        let start = shared.find(&format!("type alias {req}")).unwrap();
        let block = &shared[start..];
        let end = block.find("\n\n").unwrap_or(block.len());
        let block = &block[..end];
        for field in ["n :", "scale :", "log :"] {
            assert!(
                block.contains(field),
                "{req} must carry `{field}` (whole-model read-set — the helper-threaded \
                 read of `n` must not be dropped):\n{block}"
            );
        }
    }

    // And the backend handler must reconstruct the model from the wire payload
    // (`m = { … = p.… }`), NOT from a fresh `init ()`, so the branch runs against
    // the client's real state.
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let hstart = back
        .find("setScaleHandler")
        .expect("setScaleHandler present");
    let hblock = &back[hstart..];
    let hend = hblock.find("Task.succeed").unwrap_or(hblock.len());
    let hblock = &hblock[..hend];
    assert!(
        hblock.contains("n = p.n"),
        "setScaleHandler must rebuild the model from the payload (n = p.n), not \
         init ():\n{hblock}"
    );

    // Msg-arg / Model-field NAME COLLISION: `SetScaleArg scale` binds an arg named
    // `scale`, which the Model also has. Under the whole-model request the arg must
    // ride a RENAMED field (`spaMsgArg_scale`) so the handler uses the NEW value,
    // not the old model field. (The shop-app `SetRegion` "switching location
    // does nothing" bug: the arg was dropped as a duplicate.)
    assert!(
        shared.contains("spaMsgArg_scale"),
        "SetScaleArgReq must carry the collision-renamed Msg arg `spaMsgArg_scale` \
         alongside the model's `scale` field:\n{shared}"
    );
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("spaMsgArg_scale = scale"),
        "the dispatch must send the NEW arg value under the renamed field \
         (spaMsgArg_scale = scale):\n(SetScaleArg dispatch missing)"
    );
    assert!(
        back.contains("update (SetScaleArg p.spaMsgArg_scale)"),
        "the handler must construct the Msg from the renamed arg field \
         (update (SetScaleArg p.spaMsgArg_scale)), not p.scale (the old model \
         field):\n(SetScaleArg handler wrong)"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A client-ONLY app (every `update` branch pure, no effect kernels) still
/// spa-splits into a buildable backend. Regression for the empty-route-list bug:
/// with no RPC/push routes the generated `Server.listen` list opened
/// `[ , Server.static …]` — a leading comma the parser rejected — so `sky build`
/// on the generated backend failed with a PARSE ERROR. The static-asset route is
/// now a normal list entry, so the `[` always has a first element to attach to.
#[test]
fn client_only_app_generates_a_buildable_static_only_backend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientonly_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on a client-only app"
    );

    // The backend has NO RPC routes (nothing was server-tainted) but MUST still
    // serve static assets — and the generated list must be well-formed.
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("Server.static \"/\" \"../frontend/dist\""),
        "backend must still serve the frontend's static assets"
    );
    assert!(
        !back.contains("/_rpc/"),
        "a client-only app must generate no RPC endpoints"
    );
    // The exact defect: a list opening with a leading comma.
    assert!(
        !back.contains("[\n        , Server.static") && !back.contains("[ , Server.static"),
        "backend Server.listen list must not open with a leading comma"
    );

    // The real proof: the generated backend BUILDS (it used to fail to parse).
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "client-only backend must build (regression: leading-comma parse error)"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend build must produce sky-out/app"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// `App.withClientCrypto` on a Std.App entry, built for `--target web:app`: the
/// synthesis carries it to `Spa.withClientCrypto`, the key operations stay in the
/// wasm client (no RPC), the SSR first paint and the saved model write the
/// device's `Maybe Noise.Handshake` field as `Nothing`, and the backend and the
/// wasm frontend both build.
#[test]
fn client_crypto_std_app_builds_and_leaves_keys_out_of_the_first_paint() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let src =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-client-crypto");
    let dir = scratch();
    let _ = std::fs::remove_dir_all(&dir);
    let cp = Command::new("cp")
        .arg("-R")
        .arg(&src)
        .arg(&dir)
        .status()
        .expect("cp -R fixture");
    assert!(cp.success());
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("sky build --target web:app");
    let log = format!(
        "{}\n{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let split = dir.join(".skyapp/web-app/.split");
    let read = |p: &str| std::fs::read_to_string(split.join(p)).unwrap_or_default();
    let back = read("backend/src/Main.sky");
    let front = read("frontend/src/Main.sky");
    let backend_ok = split.join("backend/sky-out/app").exists();
    let wasm_ok = dist_has_wasm(&split.join("frontend/dist"))
        || split.join("frontend/sky-out/main.wasm").exists();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(out.status.success(), "the build must succeed:\n{log}");
    assert!(
        backend_ok && wasm_ok,
        "backend + wasm frontend must be built:\n{log}"
    );
    assert!(
        front.contains("Spa.withClientCrypto"),
        "the synthesis must carry App.withClientCrypto:\n{front}"
    );
    assert!(
        front.contains("Noise.initiatorWith") && front.contains("Kx.generate"),
        "the key operations must stay in the wasm client:\n{front}"
    );
    assert!(
        !back.contains("POST /_rpc/Connect") && !back.contains("POST /_rpc/GotHandshake"),
        "the backend must have no RPC for a key operation:\n{back}"
    );
    assert!(
        back.contains("spaModelToJson_ ({ m_ | handshake = Nothing })")
            && back.contains("Ffi.kernel \"Spa_modelToJson\""),
        "the SSR first paint must write the device handshake as Nothing (through \
         `Spa_modelToJson`: `Codec.auto`'s Encodable bound refuses a key type):\n{back}"
    );
    assert!(
        front.contains("({ m_ | handshake = Nothing })"),
        "the saved model must write the device handshake as Nothing:\n{front}"
    );
    assert!(
        !log.contains("Codec.auto` cannot round-trip"),
        "a cleared device-only field must not raise the SSR-embed warning:\n{log}"
    );
}

/// `withClientCrypto` with a device-only key field AND a server branch, in an
/// app with no type annotations (`tests/fixtures/spa-client-crypto-ssr`). The
/// backend's first-paint encoder derived `Codec.auto` from `init`'s value,
/// where `hs = Nothing` is a free `Maybe a` because `update` never sets it, so
/// the backend failed with [E2009] "cannot derive an element codec for this
/// `Maybe` field". With no server branch there is no first paint to encode,
/// which is why `client_crypto_std_app_builds_and_leaves_keys_out_of_the_first_paint`
/// passed. The encoder is now a top-level function annotated with the declared
/// `Model`, the key field is found from that alias (the inferred model did
/// not list it), and the running backend's first paint carries `"hs":null`.
#[test]
fn a_device_key_field_with_a_server_branch_builds_and_paints_nothing() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-client-crypto-ssr"),
        &proj,
    );
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let split = proj.join(".skyapp/web-app/.split");
    let back = std::fs::read_to_string(split.join("backend/src/Main.sky")).unwrap_or_default();
    let front = std::fs::read_to_string(split.join("frontend/src/Main.sky")).unwrap_or_default();
    assert!(
        back.contains("spaSsrModelJson_ : Model -> String")
            && back.contains("spaModelToJson_ ({ m_ | hs = Nothing })"),
        "the first-paint encoder is pinned to Model and clears the key:\n{back}\n{log}"
    );
    assert!(
        front.contains("spaModelFromJson_ spaModelBlank_ jsonStr_")
            && front.contains("|> Result.map (\\m_ -> { m_ | hs = Nothing })")
            && front.contains("spaModelToJson_ ({ m_ | hs = Nothing })"),
        "the client clears the key after a decode and in the saved model:\n{front}"
    );
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let port = free_port();
    let back_dir = split.join("backend");
    let log_path = back_dir.join("server.log");
    let child = Killed(
        Command::new(back_dir.join("sky-out/app"))
            .current_dir(&back_dir)
            .env("PORT", port.to_string())
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&log_path).unwrap())
            .stderr(std::fs::File::create(back_dir.join("server.err")).unwrap())
            .spawn()
            .expect("start the backend"),
    );
    assert!(
        wait_for_spa_backend(&log_path, 120),
        "the backend did not start"
    );
    let page = curl_body_p(port, "/").unwrap_or_default();
    drop(child);
    let _ = std::fs::remove_dir_all(&proj);
    assert!(
        page.contains(r#"{"hs":null,"note":""}"#),
        "the first paint writes the device key as null:\n{page}"
    );
}

/// A `Std.Native.*` effect is a CLIENT effect: it must stay in the wasm frontend,
/// never become a server RPC. Native capabilities (`clipboardWrite`, `share`, …)
/// reach a browser/webview-only platform API whose `//go:build !js` counterpart is
/// an `Err` stub, so routing them server-side — the fail-closed default for an
/// unknown effect — would make every call fail (this fixture's kernels once
/// generated `/_rpc/Copy` + `/_rpc/Share`, and the round-trip hit the Err stubs).
/// `classify_kernel` now maps the `Native_` family to `ClientEffect`, so the
/// frontend keeps the kernel call and the backend generates NO RPC for it.
#[test]
fn native_effects_stay_client_side_not_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientnative_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on a client-native app"
    );

    // The frontend KEEPS the native kernel calls in its `update` — they run in the
    // wasm client. (Both are inside a `Cmd.perform (Native.… ) Done`.)
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Native.clipboardWrite") && front.contains("Native.share"),
        "frontend must keep the Std.Native kernel calls (they run client-side)"
    );

    // The definitive proof it was NOT server-routed: the backend generates NO RPC
    // ENDPOINT for the native effects (a server-routed effect emits
    // `Server.api "POST /_rpc/<Msg>"`). It must still serve the static assets.
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        !back.contains("POST /_rpc/"),
        "a client-native effect must NOT generate an RPC endpoint on the backend"
    );
    assert!(
        back.contains("Server.static \"/\" \"../frontend/dist\""),
        "backend must still serve the frontend's static assets"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// The generated backend's DEFAULT port must match the port the generated
/// desktop / iOS / Android shells load, or a user who starts the backend bare
/// (`./app`, no PORT) and launches a shell lands on a dead port. Regression: the
/// backend defaulted to 8971 while every shell baked 8951, so the mobile shells
/// could not reach the backend on its own default. Both sides now default 8951.
#[test]
fn backend_default_port_matches_the_generated_shell() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            clientnative_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("getenvOr \"PORT\" \"8951\""),
        "backend serverPort must default to 8951 (the shells' port), got:\n{}",
        back.lines()
            .filter(|l| l.contains("PORT"))
            .collect::<Vec<_>>()
            .join("\n")
    );

    // The shell generator must default to the SAME port, or the two drift apart
    // again. The shells' default address (`app_url.rs`, used by the iOS /
    // Android / desktop templates when neither `App.withAppUrl` nor
    // `SKY_APP_URL` is set) is built from `DEFAULT_PORT`. Pin them together.
    let app_url_rs = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src/app_url.rs"),
    )
    .unwrap();
    assert!(
        app_url_rs.contains("pub const DEFAULT_PORT: u16 = 8951;"),
        "the generated shells (app_url.rs) must default to the same 8951 the backend serves"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// An app that imports an EXTERNAL Sky library (a `.skydeps/` package) must
/// survive spa-split: the generated frontend/backend need the `[dependencies]`
/// section AND a copy of the `.skydeps/` tree, or they can't rebuild the import.
/// Regression: before this, spa-split emitted a fixed manifest and copied only the
/// project's own src/, so a third-party import analysed fine but the generated
/// projects failed to resolve it. `.skydeps/` is gitignored, so the lib is
/// constructed here rather than checked in.
#[test]
fn spa_split_carries_external_sky_dependencies() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    let slug = "github.com_test_sky-greet";

    // The fetched library (as `sky add --sky` would leave it under .skydeps/).
    let lib_src = proj.join(".skydeps").join(slug).join("src/Ext");
    std::fs::create_dir_all(&lib_src).unwrap();
    std::fs::write(
        lib_src.join("Greet.sky"),
        "module Ext.Greet exposing (greet)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\
         greet : String -> String\ngreet name =\n    \"Hi from the lib, \" ++ name\n",
    )
    .unwrap();

    // The consumer project declaring the dependency + importing it.
    std::fs::write(
        proj.join("sky.toml"),
        "name = \"extdep\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n\n[dependencies]\n\"github.com/test/sky-greet\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::create_dir_all(proj.join("src")).unwrap();
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.Spa as Spa
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)
import Ext.Greet exposing (greet)


type alias Model =
    { who : String }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { who = "Sky" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text (greet model.who))


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task Error ()
main =
    Spa.app
        (Spa.config
            { init = init, update = update, view = view, subscriptions = subscriptions }
        )
"#;
    std::fs::write(proj.join("src/Main.sky"), main_sky).unwrap();

    let out = proj.join("dist");
    let status = Command::new(SKY)
        .args([
            "spa-split",
            proj.join("src/Main.sky").to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "spa-split should succeed on an app with an external dep"
    );

    // The generated frontend must declare the dep AND carry its .skydeps source.
    let front_toml = std::fs::read_to_string(out.join("frontend/sky.toml")).unwrap();
    assert!(
        front_toml.contains("[dependencies]") && front_toml.contains("github.com/test/sky-greet"),
        "generated frontend manifest must carry [dependencies], got:\n{front_toml}"
    );
    assert!(
        out.join("frontend/.skydeps")
            .join(slug)
            .join("src/Ext/Greet.sky")
            .is_file(),
        "generated frontend must carry the .skydeps source tree"
    );
    // And the same for the backend.
    let back_toml = std::fs::read_to_string(out.join("backend/sky.toml")).unwrap();
    assert!(
        back_toml.contains("github.com/test/sky-greet"),
        "generated backend manifest must carry [dependencies] too"
    );

    // The real proof: the generated frontend BUILDS (resolves the import).
    if required(Need::Go, have_go()) {
        let build = Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(out.join("frontend"))
            .status()
            .expect("run sky build (frontend)");
        assert!(
            build.success(),
            "generated frontend must build with the external import resolved"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
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
    assert!(
        status.success(),
        "sky spa-split should succeed on the todos app"
    );

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
        shared.contains("toggleReqCodec") && shared.contains("Codec.field \"id\" .id Codec.int"),
        "toggleReqCodec must encode the Msg arg with a real codec"
    );
    // Backend RECONSTRUCTS the Msg with the wire arg, not a bare ctor.
    assert!(
        back.contains("update (Toggle p.id) m"),
        "backend must reconstruct `update (Toggle p.id) m`:\n{back}"
    );
    // Frontend SENDS the Msg arg.
    assert!(
        front.contains(
            "Spa.rpcHold toggleReqCodec toggleRespCodec \"/_rpc/Toggle\" { id = id } AppliedToggle"
        ),
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
        shared.contains("type alias Todo =")
            && shared.contains("todoCodec =")
            && shared.contains("todoListCodec ="),
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
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "todos frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A MULTI-MODULE app (docs/skyspa/auto-split.md §17): the todos app split
/// across `Main` (Model/Msg/TEA loop) + a PURE `Domain` (Todo type + codecs) +
/// an EFFECTFUL `Store` (File load/save). The generator must:
///   * copy the pure `Domain` module into BOTH trees (frontend + backend);
///   * route the server-tainted `Store` module to the BACKEND ONLY, and NEVER
///     emit it — or an import of it — into the wasm frontend (the security spine);
///   * have `Shared` reference the sibling codec (`todoListCodec`) by IMPORTING
///     `Domain` rather than re-copying it;
///   * still wire the RPCs (Msg-arg Req fields, non-primitive codecs) as it does
///     for a single-module app.
#[test]
fn splits_a_multi_module_app_routing_pure_and_effectful_modules() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            multimodule_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on a multi-module app (no longer refused)"
    );

    // --- Module routing: pure `Domain` → both trees, effectful `Store` → backend only. ---
    assert!(
        out.join("backend/src/Domain.sky").is_file()
            && out.join("frontend/src/Domain.sky").is_file(),
        "the PURE Domain module must be copied into BOTH trees"
    );
    assert!(
        out.join("backend/src/Store.sky").is_file(),
        "the effectful Store module must be present in the backend"
    );
    assert!(
        !out.join("frontend/src/Store.sky").exists(),
        "SECURITY LEAK: the server-tainted Store module must NOT be in the frontend"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Shared IMPORTS the sibling codec's module (Domain), does NOT re-copy it. ---
    assert!(
        shared.contains("import Domain"),
        "Shared must import the sibling Domain module for the codec/type:\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"todos\" .todos todoListCodec"),
        "the todos field must wire to the sibling `todoListCodec`:\n{shared}"
    );
    assert!(
        !shared.contains("type alias Todo ="),
        "Shared must NOT re-declare Todo — it comes from the imported Domain module"
    );

    // --- Frontend must NOT import the backend-only Store module. ---
    assert!(
        !front.contains("import Store"),
        "SECURITY LEAK: frontend/src/Main.sky imports the backend-only Store module:\n{front}"
    );

    // --- SECURITY: no server effect / tainted helper / effectful module in the client. ---
    for needle in [
        "File.",
        "loadTodos",
        "saveTodos",
        "Store.",
        "Db.",
        "System.",
    ] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`"
        );
        assert!(
            !std::fs::read_to_string(out.join("frontend/src/Domain.sky"))
                .unwrap()
                .contains(needle),
            "SECURITY LEAK: frontend/src/Domain.sky contains `{needle}`"
        );
    }

    // --- The backend keeps the effects + reconstructs the Msg-arg RPCs. ---
    assert!(
        std::fs::read_to_string(out.join("backend/src/Store.sky"))
            .unwrap()
            .contains("File."),
        "backend Store must keep the File effect (it runs it server-side)"
    );
    assert!(
        back.contains("update (Toggle p.id) m"),
        "backend must reconstruct `update (Toggle p.id) m`:\n{back}"
    );
    assert!(
        front.contains(
            "Spa.rpcHold toggleReqCodec toggleRespCodec \"/_rpc/Toggle\" { id = id } AppliedToggle"
        ),
        "frontend must send the Msg arg to the RPC:\n{front}"
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
    assert!(
        backend_build.success(),
        "multi-module backend must build natively"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "multi-module frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

// The `Msg` union, the wire record `Item` and the `Model` all live in ONE module
// (`Types`), and sibling modules read `Types` via `exposing (..)`. Injecting the
// `Applied<Msg>` RPC variants into `Types` makes it import `Shared`, so `Shared`
// must NOT import `Types` back. The split resolves the would-be cycle (E1010) by
// giving `Shared` its OWN copy of `Item` + `itemCodec`; because a record
// `type alias` is structural, the model field (`Types.Item`) and the RPC response
// field (`Shared.Item`) unify, so the round-trip is sound. Regression for the
// deleted "Move `Msg` into its own module" refusal.
#[test]
fn splits_a_module_that_co_locates_msg_with_its_wire_types() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            msg_with_wire_types_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split MUST succeed when `Msg` co-locates with its wire types (the E1010 refusal is deleted)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_types = std::fs::read_to_string(out.join("frontend/src/Types.sky")).unwrap();
    let back_types = std::fs::read_to_string(out.join("backend/src/Types.sky")).unwrap();
    let front_main = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back_main = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared OWNS the wire type + codec, and never imports the Msg module. ---
    assert!(
        shared.contains("type alias Item =") && shared.contains("itemCodec"),
        "Shared must OWN a copy of the co-located `Item` + `itemCodec`:\n{shared}"
    );
    assert!(
        !shared.contains("import Types"),
        "NO CYCLE: Shared must NOT import the Msg module `Types` (Types imports Shared):\n{shared}"
    );

    // --- The Msg module imports Shared for the injected `Applied<Msg>` variants,
    //     and still resolves `Item` for its `exposing (..)` consumers. ---
    assert!(
        front_types.contains("import Shared") && front_types.contains("AppliedSave"),
        "frontend Types must import Shared and carry the injected Applied<Msg> variant:\n{front_types}"
    );
    assert!(
        front_types.contains("type alias Item ="),
        "frontend Types must keep `Item` so its `exposing (..)` consumers still resolve it:\n{front_types}"
    );
    // Neither tree may pull the wire type from BOTH Types and Shared unqualified
    // (that would be an ambiguous double-import). The entry imports Shared with an
    // explicit exposing list that excludes the copied names it already reads from
    // `Types exposing (..)`.
    assert!(
        !front_main.contains("import Shared exposing (..)"),
        "frontend Main must import Shared with an explicit list (not `..`) to avoid an ambiguous `Item`:\n{front_main}"
    );
    assert!(
        !back_main.contains("import Shared exposing (..)"),
        "backend Main must import Shared with an explicit list (not `..`) to avoid an ambiguous `Item`:\n{back_main}"
    );

    // --- SECURITY: the File effect never reaches the frontend. ---
    for needle in ["File.", "loadItems", "saveItems"] {
        assert!(
            !front_main.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front_main}"
        );
    }

    // backend Types stays a normal module (the wire type lives there for the
    // native server too).
    assert!(
        back_types.contains("type alias Item ="),
        "backend Types keeps `Item`:\n{back_types}"
    );

    // --- Both build (Go-gated). This is the type-identity proof: the model field
    //     `items : List Item` (Types.Item) and the RPC response field
    //     `items : List Item` (Shared.Item) must reconcile, or `go build` fails. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "backend must build natively (proves Types.Item unifies with Shared.Item on the RPC fold)"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "frontend must build to wasm (proves the client-side fold unifies too)"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

// A NOMINAL `union` (`Page`) is declared in the `Msg` module (`Types`) and rides
// the RPC wire as a `Model` field (`page : Page`); a server branch writes it, and
// a view sibling reads `Types exposing (..)` and references it. A union cannot be
// duplicated copy-and-leave (two same-named unions never unify), so `Shared` must
// OWN the single definition: it declares `Page` ONCE, `Types` strips its `Page`
// declaration, and every consumer (the entry, the Msg module, the view sibling)
// imports `Page(..)` from `Shared`. Regression for the deleted "cannot auto-split:
// the wire needs the union type `Page`" refusal — the case must now SUCCEED and
// both trees build (the single-definition identity proof).
#[test]
fn splits_a_module_whose_wire_rides_a_nominal_union_by_owning_it_in_shared() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            union_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split MUST succeed when a wire-riding NOMINAL union lives in the Msg module (the union refusal is deleted; Shared OWNS the union)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_types = std::fs::read_to_string(out.join("frontend/src/Types.sky")).unwrap();
    let back_types = std::fs::read_to_string(out.join("backend/src/Types.sky")).unwrap();
    let front_view = std::fs::read_to_string(out.join("frontend/src/View.sky")).unwrap();
    let back_view = std::fs::read_to_string(out.join("backend/src/View.sky")).unwrap();
    let front_main = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back_main = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared declares `Page` ONCE (the single definition) and never imports
    //     the Msg module `Types`. ---
    assert!(
        shared.contains("type Page")
            && shared.contains("HomePage")
            && shared.contains("AccountPage"),
        "Shared must declare the `Page` union (its single definition):\n{shared}"
    );
    assert!(
        !shared.contains("import Types"),
        "NO CYCLE: Shared must NOT import the Msg module `Types`:\n{shared}"
    );

    // --- NO module declares `Page` twice: the Msg module strips its declaration,
    //     and every reference resolves to Shared's copy. ---
    for (label, src) in [
        ("frontend Types", &front_types),
        ("backend Types", &back_types),
        ("frontend View", &front_view),
        ("backend View", &back_view),
        ("frontend Main", &front_main),
        ("backend Main", &back_main),
    ] {
        assert!(
            !src.contains("type Page"),
            "{label} must NOT re-declare `Page` (Shared owns the single definition):\n{src}"
        );
    }

    // --- The Msg module imports `Page(..)` from Shared (its `Model` field + the
    //     `pathOf` helper reference it), and keeps its `Msg` union + `Model`. ---
    for (label, src) in [
        ("frontend Types", &front_types),
        ("backend Types", &back_types),
    ] {
        assert!(
            src.contains("import Shared exposing (") && src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared:\n{src}"
        );
        assert!(
            src.contains("type alias Model ="),
            "{label} must keep `Model` (a structural record):\n{src}"
        );
    }
    assert!(
        front_types.contains("AppliedDoSignIn"),
        "frontend Types must carry the injected Applied<Msg> variant:\n{front_types}"
    );

    // --- The view sibling imports `Page(..)` from Shared in BOTH trees, because
    //     `Types exposing (..)` no longer surfaces the moved union. ---
    for (label, src) in [("frontend View", &front_view), ("backend View", &back_view)] {
        assert!(
            src.contains("import Shared exposing (") && src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared (its `case`/annotation reference it):\n{src}"
        );
    }

    // --- The entry imports `Page(..)` from Shared (it writes `Page` ctors), with
    //     an explicit exposing list (not `..`). ---
    for (label, src) in [("frontend Main", &front_main), ("backend Main", &back_main)] {
        assert!(
            !src.contains("import Shared exposing (..)"),
            "{label} must import Shared with an explicit list, not `..`:\n{src}"
        );
        assert!(
            src.contains("Page(..)"),
            "{label} must import `Page(..)` from Shared:\n{src}"
        );
    }

    // --- SECURITY: the File effect never reaches the frontend. ---
    for needle in ["File.", "writeFile"] {
        assert!(
            !front_main.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front_main}"
        );
    }

    // --- Both build (Go-gated). This is the single-definition identity proof: the
    //     model field `page : Page` (now Shared.Page) and the RPC response field
    //     `page : Page` (Shared.Page) resolve to the SAME type, or `go build`
    //     fails. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "backend must build natively (proves every `Page` reference resolves to Shared's single definition)"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "frontend must build to wasm (proves the client-side fold resolves to Shared.Page too)"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A `Codec <T>` binding that lives in a MIXED module (one that ALSO owns a
/// server effect) is itself PURE, and the wire needs it. The generator must
/// COPY that codec + its type into `Shared` — NEVER import the tainted module
/// into `Shared` (that would drag the File effect into the wasm frontend, which
/// `Shared` compiles into). Before this fix the codec registry scanned only the
/// entry + PURE sibling modules, so a `List Item` field whose `itemCodec` lived
/// beside a `File` effect fell through to `no codec for a field of type any`.
#[test]
fn splits_a_mixed_module_codec_by_copying_it_into_shared() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            mixed_codec_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed by COPYING the mixed-module codec into Shared (was: `no codec for a field of type any`)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // --- Shared COPIES the mixed-module codec + its type (never imports Data). ---
    assert!(
        shared.contains("itemCodec") && shared.contains("type alias Item ="),
        "Shared must COPY the pure `itemCodec` + `Item` type from the mixed module:\n{shared}"
    );
    assert!(
        !shared.contains("import Data"),
        "SECURITY LEAK: Shared must NOT import the server-tainted `Data` module:\n{shared}"
    );
    assert!(
        shared.contains("Codec.field \"items\" .items (Codec.list itemCodec)"),
        "the items field must wire to `Codec.list itemCodec`:\n{shared}"
    );

    // --- The server File effect must be routed backend-only; Data stays backend. ---
    assert!(
        out.join("backend/src/Data.sky").is_file(),
        "the mixed Data module must be present in the backend (it runs the File effect)"
    );

    // --- SECURITY: no server effect / tainted helper leaks into the client. ---
    for needle in ["File.", "loadItems", "saveItems", "Db.", "System."] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front}"
        );
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`:\n{front_shared}"
        );
    }
    assert!(
        !front.contains("import Data"),
        "SECURITY LEAK: frontend/src/Main.sky imports the backend-only `Data` module:\n{front}"
    );
    assert!(
        !out.join("frontend/src/Data.sky").exists(),
        "SECURITY LEAK: the server-tainted `Data` module must NOT be in the frontend"
    );

    // --- The backend keeps the effect + carries the codec via Shared (no clash). ---
    assert!(
        std::fs::read_to_string(out.join("backend/src/Data.sky"))
            .unwrap()
            .contains("File."),
        "backend Data must keep the File effect (it runs it server-side)"
    );
    assert!(
        back.contains("import Shared exposing (")
            && back.contains("Item")
            && back.contains("itemCodec"),
        "backend Main imports Shared for the copied codec/type (explicit exposing list):\n{back}"
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
    assert!(
        backend_build.success(),
        "mixed-codec backend must build natively"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "mixed-codec frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// An `Error` value MAY cross the Sky.Spa RPC wire by DEFAULT — it must
/// serialise, not be refused. A server branch whose payload is
/// `Result Error String` (the `Cmd.perform … Sent` shape, e.g. shop-app's
/// `EmailSent (Result Error String)`) must wire through the stdlib
/// `Codec.result` + `Codec.error` with NO hand-written app codec. Before this fix
/// the resolver had no `Result`/`Error` arm and refused with `no codec for a
/// field of type Result Error String`. The two-level-error concern is satisfied
/// off-wire: logging stays a server effect (`Std.Log`) and an app controls
/// handling via `App.withRpcError` — so the wire itself must round-trip.
#[test]
fn wires_a_result_error_payload_through_the_stdlib_error_codec() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            error_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must SUCCEED wiring a `Result Error String` payload (was: `no codec for a field of type Result Error String`)"
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let front_shared = std::fs::read_to_string(out.join("frontend/src/Shared.sky")).unwrap();

    // --- Shared wires the field via the stdlib Result + Error codecs. ---
    assert!(
        shared.contains("outcome : Result Error String"),
        "the RPC field keeps its `Result Error String` surface type:\n{shared}"
    );
    assert!(
        shared.contains("(Codec.result Codec.error Codec.string)"),
        "the `Result Error String` field must wire to `Codec.result Codec.error Codec.string`:\n{shared}"
    );
    // --- No hand-written codec was needed / copied (the stdlib provides it). ---
    assert!(
        !shared.contains("errorCodec") && !shared.contains("resultCodec"),
        "no app-level Error/Result codec should be copied — Shared references the stdlib `Codec.error`/`Codec.result`:\n{shared}"
    );
    // --- The Secret/Set fail-closed refusal is untouched (no false refusal here). ---
    assert!(
        !shared.contains("Secret") && !shared.contains("Set "),
        "the error-wire fixture carries no Secret/Set — none should appear:\n{shared}"
    );

    // --- SECURITY: no server effect leaks into the client Shared. ---
    for needle in ["File.", "audit.txt", "Db.", "System."] {
        assert!(
            !front_shared.contains(needle),
            "SECURITY LEAK: frontend/src/Shared.sky contains `{needle}`:\n{front_shared}"
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
    assert!(
        backend_build.success(),
        "error-wire backend must build natively"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "error-wire frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// A plain RECORD payload crosses the Sky.Spa wire with NO hand-written codec:
/// the split AUTO-DERIVES one (§14 #2, option B). The fixture reproduces
/// shop-app's `OrderFinalized (Result Error CheckoutResult)` — a
/// server-result Msg carries a `Result Error Receipt` where `Receipt` is a plain
/// record with MIXED fields (String, Int, `Maybe`, `List`, and a NESTED record
/// `Address`). Before the fix the record solved to a structural row and the split
/// failed with `no codec for a field of type any`. The fix synthesises a
/// nominally-annotated blank + `Codec.auto` and copies `Receipt` + `Address` into
/// `Shared`; the `Codec.auto` codec ROUND-TRIPS (the SSR model embed relies on
/// the same property), which the build legs prove end-to-end.
#[test]
fn auto_derives_a_record_codec_for_a_plain_record_wire_field() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            auto_record_codec_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        output.status.success(),
        "sky spa-split must SUCCEED by AUTO-DERIVING a `Codec.auto` codec for the plain record `Receipt` (was: `no codec for a field of type any`), got:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );

    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();

    // --- The nominally-annotated blank + auto codec are synthesised. ---
    assert!(
        shared.contains("blankReceipt_ : Receipt"),
        "Shared must synthesise a NOMINALLY-annotated blank `blankReceipt_ : Receipt` (an inline unannotated literal erases element types):\n{shared}"
    );
    assert!(
        shared.contains("autoReceiptCodec_ =") && shared.contains("Codec.auto blankReceipt_"),
        "Shared must derive `autoReceiptCodec_ = Codec.auto blankReceipt_`:\n{shared}"
    );
    // --- The record (and its NESTED record) are copied into Shared. ---
    assert!(
        shared.contains("type alias Receipt =") && shared.contains("type alias Address ="),
        "Shared must COPY `Receipt` AND the nested `Address` it references:\n{shared}"
    );
    // --- The wire field wires through the derived codec (wrapped by Result). ---
    assert!(
        shared.contains("(Codec.result Codec.error autoReceiptCodec_)"),
        "the `Result Error Receipt` field must wire `Codec.result Codec.error autoReceiptCodec_`:\n{shared}"
    );
    // --- The mixed field defaults are sound (String/Int/Maybe/List/nested). ---
    for needle in [
        "orderId = \"\"",
        "amountMinor = 0",
        "note = Nothing",
        "tags = []",
    ] {
        assert!(
            shared.contains(needle),
            "blankReceipt_ must default `{needle}`:\n{shared}"
        );
    }
    // --- The Secret/Set fail-closed refusal is NOT falsely tripped here. ---
    assert!(
        !shared.contains("Secret") && !shared.contains("Set "),
        "this fixture carries no Secret/Set — none should appear:\n{shared}"
    );

    // --- Both trees BUILD (Go-gated). The build IS the round-trip proof: the ---
    // --- backend embeds the SSR model and the frontend decodes it symmetrically. ---
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "auto-record-codec backend must build natively (the derived codec must type-check)"
    );
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "auto-record-codec frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// The auto-derive fails CLOSED, with an actionable message, when the wire field
/// is a BARE top-level data-carrying union. `Codec.auto` has no decode/rebuild
/// path for a bare ADT (only a record's FIELDS decode an ADT), so the split must
/// refuse — telling the user to declare a top-level `Codec <T>` — rather than
/// emit a codec that will not round-trip. (A union NESTED in a record is fine.)
#[test]
fn refuses_a_bare_data_carrying_union_wire_field_with_an_actionable_error() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            bare_adt_wire_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        !output.status.success(),
        "sky spa-split must FAIL CLOSED on a bare data-carrying union wire field"
    );
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        combined.contains("cannot DECODE a bare data-carrying union"),
        "the error must explain WHY a bare ADT cannot cross the wire:\n{combined}"
    );
    assert!(
        combined.contains("Define a top-level `Codec Outcome` binding"),
        "the error must be ACTIONABLE — naming the type + the fix:\n{combined}"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// C-10: `sky check` ≡ `sky build` for a split target. A direct `Spa.app`
/// entry whose server branch carries a bare data-carrying union is refused by
/// `sky check` with the build's own diagnostic, in text and in json. Before
/// v0.27.0 the check passed and only the build failed.
#[test]
fn sky_check_refuses_what_the_split_refuses_for_a_spa_entry() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let dir = scratch();
    let _ = std::fs::remove_dir_all(&dir);
    copy_tree(
        &bare_adt_wire_fixture_entry()
            .parent()
            .unwrap()
            .parent()
            .unwrap(),
        &dir,
    );
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("sky check");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !out.status.success(),
        "sky check must fail as the build does:\n{log}"
    );
    assert!(
        log.contains("cannot DECODE a bare data-carrying union"),
        "{log}"
    );
    assert!(
        log.contains("docs/migration/v0.27.md#sky-check-runs-the-spa-split"),
        "the refusal links the migration guide: {log}"
    );
    let out = Command::new(SKY)
        .args(["check", "--format", "json", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("sky check --format json");
    let json = String::from_utf8_lossy(&out.stdout);
    assert!(!out.status.success(), "{json}");
    assert!(
        json.contains("cannot DECODE a bare data-carrying union") && json.contains("\"ok\":false"),
        "the json stream carries the split's diagnostic:\n{json}"
    );
    assert!(
        !dir.join(".split").exists(),
        "a check writes no split into the project"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

const STD_APP_WIRE: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict exposing (Dict)
import Sky.Core.File as File
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.App as App
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


type Filter
    = All
    | Tagged String


type alias Model =
    { counts : Dict String Int
    , FIELD
    }


type Msg
    = Bump String


init : () -> ( Model, Cmd Msg )
init _ =
    ( { counts = Dict.empty, INIT }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Bump k ->
            -- SERVER: a File effect keeps this arm on the backend, so the
            -- model fields it writes cross the wire.
            let
                _ =
                    Task.run (File.writeFile "audit.txt" k)
            in
            ( { model | counts = Dict.insert k 1 model.counts, WRITE }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column []
        [ Ui.text (String.fromInt (Dict.size model.counts))
        , Ui.button [] { onPress = Just (Bump "a"), label = Ui.text "bump" }
        ]


main : Task Error ()
main =
    App.run
        (App.app
            { init = init
            , update = update
            , view = view
            , subscriptions = \_ -> Sub.none
            }
            |> App.withNotFound ()
        )
"#;

fn std_app_wire(extra_field: &str, init: &str, write: &str) -> PathBuf {
    let src = STD_APP_WIRE
        .replace("FIELD", extra_field)
        .replace("INIT", init)
        .replace("WRITE", write);
    let dir = scratch_std_app("c10", &src);
    let toml = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("{toml}\n[app]\ntarget = \"web:app\"\n"),
    )
    .unwrap();
    dir
}

/// C-10 on a Std.App entry with `[app] target = "web:app"`: a bare
/// data-carrying union model field written by a server arm is refused by
/// `sky check`, as by `sky build`.
#[test]
fn sky_check_refuses_what_the_split_refuses_for_a_web_app_target() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let dir = std_app_wire("filter : Filter", "filter = All", "filter = Tagged k");
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("sky check");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !out.status.success(),
        "sky check must refuse the split:\n{log}"
    );
    assert!(
        log.contains("no codec") || log.contains("cannot DECODE"),
        "{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// C-10: a `Dict String Int` model field crosses the Sky.Spa wire through the
/// new `Codec.dict`, so both `sky check` and `sky build --target web:app`
/// accept it. Before v0.27.0 the check passed, the build failed, and the hint
/// asked for an unparsable `Codec Dict String Int` binding.
#[test]
fn a_dict_model_field_crosses_the_spa_wire() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let dir = std_app_wire("note : String", "note = \"\"", "note = k");
    for args in [
        vec!["check", "src/Main.sky"],
        vec!["build", "--target", "web:app", "src/Main.sky"],
    ] {
        let out = Command::new(SKY)
            .args(&args)
            .current_dir(&dir)
            .output()
            .expect("sky");
        let log = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        assert!(out.status.success(), "sky {args:?}:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&dir);
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
    assert!(
        status.success(),
        "sky spa-split should succeed on the push fixture"
    );

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
    // --- The RPC is a cookie-session route with the origin guard, not an
    // API route (Server.api is CSRF-exempt with no other check). ---
    assert!(
        back.contains("Server.rpc \"POST /_rpc/Increment\" incrementHandler")
            && !back.contains("Server.api \"POST /_rpc/"),
        "every /_rpc/<Msg> must be a Server.rpc route:\n{back}"
    );
    // --- `/_sky/sub` authorises the topic against the app's own
    // `subscriptions` (a hand-authored entry: the top-level name). ---
    assert!(
        back.contains("Ffi.kernel \"Spa_subAllowsTopic\"")
            && back
                .contains("if spaSubAllowsTopic_ (subscriptions (spaSubModel_ req)) topic_ then")
            && back.contains("spaSubModel_ req_ =")
            && back.contains("Server.withStatus 403"),
        "the SSE endpoint must stream only topics the app's subscriptions name:\n{back}"
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
        front.contains("Spa.rpc") && front.contains("/_rpc/Increment"),
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
    assert!(
        out.join("backend/sky-out/app").is_file(),
        "backend produces sky-out/app"
    );

    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(frontend_build.success(), "push frontend must build to wasm");
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Regression for issue #195. A branch whose ONLY server contact is an EXPLICIT
/// `Spa.postJson` / `Spa.getJson` (over a user-provided codec) is a CLIENT branch
/// — the explicit RPC IS the boundary, not a server effect to lift into a
/// synthesized whole-model RPC. Before the fix, `spa_partition` followed
/// `Std.Spa.postJson` into its internal `Http.*` and marked `AddItem` SERVER;
/// because `AddItem` returns the whole model opaquely (`setUi (…) model`), the
/// synthesized RPC request carried EVERY Model field, including the record-alias
/// field `data : Data` — which no codec could wire, so `sky spa-split` failed with
/// `branch \`AddItem\`, field \`data\`: no codec for a field of type \`any\``.
///
/// The fix classifies `Std.Spa`'s client-boundary helpers as CLIENT (pure leaves
/// in the taint graph), so every branch stays client, no `<Msg>Req`/`<Msg>Resp`
/// is synthesized, and the `Spa.postJson` call is copied verbatim into the wasm
/// frontend. The generated static-only backend still builds (it copies none of
/// the app's TEA decls, which reference the client-only `Std.Spa` framework).
#[test]
fn explicit_spa_rpc_branch_stays_client_not_a_synthesized_model_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let output = Command::new(SKY)
        .args([
            "spa-split",
            explicit_rpc_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        output.status.success(),
        "sky spa-split must SUCCEED on an explicit-RPC client (issue #195), got:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );
    // The exact #195 symptom must be gone.
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        !combined.contains("no codec for a field of type"),
        "the spurious whole-model codec error must not appear:\n{combined}"
    );
    // The split report classifies AddItem CLIENT (local), with NO server branches.
    assert!(
        combined.contains("server branches (→ RPC): (none)"),
        "an explicit-RPC client must have NO server branches:\n{combined}"
    );

    // The frontend keeps the explicit `Spa.postJson` with the user's own codec —
    // it is NOT rewritten into a synthesized `Spa.postJson … "/_rpc/AddItem" …`.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Spa.postJson newItemCodec itemListCodec \"/api/items\""),
        "frontend must keep the author's explicit Spa.postJson verbatim"
    );
    assert!(
        !front.contains("/_rpc/AddItem"),
        "AddItem must NOT be rewritten into a synthesized RPC"
    );

    // Nothing anywhere synthesized an `AddItemReq` / `AddItemResp` wire record.
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    for (name, text) in [
        ("shared", &shared),
        ("backend", &back),
        ("frontend", &front),
    ] {
        assert!(
            !text.contains("AddItemReq") && !text.contains("AddItemResp"),
            "no synthesized AddItemReq/AddItemResp wire record should exist in {name}"
        );
        assert!(
            !text.contains("/_rpc/"),
            "no /_rpc endpoint should be generated for a client-only app ({name})"
        );
    }

    // The real proof: both trees BUILD (backend native, frontend wasm). Gated on
    // the Go toolchain, exactly like the other build-leg tests here.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "explicit-RPC static-only backend must build"
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "explicit-RPC frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend stages a hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// BUG-1 (headline). `sky build --target web:app` on a Std.App `App.app`
/// (Std.Ui `Element`-view) app that configures itself through `App.withConfig
/// (App.WebConfig { App.webDefaults | port = … })` MUST succeed end-to-end.
///
/// The App→Spa synthesis chooses whether to wrap the user's `view` in
/// `Ui.layout []` (Element → Html) by detecting the view family. The old guard
/// was `src.contains("App.web")`, which ALSO matched the `App.webDefaults`
/// opts helper that a NORMAL `App.app` web app uses — so the app was
/// misclassified as an `App.web` (Std.Html) app, the wrap was skipped, and the
/// synthesised `Spa.config` failed to type-check with `Html Msg vs Element Msg`.
/// The fix detects the `App.web` BUILDER at a call boundary (not the
/// `App.webDefaults`/`App.webConfig` idents, and not inside `--` comments).
#[test]
fn web_app_target_wraps_ui_element_view_despite_webdefaults() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&web_config_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The exact pre-fix symptom must be gone — regardless of the Go toolchain,
    // because it is a type error the synthesis produced BEFORE any `go build`.
    assert!(
        !log.contains("Html Msg` vs `Element Msg") && !log.contains("Html Msg vs Element Msg"),
        "BUG-1: the Html/Element mismatch must be gone (App.webDefaults must not be read as App.web):\n{log}"
    );
    // The synthesised entry must wrap the Element view in `Ui.layout []` (not
    // pass it through as an already-Html view). This is the direct proof and
    // needs no Go toolchain.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("Ui.layout [] (view model_)"),
        "BUG-1: the Element view must be wrapped in `Ui.layout []`:\n{synth}"
    );
    // Synthesis + the split's type-check passed (this line prints only after
    // `generate` type-checks clean).
    assert!(
        log.contains("client/server split"),
        "the split must run (synthesis + type-check passed):\n{log}"
    );

    // Full end-to-end proof (Go-gated): the whole `--target web:app` build
    // produces the native backend + the wasm frontend.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "BUG-1: --target web:app must build end-to-end:\n{log}"
    );
    assert!(
        proj.join(".skyapp/web-app/.split/backend/sky-out/app")
            .is_file(),
        "backend binary must be built:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// BUG-2. A `|> App.withX` builder step the synthesis does NOT carry into the
/// derived Spa entry must be reported by name, never dropped silently. Runs
/// during synthesis, so no Go toolchain is needed.
///
/// SSR-P0 update: `withHead` is now CARRIED (it becomes `|> Spa.withHead` in the
/// synthesised entry — the SSR per-route `<head>` channel), so it must NOT be in
/// the dropped list any more. A genuinely server-only builder (`withGuard`) is
/// added here to keep the never-drop-silently invariant under test.
#[test]
fn web_app_synthesis_warns_about_dropped_builder_steps() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)
import Std.Html as Html exposing (Html)


type alias Model =
    { count : Int }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Element Msg
view _ =
    Ui.text "hi"


pageHead : Model -> List (Html Msg)
pageHead _ =
    [ Html.node "title" [] [ Html.text "My App" ] ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()
        |> App.withHead pageHead
        |> App.withOnKey (\_ -> Noop)


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("withheaddrop", main_sky);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert!(
        log.contains("NOT carried") || log.contains("not carried"),
        "BUG-2: a dropped builder step must be reported, not dropped silently:\n{log}"
    );
    // The dropped LIST (the segment after `client entry: `) must name the
    // genuinely-uncarried `withOnKey` (terminal-only). `withRoutes`/`withNotFound`/
    // `withHead`/`withOnNavigate`/`withRequest`/`withGuard` are all CARRIED, so
    // although they may appear in the warning's explanatory prose, they must NOT be
    // in the dropped list.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    assert!(
        dropped_list.contains("withOnKey"),
        "BUG-2: `App.withOnKey` (terminal-only) must be named in the dropped list, got `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withHead"),
        "SSR-P0: `App.withHead` is now carried into the Spa entry and must NOT be in the dropped list `{dropped_list}`:\n{log}"
    );
    assert!(
        !dropped_list.contains("withNotFound"),
        "withNotFound is carried into the Spa entry and must not be in the dropped list `{dropped_list}`"
    );
    // Fix 5: withGuard is carried (enforced server-side) — must NOT be dropped.
    assert!(
        !dropped_list.contains("withGuard"),
        "Fix 5: `App.withGuard` is now carried (enforced server-side) and must NOT be in the dropped list `{dropped_list}`:\n{log}"
    );

    // A `sky doc` command runs the SAME synthesis only to ANALYSE the app (it
    // stages the Spa split read-only), so it must NOT print the build-time
    // dropped-builder warning — a doc/spec generator is not a build. Same
    // fixture (it drops `withOnKey`), via the diagram staging path.
    let doc = Command::new(SKY)
        .args([
            "doc",
            "--diagram",
            "journey",
            "--target",
            "web:app",
            "--format",
            "md",
        ])
        .current_dir(&proj)
        .output()
        .expect("run sky doc --diagram journey");
    let doc_log = format!(
        "{}{}",
        String::from_utf8_lossy(&doc.stdout),
        String::from_utf8_lossy(&doc.stderr)
    );
    assert!(
        !doc_log.contains("NOT carried") && !doc_log.contains("not carried"),
        "a read-only `sky doc --diagram` must NOT emit the Spa-build dropped-builder warning:\n{doc_log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// SSR-P0. `App.withHead` is CARRIED through the App→Spa synthesis into a
/// `|> Spa.withHead` step (the SSR per-route `<head>` channel, design §4.3 /
/// §7-P0). The argument may be a `sky fmt`-wrapped MULTI-LINE lambda; the
/// line-based `extract_app_fields` must gather the whole lambda by bracket
/// balancing, not truncate it to its first physical line (§7-P0(c)). This proves
/// the multi-line capture + the carry, and that the drop-warning no longer names
/// `withHead`. Synthesis-only assertions need no Go toolchain.
#[test]
fn web_app_carries_multiline_withhead_into_spa_entry() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui exposing (Element)
import Std.Live.Head as Head


type alias Model =
    { title : String }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { title = "Home" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Element Msg
view _ =
    Ui.text "hi"


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()
        |> App.withHead
            (\m ->
                [ Head.title ("SSR-HEAD-MARKER: " ++ m.title)
                , Head.meta "description" "a spa ssr page"
                ]
            )


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("multilinehead", main_sky);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The synthesised entry must carry the head as a `Spa.withHead` builder step
    // AND contain the FULL multi-line lambda body — not a first-line truncation.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("Spa.withHead"),
        "SSR-P0: the synthesised entry must carry `withHead` as `Spa.withHead`:\n{synth}"
    );
    assert!(
        synth.contains("SSR-HEAD-MARKER") && synth.contains("Head.meta") && synth.contains("description"),
        "SSR-P0: the FULL multi-line withHead lambda must be captured (all lines), not truncated:\n{synth}"
    );

    // The drop-warning, if any fired, must NOT name withHead (it is carried).
    if let Some(after) = log.split("client entry: ").nth(1) {
        let dropped_list = after.split('.').next().unwrap_or("");
        assert!(
            !dropped_list.contains("withHead"),
            "SSR-P0: withHead is carried and must not appear in the dropped list `{dropped_list}`:\n{log}"
        );
    }

    // Synthesis + the split's type-check passed (only prints after `generate`
    // type-checks the derived entry clean — so `Spa.withHead pageHead` is well
    // typed against the new `Std.Spa.withHead` signature).
    assert!(
        log.contains("client/server split"),
        "SSR-P0: the split must run (synthesis + type-check of the head-carrying entry passed):\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

fn web_withhead_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-web-withhead")
}

fn web_head_server_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-web-head-server")
}

fn web_view_server_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-web-view-server")
}

/// The client-builder invariant, CARRY leg. A real-app shape — `App.web { view =
/// View.view } |> App.withHead View.head` with `view`/`head` factored into a
/// sibling `View` module, both PURE — synthesises `spaView_`/`spaHead_` wrappers.
/// Because neither reaches a server effect, BOTH must be carried into the frontend
/// entry (defined AND referenced), and the wasm client must build. Regression for
/// the shop-app dangle: the split kept `view = spaView_` / `|> Spa.withHead
/// spaHead_` in the client `main` but dropped their definitions -> `E1001
/// Undefined name: spaView_` / `spaHead_`. This fixture proves the carry works for
/// a pure sibling-module view/head (the guard that the drop leg below is precise).
#[test]
fn web_app_carries_sibling_module_view_and_head_into_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&web_withhead_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let front = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend entry must exist");

    // Every `spa*_` the client chain references must be DEFINED — no dangling ref.
    assert!(
        front.contains("spaView_ model_ ="),
        "CARRY: the pure `spaView_` must be DEFINED in the frontend entry:\n{front}"
    );
    assert!(
        front.contains("spaHead_ model_ ="),
        "CARRY: the pure `spaHead_` must be DEFINED in the frontend entry:\n{front}"
    );
    assert!(
        front.contains("view = spaView_"),
        "CARRY: the client `Spa.config` must still reference `spaView_`:\n{front}"
    );
    assert!(
        front.contains("|> Spa.withHead spaHead_"),
        "CARRY: a PURE head must keep its `Spa.withHead spaHead_` step (not be dropped):\n{front}"
    );
    assert!(
        !log.contains("Undefined name: spaView_") && !log.contains("Undefined name: spaHead_"),
        "CARRY: no dangling `spaView_`/`spaHead_` reference in the client build:\n{log}"
    );

    // Go-gated: the FRONTEND (wasm) must build — not just the backend.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "CARRY: --target web:app must build end-to-end:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "CARRY: the frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// The client-builder invariant, DROP leg. `view` is pure but `head` reaches
/// `Config.siteUrl` — an environment read (`System.getenvOr`) — so `head` is
/// server-only: it cannot run in the wasm client, but the SSR backend still
/// renders it. The split must DROP the optional `|> Spa.withHead spaHead_` step
/// from the client chain (never leave a dangling `spaHead_`), while still carrying
/// the pure `spaView_`. Pre-fix this failed with `E1001 Undefined name:
/// spaHead_`. Reproduces the shop-app `spaHead_` half exactly.
#[test]
fn web_app_drops_server_tainted_head_from_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&web_head_server_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let front = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend entry must exist");
    let back = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");

    // The pure view is carried; the server-tainted head is dropped WHOLE — both
    // its definition AND its builder-chain reference — so no dangling name.
    assert!(
        front.contains("spaView_ model_ =") && front.contains("view = spaView_"),
        "DROP: the pure `spaView_` must still be carried:\n{front}"
    );
    assert!(
        !front.contains("spaHead_"),
        "DROP: the server-tainted `spaHead_` must be absent from the client entry (def AND reference):\n{front}"
    );
    assert!(
        !front.contains("Spa.withHead"),
        "DROP: the `|> Spa.withHead spaHead_` step must be stripped from the client chain:\n{front}"
    );
    assert!(
        !log.contains("Undefined name: spaHead_"),
        "DROP: the pre-fix dangling `spaHead_` E1001 must be gone:\n{log}"
    );
    // The BACKEND keeps the head (it renders it server-side at SSR).
    assert!(
        back.contains("spaHead_"),
        "DROP: the backend must keep `spaHead_` for SSR head rendering:\n{back}"
    );

    // Go-gated: the FRONTEND (wasm) must now build.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "DROP: --target web:app must build after the server-tainted head is dropped:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "DROP: the frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// The client-builder invariant, MANDATORY-VIEW leg. `view` itself reaches
/// `Config.siteUrl` — an environment read — so the mandatory `view = spaView_`
/// field cannot be dropped. The split must FAIL with a clear, actionable
/// diagnostic (the client view must be pure), NEVER a bare `E1001 Undefined name:
/// spaView_`. Reproduces the shop-app `spaView_` half, which is why the real
/// app's frontend cannot build until its view stops reading env. Synthesis-level —
/// no Go toolchain needed (the failure is before any `go build`).
#[test]
fn web_app_rejects_server_tainted_view_with_a_clear_diagnostic() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&web_view_server_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert!(
        !output.status.success(),
        "a server-tainted view must fail the build:\n{log}"
    );
    // The confusing pre-fix symptom must be gone.
    assert!(
        !log.contains("Undefined name: spaView_"),
        "DIAGNOSTIC: the bare `E1001 Undefined name: spaView_` must be replaced by a clear message:\n{log}"
    );
    // The new message must name the taint AND tell the user what to do.
    assert!(
        log.contains("client SPA `view` is server-tainted"),
        "DIAGNOSTIC: the failure must name the server-tainted client view:\n{log}"
    );
    assert!(
        log.contains("must be PURE") && log.contains("embed the value in the Model"),
        "DIAGNOSTIC: the failure must be actionable (make the view pure; pass config through the Model):\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_app_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-app")
}

/// SSR-P1. A `Std.App` app with ≥1 SERVER branch (a `File` effect) auto-splits
/// into a backend that carries `view`/`init`, and `gen_backend` must emit an SSR
/// `GET /{$}` route that server-renders the root's first paint (design §4.1) —
/// the SEO enabler that replaces the empty `#app` static shell with real,
/// crawlable HTML + a per-route `<head>`.
///
/// Two layers of proof:
///  - synthesis: `App.withHead` is carried into a NAMED `spaHead_` binding (so it
///    reaches the backend, where the SSR route calls it — an inline lambda would
///    live only in the dropped `main`);
///  - emission: the backend source carries the `ssrHandler` + the `GET /{$}`
///    route (registered AHEAD of `Server.static`) that renders
///    `spaSsrRenderBody (spaView_ …)` with the `spaHead_` head, and the whole
///    split type-checks (`generate` type-checks the emitted backend — a malformed
///    SSR route would fail HERE, before any Go).
/// The Go-gated leg proves the whole thing builds end-to-end. The SERVED HTML
/// (body inside a `data-sky-ssr` `#app` + the `<head>`) is asserted by the Go
/// runtime tests (spa_ssr_notjs_test.go / spa_ssr_test.go); the browser hydration
/// leg is manual (a wasm/browser attach cannot run here).
#[test]
fn spa_ssr_app_emits_a_server_render_route_for_the_root() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_app_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis carried `withHead` into a NAMED binding referenced by the config.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("spaHead_ model_") && synth.contains("|> Spa.withHead spaHead_"),
        "SSR-P1: withHead must be a NAMED `spaHead_` binding (reaches the backend), \
         referenced by the config:\n{synth}"
    );

    // The split ran → the emitted backend (incl. the SSR route) TYPE-CHECKED.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "SSR-P1: the split must run (the emitted backend SSR route type-checked):\n{log}"
    );

    // The backend carries the SSR route, ahead of the static route.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // P3 renamed the handler's rendered model to `resolved` (the per-route,
    // optionally data-settled model). This fixture is route-less with a
    // `Cmd.none` init, so `resolved` folds to the pure init model (chrome-only) —
    // the SSR route + render kernels are still exactly what P1 asserted.
    for needle in [
        "ssrHandler",
        "Server.api \"GET /{$}\" ssrHandler",
        "spaSsrPage",
        "spaSsrRenderBody (spaView_ resolved)",
        "spaSsrRenderHead spaHead_ resolved",
        "spaSsrWasmNameBuilt spaBuiltWasmName_ \"../frontend/dist\"",
    ] {
        assert!(
            backend.contains(needle),
            "SSR-P1: the generated backend must carry the SSR route piece `{needle}`:\n{backend}"
        );
    }
    // v0.25.20: the build wrote the frontend's wasm name into the backend, so the
    // SSR page names it without reading ../frontend/dist at run time.
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    let wasm = std::fs::read_dir(&dist)
        .expect("frontend dist")
        .flatten()
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .find(|n| n.starts_with("main.") && n.ends_with(".wasm"))
        .expect("a main.<hash>.wasm in the dist");
    assert!(
        backend.contains(&format!("spaBuiltWasmName_ =\n    \"{wasm}\"")),
        "the build must bake the dist's {wasm} into the backend:\n{backend}"
    );
    // The SSR route must be registered BEFORE the static fallthrough so asset
    // GETs (main.<hash>.wasm, wasm_exec.js) still reach the file server.
    let ssr_at = backend.find("Server.api \"GET /{$}\" ssrHandler");
    let static_at = backend.find("Server.static \"/\"");
    assert!(
        ssr_at.is_some() && static_at.is_some() && ssr_at < static_at,
        "SSR-P1: the SSR route must precede the static route:\n{backend}"
    );
    // P3 fail-closed: this fixture's `init` is `Cmd.none` (its `File.writeFile`
    // lives in a `Persist` branch, NOT in `init`) AND it has no `withOnNavigate`,
    // so there is no GET-safe read to settle → NO data-resolve settle is emitted,
    // and the route renders the pure model. `Spa_ssrSettle` must be absent. (No
    // `withRoutes` either, so the pure model is `model0` directly, not a `routed`
    // binding — fix 2 emits the route-resolve binding only when routes exist.)
    assert!(
        !backend.contains("spaSsrSettle") && backend.contains("resolved =\n            model0"),
        "SSR-P3 fail-closed: a `Cmd.none` init must NOT get a data-resolve settle:\n{backend}"
    );

    // Full end-to-end proof (Go-gated): the whole thing builds — a broken kernel
    // reference (`Spa_ssr*`) would fail the backend `go build` here.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR-P1: --target web:app must build end-to-end:\n{log}"
    );
    assert!(
        proj.join(".skyapp/web-app/.split/backend/sky-out/app")
            .is_file(),
        "backend binary must be built:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_p3_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-p3")
}

fn spa_guard_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-guard")
}

fn spa_onnav_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-onnav")
}

/// Fix 2. A routed content site whose PER-ROUTE data is loaded in `onNavigate`
/// (not in `init`, which is `Cmd.none`). The SSR settle must fire
/// `onNavigate page` for the resolved route and settle its GET-safe read, so a
/// direct GET of a deep route server-renders that route's REAL data — into the
/// body a crawler sees AND the embedded `#sky-model` the client boots from.
/// Before the fix the settle ran only `init`'s single (here empty) command, so a
/// deep route rendered a blank body: RED. After: the route's data is present.
///
/// Two layers of proof:
///   * emission (no Go): the backend `ssrHandler` fires `onNavigate` on the
///     resolved route, runs it through `update`, and settles that command;
///   * Go-gated e2e: `GET /a` carries "Alpha content", `GET /b` "Beta content",
///     each in the rendered body and the `#sky-model` blob; `GET /` (no per-route
///     data) carries an empty body — proving the data is per-route + server-resolved.
#[test]
fn spa_ssr_settles_per_route_onnavigate_data() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_onnav_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-ssr-onnav fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: onNavigate is carried onto the client config.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("|> Spa.withOnNavigate spaOnNavigate_"),
        "fix 2: onNavigate must be carried onto the client Spa config:\n{synth}"
    );

    // Emission: the SSR handler fires onNavigate on the resolved route and
    // settles its command (the per-route data load).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    for needle in [
        "spaOnNavigate_ preNav_.page",
        "update navMsg_ preNav_",
        "spaSsrSettleFull navModel_ navCmd_ update",
    ] {
        assert!(
            backend.contains(needle),
            "fix 2: the SSR handler must fire + settle onNavigate — missing `{needle}`:\n{backend}"
        );
    }

    // ── Go-gated e2e: serve and assert REAL per-route data. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "fix 2: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the per-route data the onNavigate reads settle.
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/a.txt"), "Alpha content\n").unwrap();
    std::fs::write(backend_dir.join("data/b.txt"), "Beta content\n").unwrap();

    let port = 8977u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-ssr-onnav backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-ssr-onnav backend never reported listening on :{port}");
    }
    let a_body = curl_body_p(port, "/a");
    let b_body = curl_body_p(port, "/b");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let a_body = a_body.expect("GET /a should return a body");
    let b_body = b_body.expect("GET /b should return a body");
    let home_body = home_body.expect("GET / should return a body");

    // /a carries the SERVER-RESOLVED per-route data (the onNavigate read settled),
    // both in the rendered body and the embedded #sky-model blob.
    assert!(
        a_body.contains("data-sky-ssr")
            && a_body.contains("Route A")
            && a_body.contains("Alpha content"),
        "fix 2: GET /a must carry the onNavigate-resolved data in the body:\n{a_body}"
    );
    let a_blob_start = a_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("fix 2: the #sky-model blob must be present on /a");
    let a_blob = &a_body[a_blob_start..];
    let a_blob = &a_blob[..a_blob.find("</script>").expect("blob must close")];
    assert!(
        a_blob.contains(r#""page":"a""#) && a_blob.contains("Alpha content"),
        "fix 2: the /a #sky-model must carry the resolved page + body:\n{a_blob}"
    );
    // /b resolves to its OWN data.
    assert!(
        b_body.contains("Route B") && b_body.contains("Beta content"),
        "fix 2: GET /b must carry its own resolved data (not /a's):\n{b_body}"
    );
    // / has no per-route onNavigate data → empty body (proves per-route, not global).
    let home_app = {
        let s = home_body
            .find(r#"<div id="app""#)
            .expect("home #app must exist");
        let e = home_body
            .find(r#"<script id="sky-model""#)
            .unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Home page")
            && !home_app.contains("Alpha content")
            && !home_app.contains("Beta content"),
        "fix 2: GET / must render Home with no per-route data:\n{home_app}"
    );
}

/// Fix 5. `App.withGuard` / `App.withOnNavigate` / `App.withRequest` are CARRIED
/// through the App→Spa synthesis (never named in the drop warning), and the
/// per-Msg guard is enforced SERVER-side: the generated `POST /_rpc/<Msg>`
/// handler calls `spaGuard_ <msg> m` BEFORE `update`, answering 403 on `Err`
/// and never running the effect. This is the trusted authorisation point — the
/// wasm client is untrusted, so a client that skips its own guard still cannot
/// fire the effect.
///
/// Two layers of proof:
///   * synthesis + emission (no Go): the three builders reach named bindings;
///     the backend's `saveHandler` calls `spaGuard_` ahead of `update`.
///   * Go-gated e2e: a valid `POST /_rpc/Save` that the guard DENIES returns 403
///     and leaves the write target untouched (the effect never ran).
#[test]
fn spa_guard_is_enforced_server_side_on_rpc() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_guard_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-guard fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: the three builders are carried into NAMED bindings.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    for needle in [
        "\nspaGuard_ ",
        "spaOnNavigate_ =",
        "\nspaOnRequest_ ",
        "|> Spa.withOnNavigate spaOnNavigate_",
    ] {
        assert!(
            synth.contains(needle),
            "fix 5: the synthesised entry must carry `{needle}`:\n{synth}"
        );
    }
    // None of the three may appear in the drop warning's dropped LIST.
    let dropped_list = log
        .split("client entry: ")
        .nth(1)
        .and_then(|s| s.split('.').next())
        .unwrap_or("")
        .to_string();
    for banned in ["withGuard", "withOnNavigate", "withRequest"] {
        assert!(
            !dropped_list.contains(banned),
            "fix 5: `{banned}` is carried and must NOT be in the dropped list `{dropped_list}`:\n{log}"
        );
    }

    // Emission: the backend enforces the guard on the `/_rpc/Save` handler,
    // ahead of `update`, and has a `forbidden` (403) responder.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // The fixture declares `withRequest`, so the guard + update run against the
    // REQUEST-SEEDED model `mReq` (`spaOnRequest_ req m`), NOT the raw client
    // payload model `m`. This closes Judge finding 5: the wasm client can forge
    // any field of `m`, so a guard reading an identity / session field off the
    // payload would trust forged data; re-applying withRequest overwrites those
    // fields from the real request before the guard runs.
    assert!(
        backend.contains("case spaGuard_ (Save p.content) mReq of"),
        "fix 5: the guard must run against the request-seeded model mReq, not the forgeable payload m:\n{backend}"
    );
    assert!(
        backend.contains("Task.succeed (forbidden"),
        "fix 5: a denied guard must answer 403 (forbidden):\n{backend}"
    );
    let reseed_at = backend
        .find("spaOnRequest_ req m")
        .expect("fix 5: the /_rpc handler must re-apply withRequest server-side");
    let guard_at = backend
        .find("case spaGuard_ (Save p.content) mReq of")
        .expect("guard check present");
    let update_at = backend
        .find("update (Save p.content) mReq")
        .expect("update present");
    assert!(
        reseed_at < guard_at && guard_at < update_at,
        "fix 5: the request re-seed must precede the guard, and the guard must precede the update:\n{backend}"
    );

    // ── Go-gated e2e: a denied Save returns 403 and never runs the write. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "fix 5: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the write target with a known sentinel so we can prove it is UNCHANGED.
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/out.txt"), "seed\n").unwrap();

    let port = 8974u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-guard backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-guard backend never reported listening on :{port}");
    }
    // A VALID request body (decodes to SaveReq) so the handler reaches the guard,
    // not the 400 decode-error path. The guard DENIES Save → 403.
    let posted = curl_post_status_body(port, "/_rpc/Save", r#"{"content":"pwned"}"#);
    let after = std::fs::read_to_string(backend_dir.join("data/out.txt")).unwrap_or_default();
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let (code, _body) = posted.expect("POST /_rpc/Save should return");
    assert_eq!(
        code, 403,
        "fix 5: a guard-denied /_rpc/Save must return 403 (the trusted server-side enforcement)"
    );
    assert_eq!(
        after, "seed\n",
        "fix 5: the denied effect must NOT run — data/out.txt must be unchanged, was {after:?}"
    );
}

fn spa_writeset_roundtrip_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-writeset-roundtrip")
}

fn spa_sibling_rpcerror_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-sibling-rpcerror")
}

/// Bug #4 (all three layers): an App.app app whose TEA `update` lives in a
/// SIBLING module, whose `Save` arm calls a server kernel DIRECTLY, with
/// `App.withRpcError` declared.
///
///   * #4c — the sibling `update` module is routed for per-binding regeneration
///     even though it holds no tainted PROJECT binding (its `Save` arm calls
///     `File.writeFile` directly). Without it the module is classified pure and
///     copied verbatim, leaking `File.` into the wasm client and never adding the
///     `AppliedSave` RPC arm.
///   * #4b — the regenerated sibling `update`'s `AppliedSave (Err e)` arm routes
///     the failure through `spaRpcError_`; the synthesis puts that binding in the
///     entry, which the sibling cannot import (cycle), so the handler is copied
///     into the sibling frontend module.
///
/// Before the fix `sky build --target web:app` failed to compile the client
/// ("case does not cover AppliedSave", then "Undefined name: spaRpcError_").
#[test]
fn web_app_sibling_update_with_rpc_error_builds() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_sibling_rpcerror_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the sibling-rpcerror fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The sibling `Logic` module is REGENERATED in the frontend (bug #4c): its
    // `Save` arm becomes an RPC and the `AppliedSave` fold arm is present.
    let front_logic =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Logic.sky"))
            .expect("the regenerated frontend Logic module must exist");
    assert!(
        front_logic.contains("Spa.rpc") && front_logic.contains("/_rpc/Save"),
        "bug #4c: the sibling `update`'s Save arm must become an RPC in the frontend:\n{front_logic}"
    );
    assert!(
        front_logic.contains("AppliedSave"),
        "bug #4c: the frontend sibling `update` must carry the AppliedSave fold arm:\n{front_logic}"
    );
    // The withRpcError handler is copied INTO the sibling so `spaRpcError_`
    // resolves there (bug #4b).
    assert!(
        front_logic.contains("spaRpcError_") && front_logic.contains("onRpcError"),
        "bug #4b: the withRpcError handler must be copied into the sibling frontend:\n{front_logic}"
    );
    assert!(
        front_logic.contains("update (spaRpcError_ e)"),
        "bug #4b: the sibling Err arm must route through spaRpcError_:\n{front_logic}"
    );
    // SECURITY: the server kernel must NOT leak into the wasm frontend.
    let front_tree = concat_sky_tree(&proj.join(".skyapp/web-app/.split/frontend"));
    assert!(
        !front_tree.contains("File."),
        "SECURITY: the server `File` kernel must not reach the wasm frontend:\n{front_tree}"
    );

    // Go-gated: the whole thing builds end to end.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "bug #4: --target web:app must build a sibling-update + withRpcError app end-to-end:\n{log}"
    );
    let app_bin = proj.join(".skyapp/web-app/.split/backend/sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    let _ = std::fs::remove_dir_all(&proj);
}

/// Soundness regression for Sky.Spa auto-split bug #1 — the RPC request must
/// carry `reads union writes`, not the reads alone.
///
/// `Act` reads `counter` and writes `note` on the then-arm; the else-arm bumps
/// `counter` and PRESERVES `note`. The per-Msg response write-set is the union
/// `{counter, note}`. Before the fix the request carried only the read-set
/// `{counter}`, so a `note` the client held was NOT sent, the server rebuilt it
/// as the empty-Model default `""`, and the executed else-path returned that —
/// silently clobbering the client value.
///
/// This asserts BOTH legs: the generated wire shape (the shared `ActReq` and the
/// backend reconstruct both carry `note`, always-run) AND, Go-gated, the real
/// round-trip — POST a non-default `note`, and it must survive on the executed
/// else-path. A shape-only test is exactly what let this ship, so the e2e leg is
/// the one that matters.
#[test]
fn spa_split_request_carries_a_preserved_write_field_round_trip() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_writeset_roundtrip_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-writeset-roundtrip fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── Always-run wire-shape leg. ──
    let shared = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/shared/Shared.sky"))
        .expect("generated shared module must exist");
    // The request type carries the PRESERVED write field `note`, not the read
    // `counter` alone. `Codec.field "note"` inside the request codec BODY (from
    // `actReqCodec =` to its `buildObject`) is the wire proof — sliced tightly so
    // it cannot match the response codec's own `note` field.
    let req_codec_body = shared
        .split_once("actReqCodec =")
        .and_then(|(_, rest)| rest.split_once("buildObject"))
        .map(|(body, _)| body)
        .unwrap_or("");
    assert!(
        req_codec_body.contains("Codec.field \"note\""),
        "bug #1: the request codec must carry the preserved write field `note` (read union write):\n{shared}"
    );

    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // The server reconstruct threads `note` FROM THE REQUEST (`p.note`), not from
    // an empty-Model default.
    assert!(
        backend.contains("note = p.note"),
        "bug #1: the backend must reconstruct `note` from the request payload, not default it:\n{backend}"
    );

    let frontend =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .expect("generated frontend entry must exist");
    assert!(
        frontend.contains("note = model.note"),
        "bug #1: the client must send its own `note` in the request:\n{frontend}"
    );

    // ── Go-gated e2e: the preserved field survives the real round-trip. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "bug #1: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();

    let port = 8977u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-writeset-roundtrip backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-writeset-roundtrip backend never reported listening on :{port}");
    }
    // `counter=3` takes the else-path (3 > 5 is false): bump `counter` to 4 and
    // PRESERVE `note`. The client sends a non-default `note`, which must ride the
    // request and return unchanged. On the pre-fix code the response held
    // `"note":""` (rebuilt default).
    let posted = curl_post_status_body(port, "/_rpc/Act", r#"{"counter":3,"note":"keep"}"#);
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let (code, body) = posted.expect("POST /_rpc/Act should return");
    assert_eq!(
        code, 200,
        "bug #1: a valid /_rpc/Act must return 200; body {body:?}"
    );
    assert!(
        body.contains("\"note\":\"keep\""),
        "bug #1: the preserved `note` must survive the round-trip (not the empty default), was {body:?}"
    );
    assert!(
        body.contains("\"counter\":4"),
        "bug #1: the else-path must have bumped counter to 4, was {body:?}"
    );
}

fn spa_deeplink_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-deeplink")
}

/// Wait until the generated Sky.Spa backend logs its `Sky server listening` line
/// (the auto-split backend is a `Sky.Http.Server`, not a Sky.Live one, so its
/// ready line differs from `wait_for_listening`'s). Returns true once seen.
fn wait_for_spa_backend(log_path: &std::path::Path, tries: u32) -> bool {
    use std::io::Read as _;
    for _ in 0..tries {
        if let Ok(mut f) = std::fs::File::open(log_path) {
            let mut buf = String::new();
            if f.read_to_string(&mut buf).is_ok() && buf.contains("Sky server listening") {
                return true;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

/// SSR-P3 (design §4.1 + §4.2). A ROUTED `Std.App` app whose `init` reads real
/// data via a curated GET-safe kernel (`File.readFile`) must:
///
///   * synthesise NAMED `spaRoutes_` / `spaNotFound_` bindings (so the route
///     table reaches the backend, mirroring `spaHead_`);
///   * per-route emit `GET /{$}` + `GET /items` (each a more-specific mux entry
///     than `Server.static "/"`, so asset GETs still fall through) all pointing
///     at an `ssrHandler` that resolves the request path (Spa_ssrResolveModel);
///   * data-resolve: because `init`'s command is GET-safe, emit `Spa_ssrSettle`
///     and render `resolved` (not the pure init model);
///   * (Go-gated e2e) serve REAL per-route content: `GET /items` carries the
///     resolved item list a crawler sees; `GET /` carries the Home content and
///     NOT the items — proving per-route + data-resolved SSR end to end.
#[test]
fn spa_ssr_p3_resolves_real_per_route_data_for_a_get_safe_init() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_p3_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR-P3 fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Synthesis: named route + notFound bindings reach the backend.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("spaRoutes_ =") && synth.contains("|> Spa.withRoutes spaRoutes_"),
        "SSR-P3: routes must be a NAMED `spaRoutes_` binding referenced by the config:\n{synth}"
    );
    assert!(
        synth.contains("spaNotFound_ =") && synth.contains("|> Spa.withNotFound spaNotFound_"),
        "SSR-P3: notFound must be a NAMED `spaNotFound_` binding:\n{synth}"
    );

    // The split ran → the emitted backend (per-route + settle) TYPE-CHECKED.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "SSR-P3: the split must run (the emitted backend type-checked):\n{log}"
    );

    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    for needle in [
        // per-route resolver + registrations
        "spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path",
        "Server.api \"GET /{$}\" ssrHandler",
        "Server.api \"GET /items\" ssrHandler",
        // data-resolved settle (init IS get-safe)
        "spaSsrSettleFull routed cmd0 update",
        "( initSettled_, initDone_ ) =\n            spaSsrSettleFull routed cmd0 update",
    ] {
        assert!(
            backend.contains(needle),
            "SSR-P3: the generated backend must carry `{needle}`:\n{backend}"
        );
    }
    // Per-route SSR routes must precede the static fallthrough. Fix 6: the
    // catch-all is `Server.staticNotFound … ssrHandler`, so a cold unmatched path
    // SSRs the NotFound page instead of a bare file-server 404.
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "SSR-P3: per-route SSR routes must precede the static NotFound fallback:\n{backend}"
    );

    // ── Go-gated e2e: run the backend, curl each route, assert REAL per-route
    // content. The backend reads `data/items.json` relative to its cwd, so run it
    // from the backend dir with the data file staged there. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR-P3: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the data the settle reads (init: File.readFile "data/items.json").
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::copy(
        proj.join("data/items.json"),
        backend_dir.join("data/items.json"),
    )
    .unwrap();

    let port = 8973u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled SSR-P3 backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&proj);
        panic!("SSR-P3 backend never reported listening on :{port}\nlog:\n{buf}");
    }
    let items_body = curl_body_p(port, "/items");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let items_body = items_body.expect("GET /items should return a body");
    let home_body = home_body.expect("GET / should return a body");
    // /items carries the RESOLVED, crawlable data + the SSR marker.
    assert!(
        items_body.contains("data-sky-ssr")
            && items_body.contains("Item list:")
            && items_body.contains("Alpha Widget")
            && items_body.contains("Beta Gadget")
            && items_body.contains("Gamma Gizmo"),
        "SSR-P3: GET /items must carry the SERVER-RESOLVED item list (crawlable), \
         not a loading state. Body was:\n{items_body}"
    );
    // The embedded #sky-model blob (design §4.5) carries the RESOLVED model as
    // JSON — the route's page + the settled items — so the client can boot from
    // it. It must be present and decode to the resolved data.
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("SSR-P3: the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"Items""#)
            && blob.contains("Alpha Widget")
            && blob.contains("Gamma Gizmo"),
        "SSR-P3: the #sky-model blob must decode to the RESOLVED model \
         (page=Items + the settled items). Blob was:\n{blob}"
    );
    // / renders its OWN (Home) VIEW — "Welcome home", and NOT the Items view's
    // "Item list:" header — proving per-route BODY rendering. (The embedded model
    // blob DOES carry the settled items for every route, which is correct: the
    // data is resolved once and the Home *view* simply does not display it.) So
    // assert on the rendered body region, not the whole document.
    let home_app = {
        let s = home_body
            .find(r#"<div id="app""#)
            .expect("home #app must exist");
        let e = home_body
            .find(r#"<script id="sky-model""#)
            .unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Welcome home") && !home_app.contains("Item list:"),
        "SSR-P3: GET / must render Home's own view, not the Items view:\n{home_app}"
    );
}

/// Fix 1 (cold deep-link is dark). A cold two-segment URL (`/blog/<slug>`) served
/// by the SSR backend must reference its assets by ROOT-ABSOLUTE URL, so the
/// browser fetches `/wasm_exec.js` + `/main.<hash>.wasm` at ANY route depth. With
/// a bare relative `wasm_exec.js`, a `/blog/<slug>` document resolves it to
/// `/blog/wasm_exec.js` (404 → text/html → "Go is not defined") and the wasm
/// never boots. The Go-gated leg serves the real backend and confirms the SSR
/// document's asset URLs are root-absolute AND that `/wasm_exec.js` is a 200
/// JavaScript asset.
#[test]
fn spa_deep_link_ssr_references_root_absolute_assets() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_deeplink_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the deep-link fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The two-segment param route reaches the SSR handler (a more-specific mux
    // entry than the static catch-all).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    assert!(
        backend.contains("Server.api \"GET /blog/:slug\" ssrHandler"),
        "the /blog/:slug route must be a per-route SSR GET:\n{backend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "deep-link fixture must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8974u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled deep-link backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("deep-link backend never reported listening on :{port}");
    }
    let deep_body = curl_body_p(port, "/blog/hello");
    let asset = curl_status_ctype(port, "/wasm_exec.js");
    let _ = child.kill();
    let _ = child.wait();

    let deep_body = deep_body.expect("GET /blog/hello should return a body");
    // The cold deep-link SSRs the Post route (crawlable) with the SSR marker.
    assert!(
        deep_body.contains("data-sky-ssr") && deep_body.contains("hello"),
        "GET /blog/hello must SSR the Post route (marker + slug). Body was:\n{deep_body}"
    );
    // Assets are ROOT-ABSOLUTE — correct at this two-segment depth.
    assert!(
        deep_body.contains(r#"<script src="/wasm_exec.js">"#),
        "the deep-link document must load /wasm_exec.js (root-absolute):\n{deep_body}"
    );
    assert!(
        !deep_body.contains(r#"<script src="wasm_exec.js">"#),
        "the deep-link document must NOT reference a bare relative wasm_exec.js:\n{deep_body}"
    );
    assert!(
        deep_body.contains(r#"data-wasm="/main."#) && deep_body.contains(".wasm\"></script>"),
        "the deep-link document must name the wasm by root-absolute URL:\n{deep_body}"
    );
    // Strict CSP: the loader is the same-origin file /spa-boot.<hash>.js, and the
    // document carries no inline executable script.
    assert!(
        deep_body.contains(r#"<script src="/spa-boot."#),
        "the deep-link document must boot through /spa-boot.<hash>.js:\n{deep_body}"
    );
    for tag in deep_body.split("<script").skip(1) {
        let open = tag.split('>').next().unwrap_or("");
        assert!(
            open.contains("src=") || open.contains("application/json"),
            "the deep-link document carries an inline executable <script{open}>"
        );
    }

    // /wasm_exec.js is a REAL asset served by the static mount: 200 + JavaScript.
    let (code, ctype) = asset.expect("GET /wasm_exec.js should answer");
    let _ = std::fs::remove_dir_all(&proj);
    assert_eq!(code, 200, "/wasm_exec.js must be 200, got {code} ({ctype})");
    assert!(
        ctype.contains("javascript"),
        "/wasm_exec.js must be served as JavaScript, got Content-Type {ctype}"
    );
}

/// Fix 6 (unknown deep path does not SSR the NotFound page). A cold load of an
/// unknown path used to match no mux pattern and fall through to `Server.static`,
/// returning a bare file-server 404. With the SPA NotFound fallback the catch-all
/// is `Server.staticNotFound … ssrHandler`, so an unmatched app path boots the
/// shell and SSRs the NotFound page (`data-sky-ssr` + the NotFound view) exactly
/// as a known route would, while a request that maps to a REAL asset still serves
/// the file. RED before the fix (a plain `Server.static` catch-all → 404).
#[test]
fn spa_unknown_deep_path_ssrs_the_not_found_page() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_deeplink_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the deep-link fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Backend-source: the static catch-all is the SPA NotFound fallback, NOT a
    // bare `Server.static` (which would 404 unmatched paths).
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    assert!(
        backend.contains("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler"),
        "the static catch-all must be the SPA NotFound fallback:\n{backend}"
    );
    assert!(
        !backend.contains("Server.static \"/\" \"../frontend/dist\""),
        "an SSR+routed app must NOT emit a bare Server.static catch-all:\n{backend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "deep-link fixture must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8975u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled deep-link backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("deep-link backend never reported listening on :{port}");
    }
    let unknown_status = curl_status_ctype(port, "/some/unknown/deep/path");
    let unknown_body = curl_body_p(port, "/some/unknown/deep/path");
    let asset = curl_status_ctype(port, "/wasm_exec.js");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    // An unmatched deep path SSRs the NotFound page — 200, not a bare 404.
    let (code, ctype) = unknown_status.expect("GET unknown path should answer");
    assert_eq!(
        code, 200,
        "unmatched path must SSR NotFound (200), got {code} ({ctype})"
    );
    assert!(
        ctype.starts_with("text/html"),
        "the NotFound SSR must be text/html, got {ctype}"
    );
    let unknown_body = unknown_body.expect("GET unknown path should return a body");
    assert!(
        unknown_body.contains("data-sky-ssr")
            && unknown_body.contains("No such page here")
            && unknown_body.contains(r#"<script src="/wasm_exec.js">"#),
        "the unmatched path must SSR the NotFound page inside the shell:\n{unknown_body}"
    );
    assert!(
        !unknown_body.contains("404 page not found"),
        "the bare file-server 404 must be suppressed:\n{unknown_body}"
    );

    // A REAL asset is NOT shadowed by the fallback: still 200 JavaScript.
    let (acode, actype) = asset.expect("GET /wasm_exec.js should answer");
    assert_eq!(
        acode, 200,
        "/wasm_exec.js must still be 200, got {acode} ({actype})"
    );
    assert!(
        actype.contains("javascript"),
        "/wasm_exec.js must serve as JavaScript, got {actype}"
    );
}

/// SSR-P3 fail-closed (design §4.2). An `init` whose command is NOT a curated
/// GET-safe read — here `Time.now` (non-deterministic) — must NOT get a
/// data-resolve settle: the allowlist scan errs toward chrome-only. Per-route
/// resolution still works; only the settle is withheld, so the route renders the
/// pure init model (exactly P1). This is the "a GET must never run a
/// non-deterministic effect / mutate" boundary.
#[test]
fn spa_ssr_p3_fail_closed_when_init_is_not_get_safe() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Time as Time
import Std.App as App
import Std.Ui as Ui exposing (Element)


type Page
    = Home
    | Stamp


type alias Model =
    { page : Page, stamp : Int }


type Msg
    = Load
    | Got (Result Error Int)


init : () -> ( Model, Cmd Msg )
init () =
    ( { page = Home, stamp = 0 }, Cmd.perform (Time.now ()) Got )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Load ->
            ( model, Cmd.perform (Time.now ()) Got )

        Got (Ok t) ->
            ( { model | stamp = t }, Cmd.none )

        Got (Err _) ->
            ( { model | stamp = 0 }, Cmd.none )


view : Model -> Element Msg
view model =
    case model.page of
        Home ->
            Ui.column [] [ Ui.text "home" ]

        Stamp ->
            Ui.column [] [ Ui.text ("stamp " ++ String.fromInt model.stamp) ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withRoutes [ App.route "/" Home, App.route "/stamp" Stamp ]
        |> App.withNotFound Home


main =
    App.run app
"#;
    let proj = scratch_std_app("ssr-failclosed", main_sky);
    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the fail-closed fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated backend must exist:\n{log}"));

    // Per-route resolution STILL happens (routes are unaffected by the allowlist).
    assert!(
        backend.contains("spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path")
            && backend.contains("Server.api \"GET /stamp\" ssrHandler"),
        "SSR-P3 fail-closed: per-route SSR must still be emitted:\n{backend}"
    );
    // …but NO data-resolve settle for a Time.now init (fail-closed → chrome-only).
    assert!(
        !backend.contains("spaSsrSettle") && backend.contains("resolved =\n            routed"),
        "SSR-P3 fail-closed: a non-deterministic (Time.now) init must NOT get a \
         data-resolve settle — it must render the pure init model:\n{backend}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_db_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-db")
}

fn ssr_nested_record_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-nested-record")
}

fn ssr_union_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-union")
}

/// SSR hydration of a DATA-CARRYING UNION model field (the union sibling of
/// `spa_ssr_nested_record_...`). For the client `Codec.auto` to decode the union
/// from the SSR embed (`{"tag":"Ready","v0":3}`), the emitted frontend Go must
/// BOTH pin the field to the nominal `Main_Status` (not an erased `any`) AND
/// register `Main_Status`'s variants (so `BuildAdtFromWire` can reconstruct
/// them). This guards the union hydration path against a silent reset-to-init.
#[test]
fn spa_ssr_union_field_pins_and_registers_variants_so_hydration_is_lossless() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_union_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR union fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // Always-run: the decoder derives from an annotated `spaModelBlank_` binding.
    let frontend =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated frontend entry must exist:\n{log}"));
    let frontend_code = strip_line_comments(&frontend);
    assert!(
        frontend_code.contains("spaModelBlank_ :")
            && frontend_code.contains("Codec.fromJson (Codec.auto spaModelBlank_)"),
        "SSR union: the decoder must derive `Codec.auto` from the annotated blank:\n{frontend}"
    );

    // Go-gated: the emitted frontend Go must pin the union field to the nominal
    // `Main_Status` AND register its variants (the two things the union decode
    // needs).
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR union: --target web:app must build end-to-end:\n{log}"
    );
    let fe_go =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"))
            .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            fe_go.contains("Status Main_Status"),
            "SSR union: the emitted frontend model must pin `status` to the nominal \
             `Main_Status` union, not an erased `any`:\n(model struct not found as expected)"
        );
        assert!(
            fe_go.contains("RegisterAdtVariant(\"main.Main_Status\", \"Ready\""),
            "SSR union: the frontend Go must register the union's data-carrying \
             variants so `BuildAdtFromWire` can reconstruct them on decode; a \
             missing registration is a silent hydration reset"
        );
    }

    let _ = std::fs::remove_dir_all(&proj);
}

fn ssr_sibling_db_init_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-sibling-db-init")
}

/// GAP-2 (sibling-module init strip). The `db`-CAF SSR client-leg, but with
/// `init` factored into the SIBLING module `Boot` (the sky-lang.org shape),
/// resolved through the import graph — not the entry source. Before the fix the
/// frontend init-command strip read the ENTRY only, so the sibling `Boot.init`
/// was copied VERBATIM into the wasm frontend with its `Cmd.perform (Db.query db
/// …)`, leaving `Undefined name: db` + the server-only `Db.query` kernel. The fix
/// strips the sibling's init to `Cmd.none` in its frontend copy, drops the
/// dangling `Conn` (backend-only) + `Std.Db` (server-only) imports, and still
/// emits + wires the client model decoder from the sibling init's pure model.
#[test]
fn spa_ssr_sibling_db_init_is_stripped_in_the_frontend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_sibling_db_init_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the sibling-db-init fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The crux: the FRONTEND copy of the SIBLING `Boot` module is stripped to
    // `Cmd.none` and carries no `db` / `Db.*` / backend-only-module reference.
    // Holds without a Go toolchain (the `.sky` is generated before any go build). ──
    let boot = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Boot.sky"))
        .unwrap_or_else(|_| panic!("generated frontend Boot.sky must exist:\n{log}"));
    let boot_code = strip_line_comments(&boot);
    assert!(
        boot_code.contains("Cmd.none"),
        "GAP-2: the sibling `Boot.init` must be stripped to `Cmd.none`:\n{boot}"
    );
    assert!(
        !references_word_test(&boot_code, "db"),
        "GAP-2: the frontend `Boot` must NOT reference the `db` CAF:\n{boot}"
    );
    for needle in ["Db.query", "import Std.Db", "import Conn"] {
        assert!(
            !boot_code.contains(needle),
            "GAP-2: the frontend `Boot` must NOT contain `{needle}`:\n{boot}"
        );
    }

    // The model DECODER is still emitted + wired (derived from the SIBLING init's
    // pure model), so the client boots from `#sky-model` — symmetric with the
    // backend embed.
    let fe_main =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated frontend Main.sky must exist:\n{log}"));
    let fe_main_code = strip_line_comments(&fe_main);
    assert!(
        fe_main_code.contains("spaModelDecoder_ jsonStr_ =")
            && fe_main_code.contains("Codec.fromJson (Codec.auto")
            && fe_main_code.contains("|> Spa.withModelDecoder spaModelDecoder_")
            && fe_main_code.contains("import Std.Codec"),
        "GAP-2: the frontend must emit + wire a model decoder for the sibling init:\n{fe_main}"
    );

    // ── The BACKEND keeps the `db` CAF (in `Conn`) + settles the read. ──
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Conn.sky"))
        .unwrap_or_else(|_| panic!("generated backend Conn.sky must exist:\n{log}"));
    assert!(
        backend.contains("db =") && backend.contains("Db.open"),
        "GAP-2: the `db` CAF must remain in the BACKEND `Conn` module:\n{backend}"
    );
    let backend_main =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    assert!(
        backend_main.contains("spaSsrSettleFull routed cmd0 update"),
        "GAP-2: the sibling init must be resolved GET-safe → a data-resolve settle:\n{backend_main}"
    );

    // ── Go-gated e2e: the whole thing builds; the wasm frontend links with no
    // `Db_*` kernel. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "GAP-2: --target web:app must build end-to-end:\n{log}"
    );
    let fe_go =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"))
            .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query") && !fe_go.contains("Db_open"),
            "GAP-2: the emitted wasm frontend Go must contain no Db_* kernel"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
}

fn mixed_routes_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-mixed-routes")
}

/// GAP-1 (mixed page + `App.api` routes) + GAP-2 together — the full sky-lang.org
/// shape. `withRoutes (Routes.routes ++ apiRoutes)` mixes page routes (sibling
/// `Routes`) with server `App.api` endpoints (`apiRoutes`, its handlers reaching
/// server effects), and `init` is a sibling db-read. Before the fix the
/// synthesised client `spaRoutes_` referenced the server-tainted `apiRoutes` the
/// split drops → the client entry failed to compile (`Undefined name:
/// spaRoutes_`). The fix partitions `withRoutes`: page routes drive the client
/// `spaRoutes_`, the api endpoints become a BACKEND-only `spaApiRoutes_` the
/// server mounts via `App.apiServerRoute`.
#[test]
fn splits_mixed_page_and_api_routes() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&mixed_routes_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the mixed-routes fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── GAP-1 client leg: the synthesised client `spaRoutes_` carries ONLY the
    // page routes; it does NOT reference the server `apiRoutes` binding (which the
    // split drops) nor any api handler. Holds without a Go toolchain. ──
    let fe_main =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated frontend Main.sky must exist:\n{log}"));
    let fe_code = strip_line_comments(&fe_main);
    // The client router also learns every `App.api` path as a server route, so
    // a link to one is a full navigation and never a client page match.
    assert!(
        fe_code.contains("|> Spa.withRoutes (spaRoutes_ ++ [")
            && fe_code.contains("Spa.serverRoute \"GET /items.json\"")
            && fe_code.contains("Spa.serverRoute \"GET /healthz\"")
            && fe_code.contains("spaRoutes_ ="),
        "GAP-1: the frontend must define the page-only `spaRoutes_` and wire it with \
         every App.api path as a server route:\n{fe_main}"
    );
    for needle in [
        "apiRoutes",
        "spaApiRoutes_",
        "handleItemsApi",
        "handleHealthz",
    ] {
        assert!(
            !references_word_test(&fe_code, needle),
            "GAP-1: the client `spaRoutes_`/entry must NOT reference the api binding `{needle}`:\n{fe_main}"
        );
    }

    // ── GAP-1 backend leg: the api endpoints are mounted BACKEND-ONLY, via
    // `App.apiServerRoute spaApiRoutes_`, and `spaApiRoutes_` carries the api
    // route source. ──
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    assert!(
        backend.contains("spaApiRoutes_ =")
            && backend.contains("++ List.concatMap App.apiServerRoute spaApiRoutes_"),
        "GAP-1: the backend must mount the api routes via `App.apiServerRoute spaApiRoutes_`:\n{backend}"
    );
    // The api handlers survive into the backend (they run there).
    assert!(
        backend.contains("handleItemsApi") && backend.contains("handleHealthz"),
        "GAP-1: the api handlers must remain in the BACKEND tree:\n{backend}"
    );

    // ── GAP-1 page routes still SSR, ahead of the static NotFound fallback. ──
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "GAP-1: page routes must SSR ahead of the static NotFound fallback:\n{backend}"
    );

    // ── GAP-2 within the mixed app: the sibling `Boot.init` is stripped. ──
    let boot = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Boot.sky"))
        .unwrap_or_else(|_| panic!("generated frontend Boot.sky must exist:\n{log}"));
    let boot_code = strip_line_comments(&boot);
    assert!(
        boot_code.contains("Cmd.none")
            && !references_word_test(&boot_code, "db")
            && !boot_code.contains("Db.query"),
        "GAP-2: the sibling `Boot.init` must be stripped to `Cmd.none` in the frontend:\n{boot}"
    );

    // ── Go-gated e2e: the whole `--target web:app` build succeeds and the wasm
    // frontend links with no `Db_*` kernel. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "GAP-1: --target web:app must build end-to-end:\n{log}"
    );
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "GAP-1: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );
    let fe_go =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"))
            .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query"),
            "GAP-2: the emitted wasm frontend Go must contain no Db_query kernel"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
}

fn dup_route_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-ssr-dup-route")
}

/// FINDING A. A path that is BOTH an `App.route` PAGE route (`/admin/login`) AND
/// an `App.api "GET /admin/login"` server endpoint (the sky-lang.org OAuth-entry
/// shape). The App→Spa synthesis mounts the page route as a per-route SSR
/// `Server.api "GET /admin/login" ssrHandler` AND the api endpoint via
/// `App.apiServerRoute spaApiRoutes_`; both register `GET /admin/login` on Go's
/// mux, which PANICS at boot on the duplicate pattern (`http.ServeMux`),
/// crash-looping the backend. The api endpoint must WIN — the SSR page mount for
/// the same METHOD+PATH is suppressed — so the backend boots. (The Live build
/// tolerates the mix via its single `/` dispatcher, so this is split-only.)
#[test]
fn mixed_page_and_get_api_route_on_same_path_does_not_double_register() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&dup_route_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the dup-route fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let backend_raw =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated backend Main.sky must exist:\n{log}"));
    // Strip `--` comments: the fixture's own doc comment is copied verbatim into
    // the backend and mentions `Server.api "GET /admin/login" ssrHandler` in
    // prose — a `.contains` on the raw source would match THAT, not a real
    // registration. Assert against the code only.
    let backend = strip_line_comments(&backend_raw);

    // ── The api endpoints are mounted (the api handler is the winner). ──
    assert!(
        backend.contains("spaApiRoutes_ =")
            && backend.contains("++ List.concatMap App.apiServerRoute spaApiRoutes_"),
        "FINDING A: the api endpoints must still mount via `App.apiServerRoute`:\n{backend}"
    );

    // ── The colliding page's SSR GET mount is SUPPRESSED — the mux registers
    // `GET /admin/login` exactly once (from the api side), so the backend boots.
    // Before the fix the SSR block ALSO emitted this line → duplicate → panic. ──
    assert!(
        !backend.contains("Server.api \"GET /admin/login\" ssrHandler"),
        "FINDING A: the SSR page mount for `/admin/login` must be suppressed \
         (it collides with `App.api \"GET /admin/login\"`), else the mux \
         double-registers and boot-panics:\n{backend}"
    );

    // ── Non-colliding page routes STILL SSR-mount (the dedupe is scoped to the
    // exact method+path collision, not a blanket suppression). ──
    assert!(
        backend.contains("Server.api \"GET /items\" ssrHandler")
            && backend.contains("Server.api \"GET /{$}\" ssrHandler"),
        "FINDING A: non-colliding page routes must keep their SSR GET mounts:\n{backend}"
    );

    // ── Go-gated real proof: the built backend BOOTS without a
    // duplicate-pattern panic (the actual failure this fixes). ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "FINDING A: --target web:app must build end-to-end:\n{log}"
    );
    let app_bin = proj.join(".skyapp/web-app/.split/backend/sky-out/app");
    assert!(app_bin.is_file(), "backend binary must exist:\n{log}");

    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let boot_log = backend_dir.join("boot.log");
    let logf = std::fs::File::create(&boot_log).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", "8973")
        .stdout(logf.try_clone().unwrap())
        .stderr(logf)
        .spawn()
        .expect("spawn the split backend");
    // Give the mux setup (where the duplicate-pattern panic fires, before the
    // listen loop) time to run, then check the process is still alive.
    std::thread::sleep(std::time::Duration::from_millis(1500));
    let status = child.try_wait().expect("poll the backend process");
    let mut boot = String::new();
    use std::io::Read as _;
    let _ = std::fs::File::open(&boot_log).and_then(|mut f| f.read_to_string(&mut boot));
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    assert!(
        status.is_none(),
        "FINDING A: the split backend must BOOT, not exit at mux setup with a \
         duplicate-registration panic. It exited early ({status:?}); boot log:\n{boot}"
    );
    assert!(
        !boot.contains("multiple registrations") && !boot.contains("panic:"),
        "FINDING A: the backend logged a mux/panic error at boot:\n{boot}"
    );
}

fn static_assets_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-static-assets")
}

/// FINDING C. A `Std.App` web app that DECLARES a served static-file dir
/// (`static = "brand"`, mounted at `staticUrl = "/brand"`) with a real asset at
/// `brand/screenshots/spa-web.png`. The Live runtime mounts that dir; the split
/// backend serves only `frontend/dist`, so before the fix every `/brand/…` asset
/// 404'd under `--target web:app`. The split must copy the declared dir into
/// `frontend/dist/brand/` (at the mount prefix, structure preserved) so the
/// generated backend's `Server.static "/" "../frontend/dist"` serves it. The
/// dir is written by the split GENERATOR (before any build), so this holds
/// without a Go toolchain.
#[test]
fn declared_static_dir_is_propagated_into_the_frontend_dist() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&static_assets_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the static-assets fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The declared static dir lands in the frontend dist at the mount prefix,
    // structure preserved — so the backend serves `/brand/screenshots/spa-web.png`
    // same-origin. Written by the generator, so it holds even without Go.
    let asset = proj.join(".skyapp/web-app/.split/frontend/dist/brand/screenshots/spa-web.png");
    assert!(
        asset.is_file(),
        "FINDING C: the declared static dir must be copied into the frontend \
         dist at its mount prefix (expected {} — split log:\n{log})",
        asset.display()
    );
    // Byte-identical to the source asset (a real copy, not a stub).
    let want =
        std::fs::read(static_assets_fixture_dir().join("brand/screenshots/spa-web.png")).unwrap();
    let got = std::fs::read(&asset).unwrap();
    assert_eq!(
        got, want,
        "FINDING C: the propagated asset must be byte-identical to the source"
    );
    // Directory structure is preserved (the top-level asset too).
    assert!(
        proj.join(".skyapp/web-app/.split/frontend/dist/brand/logo.png")
            .is_file(),
        "FINDING C: nested + top-level assets under the static dir must be copied"
    );

    // TRANSPARENT RUNTIME-UPLOAD CARRY. The Live runtime serves its static dir
    // from disk at request time, so a file WRITTEN at runtime under that dir
    // (`File.writeFile "brand/…"`) is served immediately. The build-time dist
    // copy above cannot hold a runtime write. So the generated backend must ALSO
    // emit a LIVE `Server.static "/brand" "brand"` mount (the dir relative to the
    // backend cwd, where its runtime writes land), registered BEFORE the `/`
    // catch-all, and the committed seed assets must be staged into `backend/brand`
    // so seed + runtime uploads serve from one place. Written by the generator,
    // so this holds without a Go toolchain.
    let back = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("the generated backend source must exist");
    // Anchor inside the route block: the module doc comment copied to the top of
    // the backend mentions `Server.static "/" "../frontend/dist"`, so a naive
    // whole-file find would match the comment, not the route.
    let listen = back
        .find("Server.listen")
        .expect("backend must have Server.listen");
    let routes = &back[listen..];
    let live = routes
        .find("Server.static \"/brand\" \"brand\"")
        .unwrap_or_else(|| {
            panic!(
                "the backend must emit a LIVE static mount for runtime uploads \
             (Server.static \"/brand\" \"brand\") — split log:\n{log}"
            )
        });
    let catch_all = routes
        .find("\"../frontend/dist\"")
        .expect("the backend must still serve the dist catch-all");
    assert!(
        live < catch_all,
        "the live static mount must be registered BEFORE the dist catch-all \
         (Go 1.22 mux longest-prefix), else asset GETs would not reach it"
    );
    // Committed seed assets are staged into backend/<dir> — the live dir the mount
    // serves and the cwd-relative dir the app's runtime writes land in.
    assert!(
        proj.join(".skyapp/web-app/.split/backend/brand/screenshots/spa-web.png")
            .is_file(),
        "FINDING C: committed seed assets must be staged into backend/<dir> so \
         the live mount serves them alongside runtime uploads"
    );

    // ── Go-gated e2e: the whole `--target web:app` build succeeds and the wasm
    // frontend is staged ALONGSIDE the propagated static dir (stage_web_bundle
    // must not clobber it). ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "FINDING C: --target web:app must build end-to-end:\n{log}"
    );
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "FINDING C: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );
    assert!(
        dist.join("brand/screenshots/spa-web.png").is_file(),
        "FINDING C: the propagated static asset must survive the frontend build \
         (stage_web_bundle must not wipe dist/):\n{log}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

fn have_sqlite3() -> bool {
    Command::new("sqlite3")
        .arg("--version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// SSR CLIENT-LEG (design §4.4/§4.5, blocker #3). An `init` that reads through a
/// **`db` CAF** — the sky-lang.org shape. The `db` binding reaches `Db.open`, so
/// the split routes it to the BACKEND ONLY; kept verbatim, the client `init`'s
/// `Cmd.perform (Db.query db …)` would leave `Undefined name: db` in the wasm
/// frontend. The client-leg fix STRIPS `init`'s command to `Cmd.none` in the
/// frontend (the server settles the read + embeds `#sky-model`; the client boots
/// from that blob), so the client tree compiles WITHOUT the `db` CAF while the
/// backend still SSRs + embeds the resolved rows.
#[test]
fn spa_ssr_db_client_leg_excludes_the_db_caf() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_db_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR db-init fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The crux: the FRONTEND (wasm client) source compiles WITHOUT the `db`
    // CAF. `init`'s command is stripped to `Cmd.none`; no `db`/`Db.*` reference
    // survives into the client tree. This assertion holds without a Go toolchain
    // (the frontend `.sky` is generated before any `go build`). ──
    let frontend =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated frontend entry must exist:\n{log}"));
    // Scan CODE only — the generated frontend carries the module doc comment,
    // which legitimately mentions `db` / `Db.query`. Strip `--` line comments so
    // the assertions test references in code, not prose.
    let frontend_code = strip_line_comments(&frontend);
    assert!(
        !references_word_test(&frontend_code, "db"),
        "SSR client-leg: the frontend tree must NOT reference the `db` CAF:\n{frontend}"
    );
    for needle in ["Db.query", "Db.open", "Std.Db"] {
        assert!(
            !frontend_code.contains(needle),
            "SSR client-leg: the frontend tree must NOT reference `{needle}`:\n{frontend}"
        );
    }
    // The strip landed: init returns `Cmd.none`, and the pure model is preserved.
    assert!(
        frontend_code.contains("init () =") && frontend_code.contains("Cmd.none"),
        "SSR client-leg: the frontend `init` must be stripped to `Cmd.none`:\n{frontend}"
    );
    // The model DECODER (blocker #1/#2) is emitted + wired onto the config, so the
    // client can boot from `#sky-model` — symmetric with the backend embed.
    assert!(
        frontend_code.contains("spaModelDecoder_ jsonStr_ =")
            && frontend_code.contains("Codec.fromJson (Codec.auto")
            && frontend_code.contains("|> Spa.withModelDecoder spaModelDecoder_")
            && frontend_code.contains("import Std.Codec"),
        "SSR client-leg: the frontend must emit + wire a model decoder:\n{frontend}"
    );
    // P2 client persistence: the SYMMETRIC model ENCODER is emitted beside the
    // decoder (same `Codec.auto spaModelBlank_`) and wired onto the config, plus
    // the protected-session-fields builder so the runtime persists scratch state
    // and keeps the session from the SSR seed on restore.
    assert!(
        frontend_code.contains("spaModelEncoder_ m_ =")
            && frontend_code.contains("Codec.toJson (Codec.auto spaModelBlank_) m_")
            && frontend_code.contains("|> Spa.withModelEncoder spaModelEncoder_")
            && frontend_code.contains("|> Spa.withPersistProtectedFields "),
        "P2: the frontend must emit + wire a model encoder + protected fields:\n{frontend}"
    );

    // ── The BACKEND still resolves + settles the read + embeds the model. ──
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("generated backend entry must exist:\n{log}"));
    for needle in [
        "spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path",
        "spaSsrSettleFull routed cmd0 update",
        // The first-paint encoder is typed with the app's model (so a field
        // `init` leaves unconstrained still has a codec element).
        "Codec.toJson (Codec.auto m_) m_",
        "spaSsrModelJson_ resolved",
    ] {
        assert!(
            backend.contains(needle),
            "SSR client-leg: the backend must carry `{needle}`:\n{backend}"
        );
    }
    // The `db` CAF DOES survive into the backend (it is server-owned).
    assert!(
        backend.contains("db =") && backend.contains("Db.open"),
        "SSR client-leg: the `db` CAF must remain in the BACKEND tree:\n{backend}"
    );

    // ── Go-gated e2e: the whole thing builds, and (with sqlite3 to seed the DB)
    // the embedded `#sky-model` carries the SERVER-RESOLVED rows a crawler sees. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR client-leg: --target web:app must build end-to-end:\n{log}"
    );
    // The wasm frontend actually links with no `db`/`Db_*` symbol.
    let fe_go =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"))
            .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            !fe_go.contains("Db_query") && !fe_go.contains("Db_open"),
            "SSR client-leg: the emitted wasm frontend Go must contain no Db_* kernel"
        );
    }

    if !required(Need::Sqlite3, have_sqlite3()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let db_path = backend_dir.join("app.db");
    let seed = Command::new("sqlite3")
        .arg(&db_path)
        .arg("CREATE TABLE items(name TEXT); INSERT INTO items(name) VALUES('Alpha Widget'),('Beta Gadget'),('Gamma Gizmo');")
        .status()
        .expect("seed sqlite db");
    assert!(seed.success(), "seeding the sqlite db must succeed");

    let port = 8977u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(backend_dir.join("sky-out/app"))
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("SSR_DB_PATH", "app.db")
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled SSR db backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("SSR db backend never reported listening on :{port}");
    }
    let items_body = curl_body_p(port, "/items");
    let _ = child.kill();
    let _ = child.wait();

    let items_body = items_body.expect("GET /items should return a body");
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"ItemsPage""#)
            && blob.contains("Alpha Widget")
            && blob.contains("Gamma Gizmo"),
        "SSR client-leg: the #sky-model blob must decode to the SERVER-RESOLVED \
         rows (from the `db` read the client never runs). Blob was:\n{blob}"
    );
    assert!(
        items_body.contains("data-sky-ssr") && items_body.contains("Item list:"),
        "SSR client-leg: GET /items must carry the server-rendered, crawlable body:\n{items_body}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// HYDRATION-LOSS regression (the sky-lang.org blog bug). A model with a NESTED
/// RECORD collection (`posts : List Post`) must survive the client hydration
/// decoder. The decoder is `Codec.fromJson (Codec.auto blank)`; if `blank` is an
/// INLINE literal (`Codec.auto ({ page = Home, posts = [] })`), the constrained
/// split-frontend module type-checks it to a STRUCTURAL row and lowers the empty
/// `posts = []` with its element type ERASED to `[]any`. `Codec.auto` then
/// reflects `kind interface`, `Codec.fromJson` returns `Err`, and the boot path
/// SILENTLY falls back to `init` — so every `Post` the server embedded in
/// `#sky-model` is dropped on hydration, with no error (the home page, whose
/// model needs no nested record, hydrated fine; the blog list collapsed to empty).
/// The fix hoists the blank to a top-level ANNOTATED binding
/// (`spaModelBlank_ : Model`) so codegen pins the nominal `List Post` element
/// type and the decoder's `Codec.auto` matches the encoder's byte-for-byte.
///
/// `spa-ssr-db` never caught this: its model is `items : List String`, and a
/// `String` element round-trips through `[]any` unharmed.
#[test]
fn spa_ssr_nested_record_model_blank_pins_the_nominal_type_so_hydration_is_lossless() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_nested_record_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the SSR nested-record fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // ── The FIX (no Go toolchain needed): the client model decoder must derive
    // its `Codec.auto` from a top-level ANNOTATED blank binding, NOT an inline
    // literal that erases nested element types to `[]any`. This is the
    // deterministic RED→GREEN gate: pre-fix the frontend had no `spaModelBlank_`
    // binding and used the inline `Codec.auto ({ … })` form. ──
    let frontend =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .unwrap_or_else(|_| panic!("generated frontend entry must exist:\n{log}"));
    let frontend_code = strip_line_comments(&frontend);
    assert!(
        frontend_code.contains("spaModelBlank_ :") && frontend_code.contains("spaModelBlank_ ="),
        "SSR hydration: the client model blank must be a top-level ANNOTATED \
         binding (`spaModelBlank_ : Model`) so its nested collection element types \
         survive lowering:\n{frontend}"
    );
    assert!(
        frontend_code.contains("Codec.fromJson (Codec.auto spaModelBlank_)"),
        "SSR hydration: the decoder must derive `Codec.auto` from the annotated \
         `spaModelBlank_` binding:\n{frontend}"
    );
    assert!(
        !frontend_code.contains("Codec.fromJson (Codec.auto ({"),
        "SSR hydration: the decoder must NOT use the inline blank literal that \
         erases nested element types to `[]any`:\n{frontend}"
    );

    // ── Go-gated: the annotated blank type-checks in the constrained frontend
    // module (the risk the fix takes on) and the frontend links, and the emitted
    // Go pins the model's `posts` field to the nominal `Post` record — NOT `[]any`.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "SSR nested-record: --target web:app must build end-to-end (the annotated \
         blank must type-check in the constrained frontend module):\n{log}"
    );
    let fe_go =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/sky-out/main.go"))
            .unwrap_or_default();
    if !fe_go.is_empty() {
        assert!(
            fe_go.contains("Posts []Main_Post_R"),
            "SSR nested-record: the emitted frontend model must pin `posts` to the \
             nominal `Post` record slice (`[]Main_Post_R`); an erased `[]any` field \
             is what `codec_auto` cannot decode → silent hydration loss"
        );
    }

    let _ = std::fs::remove_dir_all(&proj);
}

/// Drop `--` line comments (the generated frontend carries the module doc
/// comment, which mentions `db`/`Db.*` in prose). No `--` appears inside a string
/// literal in the generated frontend's decls, so a per-line cut at the first `--`
/// is sufficient to isolate code.
fn strip_line_comments(src: &str) -> String {
    src.lines()
        .map(|l| match l.find("--") {
            Some(i) => &l[..i],
            None => l,
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Whole-word membership test mirroring `spa_split::references_word` (that helper
/// is crate-private). Used only to assert the frontend does not reference `db`.
fn references_word_test(hay: &str, needle: &str) -> bool {
    let is_ident = |b: u8| b.is_ascii_alphanumeric() || b == b'_';
    let bytes = hay.as_bytes();
    let mut from = 0;
    while let Some(rel) = hay[from..].find(needle) {
        let i = from + rel;
        let before_ok = i == 0 || !is_ident(bytes[i - 1]);
        let after = i + needle.len();
        let after_ok = after >= bytes.len() || !is_ident(bytes[after]);
        if before_ok && after_ok {
            return true;
        }
        from = i + needle.len();
    }
    false
}

// Full response BODY of `GET http://127.0.0.1:<port><path>` (P3 e2e helper).
fn curl_body_p(port: u16, path: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl").args(["-s", &url]).output().ok()?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
}

// POST a JSON `body` to `path` and return (status_code, response_body). Used by
// the server-side guard test (fix 5): a denied `/_rpc/<Msg>` must answer 403.
fn curl_post_status_body(port: u16, path: &str, body: &str) -> Option<(u32, String)> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args([
            "-s",
            "-w",
            "\n%{http_code}",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-d",
            body,
            &url,
        ])
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let idx = s.rfind('\n')?;
    let (resp_body, code) = s.split_at(idx);
    let code = code.trim().parse::<u32>().ok()?;
    Some((code, resp_body.to_string()))
}

// POST a JSON `body`, optionally sending a `Cookie:` header, and return
// (status, response body, the `sky_sid=<value>` from a Set-Cookie response
// header if present). Used by the stateless-signed-session e2e: it must capture
// the login cookie and replay it on the admin call. `-D -` dumps the response
// headers to stdout ahead of the body, separated by the first blank line.
fn curl_post_full(
    port: u16,
    path: &str,
    body: &str,
    cookie: Option<&str>,
) -> Option<(u32, String, Option<String>)> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> = vec![
        "-s".into(),
        "-D".into(),
        "-".into(),
        "-o".into(),
        "-".into(),
        "-X".into(),
        "POST".into(),
        "-H".into(),
        "Content-Type: application/json".into(),
        "-d".into(),
        body.into(),
    ];
    if let Some(c) = cookie {
        args.push("-H".into());
        args.push(format!("Cookie: {c}"));
    }
    args.push(url);
    let out = Command::new("curl").args(&args).output().ok()?;
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let mut status = 0u32;
    let mut set_cookie: Option<String> = None;
    let mut body_started = false;
    let mut resp_body = String::new();
    for line in s.lines() {
        if body_started {
            resp_body.push_str(line);
            resp_body.push('\n');
            continue;
        }
        if line.starts_with("HTTP/") {
            if let Some(code) = line.split_whitespace().nth(1) {
                if let Ok(c) = code.parse::<u32>() {
                    status = c;
                }
            }
        } else if line.to_ascii_lowercase().starts_with("set-cookie:") {
            let v = line[line.find(':').unwrap() + 1..].trim();
            // v0.27.0: the Sky.Spa session cookie is `sky_spa` (A-2b).
            if v.starts_with("sky_spa=") {
                set_cookie = Some(v.split(';').next().unwrap_or(v).trim().to_string());
            }
        } else if line.trim().is_empty() {
            body_started = true;
        }
    }
    Some((status, resp_body.trim_end().to_string(), set_cookie))
}

fn signed_session_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-signed-session")
}

/// STATELESS SIGNED SESSION (security). Under `--target web:app` a Sky.Live
/// app's `update` becomes `/_rpc/<Msg>` handlers built from the CLIENT-supplied
/// wire model, so a branch that gates on `model.session` for a trust decision
/// would trust a FORGEABLE wire value. The backend must instead sign the identity
/// projection into an httpOnly `sky_sid` cookie on login and VERIFY that cookie
/// on every RPC, taking the session from the cookie — never from the wire.
///
/// Two layers of proof:
///   * emission (no Go): the backend imports Std.Auth; the admin branch's run
///     model takes `session` from `verifiedSession_` (the cookie), not the wire
///     `p.session`; the login branch signs a Set-Cookie; the session codec is
///     reused from the resolver (Shared), never hand-rolled.
///   * Go-gated e2e: a forged `session={role:admin}` with NO cookie must NOT run
///     the admin effect; a login issues a signed cookie; the admin RPC WITH that
///     cookie runs the effect.
#[test]
fn spa_stateless_signed_session_defeats_wire_forgery() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&signed_session_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-signed-session fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let backend = std::fs::read_to_string(proj.join(".split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");

    // (a) the backend signs and verifies through the Spa session kernels, which
    // reuse Std.Auth's HS256 token logic and add the sign-out check.
    assert!(
        backend.contains("Ffi.kernel \"Spa_verifySession\"")
            && backend.contains("Ffi.kernel \"Spa_signSession\""),
        "signed session must use the Spa session kernels:\n{backend}"
    );
    // (b) the admin branch runs against the COOKIE-verified model, never the wire
    // payload. `mAuth` overrides `session` from `verifiedSession_ req base.session`
    // (the init value seeds the no-cookie case), and `update` runs on `mAuth`.
    assert!(
        backend.contains("{ m | session = verifiedSession_ req base.session }"),
        "the guard model must override session from the verified cookie:\n{backend}"
    );
    assert!(
        backend.contains("update (SaveAdmin p.content) mAuth"),
        "the admin branch must run against the cookie-verified model mAuth, not the wire model:\n{backend}"
    );
    assert!(
        backend.contains("verifiedSession_ req initVal ="),
        "the per-field verify helper must be emitted:\n{backend}"
    );
    // the verify helper reads the field codec from the signed claim, and falls
    // back to the init value (never the wire value) when there is no valid cookie.
    assert!(
        backend.contains("case Codec.fromJson spaSessionCodecSession_ claims.p0 of")
            && backend.contains("if tok == \"\" then\n        initVal"),
        "the verify helper must decode the signed claim and fall back to initVal:\n{backend}"
    );
    // (c) the login branch signs a Set-Cookie around its response.
    assert!(
        backend.contains("signedResponse_ req m2 (Server.json"),
        "the establishing (login) branch must sign a Set-Cookie:\n{backend}"
    );
    assert!(
        backend.contains(r#"Server.withCookie "sky_spa" tok "Path=/; HttpOnly; SameSite=Lax""#),
        "signedResponse_ must set an httpOnly sky_spa cookie:\n{backend}"
    );
    // A-2b / E-3 / A-6 (generator half; the runtime is S2's): the token is read
    // through the runtime (legacy `sky_sid` converted once), every answer moves
    // the cookie to `sky_spa`, and the sign-out store is opened at boot.
    assert!(
        backend.contains("spaSessionToken_ sessionSecret_ req")
            && backend.contains("spaWithSession_ req (")
            && !backend.contains("Server.getCookie \"sky_sid\""),
        "the session token and cookie move go through the runtime:\n{backend}"
    );
    let main_at = backend.find("\nmain =").expect("a main");
    assert!(
        backend[main_at..].contains("spaSessionBoot_ ()"),
        "main opens the sign-out store before it listens:\n{backend}"
    );
    // a SaveAdmin (write-set {note}, no session) must NOT sign a cookie.
    assert!(
        backend.contains("Task.succeed (Server.json (Codec.toJson saveAdminRespCodec"),
        "a branch that does not establish the session must answer plain (no Set-Cookie):\n{backend}"
    );
    // the framework sign-out endpoint exists.
    assert!(
        backend.contains(r#"Server.rpc "POST /_rpc/__spaSignOut" spaSignOutHandler"#),
        "the sign-out endpoint must be registered:\n{backend}"
    );
    // the session codec is DERIVED (reused from the wire resolver), not hand-rolled.
    let shared = std::fs::read_to_string(proj.join(".split/shared/Shared.sky"))
        .expect("generated shared module must exist");
    assert!(
        shared.contains("spaSessionCodecSession_ : Codec (Maybe Session)"),
        "the session field codec must be derived + exported from Shared:\n{shared}"
    );

    // ── Go-gated e2e ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "signed session: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8979u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        // Pin the signing secret so a lone process signs + verifies with one key.
        .env(
            "SKY_SPA_SESSION_SECRET",
            "0123456789abcdef0123456789abcdef0123456789",
        )
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-signed-session backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-signed-session backend never reported listening on :{port}");
    }

    // (a) forged session, NO cookie → the admin effect must NOT run.
    // `note` rides the request because `SaveAdmin` PRESERVES it on the
    // non-admin / no-session paths (`( model, … )`): without it the server would
    // rebuild `note` as the empty-Model default and clobber the client's value
    // (soundness bug #1). The forged `session` is still overridden server-side.
    let forged = curl_post_full(
        port,
        "/_rpc/SaveAdmin",
        r#"{"session":{"userId":"x","role":"admin"},"content":"pwned","note":"keep"}"#,
        None,
    );
    let admin_after_forge =
        std::fs::read_to_string(backend_dir.join("admin.txt")).unwrap_or_default();
    // (b) login → a signed sky_sid Set-Cookie.
    let login = curl_post_full(port, "/_rpc/LogIn", "{}", None);
    let cookie = login.as_ref().and_then(|(_, _, c)| c.clone());
    // (c) admin WITH the cookie → the admin effect runs.
    let admin_ok = cookie.as_deref().and_then(|c| {
        curl_post_full(
            port,
            "/_rpc/SaveAdmin",
            r#"{"session":null,"content":"legit","note":"keep"}"#,
            Some(c),
        )
    });
    let admin_after_ok = std::fs::read_to_string(backend_dir.join("admin.txt")).unwrap_or_default();

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let (fcode, _, _) = forged.expect("forged SaveAdmin should return");
    assert_eq!(
        fcode, 200,
        "the handler answers 200 (the inline gate no-ops on a missing session), never a crash"
    );
    assert_eq!(
        admin_after_forge, "",
        "SECURITY: a forged wire session must NOT run the admin effect — admin.txt must be absent/empty, was {admin_after_forge:?}"
    );
    assert!(cookie.is_some(), "login must issue a signed sky_sid cookie");
    let (acode, _, _) = admin_ok.expect("cookie'd SaveAdmin should return");
    assert_eq!(acode, 200, "cookie'd SaveAdmin should answer 200");
    assert_eq!(
        admin_after_ok, "legit",
        "with a valid signed cookie the admin effect MUST run — admin.txt must be \"legit\", was {admin_after_ok:?}"
    );
}

// Send one request with an optional `Cookie:` header and return (status, every
// `Set-Cookie` line of the answer, in order). `-D -` dumps the response headers
// ahead of the body; the body is discarded.
fn curl_set_cookies(
    port: u16,
    method: &str,
    path: &str,
    body: Option<&str>,
    cookie: &str,
) -> (u32, Vec<String>) {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> = vec![
        "-s".into(),
        "-D".into(),
        "-".into(),
        "-o".into(),
        "/dev/null".into(),
        "-X".into(),
        method.into(),
        "-H".into(),
        format!("Cookie: {cookie}"),
    ];
    if let Some(b) = body {
        args.push("-H".into());
        args.push("Content-Type: application/json".into());
        args.push("-d".into());
        args.push(b.into());
    }
    args.push(url);
    let out = Command::new("curl").args(&args).output().expect("run curl");
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let mut status = 0u32;
    let mut cookies = Vec::new();
    for line in s.lines() {
        if line.starts_with("HTTP/") {
            status = line
                .split_whitespace()
                .nth(1)
                .and_then(|c| c.parse().ok())
                .unwrap_or(0);
        } else if line.to_ascii_lowercase().starts_with("set-cookie:") {
            cookies.push(line[line.find(':').unwrap() + 1..].trim().to_string());
        }
    }
    (status, cookies)
}

/// RC R-2 (v0.27.0 real-app run). A request that carries the Spa session only
/// under the pre-v0.27 cookie name `sky_sid` is answered with TWO cookies: the
/// session moved to `sky_spa=…` and `sky_sid=; Max-Age=0` to expire the old
/// one (`Spa_sessionCookies`, runtime-go/rt/spa_session_legacy.go). The answer
/// passes through `spaWithSession_` in Sky code, where the response is narrowed
/// into the typed `Response` record (status / body / headers / contentType);
/// only the first cookie crossed that narrowing, so the server-rendered page
/// and every RPC answer sent `sky_spa=…` alone and the browser kept the stale
/// `sky_sid` forever. Both lines must reach the wire, on the SSR page and on
/// an RPC answer.
#[test]
fn spa_legacy_cookie_conversion_sends_every_cookie() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    if !required(Need::Go, have_go()) {
        return;
    }
    // A routed Std.App app with a session field: its server-rendered page and
    // its RPC answers both pass through `spaWithSession_`.
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-identity-slot");
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&fixture, &proj);
    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-identity-slot fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(output.status.success(), "the fixture must build:\n{log}");
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let backend = std::fs::read_to_string(backend_dir.join("src/Main.sky")).unwrap();
    assert!(
        backend.contains("ssrHandler req =\n    spaWithSession_ req (ssrInner_ req)"),
        "the SSR page must pass through spaWithSession_:\n{backend}"
    );

    let port = 19651u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(backend_dir.join("sky-out/app"))
        .current_dir(&proj)
        .env("PORT", port.to_string())
        .env(
            "SKY_SPA_SESSION_SECRET",
            "0123456789abcdef0123456789abcdef0123456789",
        )
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-identity-slot backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let server_log = std::fs::read_to_string(&log_path).unwrap_or_default();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-identity-slot backend never reported listening on :{port}:\n{server_log}");
    }

    // A signed session token, then sent under the OLD cookie name only.
    let sign_in = r#"{"session":null,"kind":"practitioner","uid":"u1"}"#;
    let login = curl_post_full(port, "/_rpc/SignIn", sign_in, None);
    let token = login
        .as_ref()
        .and_then(|(_, _, c)| c.clone())
        .and_then(|c| c.strip_prefix("sky_spa=").map(str::to_string));
    let legacy = token.as_ref().map(|t| format!("sky_sid={t}"));
    let page = legacy
        .as_deref()
        .map(|c| curl_set_cookies(port, "GET", "/", None, c));
    let rpc = legacy
        .as_deref()
        .map(|c| curl_set_cookies(port, "POST", "/_rpc/SignIn", Some(sign_in), c));
    let _ = child.kill();
    let _ = child.wait();
    let server_log = std::fs::read_to_string(&log_path).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&proj);

    let token = token.unwrap_or_else(|| {
        panic!("SignIn must issue a signed sky_spa cookie: {login:?}\n{server_log}")
    });
    // The page converts the session: it moves the token itself to `sky_spa`.
    // The RPC answer signs a fresh `sky_spa` of its own; either way the old
    // `sky_sid` must be expired on the same answer.
    for (what, answer, same_token) in [("the SSR page", page, true), ("an RPC answer", rpc, false)]
    {
        let (code, cookies) = answer.expect("the request must be sent");
        assert_eq!(code, 200, "{what}: status {code}, cookies {cookies:?}");
        let spa_prefix = if same_token {
            format!("sky_spa={token};")
        } else {
            "sky_spa=".to_string()
        };
        assert!(
            cookies.iter().any(|c| c.starts_with(&spa_prefix)),
            "{what} must carry the session on sky_spa: {cookies:?}"
        );
        assert!(
            cookies
                .iter()
                .any(|c| c.starts_with("sky_sid=;") && c.contains("Max-Age=0")),
            "{what} must expire the legacy sky_sid (every cookie reaches the wire): {cookies:?}"
        );
    }
}

fn session_revocation_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-session-revocation")
}

/// Spawn the compiled split backend in `backend_dir` on `port` with `envs`,
/// logging to `<backend_dir>/<log_name>`, and wait for its listening line.
/// Panics (after killing the child) when it never starts.
fn spawn_spa_backend(
    backend_dir: &std::path::Path,
    port: u16,
    log_name: &str,
    envs: &[(&str, String)],
) -> std::process::Child {
    let log_path = backend_dir.join(log_name);
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut cmd = Command::new(backend_dir.join("sky-out/app"));
    cmd.current_dir(backend_dir)
        .env("PORT", port.to_string())
        .env(
            "SKY_SPA_SESSION_SECRET",
            "0123456789abcdef0123456789abcdef0123456789",
        )
        .env_remove("SKY_LIVE_STORE")
        .env_remove("SKY_LIVE_STORE_PATH")
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file);
    for (k, v) in envs {
        cmd.env(k, v);
    }
    let mut child = cmd.spawn().expect("spawn the compiled split backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let log = std::fs::read_to_string(&log_path).unwrap_or_default();
        panic!("split backend never reported listening on :{port}:\n{log}");
    }
    child
}

/// SIGN-OUT REVOCATION (security, v0.27.0). The auto-split signs the session
/// projection into the `sky_sid` cookie. Before v0.27.0 that token was checked
/// only by its signature and its 30-day `exp`: sign-out cleared the cookie in
/// the browser, but a copy of the cookie taken before sign-out still signed the
/// user in until it expired. Sky.Live closed the same class in Phase 1A.
///
/// The token now carries a session id (`sid`). Sign-out records the id as ended
/// in the configured session store (for its remaining lifetime), and every
/// verification refuses an ended id. Proven end to end on the built backend:
///
///   * two replicas share one store: a sign-out on replica A refuses the old
///     cookie on A AND on B (the record is in the store, not in a process);
///   * both sign-out paths revoke: the framework endpoint `__spaSignOut` (the
///     client-side sign-out) and a server branch that clears the session;
///   * a fresh sign-in after a sign-out works;
///   * the default store (none configured) keeps the record across a restart,
///     and a cookie that was NOT signed out still works after the restart.
#[test]
fn spa_sign_out_revokes_the_signed_session_cookie() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&session_revocation_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-session-revocation fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let backend = std::fs::read_to_string(proj.join(".split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");

    // Emission: verification and signing go through the revocation-aware
    // kernels, and the sign-out endpoint ends the session server-side.
    assert!(
        backend.contains("Ffi.kernel \"Spa_verifySession\"")
            && backend.contains("case spaVerifySession_ sessionSecret_ tok of"),
        "the verify helper must check revocation (Spa_verifySession):\n{backend}"
    );
    assert!(
        !backend.contains("Auth.verifyToken sessionSecret_"),
        "no verify path may skip the revocation check:\n{backend}"
    );
    assert!(
        backend.contains("Ffi.kernel \"Spa_signSession\"")
            && backend.contains("signedResponse_ req m2 (Server.json"),
        "the establishing branch must sign through Spa_signSession with the request:\n{backend}"
    );
    assert!(
        backend.contains("Ffi.kernel \"Spa_endSession\"")
            && backend.contains("spaEndSession_ sessionSecret_"),
        "the sign-out endpoint must end the session server-side:\n{backend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "revocation fixture: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".split/backend");
    let admin = || std::fs::read_to_string(backend_dir.join("admin.txt")).unwrap_or_default();
    let save = |port: u16, content: &str, cookie: &str| {
        curl_post_full(
            port,
            "/_rpc/SaveAdmin",
            &format!(r#"{{"session":null,"content":"{content}","note":""}}"#),
            Some(cookie),
        )
        .expect("SaveAdmin should answer")
    };

    // ── Phase 1: two replicas sharing one sqlite session store. ──
    let shared_db = proj.join("shared-sessions.db");
    let store_env = vec![
        ("SKY_LIVE_STORE", "sqlite".to_string()),
        (
            "SKY_LIVE_STORE_PATH",
            shared_db.to_string_lossy().to_string(),
        ),
    ];
    let (pa, pb) = (8986u16, 8987u16);
    let mut a = spawn_spa_backend(&backend_dir, pa, "replica-a.log", &store_env);
    let mut b = spawn_spa_backend(&backend_dir, pb, "replica-b.log", &store_env);

    let login = curl_post_full(pa, "/_rpc/LogIn", "{}", None).expect("LogIn answers");
    let c1 = login.2.clone().expect("LogIn must issue a sky_sid cookie");
    let r_one = save(pb, "one", &c1);
    let after_one = admin();
    let signout =
        curl_post_full(pa, "/_rpc/__spaSignOut", "{}", Some(&c1)).expect("sign-out answers");
    let r_two = save(pa, "two", &c1);
    let after_two = admin();
    let r_three = save(pb, "three", &c1);
    let after_three = admin();
    let login2 = curl_post_full(pb, "/_rpc/LogIn", "{}", None).expect("LogIn answers");
    let c2 = login2
        .2
        .clone()
        .expect("a second LogIn must issue a cookie");
    let r_four = save(pa, "four", &c2);
    let after_four = admin();
    let logout =
        curl_post_full(pa, "/_rpc/LogOut", r#"{"note":""}"#, Some(&c2)).expect("LogOut answers");
    let r_five = save(pb, "five", &c2);
    let after_five = admin();

    let _ = a.kill();
    let _ = a.wait();
    let _ = b.kill();
    let _ = b.wait();
    let replica_logs = format!(
        "{}\n{}",
        std::fs::read_to_string(backend_dir.join("replica-a.log")).unwrap_or_default(),
        std::fs::read_to_string(backend_dir.join("replica-b.log")).unwrap_or_default()
    );

    // ── Phase 2: the default store (none configured), across a restart. ──
    let _ = std::fs::remove_file(backend_dir.join("admin.txt"));
    let pc = 8988u16;
    let mut c = spawn_spa_backend(&backend_dir, pc, "default-1.log", &[]);
    let c4 = curl_post_full(pc, "/_rpc/LogIn", "{}", None)
        .and_then(|r| r.2)
        .expect("LogIn on the default store must issue a cookie");
    let c5 = curl_post_full(pc, "/_rpc/LogIn", "{}", None)
        .and_then(|r| r.2)
        .expect("a second LogIn on the default store must issue a cookie");
    let signout4 =
        curl_post_full(pc, "/_rpc/__spaSignOut", "{}", Some(&c4)).expect("sign-out answers");
    let _ = c.kill();
    let _ = c.wait();
    let mut c = spawn_spa_backend(&backend_dir, pc, "default-2.log", &[]);
    let r_six = save(pc, "six", &c4);
    let after_six = admin();
    let r_seven = save(pc, "seven", &c5);
    let after_seven = admin();
    let _ = c.kill();
    let _ = c.wait();
    let default_logs = format!(
        "{}\n{}",
        std::fs::read_to_string(backend_dir.join("default-1.log")).unwrap_or_default(),
        std::fs::read_to_string(backend_dir.join("default-2.log")).unwrap_or_default()
    );
    let data_dir_db = backend_dir.join(".skydata/spa-sessions.db");
    let default_store_on_disk = data_dir_db.is_file();
    let _ = std::fs::remove_dir_all(&proj);

    let ctx = format!("replica logs:\n{replica_logs}\ndefault-store logs:\n{default_logs}");
    assert_eq!(r_one.0, 200, "{ctx}");
    assert_eq!(
        after_one, "one",
        "a valid cookie must act as the signed-in admin on the other replica: {ctx}"
    );
    assert_eq!(
        signout.0, 200,
        "the sign-out endpoint must answer 200: {ctx}"
    );
    assert!(
        signout.2.as_deref() == Some("sky_spa="),
        "the sign-out endpoint must clear the cookie, got {:?}: {ctx}",
        signout.2
    );
    assert_eq!(r_two.0, 200, "{ctx}");
    assert_eq!(
        after_two, "one",
        "SECURITY: a cookie copied before sign-out must NOT act as the user after sign-out (same replica): {ctx}"
    );
    assert_eq!(r_three.0, 200, "{ctx}");
    assert_eq!(
        after_three, "one",
        "SECURITY: a sign-out on replica A must refuse the old cookie on replica B: {ctx}"
    );
    assert_eq!(r_four.0, 200, "{ctx}");
    assert_eq!(
        after_four, "four",
        "a fresh sign-in after a sign-out must work: {ctx}"
    );
    assert_eq!(logout.0, 200, "{ctx}");
    assert!(
        logout.2.is_some(),
        "the server sign-out branch re-issues the (signed-out) cookie: {ctx}"
    );
    assert_eq!(r_five.0, 200, "{ctx}");
    assert_eq!(
        after_five, "four",
        "SECURITY: a server-branch sign-out must refuse the pre-sign-out cookie: {ctx}"
    );
    assert_eq!(signout4.0, 200, "{ctx}");
    assert!(
        default_store_on_disk,
        "with no store configured the record must live in the data dir: {ctx}"
    );
    assert_eq!(r_six.0, 200, "{ctx}");
    assert_eq!(
        after_six, "",
        "SECURITY: a signed-out cookie must stay refused after a restart (default store): {ctx}"
    );
    assert_eq!(r_seven.0, 200, "{ctx}");
    assert_eq!(
        after_seven, "seven",
        "a cookie that was NOT signed out must still work after a restart: {ctx}"
    );
}

// HTTP status code + Content-Type of `GET http://127.0.0.1:<port><path>`.
fn curl_status_ctype(port: u16, path: &str) -> Option<(u32, String)> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args([
            "-s",
            "-o",
            "/dev/null",
            "-w",
            "%{http_code} %{content_type}",
            &url,
        ])
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let mut it = s.splitn(2, ' ');
    let code = it.next()?.trim().parse::<u32>().ok()?;
    let ctype = it.next().unwrap_or("").trim().to_string();
    Some((code, ctype))
}

/// BUG-3. A type error must surface as the actual diagnostic (file:line +
/// caret), not a bare `1 type error(s)` count that discards where the error
/// is. Since v0.27.0 the build type-checks the user's own source before it
/// synthesises the client entry, so the error is reported at the user's line
/// (the `view` field of `App.app`), never in the synthesised entry.
/// Type-checking happens before any `go build`, so no Go is needed.
#[test]
fn web_app_type_error_reports_file_line_not_just_a_count() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    // `view` returns an `Int`, so the synthesised `Ui.layout [] (view model_)`
    // cannot type-check — a deterministic single type error in the derived entry.
    let main_sky = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Std.App as App
import Std.Sub as Sub
import Std.Cmd as Cmd
import Std.Ui as Ui


type alias Model =
    { count : Int }


type Msg
    = Noop


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )


view : Model -> Int
view model =
    model.count


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


app =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()


main : Task Error ()
main =
    App.run app
"#;
    let proj = scratch_std_app("brokenentry", main_sky);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert!(
        !output.status.success(),
        "a broken synthesised entry must fail the build"
    );
    // The rendered diagnostic — an Elm-style TYPE ERROR block with a file:line
    // header — must be present (BUG-3: the count alone used to be all we got).
    assert!(
        log.contains("TYPE ERROR") && log.contains("[E2"),
        "BUG-3: the actual type diagnostic (file:line + code) must be shown, not just a count:\n{log}"
    );
    // …at the user's own line, the `view` field, not in a synthesised file.
    assert!(
        log.contains("src/Main.sky:")
            && log.contains("in the `view` field of the record passed to `Std.App.app`")
            && !log.contains("SYNTHESISED"),
        "the error is reported at the user's `view` field:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

/// Multi-module TEA auto-split (the sky-lang.org SPA-SSR shape). The TEA core is
/// factored into SHARED modules — `Msg`/`Model`/`Page` in `State`, `init` in
/// `Data`, the route table in `Routes` — not the entry. `sky build --target
/// web:app` must resolve each through the module/import graph, not the entry
/// source text:
///
///   * GAP-A: inject the `Applied<Msg>` RPC-response variants into `State`'s
///     (imported) `Msg` union so the wasm FRONTEND compiles — the generated
///     frontend `update` references `AppliedSaveItem`, undefined before the fix
///     (`E1001`). And `Shared` must not re-import the TEA siblings (they reach
///     `Msg`), or the frontend cycles (`E1010`).
///   * GAP-B: resolve `init`'s DECLARING module (`Data`) for the GET-safe scan so
///     the SSR settle runs and `#sky-model` carries REAL resolved data.
///   * GAP-C: SSR the `/items` route whose literal lives in the sibling `Routes`
///     module, not inline in `spaRoutes_` — a non-root route must 200 with
///     server-rendered content, not fall through to `Server.static` (404).
///
/// The security spine still holds: the effectful `Store` module is backend-only
/// and never reaches the wasm frontend.
#[test]
fn splits_a_multi_module_app_with_tea_core_in_imported_modules() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&ssr_multimodule_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the multi-module SSR fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // The split ran + the emitted trees type-checked.
    assert!(
        log.contains("client/server split") || log.contains("Built Std.App entry"),
        "the split must run (emitted frontend + backend type-checked):\n{log}"
    );

    // GAP-A: the FRONTEND copy of the imported `Msg` module (`State`) carries the
    // generated `Applied<Msg>` variants + the `Shared` import they need.
    let fe_state =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/State.sky"))
            .expect("generated frontend State.sky must exist");
    assert!(
        fe_state.contains("| AppliedSaveItem (Result Error SaveItemResp)"),
        "GAP-A: the `Applied<Msg>` variant must be injected into the imported `Msg` union:\n{fe_state}"
    );
    assert!(
        fe_state.contains("import Shared"),
        "GAP-A: the injected Msg module must import `Shared` for the `<Msg>Resp` payload types:\n{fe_state}"
    );

    // GAP-A security spine: the effectful `Store` module never reaches the
    // frontend, and no server effect (`writeFile`/`saveItems`) leaks into it.
    assert!(
        !proj
            .join(".skyapp/web-app/.split/frontend/src/Store.sky")
            .exists(),
        "the backend-only `Store` module must NOT be emitted into the wasm frontend"
    );
    let fe_main =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .expect("generated frontend Main.sky must exist");
    // The `SaveItem` server branch is rewritten to an RPC (proving the effect
    // stays server-side); the frontend never calls `Store.saveItems` directly.
    assert!(
        fe_main.contains("Spa.rpc") && fe_main.contains("/_rpc/SaveItem"),
        "GAP-A: the server branch must be rewritten to an RPC in the frontend (effect stays server-side):\n{fe_main}"
    );

    // GAP-A cycle guard: `Shared` must not re-import the TEA sibling modules
    // (they reach `Msg`, which imports `Shared`) — that would be an `E1010`.
    let fe_shared =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Shared.sky"))
            .expect("generated frontend Shared.sky must exist");
    for sib in ["import State", "import Data", "import Routes"] {
        assert!(
            !fe_shared.contains(sib),
            "GAP-A: `Shared` must not import the TEA sibling `{sib}` (cycle):\n{fe_shared}"
        );
    }

    // GAP-B + GAP-C at the backend-source level.
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend Main.sky must exist");
    assert!(
        backend.contains("spaSsrSettleFull routed cmd0 update"),
        "GAP-B: `init` (in the sibling `Data`) must be resolved GET-safe → a data-resolve settle:\n{backend}"
    );
    assert!(
        backend.contains("Server.api \"GET /items\" ssrHandler"),
        "GAP-C: the `/items` route (literal in the sibling `Routes`) must be SSR-registered:\n{backend}"
    );
    let items_at = backend.find("Server.api \"GET /items\" ssrHandler");
    let static_at = backend.find("Server.staticNotFound \"/\" \"../frontend/dist\" ssrHandler");
    assert!(
        items_at.is_some() && static_at.is_some() && items_at < static_at,
        "GAP-C: per-route SSR routes must precede the static NotFound fallback:\n{backend}"
    );

    // ── Go-gated: the wasm FRONTEND built + the backend serves REAL per-route
    // data. ──
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "--target web:app must build end-to-end:\n{log}"
    );

    // GAP-A: the wasm frontend built to a hashed bundle (no `E1001`).
    let dist = proj.join(".skyapp/web-app/.split/frontend/dist");
    assert!(
        dist_has_wasm(&dist),
        "GAP-A: the wasm frontend must build to a content-hashed main.<hash>.wasm:\n{log}"
    );

    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");
    // Stage the data the settle reads (init: File.readFile "data/items.json").
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::copy(
        proj.join("data/items.json"),
        backend_dir.join("data/items.json"),
    )
    .unwrap();

    let port = 8976u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled multi-module SSR backend");
    let ready = wait_for_spa_backend(&log_path, 80);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&proj);
        panic!("multi-module SSR backend never reported listening on :{port}\nlog:\n{buf}");
    }
    let items_body = curl_body_p(port, "/items");
    let home_body = curl_body_p(port, "/");
    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    let items_body = items_body.expect("GET /items should return a body");
    let home_body = home_body.expect("GET / should return a body");

    // GAP-C + GAP-B: `/items` server-renders the RESOLVED item list (not a 404 /
    // loading state).
    assert!(
        items_body.contains("data-sky-ssr")
            && items_body.contains("Item list:")
            && items_body.contains("Alpha Widget")
            && items_body.contains("Beta Gadget")
            && items_body.contains("Gamma Gizmo"),
        "GAP-B/C: GET /items must carry the SERVER-RESOLVED item list. Body was:\n{items_body}"
    );
    // GAP-B: the embedded #sky-model blob carries the resolved model.
    let blob_start = items_body
        .find(r#"<script id="sky-model" type="application/json">"#)
        .expect("GAP-B: the #sky-model blob must be present");
    let blob = &items_body[blob_start..];
    let blob = &blob[..blob.find("</script>").expect("blob must close")];
    assert!(
        blob.contains(r#""page":"ItemsPage""#) && blob.contains("Alpha Widget"),
        "GAP-B: the #sky-model blob must decode to the RESOLVED model. Blob was:\n{blob}"
    );
    // Per-route body: `/` renders Home, not the Items view.
    let home_app = {
        let s = home_body
            .find(r#"<div id="app""#)
            .expect("home #app must exist");
        let e = home_body
            .find(r#"<script id="sky-model""#)
            .unwrap_or(home_body.len());
        &home_body[s..e]
    };
    assert!(
        home_app.contains("Welcome home") && !home_app.contains("Item list:"),
        "GAP-C: GET / must render Home's own view, not the Items view:\n{home_app}"
    );
}

// ---------------------------------------------------------------------------
// GAP-1 + GAP-2: `update` in a sibling module, and per-binding subset of a
// MIXED module (pure helper next to server effects). Both are GENERATOR-side.
// ---------------------------------------------------------------------------

fn sibling_update_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-sibling-update/src/Main.sky")
}

fn sibling_update_msg_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/spa-sibling-update-msg/src/Main.sky")
}

fn mixed_purity_fixture_entry() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-mixed-purity/src/Main.sky")
}

/// Strip Sky comments (`--` line, `{- -}` block) from `src` so the leak-grep
/// tests CODE, never a docstring. A fixture's own doc-comment legitimately names
/// the server helper it drops (`persist`, `Db`), and a comment can neither run an
/// effect nor import a module — so a name that survives ONLY in a comment is not
/// a leak. Best-effort (does not special-case string literals), sufficient for
/// the generated split sources, which carry no `--` inside string literals.
fn strip_sky_comments(src: &str) -> String {
    // Block comments first (nestable), then line comments.
    let mut no_block = String::with_capacity(src.len());
    let bytes: Vec<char> = src.chars().collect();
    let mut i = 0usize;
    let mut depth = 0usize;
    while i < bytes.len() {
        let c = bytes[i];
        let c2 = bytes.get(i + 1).copied();
        if c == '{' && c2 == Some('-') {
            depth += 1;
            i += 2;
            continue;
        }
        if depth > 0 && c == '-' && c2 == Some('}') {
            depth -= 1;
            i += 2;
            continue;
        }
        if depth == 0 {
            no_block.push(c);
        } else if c == '\n' {
            no_block.push('\n');
        }
        i += 1;
    }
    no_block
        .lines()
        .map(|l| l.split("--").next().unwrap_or(""))
        .collect::<Vec<_>>()
        .join("\n")
}

/// Concatenate every `*.sky` file under `dir` (recursively) into one string —
/// the whole-tree leak-grep surface, COMMENTS STRIPPED. A server kernel /
/// tainted-helper name found ANYWHERE under `frontend/` is a security leak, so
/// the check must see the whole subtree, not just `Main.sky`.
fn concat_sky_tree(dir: &std::path::Path) -> String {
    let mut out = String::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let rd = match std::fs::read_dir(&d) {
            Ok(rd) => rd,
            Err(_) => continue,
        };
        for e in rd.filter_map(|e| e.ok()) {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().and_then(|s| s.to_str()) == Some("sky") {
                out.push_str(&format!("\n----- {} -----\n", p.display()));
                out.push_str(&strip_sky_comments(
                    &std::fs::read_to_string(&p).unwrap_or_default(),
                ));
            }
        }
    }
    out
}

/// Build the generated backend (native) + frontend (wasm) of a split, Go-gated.
fn build_both_legs(out: &std::path::Path) {
    if !required(Need::Go, have_go()) {
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
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );
}

/// GAP-1: the TEA `update` lives in a SIBLING module (`Update`), its `Msg` in a
/// THIRD module (`Msgs`). The split must regenerate the partitioned `update` IN
/// its own frontend module copy — the pure arm client-local, the server arm an
/// RPC — and NEVER leak the server helper (`persist` -> `Db`) or the backend-only
/// `Conn` connection into the wasm frontend. Was refused before this fix.
#[test]
fn sibling_module_update_regenerates_in_its_own_frontend_copy() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            sibling_update_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed when `update` lives in a sibling module (GAP-1)"
    );

    // The sibling `update` is regenerated IN its own frontend module copy.
    let front_update = std::fs::read_to_string(out.join("frontend/src/Update.sky"))
        .expect("the frontend Update module must exist");
    assert!(
        front_update.contains("cleanDraft"),
        "the pure client helper `cleanDraft` must reach the frontend Update copy:\n{front_update}"
    );
    assert!(
        front_update.contains("Spa.rpc") && front_update.contains("/_rpc/Save"),
        "the server arm `Save` must become an RPC in the frontend Update copy:\n{front_update}"
    );

    // SECURITY — no server kernel / tainted helper / backend-only module anywhere
    // in the frontend tree.
    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in [
        "Db.",
        "persist",
        "loadTodos",
        "saveTodos",
        "import Conn",
        "Conn.",
        "System.getenv",
        "File.",
    ] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }
    assert!(
        !out.join("frontend/src/Conn.sky").exists(),
        "SECURITY LEAK: the backend-only Conn module must NOT be in the frontend"
    );

    // The backend keeps the effect + exposes the RPC endpoint.
    let back_update = std::fs::read_to_string(out.join("backend/src/Update.sky")).unwrap();
    assert!(
        back_update.contains("persist") && back_update.contains("Db.query"),
        "backend Update must keep the server helper `persist` -> `Db` (it runs it):\n{back_update}"
    );
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("Server.rpc \"POST /_rpc/Save\""),
        "backend must expose the generated RPC endpoint for `Save`:\n{back}"
    );

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

/// GAP-1 compose case: `update` AND `Msg` both live in the SIBLING module. The
/// module's frontend copy composes TWO per-module transforms — inject the
/// `Applied<Msg>` RPC variants into the `Msg` union, THEN regenerate `update` —
/// and still leaks no server binding.
#[test]
fn sibling_module_update_and_msg_compose_in_one_frontend_copy() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            sibling_update_msg_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed when `update` + `Msg` share a sibling module (GAP-1 compose)"
    );

    let front_update = std::fs::read_to_string(out.join("frontend/src/Update.sky"))
        .expect("the frontend Update module must exist");
    // GAP-A: the Applied<Msg> variant is injected into the Msg union here.
    assert!(
        front_update.contains("AppliedSave"),
        "the `AppliedSave` RPC variant must be injected into the sibling `Msg` union:\n{front_update}"
    );
    // GAP-1: update regenerated with the RPC arm + the pure helper.
    assert!(
        front_update.contains("cleanDraft")
            && front_update.contains("Spa.rpc")
            && front_update.contains("/_rpc/Save"),
        "the sibling `update` must be regenerated (pure arm + RPC arm):\n{front_update}"
    );

    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in [
        "Db.",
        "persist",
        "import Conn",
        "Conn.",
        "System.getenv",
        "File.",
    ] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

/// GAP-2: a MIXED module (`Store`) holds a PURE helper (`formatTotal`, called by
/// a CLIENT `update` arm) ALONGSIDE server effects (File / env / a 3-hop server
/// chain / a higher-order server pass). The split must emit a FRONTEND copy of
/// `Store` containing ONLY `formatTotal` — dropping every server binding — while
/// the backend copy stays the FULL module. Was refused before this fix.
#[test]
fn mixed_module_emits_pure_subset_to_frontend_effects_stay_backend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            mixed_purity_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split must succeed on a mixed-purity module (GAP-2)"
    );

    // The frontend Store subset exists and carries ONLY the pure helper.
    let front_store = std::fs::read_to_string(out.join("frontend/src/Store.sky"))
        .expect("the frontend Store subset must exist");
    assert!(
        front_store.contains("formatTotal"),
        "the pure helper `formatTotal` must reach the frontend Store subset:\n{front_store}"
    );

    // SECURITY — no server binding anywhere in the frontend tree.
    let front_tree = concat_sky_tree(&out.join("frontend"));
    for needle in [
        "File.",
        "System.getenv",
        "loadTodos",
        "saveTodos",
        "deepLoad",
        "midLoad",
        "leafLoad",
        "dataDir",
        "loadEach",
        "Db.",
    ] {
        assert!(
            !front_tree.contains(needle),
            "SECURITY LEAK: frontend tree contains `{needle}`:\n{front_tree}"
        );
    }

    // The frontend `update` keeps the client arm (via `formatTotal`) and RPCs the
    // server arm; the backend keeps the File effect.
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("formatTotal") && front.contains("/_rpc/Add"),
        "frontend Main must call `formatTotal` (client) and RPC `Add` (server):\n{front}"
    );
    let back_store = std::fs::read_to_string(out.join("backend/src/Store.sky")).unwrap();
    assert!(
        back_store.contains("File.") && back_store.contains("loadTodos"),
        "backend Store must keep the FULL module (File effects):\n{back_store}"
    );

    build_both_legs(&out);
    let _ = std::fs::remove_dir_all(&out);
}

// ---------------------------------------------------------------------------
// G5: `init`'s returned MODEL embeds a server read the wasm client cannot
// reproduce — the split must REFUSE with actionable guidance, not emit a
// frontend that references the backend-only read or silently drop the data.
// ---------------------------------------------------------------------------

fn repo_root() -> PathBuf {
    // CARGO_MANIFEST_DIR = <repo>/rust/crates/sky
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("..")
        .canonicalize()
        .expect("repo root")
}

fn init_model_read_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-init-model-read")
}

/// G5 refusal: `init` bakes a server read (`loadAll` -> `Db`) into its returned
/// MODEL. The split must return `Err` naming the read + the deferral fix, and
/// must NOT have written a frontend that references `loadAll` / `Db.` / `db`.
#[test]
fn init_model_embedding_a_server_read_is_refused_with_guidance() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let res = project::spa_split::generate(
        &repo_root(),
        &init_model_read_fixture_dir(),
        Some("Main"),
        &out,
        None,
        None,
    );

    let err = match res {
        Err(e) => e,
        Ok(_) => panic!(
            "a server read baked into `init`'s returned model must REFUSE the split (the wasm client cannot reproduce it)"
        ),
    };
    // Names the offending read and points at the deferral fix.
    assert!(
        err.contains("init") && err.contains("model"),
        "the refusal must be about `init`'s returned model, got:\n{err}"
    );
    assert!(
        err.contains("loadAll") || err.contains("Db"),
        "the refusal must NAME the offending server read (`loadAll` / `Db`), got:\n{err}"
    );
    assert!(
        err.contains("command") && err.contains("Got"),
        "the refusal must give the fix (defer to `init`'s command + a `Got<Field>` arm), got:\n{err}"
    );

    // Because it refused, NO frontend that references the backend-only read may
    // have been written — no silent leak, no dropped data.
    let front_dir = out.join("frontend");
    if front_dir.exists() {
        let front_tree = concat_sky_tree(&front_dir);
        for needle in ["loadAll", "Db.", "db "] {
            assert!(
                !front_tree.contains(needle),
                "REFUSED split must not have written a frontend referencing `{needle}`:\n{front_tree}"
            );
        }
    }

    let _ = std::fs::remove_dir_all(&out);
}

/// No false positive: the SUPPORTED deferred pattern — a server read placed in
/// `init`'s COMMAND (`Cmd.perform (Db.query …) GotItems`) with a PURE returned
/// model (`{ page = Home, items = [] }`) — must NOT trip the G5 refusal. Uses the
/// existing `spa-ssr-db` fixture, whose model is pure and read is in the command.
#[test]
fn server_read_deferred_to_init_command_is_not_refused() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let res = project::spa_split::generate(
        &repo_root(),
        &ssr_db_fixture_dir(),
        Some("Main"),
        &out,
        None,
        None,
    );

    // It may succeed, or fail for an UNRELATED reason, but it must NEVER fail
    // with the init-model refusal — the read is deferred to the command.
    if let Err(e) = &res {
        assert!(
            !e.contains("returned model embeds server read"),
            "the deferred-command pattern must NOT trip the init-model refusal (false positive):\n{e}"
        );
    }

    let _ = std::fs::remove_dir_all(&out);
}

/// A server branch's command runs — the end-to-end behaviour gate. `Reload`
/// returns `Cmd.perform (File.readFile "data/note.txt") Reloaded`, and
/// `Reloaded (Ok raw)` writes `raw` into `note`. Before server chaining the
/// generated handler DISCARDED the command, so `POST /_rpc/Reload` answered
/// with nothing. `Reloaded` is a client arm, so the RPC runs the read and
/// answers with its result, which the client dispatches as `Reloaded` (it runs
/// in the client, on the model the client holds when the read ends). Builds +
/// RUNS the backend, then POSTs — Go-gated (needs the toolchain + curl).
#[test]
fn server_internal_chain_e2e_post_reload_returns_file_note() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    // 1. Generate the split.
    let status = Command::new(SKY)
        .args([
            "spa-split",
            server_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    // 2. Build the backend natively.
    let backend_dir = out.join("backend");
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&backend_dir)
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "backend must build:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );

    // 3. The file the chain reads. `File.readFile "data/note.txt"` resolves
    // relative to the process CWD, so plant it under the backend dir.
    let note_body = "hello-from-the-server-chain";
    std::fs::create_dir_all(backend_dir.join("data")).unwrap();
    std::fs::write(backend_dir.join("data/note.txt"), note_body).unwrap();

    // 4. Run the backend, wait for it to listen.
    let port = 8953u16;
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend build must produce sky-out/app");
    let log_path = backend_dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("ENV", "production")
        .env("SKY_CONSOLE_AUTH", "off")
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn the compiled backend");

    let ready = wait_for_listening_substr(&log_path, port, 120);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&out);
        panic!("backend never reported listening on :{port}\nlog:\n{buf}");
    }

    // 5. POST /_rpc/Reload with an empty read-set body; the response must carry
    // the file's contents in `note`.
    let body = curl_post(port, "/_rpc/Reload", "{}");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&out);

    let body = body.expect("POST /_rpc/Reload should return a body");
    assert!(
        body.contains(note_body),
        "POST /_rpc/Reload must run the File read server-side and return its result \
         (`{note_body}`), but the response was:\n{body}"
    );
}

// Wait until the server's log carries a line containing "listening" and `:port`.
fn wait_for_listening_substr(log_path: &std::path::Path, port: u16, tries: u32) -> bool {
    use std::io::Read as _;
    let needle = format!(":{port}");
    for _ in 0..tries {
        if let Ok(mut f) = std::fs::File::open(log_path) {
            let mut buf = String::new();
            if f.read_to_string(&mut buf).is_ok() {
                if buf
                    .lines()
                    .any(|l| l.to_lowercase().contains("listening") && l.contains(&needle))
                {
                    return true;
                }
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

// POST `data` as JSON to `http://127.0.0.1:<port><path>`, returning the body.
fn curl_post(port: u16, path: &str, data: &str) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args([
            "-s",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-d",
            data,
            &url,
        ])
        .output()
        .ok()?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
}

/// Higher-order guard-wrapper narrowing + the whole-model+Msg-arg request-record
/// fix, end-to-end through the real `sky spa-split` generator.
///
/// `Edit id -> requireSession model (\_ -> ( { model | picked = …, label = … },
/// Cmd.none ))` is a guard-wrapped server branch. Before the fix the bare `model`
/// passed to `requireSession` made the whole arm reads_whole / writes_whole: the
/// RPC `Req` carried the WHOLE model and the frontend sent bare `model`, which has
/// no `id` field the backend `Req` expects (`record is missing field(s): id`).
/// After the fix the arm narrows to the continuation's write-set {label, picked}
/// and the guard's read-set {session}, and the frontend sends `{ session = …, id =
/// id }` — the Msg arg included, `secret` / `basket` excluded.
///
/// `SaveAll tagStr` is a GENUINELY whole-model server branch (opaque thread
/// through `Result.withDefault`) that ALSO binds a Msg arg. Its frontend request
/// MUST be an explicit record of every model field PLUS `tagStr`, NEVER bare
/// `model` — the residual-soundness guard for the whole-model+Msg-arg send.
#[test]
fn guard_wrapper_narrows_and_whole_model_msg_arg_send_is_explicit() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            guard_wrapper_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on the guard-wrapper app"
    );

    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();

    // SECURITY: the guard's effect never leaks into the client.
    for needle in ["File.", "saveN"] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front}"
        );
    }

    // Edit — the guard-wrapper arm. The frontend request narrows to the guard's
    // read {session} PLUS the Msg arg `id`; it is NOT bare `model` and it CARRIES
    // `id` (the missing-field bug the fix closes).
    assert!(
        front.contains("\"/_rpc/Edit\""),
        "frontend must call the RPC boundary for the Edit server branch:\n{front}"
    );
    assert!(
        front.contains("id = id"),
        "Edit's frontend request MUST carry the Msg arg `id` (was dropped by the bare-model send):\n{front}"
    );
    assert!(
        !front.contains("\"/_rpc/Edit\" model "),
        "Edit's frontend request MUST NOT be bare `model` (misses `id`, and carries untouched fields):\n{front}"
    );
    assert!(
        front.contains("session = model.session"),
        "Edit's narrowed request reads only the guard's field `session`:\n{front}"
    );

    // SaveAll — the genuine whole-model + Msg-arg branch. Its request is an
    // EXPLICIT record covering every model field PLUS `tagStr`, never bare model.
    assert!(
        front.contains("\"/_rpc/SaveAll\""),
        "frontend must call the RPC boundary for the SaveAll server branch:\n{front}"
    );
    assert!(
        front.contains("tagStr = tagStr"),
        "SaveAll's whole-model request MUST carry the Msg arg `tagStr` (bare `model` drops it, so the backend Req decode fails with `missing field(s): tagStr`):\n{front}"
    );
    assert!(
        !front.contains("\"/_rpc/SaveAll\" model "),
        "SaveAll's request MUST NOT be bare `model` — the backend Req carries `tagStr` too:\n{front}"
    );

    // No unresolved `List any` in the generated wire types (the whole-model Req
    // symptom the Edit narrowing removes): the shared wire carries real element
    // types (`basket : List Int` on SaveAll's whole model, `id : Int` on Edit).
    assert!(
        !shared.contains(": List any") && !shared.contains(": Maybe any"),
        "generated Shared wire types must carry resolved element types, never `List any` / `Maybe any`:\n{shared}"
    );

    // Both projects build (Go-gated). Backend native, frontend wasm.
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&out);
        return;
    }
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .status()
        .expect("run sky build (backend)");
    assert!(
        backend_build.success(),
        "guard-wrapper backend must build natively"
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .status()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.success(),
        "guard-wrapper frontend must build to wasm"
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

// ─────────────────────────────────────────────────────────────────────────────
// PATTERN-2 — client-result perform (a server task whose result Msg is CLIENT-
// handled). `Upload` returns `Cmd.perform (saveBlob data) Saved` THROUGH a
// guard/HOF wrapper (`guard model (\_ -> …)`) — the shape the direct-tuple chain
// walk misses, so pattern-1 cannot settle it and, before this feature, the
// perform effect was DROPPED (bound `( m2, _ )`, saveBlob ran nowhere) and
// `Saved` never dispatched. Pattern-2: the `Upload` RPC RUNS `saveBlob` and
// answers with the task RESULT; the frontend's `AppliedUpload` dispatches
// `Saved result` client-side, so `Saved` stays a client arm.
// ─────────────────────────────────────────────────────────────────────────────

/// Generation gate (no toolchain needed). Asserts the four pattern-2 contracts:
///   1. no server effect (`saveBlob` / `persist` / `File.`) leaks into the client;
///   2. the result Msg `Saved` is NOT a wire branch (no `/_rpc/Saved`, no
///      `SavedReq`, no `AppliedSaved`) — it stays a CLIENT arm;
///   3. the whole `Result` value is carried (frontend dispatches
///      `update (Saved resp.result) model`; the response codec is
///      `Codec.result …`), never a Req that decomposes it into `url`/`e`
///      binders (the `missing field(s): url` bug);
///   4. FAIL-CLOSED — `Stored`, whose own arm reaches a server effect
///      (`persist`), is NOT a client-result dispatch (no `update (Stored …)`
///      from an `AppliedStore` raw result) and `persist` never leaks.
#[test]
fn client_result_perform_wires_task_result_to_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            client_result_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on the client-result app"
    );

    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let backend = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // The CODE only — the module doc-comment is copied verbatim and legitimately
    // names the effects, RPC routes, and Msgs, so the "must NOT contain" checks
    // run against the comment-stripped source.
    let strip_comments = |s: &str| -> String {
        s.lines()
            .map(|l| l.split("--").next().unwrap_or(""))
            .collect::<Vec<_>>()
            .join("\n")
    };
    let front_code = strip_comments(&front);
    let shared_code = strip_comments(&shared);

    // GATE 1: no server effect handed to the client.
    for needle in ["saveBlob", "persist", "File."] {
        assert!(
            !front_code.contains(needle),
            "SECURITY: frontend CODE must not contain the server effect `{needle}`:\n{front_code}"
        );
    }

    // GATE 2: `Saved` is NOT a wire branch — it stays a client arm.
    assert!(
        !front_code.contains("/_rpc/Saved"),
        "the client result Msg `Saved` must have NO RPC route:\n{front_code}"
    );
    assert!(
        !shared_code.contains("SavedReq") && !shared_code.contains("SavedResp"),
        "the client result Msg `Saved` must have NO `SavedReq`/`SavedResp` wire type:\n{shared_code}"
    );
    assert!(
        !front_code.contains("AppliedSaved"),
        "the client result Msg `Saved` must have NO `AppliedSaved` variant — it is not a wire branch:\n{front_code}"
    );
    // `Saved`'s own client arm survives verbatim (it runs in the wasm client).
    assert!(
        front.contains("Saved (Ok url) ->") && front.contains("Saved (Err e) ->"),
        "`Saved`'s client arms must be kept verbatim in the frontend `update`:\n{front}"
    );

    // GATE 3: the WHOLE `Result` value crosses and is dispatched — never
    // decomposed into `url`/`e` binders.
    assert!(
        front.contains("update (Saved resp.result) model"),
        "the frontend `AppliedUpload` must dispatch `Saved` with the WHOLE result value:\n{front}"
    );
    assert!(
        shared.contains("result : Result Error String")
            && shared.contains("Codec.result Codec.error Codec.string"),
        "the `Upload` response wire must carry the task RESULT as `result : Result Error String` via `Codec.result`:\n{shared}"
    );
    // The backend RUNS the task and returns its result (never drops the perform).
    assert!(
        backend.contains("spaRunPerform_ cmd")
            && backend.contains("Ffi.kernel \"Spa_runServerPerform\""),
        "the `Upload` backend handler must RUN the server task via `spaRunPerform_` and return its result:\n{backend}"
    );
    assert!(
        backend.contains("{ result = result }"),
        "the `Upload` backend handler must answer with the task result:\n{backend}"
    );

    // GATE 4: FAIL-CLOSED — `Stored` (its arm reaches `persist`) is NOT wired as a
    // client-result dispatch, and `persist` never leaks to the client. It is
    // handled by pattern-1 (server-internal, settled server-side) instead.
    assert!(
        !front_code.contains("update (Stored"),
        "FAIL-CLOSED: `Stored`, whose arm reaches a server effect, must NOT be a client-result dispatch:\n{front_code}"
    );
    assert!(
        !shared_code.contains("StoreResp =\n    { result"),
        "FAIL-CLOSED: `Store` must NOT get a client-result `result` response (its continuation reaches a server effect):\n{shared_code}"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Build gate — both trees compile (backend natively with the
/// `Spa_runServerPerform` kernel, frontend to wasm). Go-gated.
#[test]
fn client_result_both_trees_build() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            client_result_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "client-result backend must build natively:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .output()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.status.success(),
        "client-result frontend must build to wasm:\n{}",
        String::from_utf8_lossy(&frontend_build.stderr)
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// End-to-end behaviour gate. `POST /_rpc/Upload` must RUN `saveBlob` server-side
/// and answer with the task RESULT (`Ok "blob.txt"`), for the client to dispatch
/// `Saved`. Before this feature the perform was dropped, so the RPC answered with
/// an empty write-set and the upload silently did nothing. Builds + RUNS the
/// backend, then POSTs — Go-gated (needs the toolchain + curl).
#[test]
fn client_result_e2e_post_upload_returns_task_result() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            client_result_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let backend_dir = out.join("backend");
    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&backend_dir)
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "backend must build:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );

    let port = 8974u16;
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend build must produce sky-out/app");
    let log_path = backend_dir.join("server.log");
    let log = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env("ENV", "production")
        .env("SKY_CONSOLE_AUTH", "off")
        .stdout(log.try_clone().unwrap())
        .stderr(log)
        .spawn()
        .expect("spawn the compiled backend");

    let ready = wait_for_listening_substr(&log_path, port, 120);
    if !ready {
        let _ = child.kill();
        let mut buf = String::new();
        use std::io::Read as _;
        let _ = std::fs::File::open(&log_path).and_then(|mut f| f.read_to_string(&mut buf));
        let _ = std::fs::remove_dir_all(&out);
        panic!("backend never reported listening on :{port}\nlog:\n{buf}");
    }

    // POST /_rpc/Upload. An `Authorization` header exempts the call from the CSRF
    // guard (the documented API-client path); the response must carry the task
    // RESULT `Ok "blob.txt"`.
    let url = format!("http://127.0.0.1:{port}/_rpc/Upload");
    let body = Command::new("curl")
        .args([
            "-s",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-H",
            "Authorization: Bearer test",
            "-d",
            "{\"data\":\"hello-blob\"}",
            &url,
        ])
        .output()
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string());

    let blob = std::fs::read_to_string(backend_dir.join("blob.txt")).ok();

    let _ = child.kill();
    let _ = child.wait();

    let body = body.expect("POST /_rpc/Upload should return a body");
    assert!(
        body.contains("\"result\"") && body.contains("blob.txt"),
        "POST /_rpc/Upload must return the task RESULT (`result` = Ok \"blob.txt\"), was:\n{body}"
    );
    // The server task ran SERVER-side (never handed to the client).
    assert_eq!(
        blob.as_deref(),
        Some("hello-blob"),
        "saveBlob must have run server-side inside the RPC (blob.txt should carry the posted data)"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Guard-wrapped server-internal chaining (the shop-app `requireAdmin`
/// shape), end-to-end through the real `sky spa-split` generator.
///
/// `Trigger x -> guard model (\_ -> ( { model | busy = True }, Cmd.perform
/// (saveThing x) Saved ))` is a guard-wrapped SERVER branch whose all-server
/// continuation `Saved (Ok ref) -> guard model (\_ -> … File read …, Cmd.none )`
/// is also guard-wrapped. Before this change the chaining-ROOT detection walked
/// only the DIRECT tail tuple, so the guarded `Cmd.perform` was invisible: `Saved`
/// stayed a BROKEN wire branch (`missing field(s)` / `Result Error String vs
/// Error`). After: `Trigger` settles the chain server-side and `Saved` is pruned
/// from the wire, its narrow write-set (`note` + `log`) unioned into `Trigger`'s
/// response.
#[test]
fn spa_guarded_chain_settles_server_side_and_prunes_the_wire() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            guarded_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on the guarded-chain app"
    );

    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();
    let backend = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();

    // Strip line comments — the copied module doc-comment legitimately names the
    // effects, RPC routes, and Msgs.
    let strip_comments = |s: &str| -> String {
        s.lines()
            .map(|l| l.split("--").next().unwrap_or(""))
            .collect::<Vec<_>>()
            .join("\n")
    };
    let front_code = strip_comments(&front);
    let shared_code = strip_comments(&shared);

    // GATE 1: no server effect reaches the client.
    for needle in ["saveThing", "File."] {
        assert!(
            !front_code.contains(needle),
            "SECURITY: frontend CODE must not contain the server effect `{needle}`:\n{front_code}"
        );
    }

    // GATE 2: `Saved` is SERVER-INTERNAL — no wire route, no wire type, no Applied
    // variant, and it is not constructed anywhere in the frontend.
    assert!(
        !front_code.contains("/_rpc/Saved"),
        "server-internal `Saved` must have NO RPC route:\n{front_code}"
    );
    assert!(
        !shared_code.contains("SavedReq") && !shared_code.contains("SavedResp"),
        "server-internal `Saved` must have NO `SavedReq`/`SavedResp` wire type:\n{shared_code}"
    );
    assert!(
        !front_code.contains("AppliedSaved"),
        "server-internal `Saved` must have NO `AppliedSaved` variant:\n{front_code}"
    );
    assert!(
        !front_code.contains("Saved "),
        "server-internal `Saved` must not be constructed/handled in the frontend:\n{front_code}"
    );

    // GATE 3: the backend SETTLES the chain (never drops the perform) and answers
    // from the FINAL settled model, carrying the continuation's `note`/`log`.
    assert!(
        backend.contains("spaChainSettle_"),
        "the `Trigger` backend handler must settle the guarded Cmd.perform chain server-side:\n{backend}"
    );
    for field in ["mFinal.note", "mFinal.log", "mFinal.busy"] {
        assert!(
            backend.contains(field),
            "`Trigger`'s response must carry `{field}` from the settled chain's final model:\n{backend}"
        );
    }
    // The untouched field is never sent — the union stayed NARROW.
    assert!(
        !backend.contains("mFinal.count"),
        "`count` is written by NO arm in the chain — it must NOT be in `Trigger`'s response:\n{backend}"
    );

    // GATE 4: `Trigger` keeps its own wire branch (the client still triggers it).
    assert!(
        front.contains("/_rpc/Trigger"),
        "`Trigger` must keep its own RPC route in the frontend:\n{front}"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Build gate — both trees compile (backend natively with the chain-settle
/// kernel, frontend to wasm with `Saved` pruned). Go-gated.
#[test]
fn spa_guarded_chain_both_trees_build() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            guarded_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "guarded-chain backend must build natively:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .output()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.status.success(),
        "guarded-chain frontend must build to wasm:\n{}",
        String::from_utf8_lossy(&frontend_build.stderr)
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// TRANSITIVE (multi-hop) server-internal chaining, end-to-end through the
/// generator. `Kick` -> `Fetched` (via the helper `record`) -> `SavedA` +
/// `LoggedB`: an all-server 3+-hop chain where the deep continuations live BEHIND
/// a `( model, cmd )`-returning helper. Every continuation must be pruned from the
/// wire (no `/_rpc/<Msg>` route, no `Applied<Msg>`, not constructed in the
/// frontend), and `Kick` keeps its own RPC route.
#[test]
fn spa_multihop_chain_prunes_every_transitive_continuation() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            multihop_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(
        status.success(),
        "sky spa-split should succeed on the multi-hop app"
    );

    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky")).unwrap();

    let strip_comments = |s: &str| -> String {
        s.lines()
            .map(|l| l.split("--").next().unwrap_or(""))
            .collect::<Vec<_>>()
            .join("\n")
    };
    let front_code = strip_comments(&front);
    let shared_code = strip_comments(&shared);

    // GATE 1: no server effect reaches the client.
    for needle in ["fetchThing", "saveA", "logB", "File.", "Log."] {
        assert!(
            !front_code.contains(needle),
            "SECURITY: frontend CODE must not contain the server effect `{needle}`:\n{front_code}"
        );
    }

    // GATE 2: every transitive continuation is SERVER-INTERNAL — no wire route,
    // no wire type, no Applied variant, not constructed in the frontend.
    for m in ["Fetched", "SavedA", "LoggedB"] {
        assert!(
            !front_code.contains(&format!("/_rpc/{m}")),
            "server-internal `{m}` must have NO RPC route:\n{front_code}"
        );
        assert!(
            !shared_code.contains(&format!("{m}Req")) && !shared_code.contains(&format!("{m}Resp")),
            "server-internal `{m}` must have NO `{m}Req`/`{m}Resp` wire type:\n{shared_code}"
        );
        assert!(
            !front_code.contains(&format!("Applied{m}")),
            "server-internal `{m}` must have NO `Applied{m}` variant:\n{front_code}"
        );
        assert!(
            !front_code.contains(&format!("{m} ")),
            "server-internal `{m}` must not be constructed/handled in the frontend:\n{front_code}"
        );
    }

    // GATE 3: `Kick` keeps its own RPC route (the client still triggers it).
    assert!(
        front.contains("/_rpc/Kick"),
        "`Kick` must keep its own RPC route in the frontend:\n{front}"
    );

    let _ = std::fs::remove_dir_all(&out);
}

/// Build gate — both trees compile with the transitive chain settled server-side
/// and every continuation pruned from the wasm frontend. Go-gated.
#[test]
fn spa_multihop_chain_both_trees_build() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);

    let status = Command::new(SKY)
        .args([
            "spa-split",
            multihop_chain_fixture_entry().to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .status()
        .expect("run sky spa-split");
    assert!(status.success(), "sky spa-split should succeed");

    let backend_build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(out.join("backend"))
        .output()
        .expect("run sky build (backend)");
    assert!(
        backend_build.status.success(),
        "multi-hop backend must build natively:\n{}",
        String::from_utf8_lossy(&backend_build.stderr)
    );
    let frontend_build = Command::new(SKY)
        .args(["build", "--target", "web", "src/Main.sky"])
        .current_dir(out.join("frontend"))
        .output()
        .expect("run sky build --target web (frontend)");
    assert!(
        frontend_build.status.success(),
        "multi-hop frontend must build to wasm:\n{}",
        String::from_utf8_lossy(&frontend_build.stderr)
    );
    assert!(
        dist_has_wasm(&out.join("frontend/dist")),
        "frontend build must stage a content-hashed main.<hash>.wasm"
    );

    let _ = std::fs::remove_dir_all(&out);
}

fn boot_setup_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-boot-setup")
}

/// BOOT-SETUP. A real app runs boot-time server setup in `main` BEFORE it starts
/// the app — `main = let dir = "data"; _ = Task.run (File.mkdirAll dir); _ =
/// Task.run (setupThing ()) in App.run app` — where the setup reaches `File`/`Db`
/// (server effects). The App→Spa synthesis used to DROP the whole `let`-prefix,
/// so the generated backend `main` was a bare `Server.listen …` that never
/// created the schema; every request then read a missing table.
///
/// The fix captures the boot-setup bindings into a server-tainted
/// `spaBootSetup_` binding, RUNS it in the BACKEND `main` before `Server.listen`,
/// and keeps it OUT of the wasm frontend. This proves:
///   1. the synthesised entry carries `spaBootSetup_` (with `setupThing` +
///      the named `dir` binding preserved, in order);
///   2. the BACKEND `main` forces `spaBootSetup_` BEFORE `Server.listen`;
///   3. the FRONTEND does NOT reference `setupThing` / the server effect;
///   4. (Go-gated) both legs build.
#[test]
fn web_app_boot_setup_runs_in_backend_main_not_frontend() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&boot_setup_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    // 1. The synthesised entry carries the boot setup verbatim: the named `dir`
    // binding, the `File.mkdirAll dir` effect, and the `setupThing ()` call all
    // survive inside a `spaBootSetup_` binding.
    let synth = std::fs::read_to_string(proj.join(".skyapp/web-app/src/Main.sky"))
        .expect("synthesised web-app entry must exist");
    assert!(
        synth.contains("spaBootSetup_"),
        "boot setup must be captured into a `spaBootSetup_` binding:\n{synth}"
    );
    assert!(
        synth.contains("dir = \"data\"")
            && synth.contains("File.mkdirAll dir")
            && synth.contains("setupThing ()"),
        "the boot-setup bindings (named `dir`, `File.mkdirAll dir`, `setupThing ()`) must be preserved in order:\n{synth}"
    );

    // Synthesis + the split's type-check passed (this line prints only after
    // `generate` type-checks clean).
    assert!(
        log.contains("client/server split"),
        "the split must run (synthesis + type-check passed):\n{log}"
    );

    // 2. The BACKEND `main` runs the boot setup BEFORE `Server.listen`. Before
    // the fix the backend `main` was a bare `Server.listen …` with no setup.
    let back = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .expect("generated backend entry must exist");
    // `setupThing` (the schema effect) is carried into the backend.
    assert!(
        back.contains("setupThing"),
        "backend must carry the boot-setup effect `setupThing`:\n{back}"
    );
    // The backend `main` forces `spaBootSetup_`, and it appears BEFORE the
    // `Server.listen` call (so setup runs first).
    let boot_at = back.find("_ =\n            spaBootSetup_");
    // v0.27.0: `main` listens through `spaListen_` after its boot tasks.
    let listen_at = back
        .find("main =")
        .and_then(|m| back[m..].find("spaListen_").map(|i| m + i));
    assert!(
        boot_at.is_some(),
        "backend `main` must force `spaBootSetup_` in a `let … in Server.listen`:\n{back}"
    );
    match (boot_at, listen_at) {
        (Some(b), Some(l)) => assert!(
            b < l,
            "backend must run the boot setup BEFORE `Server.listen`:\n{back}"
        ),
        _ => panic!(
            "backend `main` must both force `spaBootSetup_` and call `Server.listen`:\n{back}"
        ),
    }

    // 3. The FRONTEND must NOT reference the server effect. `setupThing`,
    // `spaBootSetup_`, and the `Db.`/`File.`/`System.` kernels are server-tainted.
    let front = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
        .expect("generated frontend entry must exist");
    for needle in [
        "setupThing",
        "spaBootSetup_",
        "File.mkdirAll",
        "Db.",
        "System.",
    ] {
        assert!(
            !front.contains(needle),
            "SECURITY LEAK: frontend/src/Main.sky contains `{needle}`:\n{front}"
        );
    }

    // 4. Both legs build (Go-gated).
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "BOOT-SETUP: --target web:app must build end-to-end:\n{log}"
    );
    assert!(
        proj.join(".skyapp/web-app/.split/backend/sky-out/app")
            .is_file(),
        "backend binary must be built:\n{log}"
    );
    assert!(
        dist_has_wasm(&proj.join(".skyapp/web-app/.split/frontend/dist")),
        "frontend wasm must be staged:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}

fn sub_auth_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-sub-auth")
}

/// One curl request with explicit headers. Returns (status, raw response
/// headers, body). `max_time` bounds a streaming response (SSE): curl then
/// exits on its timer with the status it already received.
fn curl_req(
    port: u16,
    method: &str,
    path: &str,
    headers: &[&str],
    body: Option<&str>,
    max_time: &str,
) -> (u32, String, String) {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> = vec![
        "-s".into(),
        "-D".into(),
        "-".into(),
        "--max-time".into(),
        max_time.into(),
        "-X".into(),
        method.into(),
    ];
    for h in headers {
        args.push("-H".into());
        args.push((*h).to_string());
    }
    if let Some(b) = body {
        args.push("--data-binary".into());
        args.push(b.into());
    }
    args.push(url);
    let out = Command::new("curl").args(&args).output().expect("run curl");
    let s = String::from_utf8_lossy(&out.stdout).to_string();
    let (head, rest) = match s.find("\r\n\r\n") {
        Some(i) => (s[..i].to_string(), s[i + 4..].to_string()),
        None => (s.clone(), String::new()),
    };
    let status = head
        .lines()
        .next()
        .and_then(|l| l.split_whitespace().nth(1))
        .and_then(|c| c.parse::<u32>().ok())
        .unwrap_or(0);
    (status, head, rest)
}

/// SECURITY (v0.27 Phase 1B). Under `--target web:app`:
///
///   * every `/_rpc/<Msg>` and `/_rpc/__spaSignOut` is a `Server.rpc` route: a
///     CORS-simple `text/plain` POST from a foreign Origin (sent by a browser
///     without a preflight, with the victim's `sky_sid` attached) is refused
///     with 403 BEFORE the handler runs, while a same-origin JSON POST works;
///   * `GET /_sky/sub?topic=…` streams a topic only when the app's own
///     `subscriptions`, run on the model rebuilt from the VERIFIED cookie,
///     names it. No cookie: "public" only. u1's cookie: "user:u1", never
///     "user:u2".
///
/// Emission is asserted without Go; the live probes are Go-gated.
#[test]
fn spa_rpc_origin_guard_and_sub_topic_authorisation() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&sub_auth_fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app on the spa-sub-auth fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let backend = std::fs::read_to_string(proj.join(".skyapp/web-app/.split/backend/src/Main.sky"))
        .unwrap_or_else(|e| panic!("generated backend entry must exist ({e}):\n{log}"));
    let frontend =
        std::fs::read_to_string(proj.join(".skyapp/web-app/.split/frontend/src/Main.sky"))
            .expect("generated frontend entry must exist");

    for route in [
        "Server.rpc \"POST /_rpc/LogIn\" logInHandler",
        "Server.rpc \"POST /_rpc/Post\" postHandler",
        "Server.rpc \"POST /_rpc/__spaSignOut\" spaSignOutHandler",
    ] {
        assert!(backend.contains(route), "missing `{route}`:\n{backend}");
    }
    assert!(
        !backend.contains("Server.api \"POST /_rpc/"),
        "no /_rpc route may stay a CSRF-exempt Server.api route:\n{backend}"
    );
    // The synthesis carries `subscriptions` as `spaSubscriptions_`; the sub
    // handler runs it on the verified session model.
    assert!(
        backend
            .contains("if spaSubAllowsTopic_ (spaSubscriptions_ (spaSubModel_ req)) topic_ then"),
        "the sub handler must authorise against spaSubscriptions_:\n{backend}"
    );
    assert!(
        backend.contains("{ base | session = verifiedSession_ req_ base.session }"),
        "the sub model must take the session from the verified cookie:\n{backend}"
    );
    assert!(
        !frontend.contains("spaSubscriptions_"),
        "spaSubscriptions_ is backend-only and must not reach the wasm client:\n{frontend}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        output.status.success(),
        "spa-sub-auth: --target web:app must build end-to-end:\n{log}"
    );
    let backend_dir = proj.join(".skyapp/web-app/.split/backend");
    let app_bin = backend_dir.join("sky-out/app");
    assert!(app_bin.is_file(), "backend binary must be built:\n{log}");

    let port = 8983u16;
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let mut child = Command::new(&app_bin)
        .current_dir(&backend_dir)
        .env("PORT", port.to_string())
        .env(
            "SKY_SPA_SESSION_SECRET",
            "0123456789abcdef0123456789abcdef0123456789",
        )
        .env_remove("SKY_PUBLIC_URL")
        .stdout(log_file.try_clone().unwrap())
        .stderr(log_file)
        .spawn()
        .expect("spawn the compiled spa-sub-auth backend");
    if !wait_for_spa_backend(&log_path, 80) {
        let _ = child.kill();
        let _ = std::fs::remove_dir_all(&proj);
        panic!("spa-sub-auth backend never reported listening on :{port}");
    }
    let host = format!("Origin: http://127.0.0.1:{port}");

    // (1) the live probe that found the defect: text/plain + foreign Origin.
    let forged = curl_req(
        port,
        "POST",
        "/_rpc/LogIn",
        &["Content-Type: text/plain", "Origin: https://evil.example"],
        Some(r#"{"uid":"u1"}"#),
        "5",
    );
    let last_after_forge =
        std::fs::read_to_string(backend_dir.join("last.txt")).unwrap_or_default();
    // (2) foreign Origin with JSON: still refused.
    let foreign_json = curl_req(
        port,
        "POST",
        "/_rpc/LogIn",
        &[
            "Content-Type: application/json",
            "Origin: https://evil.example",
            "Sec-Fetch-Site: cross-site",
        ],
        Some(r#"{"uid":"u1"}"#),
        "5",
    );
    // (3) same-origin JSON: works and signs the cookie.
    let login = curl_req(
        port,
        "POST",
        "/_rpc/LogIn",
        &[
            "Content-Type: application/json",
            &host,
            "Sec-Fetch-Site: same-origin",
        ],
        Some(r#"{"uid":"u1"}"#),
        "5",
    );
    let cookie = login
        .1
        .lines()
        .find(|l| l.to_ascii_lowercase().starts_with("set-cookie: sky_spa="))
        .map(|l| {
            let v = l[l.find(':').unwrap() + 1..].trim();
            v.split(';').next().unwrap().to_string()
        });
    let ck = format!("Cookie: {}", cookie.clone().unwrap_or_default());
    // (4) the sub endpoint.
    let pub_anon = curl_req(port, "GET", "/_sky/sub?topic=public", &[], None, "2");
    let u1_anon = curl_req(port, "GET", "/_sky/sub?topic=user:u1", &[], None, "2");
    let u1_cookie = curl_req(port, "GET", "/_sky/sub?topic=user:u1", &[&ck], None, "2");
    let u2_cookie = curl_req(port, "GET", "/_sky/sub?topic=user:u2", &[&ck], None, "2");
    let empty_topic = curl_req(port, "GET", "/_sky/sub", &[&ck], None, "2");

    let _ = child.kill();
    let _ = child.wait();
    let _ = std::fs::remove_dir_all(&proj);

    assert_eq!(
        forged.0, 403,
        "text/plain + foreign Origin must be refused before the handler: {forged:?}"
    );
    assert_eq!(
        last_after_forge, "",
        "the refused RPC must not run the effect, last.txt was {last_after_forge:?}"
    );
    assert_eq!(foreign_json.0, 403, "foreign Origin JSON: {foreign_json:?}");
    assert_eq!(login.0, 200, "same-origin JSON LogIn must work: {login:?}");
    assert!(
        cookie.is_some(),
        "LogIn must sign a sky_sid cookie: {login:?}"
    );
    assert_eq!(
        pub_anon.0, 200,
        "\"public\" streams without a session: {pub_anon:?}"
    );
    assert!(
        pub_anon
            .1
            .to_ascii_lowercase()
            .contains("text/event-stream"),
        "an authorised topic streams SSE: {pub_anon:?}"
    );
    assert_eq!(
        u1_anon.0, 403,
        "user:u1 without a session must be refused: {u1_anon:?}"
    );
    assert_eq!(
        u1_cookie.0, 200,
        "u1's cookie streams user:u1: {u1_cookie:?}"
    );
    assert_eq!(
        u2_cookie.0, 403,
        "u1's cookie must not stream user:u2: {u2_cookie:?}"
    );
    assert_eq!(
        empty_topic.0, 403,
        "an empty topic is refused: {empty_topic:?}"
    );
}

fn spa_arm_patterns_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-arm-patterns")
}

/// A port nothing listens on.
fn free_port() -> u16 {
    std::net::TcpListener::bind(("127.0.0.1", 0))
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// A child process that is killed when dropped (a failed assertion must not
/// leave a server holding its port).
struct Killed(std::process::Child);

impl Drop for Killed {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn wait_for_log(path: &std::path::Path, needle: &str, tries: u32) -> bool {
    for _ in 0..tries {
        if std::fs::read_to_string(path).is_ok_and(|s| s.contains(needle)) {
            return true;
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

/// `curl` GET with a cookie jar (read and written).
fn curl_get_jar(port: u16, path: &str, jar: &std::path::Path) -> String {
    let out = Command::new("curl")
        .args(["-s", "--max-time", "30", "-b"])
        .arg(jar)
        .arg("-c")
        .arg(jar)
        .arg(format!("http://127.0.0.1:{port}{path}"))
        .output()
        .expect("curl GET");
    String::from_utf8_lossy(&out.stdout).into_owned()
}

/// Dispatch the Sky.Live handler `hid` (a button's `onPress`), as the browser
/// does; returns the HTTP status.
fn live_event(port: u16, jar: &std::path::Path, hid: &str) -> String {
    let jar_text = std::fs::read_to_string(jar).unwrap_or_default();
    let csrf = jar_text
        .lines()
        .filter_map(|l| {
            let cols: Vec<&str> = l.split('\t').collect();
            (cols.len() >= 7 && cols[5] == "__sky_csrf").then(|| cols[6].to_string())
        })
        .last()
        .unwrap_or_default();
    let body = format!("{{\"sessionId\":\"\",\"msg\":\"\",\"args\":[],\"handlerId\":\"{hid}\"}}");
    let out = Command::new("curl")
        .args([
            "-s",
            "--max-time",
            "30",
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "-H",
            "Content-Type: application/json",
            "-H",
            &format!("X-Sky-Csrf: {csrf}"),
            "-X",
            "POST",
            "-d",
            &body,
            "-b",
        ])
        .arg(jar)
        .arg(format!("http://127.0.0.1:{port}/_sky/event"))
        .output()
        .expect("curl POST event");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

/// Every `data-sky-hid` in the page, in document order.
fn handler_ids(body: &str) -> Vec<String> {
    body.split("data-sky-hid=\"")
        .skip(1)
        .filter_map(|r| r.split('"').next().map(str::to_string))
        .collect()
}

/// The `STATUS=…|` text the fixture's view renders.
fn rendered_status(body: &str) -> Option<String> {
    let rest = &body[body.find("STATUS=")? + "STATUS=".len()..];
    Some(rest[..rest.find('|')?].to_string())
}

/// Server branches that match inside their Msg arguments (v0.27.0). Before,
/// the split rebuilt a server arm's Msg from the names the arm binds, so an arm
/// such as `Report (Ok line)` produced a backend that did not compile, and the
/// split then refused it. The fixture's `update` has a server arm of every
/// pattern shape — a nested constructor, Int and String literals, a tuple, a
/// record, an `as` binding and a wildcard — with client and server arms of one
/// constructor mixed.
///
/// The same source is built twice: as a Sky.Live app (`sky build`, the
/// monolithic reference, where every arm runs on the server) and as a split
/// Sky.Spa app (`sky build --target web:app`: backend + wasm client). Each
/// message is dispatched to the Live app through its button and sent to the
/// split backend over `POST /_rpc/<Msg>` with the whole argument (what the
/// client sends from the arm's positional pattern); the model each returns must
/// agree. A server arm's status carries the tag the server reads from its
/// environment, and `Any _` also chains a logged command (an `ARM …` line),
/// which must run in both. `Named
/// "guest"` is a CLIENT arm placed before the server arm `Named other`: the
/// backend reaches it too when sent the message, which proves the backend keeps
/// `case` order (the first matching arm wins), as it must for the client's
/// routing to be sound.
#[test]
fn server_arms_that_match_inside_their_msg_arguments_behave_as_the_live_app() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&spa_arm_patterns_fixture_dir(), &proj);

    let split = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let split_log = format!(
        "{}{}",
        String::from_utf8_lossy(&split.stdout),
        String::from_utf8_lossy(&split.stderr)
    );
    let split_dir = proj.join(".skyapp/web-app/.split");
    let front = std::fs::read_to_string(split_dir.join("frontend/src/Main.sky"))
        .unwrap_or_else(|_| panic!("the frontend entry must be generated:\n{split_log}"));
    for want in [
        "Report ((Ok line) as spaArg0_) ->",
        "Pick (0 as spaArg0_) ->",
        "Named (\"admin\" as spaArg0_) ->",
        "Pair (( 0, s ) as spaArg0_) ->",
        "Take ({ id, label } as spaArg0_) ->",
        "Wrap (((Just n) as whole) as spaArg0_) ->",
        "Any (_ as spaArg0_) ->",
        "Named \"guest\" ->",
    ] {
        assert!(front.contains(want), "client arm `{want}`:\n{front}");
    }

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(
        split.status.success(),
        "the web:app build failed:\n{split_log}"
    );
    assert!(
        dist_has_wasm(&split_dir.join("frontend/dist")),
        "the wasm client must be built:\n{split_log}"
    );
    let live = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build (Sky.Live)");
    assert!(
        live.status.success(),
        "the Sky.Live build failed:\n{}{}",
        String::from_utf8_lossy(&live.stdout),
        String::from_utf8_lossy(&live.stderr)
    );

    // The monolithic Sky.Live app.
    let live_port = free_port();
    let live_log = proj.join("live.log");
    let live_child = Killed(
        Command::new(proj.join("sky-out/app"))
            .current_dir(&proj)
            .env("SKY_LIVE_PORT", live_port.to_string())
            .env("PORT", live_port.to_string())
            .env("SKY_ARM_TAG", "srv")
            .env_remove("SKY_LIVE_STORE")
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&live_log).unwrap())
            .stderr(std::fs::File::create(proj.join("live.err")).unwrap())
            .spawn()
            .expect("start the Sky.Live app"),
    );
    assert!(
        wait_for_log(&live_log, &format!("listening on :{live_port}"), 120),
        "the Sky.Live app did not start:\n{}",
        std::fs::read_to_string(&live_log).unwrap_or_default()
    );
    // The split backend.
    let back_port = free_port();
    let back_dir = split_dir.join("backend");
    let back_log = back_dir.join("server.log");
    let back_child = Killed(
        Command::new(back_dir.join("sky-out/app"))
            .current_dir(&back_dir)
            .env("PORT", back_port.to_string())
            .env("SKY_ARM_TAG", "srv")
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&back_log).unwrap())
            .stderr(std::fs::File::create(back_dir.join("server.err")).unwrap())
            .spawn()
            .expect("start the split backend"),
    );
    assert!(
        wait_for_spa_backend(&back_log, 120),
        "the split backend did not start:\n{}",
        std::fs::read_to_string(&back_log).unwrap_or_default()
    );

    // (button index in the view, constructor, the whole argument as JSON, the
    // status the arm sets, the server effect's log line or "" for none).
    // A server arm's status starts with the `SKY_ARM_TAG` the server process
    // reads ("srv"); a client arm's does not.
    let cases: [(usize, &str, &str, &str, &str); 11] = [
        (0, "Report", r#"["Ok","a"]"#, "srv report a", ""),
        (1, "Pick", "0", "srv pick zero", ""),
        (2, "Pick", "7", "srv pick 7", ""),
        (3, "Named", r#""admin""#, "srv named admin", ""),
        (4, "Named", r#""guest""#, "named guest", ""),
        (5, "Named", r#""bob""#, "srv named bob", ""),
        (6, "Pair", r#"{"0":0,"1":"x"}"#, "srv pair zero x", ""),
        (7, "Take", r#"{"id":3,"label":"l"}"#, "srv take 3 l", ""),
        (8, "Wrap", "5", "srv wrap 5 5", ""),
        (9, "Any", "null", "srv any", "ARM any"),
        (10, "Any", "1", "srv any", "ARM any"),
    ];
    let jar = proj.join("jar.txt");
    let mut failures = Vec::new();
    for (i, ctor, arg, want, effect) in cases {
        // The monolith: press the case's button, then read the rendered status.
        let live_before = std::fs::read_to_string(&live_log).unwrap_or_default();
        let page = curl_get_jar(live_port, "/", &jar);
        let hids = handler_ids(&page);
        let live_status = match hids.get(i) {
            Some(hid) => {
                let code = live_event(live_port, &jar, hid);
                assert_eq!(code, "200", "Live event for case {i} ({ctor})");
                rendered_status(&curl_get_jar(live_port, "/", &jar))
            }
            None => None,
        };
        let live_effect = arm_line_after(&live_log, live_before.len(), !effect.is_empty());
        // The split: the whole argument over the RPC.
        let before = std::fs::read_to_string(&back_log).unwrap_or_default();
        let posted = curl_post_status_body(
            back_port,
            &format!("/_rpc/{ctor}"),
            &format!("{{\"status\":\"ready\",\"spaArg0_\":{arg}}}"),
        );
        let rpc_effect = arm_line_after(&back_log, before.len(), !effect.is_empty());
        let rpc_status = posted.as_ref().and_then(|(code, body)| {
            (*code == 200)
                .then(|| serde_json::from_str::<serde_json::Value>(body).ok())
                .flatten()
                .and_then(|v| v["status"].as_str().map(str::to_string))
        });
        if live_status.as_deref() != Some(want) || rpc_status.as_deref() != Some(want) {
            failures.push(format!(
                "case {i} {ctor} {arg}: Live rendered {live_status:?}, the split RPC answered \
                 {rpc_status:?} ({posted:?}); both must be {want:?}"
            ));
        }
        if rpc_effect != effect || live_effect != effect {
            failures.push(format!(
                "case {i} {ctor} {arg}: the server effect must be {effect:?} in both: the \
                 backend logged {rpc_effect:?}, the Live app {live_effect:?}"
            ));
        }
    }
    drop(live_child);
    drop(back_child);
    let _ = std::fs::remove_dir_all(&proj);
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

// ── Msg order: async and hold RPCs (docs/skyspa/overview.md, "Msg order and
// server calls"; runtime-go/rt/spa_rpcqueue.go) ──────────────────────────────

fn named_fixture_entry(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures")
        .join(name)
        .join("src/Main.sky")
}

/// Run `sky spa-split` on a named fixture; returns (frontend, backend) sources.
fn split_named(name: &str) -> (String, String) {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let out = scratch();
    let _ = std::fs::remove_dir_all(&out);
    let res = Command::new(SKY)
        .args([
            "spa-split",
            named_fixture_entry(name).to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    assert!(
        res.status.success(),
        "sky spa-split {name} failed:\n{}{}",
        String::from_utf8_lossy(&res.stdout),
        String::from_utf8_lossy(&res.stderr)
    );
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let _ = std::fs::remove_dir_all(&out);
    (front, back)
}

/// The two downstream repro shapes (a timer that starts a call only when none
/// runs; a client arm that spends a Noise state during a slow RPC) and two
/// independent slow calls. Each server arm writes nothing itself and performs
/// a server task into a CLIENT arm, so each is an ASYNC RPC (`Spa.rpc`, no
/// hold) whose result Msg runs in the client when it arrives. Before v0.27.0
/// `Call` → `Answered` settled on the server as a chain (from the send-time
/// model) and the client replayed the ticks after the result.
#[test]
fn rpc_order_fixture_sends_async_rpcs_whose_results_run_in_the_client() {
    let (front, back) = split_named("spa-rpc-order");
    for ctor in ["slow", "call", "slowA", "slowB"] {
        assert!(
            front.contains(&format!("Spa.rpc {ctor}ReqCodec")),
            "`{ctor}` must be an async RPC (Spa.rpc):\n{front}"
        );
    }
    for ctor in ["slow", "call", "slowA", "slowB"] {
        assert!(
            !front.contains(&format!("Spa.rpcHold {ctor}ReqCodec")),
            "`{ctor}` needs no server data for its own write — it may not hold:\n{front}"
        );
    }
    // The basket steps write a server price: they hold. `Tracked` is both a
    // follow-up of `AddToBasket` and reached inside the `SignIn` chain: it
    // crosses under its own tag, never as an empty one.
    assert!(
        front.contains("Spa.rpcHold addToBasketReqCodec")
            && front.contains("Spa.rpcHold signInReqCodec")
            && front.contains("tag_ == \"Tracked\""),
        "the basket steps hold and `Tracked` decodes in the client:\n{front}"
    );
    assert!(
        !back.contains("[ \"\", \"\" ]"),
        "no empty follow-up tag:\n{back}"
    );
    for (root, result) in [
        ("Call", "Answered"),
        ("Slow", "SlowDone"),
        ("SlowA", "GotA"),
        ("SlowB", "GotB"),
    ] {
        assert!(
            front.contains(&format!("update ({result} resp.result) model")),
            "`{root}`'s result must be dispatched as `{result}` in the client:\n{front}"
        );
        assert!(
            front.contains(&format!("        {result} ")),
            "`{result}` must stay a client arm:\n{front}"
        );
    }
    assert!(
        front.contains("Noise.encrypt") && !back.contains("POST /_rpc/Seal"),
        "`Seal` is a client arm (the device holds the transport)"
    );
    for ctor in ["call", "slow", "slowA", "slowB"] {
        let handler = back
            .split(&format!("{ctor}Handler req ="))
            .nth(1)
            .and_then(|r| r.split("\n\n\n").next())
            .unwrap_or_default();
        assert!(
            !handler.contains("spaChainSettle_"),
            "`{ctor}`'s client continuation may not settle on the server:\n{handler}"
        );
    }
}

/// An arm whose own model write needs server data (an inline `Task.run`) is a
/// HOLD RPC: later Msgs wait for its answer, as they wait on a Sky.Live session
/// during a synchronous update, so `Inc` twice counts to 2 and a draft typed
/// during `Save` is applied after it.
#[test]
fn rpc_consistency_fixture_holds_arms_whose_write_needs_server_data() {
    let (front, _) = split_named("spa-rpc-consistency");
    for ctor in ["inc", "save", "setName", "hit", "touch"] {
        assert!(
            front.contains(&format!("Spa.rpcHold {ctor}ReqCodec")),
            "`{ctor}` writes a server value into the model: it must hold (Spa.rpcHold):\n{front}"
        );
    }
}

/// `Sky.Core.WebSocket` is a client effect: an arm that connects, sends or
/// receives runs in the wasm client (over the browser WebSocket API), never
/// behind an RPC, and the backend serves the socket through `App.api`.
#[test]
fn websocket_calls_stay_in_the_client() {
    let (front, back) = split_named("spa-websocket");
    assert!(
        front.contains("WebSocket.connect \"/ws\"")
            && front.contains("WebSocket.onMessage s Got")
            && front.contains("WebSocket.receiveWithin 5000 s"),
        "the socket calls must stay in the client:\n{front}"
    );
    assert!(
        !back.contains("POST /_rpc/"),
        "no WebSocket arm may become an RPC:\n{back}"
    );
}

/// The WebSocket fixture builds for `--target web:app`: the wasm client links
/// the browser WebSocket kernels (runtime-go websocket_wasm.go) and the model's
/// `Maybe WebSocket` field. Go-gated.
#[test]
fn websocket_fixture_builds_both_trees() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-websocket"),
        &proj,
    );
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let split = proj.join(".skyapp/web-app/.split");
    let ok = out.status.success()
        && split.join("backend/sky-out/app").is_file()
        && dist_has_wasm(&split.join("frontend/dist"));
    let back = std::fs::read_to_string(split.join("backend/src/Main.sky")).unwrap_or_default();
    let front = std::fs::read_to_string(split.join("frontend/src/Main.sky")).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&proj);
    assert!(
        ok,
        "the web:app build must produce the backend and the wasm client:\n{log}"
    );
    // The client-held socket (`sock : Maybe WebSocket`) cannot be encoded
    // (the v0.27.0 Encodable rule): the saved model writes it `Nothing` and a
    // decoded model clears it, through `Spa_modelToJson` / `Spa_modelFromJson`.
    assert!(
        front.contains("spaModelToJson_ ({ m_ | sock = Nothing")
            && front.contains("m_ | sock = Nothing"),
        "the saved model must leave the client socket out:\n{front}"
    );
    // `withRoutes routes` names ONE table that mixes a page route with the
    // `App.api "GET /ws"` upgrade: the endpoint must be mounted on the backend
    // (it used to stay in the client route table, and `/ws` answered 404).
    assert!(
        back.contains("spaApiRoutes_") && back.contains("App.apiServerRoute"),
        "the backend must mount the `App.api \"GET /ws\"` endpoint:\n{back}"
    );
}

/// A scratch copy of `fixtures/<name>` with `edits` applied (`(file, from, to)`,
/// each `from` occurring exactly once), built with `sky build --target web:app`.
fn web_app_build(
    name: &str,
    edits: &[(&str, &str, &str)],
) -> (PathBuf, std::process::Output, String) {
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("tests/fixtures")
            .join(name),
        &proj,
    );
    for (file, from, to) in edits {
        let p = proj.join(file);
        let src = std::fs::read_to_string(&p).unwrap();
        assert_eq!(
            src.matches(from).count(),
            1,
            "`{from}` must occur once in {file}"
        );
        std::fs::write(&p, src.replacen(from, to, 1)).unwrap();
    }
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    (proj, out, log)
}

fn split_file(proj: &std::path::Path, rel: &str) -> String {
    std::fs::read_to_string(proj.join(".skyapp/web-app/.split").join(rel)).unwrap_or_default()
}

/// The `X-Sky-Wire` header a current (v0.27.0+) client sends, with the wire
/// hash the split recorded for `proj` (E-4). A request WITHOUT it is a legacy
/// page: its follow-up branch runs the follow-ups on the server and answers
/// none, so a test of the follow-up a current client runs must send it.
fn split_wire_header(proj: &std::path::Path) -> String {
    let toml = split_file(proj, "frontend/sky.toml");
    let wire = toml
        .lines()
        .find_map(|l| l.trim().strip_prefix("wire = \""))
        .map(|w| w.trim_end_matches('"').to_string())
        .unwrap_or_else(|| panic!("the frontend records its wire hash:\n{toml}"));
    format!("X-Sky-Wire: {wire}")
}

/// Start the split backend of `proj` on a free port.
fn start_split_backend(proj: &std::path::Path) -> (Killed, u16) {
    let port = free_port();
    let dir = proj.join(".skyapp/web-app/.split/backend");
    let log = dir.join("server.log");
    let child = Killed(
        Command::new(dir.join("sky-out/app"))
            .current_dir(&dir)
            .env("PORT", port.to_string())
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&log).unwrap())
            .stderr(std::fs::File::create(dir.join("server.err")).unwrap())
            .spawn()
            .expect("start the split backend"),
    );
    assert!(
        wait_for_spa_backend(&log, 120),
        "the split backend did not start:\n{}",
        std::fs::read_to_string(&log).unwrap_or_default()
    );
    (child, port)
}

/// Build `proj` as a Sky.Live app and start it on a free port.
fn start_live_app(proj: &std::path::Path) -> (Killed, u16) {
    let live = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(proj)
        .output()
        .expect("run sky build (Sky.Live)");
    assert!(
        live.status.success(),
        "the Sky.Live build failed:\n{}{}",
        String::from_utf8_lossy(&live.stdout),
        String::from_utf8_lossy(&live.stderr)
    );
    let port = free_port();
    let log = proj.join("live.log");
    let child = Killed(
        Command::new(proj.join("sky-out/app"))
            .current_dir(proj)
            .env("SKY_LIVE_PORT", port.to_string())
            .env("PORT", port.to_string())
            .env_remove("SKY_LIVE_STORE")
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&log).unwrap())
            .stderr(std::fs::File::create(proj.join("live.err")).unwrap())
            .spawn()
            .expect("start the Sky.Live app"),
    );
    assert!(
        wait_for_log(&log, &format!("listening on :{port}"), 120),
        "the Sky.Live app did not start:\n{}",
        std::fs::read_to_string(&log).unwrap_or_default()
    );
    (child, port)
}

/// The first `ARM …` line written to `log` after byte `from`. When a line is
/// expected, the log is polled for up to 15 s: a slow runner writes it late,
/// and a fixed sleep then read turned that into a false red (G-3). When none is
/// expected, the log is read after a settle time, as before: absence cannot be
/// polled for.
fn arm_line_after(log: &std::path::Path, from: usize, expect: bool) -> String {
    let read = || {
        std::fs::read_to_string(log)
            .unwrap_or_default()
            .get(from..)
            .unwrap_or_default()
            .lines()
            .find_map(|l| l.find("ARM ").map(|i| l[i..].trim().to_string()))
    };
    if expect {
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(15);
        loop {
            if let Some(line) = read() {
                return line;
            }
            if std::time::Instant::now() >= deadline {
                return String::new();
            }
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
    }
    std::thread::sleep(std::time::Duration::from_millis(300));
    read().unwrap_or_default()
}

/// Press the `i`-th button of the Live app and return the text that follows
/// `prefix` in the re-rendered page, up to the next `<`.
fn live_press_and_read(port: u16, jar: &std::path::Path, i: usize, prefix: &str) -> String {
    let page = curl_get_jar(port, "/", jar);
    let hids = handler_ids(&page);
    let hid = hids
        .get(i)
        .unwrap_or_else(|| panic!("no button {i} in the Live page:\n{page}"));
    assert_eq!(live_event(port, jar, hid), "200", "Live event {i}");
    std::thread::sleep(std::time::Duration::from_millis(300));
    let body = curl_get_jar(port, "/", jar);
    let rest = &body[body
        .find(prefix)
        .unwrap_or_else(|| panic!("`{prefix}` not rendered:\n{body}"))
        + prefix.len()..];
    rest[..rest.find('<').unwrap_or(rest.len())].to_string()
}

/// The value the client's `Got` arm writes to `out` after a `Fetch` RPC, for
/// the fixtures whose `Got` is a client arm. Since v0.27.0 such a continuation
/// is never settled on the server (docs/skyspa/auto-split.md §18, "only server
/// continuations chain"): the RPC answers the task's `result`, the client
/// dispatches `Got result` through its own `update`. Both fixtures' `Got` arms
/// write `"failed"` for an `Err`; an `Ok` is not expected (port 1 refuses).
fn client_got_out(front: &str, posted: &(u32, String)) -> String {
    assert!(
        front.contains("update (Got resp.result)"),
        "the client dispatches the RPC's result to its own `Got` arm:\n{front}"
    );
    let v: serde_json::Value = serde_json::from_str(&posted.1).unwrap();
    match v["result"][0].as_str() {
        Some("Err") => "failed".to_string(),
        other => panic!("the RPC must answer the task's Err result, got {other:?}: {posted:?}"),
    }
}

/// With `App.withClientCrypto`, a client arm seals under a key it derived with
/// `Kdf` (the explicit-nonce AEAD) and computes a MAC: both run in the wasm
/// client. Before, the build refused the arm as "client-held crypto (Kdf) and
/// a server effect (… Crypto.chacha20Poly1305Seal)", and `Crypto.hmacSha256`
/// forced an RPC. Without the opt-in both stay server branches (the client
/// holds no keys by default).
#[test]
fn client_crypto_seals_and_macs_in_the_client_under_the_opt_in() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-client-crypto-aead", &[]);
    assert!(
        log.contains("server branches (→ RPC): (none)")
            && log.contains("client branches (local): Seal, Mac"),
        "Seal and Mac must be client branches under the opt-in:\n{log}"
    );
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains("Crypto.chacha20Poly1305Seal") && front.contains("Crypto.hmacSha256"),
        "the client update keeps the AEAD and the MAC:\n{front}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
        assert!(dist_has_wasm(
            &proj.join(".skyapp/web-app/.split/frontend/dist")
        ));
    }
    let _ = std::fs::remove_dir_all(&proj);

    let (proj, _, log) = web_app_build(
        "spa-client-crypto-aead",
        &[(
            "src/Main.sky",
            "\n            |> App.withClientCrypto)",
            ")",
        )],
    );
    assert!(
        log.contains("server branches (→ RPC): Seal, Mac"),
        "without the opt-in the keyed crypto stays on the server:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// Pure stdlib kernels with no pseudo-module (`Bytes.slice`, `Bytes.length`,
/// `Bytes.toHex`) and pure time formatting run in the client. Before, each
/// sent its branch to the server: `server branches (→ RPC): Slice, Length,
/// Hex`.
#[test]
fn pure_stdlib_kernels_stay_in_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-pure-kernels", &[]);
    assert!(
        log.contains("server branches (→ RPC): (none)")
            && log.contains("client branches (local): Slice, Length, Hex, Stamp, Plain"),
        "every branch is pure and must stay in the client:\n{log}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// A comment with parentheses above a server arm does not change how the
/// split reads the arm. Before, the pattern's span started at the comment and
/// the split refused the arm ("could not read the argument types or the
/// pattern of this branch").
#[test]
fn a_comment_above_a_server_arm_does_not_change_the_split() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-arm-comment", &[]);
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains("Fetch ((Ok url) as spaArg0_) ->"),
        "the server arm is read and rewritten positionally:\n{log}\n{front}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// A server-only `Net.send` excludes only itself: the entry's own pure `send`
/// stays in the client. Before, exclusion was by bare name, so both were
/// dropped and the client failed with `Undefined name: send`. The server arm
/// answers as the Live app does.
#[test]
fn a_server_only_function_excludes_only_itself_not_a_same_named_client_function() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-tainted-name-scope", &[]);
    assert!(
        log.contains("excluded from frontend (server-tainted): Net.send"),
        "the excluded binding is named with its module:\n{log}"
    );
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains("\nsend model =") && front.contains("( send model, Cmd.none )"),
        "the entry's own `send` stays in the client:\n{front}"
    );
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let (back, back_port) = start_split_backend(&proj);
    let posted = curl_post_status_body(
        back_port,
        "/_rpc/Fetch",
        r#"{"spaArg0_":["Ok","http://127.0.0.1:1/"]}"#,
    )
    .expect("POST /_rpc/Fetch");
    assert_eq!(posted.0, 200, "{posted:?}");
    drop(back);
    let client_out = client_got_out(&front, &posted);
    let (live, live_port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let live_out = live_press_and_read(live_port, &jar, 0, "OUT=");
    drop(live);
    assert_eq!(
        format!("{client_out} COUNT=0"),
        live_out,
        "the split must end where the Live app renders"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// A module whose only function runs on the server keeps its TYPES in the
/// client. Before, the whole module was left out of the client and the client
/// `Msg` lost `Shape.Batch` (`Undefined name: Shape.Batch`). The client-result
/// RPC answers the batch the Live app renders.
#[test]
fn a_server_only_module_keeps_its_types_in_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-server-module-types", &[]);
    let shape = split_file(&proj, "frontend/src/Shape.sky");
    assert!(
        shape.contains("module Shape exposing (Batch") && !shape.contains("fetch n ="),
        "the client keeps `Shape`'s type and drops its server function:\n{log}\n{shape}"
    );
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let (back, back_port) = start_split_backend(&proj);
    let posted = curl_post_status_body(back_port, "/_rpc/Ask", r#"{"spaArg0_":["Ok",1]}"#)
        .expect("POST /_rpc/Ask");
    drop(back);
    assert_eq!(posted.0, 200, "{posted:?}");
    assert!(
        posted.1.contains("\"next\":2") && posted.1.contains("\"ready\":false"),
        "the RPC answers the batch `Shape.fetch 1` returns: {posted:?}"
    );
    let (live, live_port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let live_out = live_press_and_read(live_port, &jar, 0, "NEXT=");
    drop(live);
    assert_eq!(
        live_out, "2 not ready",
        "the Live app renders the same batch"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// A wire record copied into `Shared` that names another module's type gets
/// what that type needs: an import of a pure module (under the alias the
/// source uses), or a copy of a type from a server-only module. Before, the
/// copy lost it (`Undefined name: Chan.Batch`; in this fixture a type
/// mismatch). The client-result RPC answers the nested record.
#[test]
fn a_copied_wire_record_brings_the_types_it_names() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let go = required(Need::Go, have_go());
    let (proj, out, log) = web_app_build("spa-shared-foreign-type", &[]);
    let shared = split_file(&proj, "shared/Shared.sky");
    assert!(
        shared.contains("import Shape exposing (..)") && shared.contains("at : Shape.Point"),
        "Shared imports the pure module the copied record names:\n{shared}"
    );
    if go {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
        let (back, back_port) = start_split_backend(&proj);
        let url = format!("http://127.0.0.1:{back_port}/");
        let posted = curl_post_status_body(
            back_port,
            "/_rpc/Fetch",
            &format!(r#"{{"spaArg0_":["Ok","{url}"]}}"#),
        )
        .expect("POST /_rpc/Fetch");
        drop(back);
        assert_eq!(posted.0, 200, "{posted:?}");
        assert!(
            posted.1.contains("\"at\"") && posted.1.contains("\"y\":0") && posted.1.contains(&url),
            "the answer carries the nested `Shape.Point`: {posted:?}"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);

    // The same record under an import alias.
    let (proj, out, log) = web_app_build(
        "spa-shared-foreign-type",
        &[
            ("src/Main.sky", "import Shape\n", "import Shape as S\n"),
            (
                "src/Main.sky",
                "    , at : Shape.Point\n",
                "    , at : S.Point\n",
            ),
            ("src/Main.sky", "(.x Shape.origin)", "(.x S.origin)"),
        ],
    );
    let shared = split_file(&proj, "shared/Shared.sky");
    assert!(
        shared.contains("import Shape as S exposing (..)"),
        "Shared imports the module under the alias the copied record uses:\n{shared}"
    );
    if go {
        assert!(
            out.status.success(),
            "the aliased web:app build failed:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);

    // The named type lives in a module with a server function: it is copied.
    let (proj, out, log) = web_app_build(
        "spa-shared-foreign-type",
        &[
            (
                "src/Shape.sky",
                "module Shape exposing (Point, origin)\n\nimport Sky.Core.Prelude exposing (..)\n",
                "module Shape exposing (Point, origin, ping)\n\nimport Sky.Core.Http as Http\nimport Sky.Core.Prelude exposing (..)\n\n\nping : String -> Task Error Int\nping url =\n    Http.get url |> Task.map .status\n",
            ),
        ],
    );
    let shared = split_file(&proj, "shared/Shared.sky");
    assert!(
        shared.contains("type alias Point")
            && shared.contains("at : Point")
            && !shared.contains("import Shape"),
        "a server module's type is copied into Shared, its references made bare:\n{shared}"
    );
    if go {
        assert!(
            out.status.success(),
            "the web:app build with a server module failed:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// A result Msg that the server arm applies to a captured argument
/// (`Cmd.perform (Http.get url) (Got url)`) crosses whole: the RPC answers
/// the follow-up `Got url result`, which the client dispatches. Before, the
/// split made a client-result RPC and the client called `update (Got
/// resp.result)`, without `url`, which did not type-check.
#[test]
fn a_result_msg_with_a_captured_argument_crosses_with_the_argument() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-captured-result-msg", &[]);
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        !front.contains("update (Got resp.result)"),
        "the client must not rebuild `Got` from the result alone:\n{front}"
    );
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let (back, back_port) = start_split_backend(&proj);
    let url = "http://127.0.0.1:1/";
    // A current client (E-4 wire header): the follow-up comes back for it.
    let wire = split_wire_header(&proj);
    let (code, _, body) = curl_post_with_headers(
        back_port,
        "/_rpc/Fetch",
        &format!(r#"{{"spaArg0_":["Ok","{url}"]}}"#),
        &[&wire],
    );
    let posted = (code, body);
    drop(back);
    assert_eq!(posted.0, 200, "{posted:?}");
    let v: serde_json::Value = serde_json::from_str(&posted.1).unwrap();
    let follow = v["spaFollow_"].as_str().unwrap_or_default().to_string();
    assert!(
        follow.contains("Got") && follow.contains(url) && follow.contains("Err"),
        "the follow-up carries `Got` with its captured url and the result: {posted:?}"
    );
    let (live, live_port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let live_out = live_press_and_read(live_port, &jar, 0, "OUT=");
    drop(live);
    assert_eq!(
        live_out,
        format!("{url} failed"),
        "the Live app runs `Got url (Err _)`"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// `init` and `update` given to `App.app` in any form: an inline lambda, a
/// function under another name, a `let`-bound lambda, an eta-expanded
/// `update`. Before, an inline `init` with a server branch gave a backend
/// that called an undefined `init`. The inline form is also run: its server
/// arm answers as the Live app does.
#[test]
fn app_fields_in_any_form_build_with_a_server_branch() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let go = required(Need::Go, have_go());
    let (proj, out, log) = web_app_build("spa-inline-init", &[]);
    assert!(log.contains("server branches (→ RPC): Fetch"), "{log}");
    if go {
        assert!(
            out.status.success(),
            "the inline-init web:app build failed:\n{log}"
        );
        let (back, back_port) = start_split_backend(&proj);
        let posted = curl_post_status_body(
            back_port,
            "/_rpc/Fetch",
            r#"{"spaArg0_":["Ok","http://127.0.0.1:1/"]}"#,
        )
        .expect("POST /_rpc/Fetch");
        drop(back);
        assert_eq!(posted.0, 200, "{posted:?}");
        let front = split_file(&proj, "frontend/src/Main.sky");
        let client_out = client_got_out(&front, &posted);
        let (live, live_port) = start_live_app(&proj);
        let jar = proj.join("jar.txt");
        let live_out = live_press_and_read(live_port, &jar, 0, "OUT=");
        drop(live);
        assert_eq!(client_out, live_out, "{posted:?}");
    }
    let _ = std::fs::remove_dir_all(&proj);

    let variants: [(&str, &[(&str, &str, &str)]); 3] = [
        (
            "a named init and a renamed update",
            &[
                (
                    "src/Main.sky",
                    "            { init = \\_ -> ( { out = \"\" }, Cmd.none )\n            , update = update\n",
                    "            { init = start\n            , update = step\n",
                ),
                ("src/Main.sky", "update : Msg -> Model", "step : Msg -> Model"),
                ("src/Main.sky", "update msg model =", "step msg model ="),
                ("src/Main.sky", "\nmain =\n", "\nstart : () -> ( Model, Cmd Msg )\nstart _ =\n    ( { out = \"\" }, Cmd.none )\n\n\nmain =\n"),
            ],
        ),
        (
            "a let-bound init",
            &[
                (
                    "src/Main.sky",
                    "main =\n    App.run\n",
                    "main =\n    let\n        start =\n            \\_ -> ( { out = \"\" }, Cmd.none )\n    in\n    App.run\n",
                ),
                (
                    "src/Main.sky",
                    "            { init = \\_ -> ( { out = \"\" }, Cmd.none )\n",
                    "            { init = start\n",
                ),
            ],
        ),
        (
            "an eta-expanded update",
            &[(
                "src/Main.sky",
                "            , update = update\n",
                "            , update = \\msg model -> update msg model\n",
            )],
        ),
    ];
    for (what, edits) in variants {
        let (proj, out, log) = web_app_build("spa-inline-init", edits);
        assert!(!log.contains("Undefined name: init"), "{what}:\n{log}");
        if go {
            assert!(
                out.status.success(),
                "{what}: the web:app build failed:\n{log}"
            );
        }
        let _ = std::fs::remove_dir_all(&proj);
    }
}

/// A model that is not a record (`String`) with a server branch: the model
/// rides the wire whole. Before, the generated RPC handler treated it as a
/// record (`[macHandler] type mismatch: String vs record`). The RPC answers
/// the MAC the Live app renders.
#[test]
fn a_non_record_model_crosses_the_wire_whole() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-nonrecord-model", &[]);
    assert!(log.contains("server branches (→ RPC): Mac"), "{log}");
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let (back, back_port) = start_split_backend(&proj);
    let posted = curl_post_status_body(back_port, "/_rpc/Mac", r#"{"spaModel_":""}"#)
        .expect("POST /_rpc/Mac");
    drop(back);
    assert_eq!(posted.0, 200, "{posted:?}");
    let rpc: serde_json::Value = serde_json::from_str(&posted.1).unwrap();
    let (live, live_port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let live_out = live_press_and_read(live_port, &jar, 0, "MAC=");
    drop(live);
    assert_eq!(
        rpc["spaModel_"].as_str(),
        Some(live_out.as_str()),
        "{posted:?}"
    );
    assert_eq!(live_out.len(), 64, "a hex HMAC-SHA256: {live_out}");
    let _ = std::fs::remove_dir_all(&proj);
}

/// An `init` whose seed is not `()` is reported once, at the `init` field of
/// `App.app`, for every target, and the user's own text is shown. Before,
/// `sky check` / `sky build` put the caret on a rewritten `App.runLive` line
/// in `main`, a `--target web:app` build compiled a backend and then failed
/// in the generated `spaModelBlank_`, and the terminal and client runners
/// accepted the program.
#[test]
fn a_wrong_init_seed_is_reported_at_init_for_every_target() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/std-app-init-seed-error"),
        &proj,
    );
    for args in [
        vec!["check", "src/Main.sky"],
        vec!["build", "src/Main.sky"],
        vec!["build", "--target", "web:app", "src/Main.sky"],
        vec!["check", "--target", "web:app", "src/Main.sky"],
        vec!["check", "--target", "terminal:tui", "src/Main.sky"],
        vec!["check", "--target", "mobile:android", "src/Main.sky"],
    ] {
        let out = Command::new(SKY)
            .args(&args)
            .current_dir(&proj)
            .output()
            .expect("run sky");
        let log = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        assert!(!out.status.success(), "{args:?} must fail:\n{log}");
        assert!(
            log.contains("src/Main.sky:23:22 [E2001]")
                && log.contains("in the `init` field of the record passed to `Std.App.app`"),
            "{args:?}: the error is at `init`:\n{log}"
        );
        assert_eq!(
            log.matches("[E2001]").count(),
            1,
            "{args:?}: one error:\n{log}"
        );
        for bad in [
            "runLive",
            "runSpa",
            "spaModelBlank_",
            "SYNTHESISED",
            "Compilation successful",
        ] {
            assert!(
                !log.contains(bad),
                "{args:?}: `{bad}` must not appear:\n{log}"
            );
        }
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// `Sub.onFragment`: the Sky.Live app receives the fragment its browser
/// client reports (the `__skyFragment` event, the path the client JS takes at
/// load and on hashchange) and renders it; the same source builds as a
/// Sky.Spa client whose subscriptions keep the leaf (the wasm client reads
/// `location.hash`), and checks for a terminal target, where it is inert.
#[test]
fn the_url_fragment_reaches_update_on_live_and_builds_for_every_target() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("std-app-fragment", &[]);
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains("Sub.onFragment FragmentChanged"),
        "the client keeps the fragment subscription:\n{log}\n{front}"
    );
    let tui = Command::new(SKY)
        .args(["check", "--target", "terminal:tui", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky check --target terminal:tui");
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    assert!(
        tui.status.success(),
        "the terminal check failed:\n{}{}",
        String::from_utf8_lossy(&tui.stdout),
        String::from_utf8_lossy(&tui.stderr)
    );
    let (live, port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let page = curl_get_jar(port, "/", &jar);
    assert!(page.contains("FRAG=none"), "{page}");
    let jar_text = std::fs::read_to_string(&jar).unwrap_or_default();
    let csrf = jar_text
        .lines()
        .filter_map(|l| {
            let cols: Vec<&str> = l.split('\t').collect();
            (cols.len() >= 7 && cols[5] == "__sky_csrf").then(|| cols[6].to_string())
        })
        .last()
        .unwrap_or_default();
    let posted = Command::new("curl")
        .args([
            "-s",
            "--max-time",
            "30",
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "-H",
            "Content-Type: application/json",
            "-H",
            &format!("X-Sky-Csrf: {csrf}"),
            "-X",
            "POST",
            "-d",
            r#"{"sessionId":"","msg":"__skyFragment","args":["part-2"],"handlerId":""}"#,
            "-b",
        ])
        .arg(&jar)
        .arg(format!("http://127.0.0.1:{port}/_sky/event"))
        .output()
        .expect("curl POST __skyFragment");
    assert_eq!(String::from_utf8_lossy(&posted.stdout).trim(), "200");
    std::thread::sleep(std::time::Duration::from_millis(300));
    let page = curl_get_jar(port, "/", &jar);
    drop(live);
    assert!(
        page.contains("FRAG=part-2"),
        "the fragment reaches update:\n{page}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// A record alias from a `[dependencies]` PATH package is a record in the
/// `--target web:app` split, as `sky check` and the Sky.Live build say. The
/// split's analysis loaded `.skydeps/` but not path packages, so `import
/// Geo.Shape exposing (Point)` named a missing module and the build failed
/// with `[update] type mismatch: Point vs record`. The fixture uses `Point`
/// in the model, in a client Msg payload, in the result of a server arm and
/// in the view; the server arm's result crosses the wire through a codec
/// derived from the dependency's declaration (named by its module in
/// `Shared`), and the RPC answers what the Sky.Live app renders.
#[test]
fn a_record_alias_from_a_path_dependency_crosses_the_split() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let root = scratch();
    let _ = std::fs::remove_dir_all(&root);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-path-dep-record"),
        &root,
    );
    let proj = root.join("app");
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !log.contains("type mismatch") && !log.contains("anonymous record"),
        "the split must type-check the dependency's record alias:\n{log}"
    );
    assert!(
        log.contains("server branches (→ RPC): Jump")
            && log.contains("client branches (local): Move, Place, Landed"),
        "{log}"
    );
    let shared = split_file(&proj, "shared/Shared.sky");
    assert!(
        shared.contains("import Geo.Shape as SpaTy_Geo_Shape_")
            && shared.contains("at : SpaTy_Geo_Shape_.Point")
            && shared.contains("Codec.auto blankGeo_Shape_Point_"),
        "Shared names the dependency's type by its module and derives its codec:\n{shared}"
    );
    for leg in ["frontend", "backend"] {
        let toml = split_file(&proj, &format!("{leg}/sky.toml"));
        assert!(
            toml.contains("\"geo\" = { path = \"/") && toml.contains("/lib\" }"),
            "the {leg} project carries the path dependency by an absolute path:\n{toml}"
        );
    }
    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&root);
        return;
    }
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let (back, back_port) = start_split_backend(&proj);
    // A current client (E-4 wire header): the follow-up comes back for it.
    let wire = split_wire_header(&proj);
    let (code, _, body) = curl_post_with_headers(
        back_port,
        "/_rpc/Jump",
        r#"{"at":{"x":1,"y":2},"log":""}"#,
        &[&wire],
    );
    let posted = (code, body);
    drop(back);
    assert_eq!(posted.0, 200, "{posted:?}");
    // `Jump` writes `log` and returns `Landed`, which the client dispatches:
    // the RPC answers the write-set and the follow-up Msg, whose `Point`
    // crossed through the codec derived from the dependency.
    let v: serde_json::Value = serde_json::from_str(&posted.1).unwrap();
    assert_eq!(v["log"], "J", "{posted:?}");
    let follow: serde_json::Value =
        serde_json::from_str(v["spaFollow_"].as_str().unwrap_or("null")).unwrap();
    assert_eq!(follow[0][0], "Landed", "{posted:?}");
    let body: serde_json::Value =
        serde_json::from_str(follow[0][1].as_str().unwrap_or("null")).unwrap();
    assert_eq!(
        body["a0"],
        serde_json::json!(["Ok", { "x": 11, "y": 2 }]),
        "the follow-up carries `Shape.shift 10 model.at`: {posted:?}"
    );
    let (live, live_port) = start_live_app(&proj);
    let jar = proj.join("jar.txt");
    let live_out = live_press_and_read(live_port, &jar, 2, "AT=");
    drop(live);
    assert_eq!(
        live_out, "11,2 LOG=JL",
        "the Live app lands on the same point"
    );
    let _ = std::fs::remove_dir_all(&root);
}

/// Every type the split generates a codec for is resolved by its MODULE,
/// never by its bare name, in both directions. The app declares `type alias
/// Pending`, and a Msg carries the stdlib's `Cpace.Pending` (device key
/// material under `App.withClientCrypto`). `Fetch`'s command cannot be read,
/// so the split writes a follow-up codec for every Msg it can. Before, it
/// resolved `Cpace.Pending` to the app's record: the build failed with
/// `[spaEncodeFollow_] type mismatch: Pending vs record`, and the "no key on
/// a wire" rule was decided on the wrong type. Now `Started` (the key) does
/// not cross, and says why on the key's own name, while `Queued` (the app's
/// `Pending`) crosses with a derived codec.
#[test]
fn a_follow_up_payload_type_is_resolved_by_its_module_not_its_bare_name() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-follow-bare-name", &[]);
    assert!(
        !log.contains("type mismatch"),
        "the follow-up codecs must type-check:\n{log}"
    );
    assert!(
        log.contains(
            "`Started` (argument 1: `Std.Crypto.Cpace.Pending` is key material the device keeps"
        ),
        "the key-on-wire rule is decided on the stdlib key type:\n{log}"
    );
    let shared = split_file(&proj, "shared/Shared.sky");
    assert!(
        !shared.contains("SpaFollowStartedReq"),
        "the key never gets a wire record:\n{shared}"
    );
    assert!(
        shared.contains("type alias SpaFollowQueuedReq")
            && shared.contains("a0 : Result Error Pending")
            && shared.contains("autoPendingCodec_"),
        "the app's own `Pending` crosses with a derived codec:\n{shared}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// A device key that a continuation would carry to the client is still
/// refused: the key-on-wire rule reads the resolved identity
/// (`Sky.Core.Secret.Secret`), so resolving by module does not open a way
/// around it.
#[test]
fn a_device_key_on_a_continuation_is_still_refused() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch_std_app(
        "spa-key-continuation",
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Secret as Secret exposing (Secret)
import Sky.Core.Task as Task
import Sky.Core.Time as Time
import Std.App as App
import Std.Ui as Ui


type alias Model =
    { key : Maybe Secret
    , out : String
    }


type Msg
    = Fetch
    | Revealed (Result Error Secret)


init : () -> ( Model, Cmd Msg )
init _ =
    ( { key = Nothing, out = "" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Fetch ->
            ( model, Cmd.perform (Time.sleep 1 |> Task.map (\_ -> Secret.fromString "k")) Revealed )

        Revealed (Ok k) ->
            ( { model | key = Just k, out = "revealed" }, Cmd.none )

        Revealed (Err _) ->
            ( { model | out = "failed" }, Cmd.none )


main =
    App.run
        (App.app
            { init = init
            , update = update
            , view = \m -> Ui.column [] [ Ui.text ("OUT=" ++ m.out), Ui.button [] { onPress = Just Fetch, label = Ui.text "fetch" } ]
            , subscriptions = \_ -> Sub.none
            }
            |> App.withNotFound ()
            |> App.withClientCrypto
        )
"#,
    );
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(!out.status.success(), "a key on the wire must fail:\n{log}");
    assert!(
        log.contains("`Sky.Core.Secret.Secret` is key material the device keeps"),
        "{log}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// Std.Nav in a SERVER arm: the navigation moves the browser, so the client
/// runs it when it sends the request (as Sky.Live runs it when the update
/// returns), and the RPC still runs the server half. The browser behaviour is
/// driven by `scripts/nav-e2e.sh`.
#[test]
fn a_server_arm_navigation_runs_in_the_client() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("nav-cmds", &[]);
    assert!(
        log.contains("server branches (→ RPC): Save")
            && log.contains("client branches (local): Navigated, GotFragment, Go, Home, Frag, Clear, Saved, Evil"),
        "a navigation alone keeps an arm in the client:\n{log}"
    );
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains(
            "Cmd.batch [ Cmd.batch [ Nav.pushUrl \"/about\" ], Spa.rpc saveReqCodec saveRespCodec \"/_rpc/Save\""
        ),
        "the client runs the server arm's navigation when it sends the RPC:\n{front}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// M-1: an arm that hands the whole model to a helper returning `( model,
/// Cmd.perform serverTask ToMsg )` writes the whole model AND has a follow-up.
/// The response holds the model's fields plus `spaFollow_`, so the client folds
/// the fields back instead of taking the response as the model. The frontend
/// used to fail to build (`record has unknown field(s) … spaFollow_`); it built
/// on v0.26.1.
#[test]
fn a_whole_model_write_with_a_follow_up_builds() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-whole-model-follow", &[]);
    let front = split_file(&proj, "frontend/src/Main.sky");
    assert!(
        front.contains("( { model | draft = resp.draft, notice = resp.notice }, Cmd.none )")
            && !front.contains("( resp, Cmd.none )"),
        "the whole-model follow-up response is folded back field by field:\n{front}"
    );
    if required(Need::Go, have_go()) {
        assert!(out.status.success(), "the web:app build failed:\n{log}");
    }
    let _ = std::fs::remove_dir_all(&proj);
}

/// POST `body` to `path` with extra `headers`; returns (status, response
/// headers lower-cased, body).
fn curl_post_with_headers(
    port: u16,
    path: &str,
    body: &str,
    headers: &[&str],
) -> (u32, String, String) {
    let mut cmd = Command::new("curl");
    cmd.args([
        "-s",
        "-i",
        "-X",
        "POST",
        "-H",
        "Content-Type: application/json",
    ]);
    for h in headers {
        cmd.args(["-H", h]);
    }
    let out = cmd
        .args(["-d", body, &format!("http://127.0.0.1:{port}{path}")])
        .output()
        .expect("curl");
    let text = String::from_utf8_lossy(&out.stdout).replace("\r\n", "\n");
    let (head, body) = text.split_once("\n\n").unwrap_or((&text, ""));
    let status = head
        .lines()
        .next()
        .and_then(|l| l.split_whitespace().nth(1))
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    (status, head.to_ascii_lowercase(), body.to_string())
}

/// E-4 (the generator half; the runtime is S2's): the split stamps a
/// wire-schema hash into the backend (`Spa_setWireHash` at boot) and the page
/// (`<meta name="sky-wire">` in the static index), and a follow-up branch
/// answers a pre-v0.27 page (no `X-Sky-Wire`) by running the server-runnable
/// follow-ups inline, so nothing runs twice.
/// - a request for another wire hash gets 409 + `X-Sky-Status: reload`;
/// - a current request answers the follow-up for the client to run;
/// - a header-less (legacy) request runs it on the server and answers none.
#[test]
fn the_wire_hash_and_legacy_follow_ups_are_generated() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build("spa-whole-model-follow", &[]);
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let front_toml = split_file(&proj, "frontend/sky.toml");
    let wire = front_toml
        .lines()
        .find_map(|l| l.trim().strip_prefix("wire = \""))
        .map(|w| w.trim_end_matches('"').to_string())
        .unwrap_or_else(|| panic!("the frontend records its wire hash:\n{front_toml}"));
    let back = split_file(&proj, "backend/src/Main.sky");
    assert!(
        back.contains(&format!("spaSetWireHash_ \"{wire}\"")),
        "the backend sets the same hash at boot:\n{back}"
    );
    let index = split_file(&proj, "frontend/dist/index.html");
    assert!(
        index.contains(&format!("<meta name=\"sky-wire\" content=\"{wire}\" />")),
        "the page carries the hash:\n{index}"
    );
    let (_child, port) = start_split_backend(&proj);
    let body = r#"{"draft":"","notice":"","text":"hi"}"#;
    let (code, head, _) =
        curl_post_with_headers(port, "/_rpc/Ask", body, &["X-Sky-Wire: 0000000000000000"]);
    assert_eq!(code, 409, "another wire hash is told to reload:\n{head}");
    assert!(head.contains("x-sky-status: reload"), "{head}");
    let wire_header = format!("X-Sky-Wire: {wire}");
    let (code, _, current) = curl_post_with_headers(port, "/_rpc/Ask", body, &[&wire_header]);
    assert_eq!(code, 200, "{current}");
    let v: serde_json::Value = serde_json::from_str(&current).expect("json");
    assert!(
        v["spaFollow_"]
            .as_str()
            .is_some_and(|f| f.contains("Submitted")),
        "a current client runs the follow-up itself: {current}"
    );
    let (code, _, legacy) = curl_post_with_headers(port, "/_rpc/Ask", body, &[]);
    assert_eq!(code, 200, "{legacy}");
    let v: serde_json::Value = serde_json::from_str(&legacy).expect("json");
    assert_eq!(
        v["spaFollow_"].as_str(),
        Some("[]"),
        "a legacy page gets no follow-up to run a second time: {legacy}"
    );
    assert_ne!(
        v["notice"].as_str(),
        Some("sending"),
        "the follow-up ran on the server for the legacy page: {legacy}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// A server arm whose navigation the split cannot isolate (a helper returns
/// it) is refused: the backend cannot move the browser, and dropping the
/// navigation would be silent.
#[test]
fn a_server_arm_navigation_the_split_cannot_isolate_is_refused() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let (proj, out, log) = web_app_build(
        "nav-cmds",
        &[
            (
                "src/Main.sky",
                "                  , Nav.pushUrl \"/about\"\n",
                "                  , goAbout\n",
            ),
            (
                "src/Main.sky",
                "\n\nbutton : String",
                "\n\ngoAbout : Cmd Msg\ngoAbout =\n    Nav.pushUrl \"/about\"\n\n\nbutton : String",
            ),
        ],
    );
    assert!(!out.status.success(), "must be refused:\n{log}");
    assert!(
        log.contains(
            "server branch `Save` returns a navigation (`Std.Nav`) the split could not isolate"
        ),
        "{log}"
    );
    let _ = std::fs::remove_dir_all(&proj);
}

/// The split's analysis loads a path dependency as the build does: as app
/// code, type-checked and reported under its own path. A type error in the
/// package stops `sky spa-split` there, never in the synthesised client or at
/// run time.
#[test]
fn a_type_error_in_a_path_dependency_stops_the_split_at_its_own_file() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let root = scratch();
    let _ = std::fs::remove_dir_all(&root);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-path-dep-record"),
        &root,
    );
    let lib = root.join("lib/src/Geo/Shape.sky");
    let src = std::fs::read_to_string(&lib).unwrap();
    assert_eq!(src.matches("{ p | x = p.x + d }").count(), 1);
    std::fs::write(
        &lib,
        src.replace("{ p | x = p.x + d }", "{ p | x = p.x ++ \"x\" }"),
    )
    .unwrap();
    let out = Command::new(SKY)
        .args(["spa-split", "src/Main.sky", "--out"])
        .arg(root.join("split"))
        .current_dir(root.join("app"))
        .output()
        .expect("run sky spa-split");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(
        !out.status.success(),
        "a broken path package must stop the split:\n{log}"
    );
    assert!(
        log.contains("../lib/src/Geo/Shape.sky:17:") && log.contains("[E2001]"),
        "the error is reported in the package's own file:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&root);
}

/// `GET /_sky/sub` with NO `subscriptions` function the generator can find:
/// the backend has nothing to authorise a topic against, so it refuses every
/// topic (fail closed) and `sky spa-split` warns. The push fixture's own
/// `subscriptions` names "count"; renamed to `subs`, the same app must answer
/// 403 for "count" rather than stream it.
#[test]
fn sub_endpoint_refuses_every_topic_when_subscriptions_cannot_be_found() {
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    let fixture_dir = push_fixture_entry()
        .parent()
        .and_then(|p| p.parent())
        .unwrap()
        .to_path_buf();
    copy_tree(&fixture_dir, &proj);
    let main_path = proj.join("src/Main.sky");
    let src = std::fs::read_to_string(&main_path).unwrap();
    let renamed = src
        .replace(
            "subscriptions : Model -> Sub Msg\nsubscriptions _ =",
            "subs : Model -> Sub Msg\nsubs _ =",
        )
        .replace(
            "            , subscriptions = subscriptions\n",
            "            , subscriptions = subs\n",
        );
    assert_ne!(src, renamed, "the fixture's subscriptions must be renamed");
    std::fs::write(&main_path, renamed).unwrap();

    let out = proj.join(".split-out");
    let output = Command::new(SKY)
        .args([
            "spa-split",
            main_path.to_str().unwrap(),
            "--out",
            out.to_str().unwrap(),
        ])
        .output()
        .expect("run sky spa-split");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        output.status.success(),
        "sky spa-split must succeed:\n{log}"
    );
    assert!(
        log.contains("refuses every topic"),
        "sky spa-split must warn that /_sky/sub refuses every topic:\n{log}"
    );
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("subHandler _ =")
            && back.contains("Server.withStatus 403")
            && !back.contains("spaSubAllowsTopic_"),
        "with no subscriptions the handler must refuse without consulting a Sub:\n{back}"
    );
    assert!(
        back.contains("Server.api \"GET /_sky/sub\" subHandler"),
        "the endpoint stays mounted (a client that subscribes gets a 403, not a 404):\n{back}"
    );

    if !required(Need::Go, have_go()) {
        let _ = std::fs::remove_dir_all(&proj);
        return;
    }
    let backend_dir = out.join("backend");
    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&backend_dir)
        .output()
        .expect("run sky build (backend)");
    assert!(
        build.status.success(),
        "the backend must build:\n{}{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr)
    );
    let port = free_port();
    let log_path = backend_dir.join("server.log");
    let log_file = std::fs::File::create(&log_path).unwrap();
    let child = Killed(
        Command::new(backend_dir.join("sky-out/app"))
            .current_dir(&backend_dir)
            .env("PORT", port.to_string())
            .stdout(log_file.try_clone().unwrap())
            .stderr(log_file)
            .spawn()
            .expect("spawn the backend"),
    );
    assert!(
        wait_for_spa_backend(&log_path, 80),
        "the backend never reported listening on :{port}"
    );
    let count = curl_req(port, "GET", "/_sky/sub?topic=count", &[], None, "2");
    let other = curl_req(port, "GET", "/_sky/sub?topic=anything", &[], None, "2");
    drop(child);
    let _ = std::fs::remove_dir_all(&proj);
    assert_eq!(
        count.0, 403,
        "a topic the app's (unfound) subscriptions would name is refused: {count:?}"
    );
    assert_eq!(other.0, 403, "every other topic is refused: {other:?}");
}

/// Build and RUN `tests/fixtures/spa-followup-internal` (`--target web:app`):
/// `POST /_rpc/Bump` must answer with `Bump`'s write (`count = 1`) AND its
/// follow-up `Tracked` under its own tag, which the client decodes. Before the
/// fix `Tracked` (also reached inside the `Save` server chain) was encoded as an
/// empty tag `["",""]`, the client refused it and kept the old model: count
/// stayed 0 (the downstream "Add to basket" regression). Go-gated.
#[test]
fn a_follow_up_also_reached_by_a_server_chain_crosses_and_the_write_applies() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _build_lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-followup-internal"),
        &proj,
    );
    let out = Command::new(SKY)
        .args(["build", "--target", "web:app", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("sky build --target web:app");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "the web:app build failed:\n{log}");
    let back_dir = proj.join(".skyapp/web-app/.split/backend");
    std::fs::copy(proj.join("sky.toml"), back_dir.join("sky.toml")).ok();
    let port = free_port();
    let log_path = back_dir.join("server.log");
    let child = Killed(
        Command::new(back_dir.join("sky-out/app"))
            .current_dir(&back_dir)
            .env("PORT", port.to_string())
            .stdin(std::process::Stdio::null())
            .stdout(std::fs::File::create(&log_path).unwrap())
            .stderr(std::fs::File::create(back_dir.join("server.err")).unwrap())
            .spawn()
            .expect("start the backend"),
    );
    assert!(
        wait_for_spa_backend(&log_path, 120),
        "the backend did not start"
    );
    // A current client (E-4 wire header): the follow-up comes back for it.
    let wire = split_wire_header(&proj);
    let (code, _, body) =
        curl_post_with_headers(port, "/_rpc/Bump?rid=t-1", "{\"count\":0}", &[&wire]);
    drop(child);
    let _ = std::fs::remove_dir_all(&proj);
    assert_eq!(code, 200, "POST /_rpc/Bump must answer:\n{body}");
    assert!(
        body.contains("\"count\":1"),
        "Bump's write must come back:\n{body}"
    );
    assert!(
        body.contains("Tracked") && !body.contains("[\\\"\\\",\\\"\\\"]"),
        "the follow-up must carry its own tag, never an empty one:\n{body}"
    );
}
