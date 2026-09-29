//! The Sky.Spa client-held crypto opt-in (`Spa.withClientCrypto` /
//! `App.withClientCrypto`).
//!
//! By default the split puts every Std.Crypto function that holds a secret key
//! (Noise, Cpace, Kx, Sign, Kdf) on the SERVER. With the opt-in the device keeps
//! its own keys: those functions run in the wasm client, and the build refuses
//! every flow that would move a key to the server.
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-client-crypto`
//! and variants of it written to a temp dir:
//!   * with the marker, the key branches are CLIENT and the file read is SERVER;
//!   * without it, the same app keeps today's verdicts (the key branch is SERVER);
//!   * a dead (unreachable) marker does not turn the opt-in on;
//!   * each refusal: a branch mixing a key operation with a server effect, a key
//!     on the wire, key operations in `init`, a key field that is not a `Maybe`;
//!   * a `Maybe` key field is written as `Nothing` in the first paint and the
//!     saved model.

use project::{spa_partition, spa_split};
use std::path::{Path, PathBuf};

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

fn fixture_dir() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-client-crypto")
}

/// A copy of the fixture with `edits` applied to `src/Main.sky` (each
/// `(from, to)` must match exactly once).
fn variant(tag: &str, edits: &[(&str, &str)]) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-spa-client-crypto-{}-{tag}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::copy(fixture_dir().join("sky.toml"), dir.join("sky.toml")).unwrap();
    let mut src = std::fs::read_to_string(fixture_dir().join("src/Main.sky")).unwrap();
    for (from, to) in edits {
        assert_eq!(
            src.matches(from).count(),
            1,
            "variant `{tag}`: `{from}` must occur exactly once in the fixture"
        );
        src = src.replace(from, to);
    }
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
    dir
}

fn analyze(dir: &Path) -> Result<spa_partition::SpaPartitionReport, String> {
    spa_partition::analyze(&repo_root(), dir, None)
}

fn generate(dir: &Path, tag: &str) -> Result<(PathBuf, spa_split::SpaSplitReport), String> {
    let out = std::env::temp_dir().join(format!(
        "sky-spa-client-crypto-out-{}-{tag}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&out);
    spa_split::generate(&repo_root(), dir, None, &out, None, None).map(|r| (out, r))
}

fn server_of(r: &spa_partition::SpaPartitionReport, msg: &str) -> bool {
    r.branches
        .iter()
        .find(|b| b.msg == msg)
        .unwrap_or_else(|| panic!("no `{msg}` branch"))
        .server
}

const MARKER: &str = "\n            |> Spa.withClientCrypto)";

#[test]
fn with_the_opt_in_key_branches_stay_on_the_client() {
    let r = analyze(&fixture_dir()).unwrap_or_else(|e| panic!("analyze failed: {e}"));
    assert!(
        r.client_crypto,
        "the marker on `main` must turn the opt-in on"
    );
    assert!(
        !server_of(&r, "Connect"),
        "Connect creates the device key (Kx.generate, Noise.initiatorWith): it must be CLIENT"
    );
    assert!(
        !server_of(&r, "GotHandshake (Ok …)"),
        "GotHandshake runs Noise.writeMessage on the device handshake: it must be CLIENT"
    );
    assert!(
        server_of(&r, "Refresh"),
        "Refresh reads a file: it must stay SERVER"
    );
}

#[test]
fn without_the_opt_in_the_split_is_unchanged() {
    let dir = variant("off", &[(MARKER, ")")]);
    let r = analyze(&dir).unwrap_or_else(|e| panic!("analyze failed: {e}"));
    assert!(!r.client_crypto);
    assert!(
        server_of(&r, "Connect"),
        "without the opt-in a key operation is a SERVER effect, as before"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_dead_marker_does_not_turn_the_opt_in_on() {
    // The marker applied in a binding nothing reaches from `main`.
    let dir = variant(
        "dead",
        &[
            (MARKER, ")"),
            (
                "subscriptions : Model -> Sub Msg",
                "unused_ cfg =\n    Spa.withClientCrypto cfg\n\n\nsubscriptions : Model -> Sub Msg",
            ),
        ],
    );
    let r = analyze(&dir).unwrap_or_else(|e| panic!("analyze failed: {e}"));
    assert!(
        !r.client_crypto,
        "an unreachable marker must not turn the opt-in on"
    );
    assert!(server_of(&r, "Connect"));
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_branch_mixing_a_key_operation_and_a_server_effect_is_refused() {
    let dir = variant(
        "mixed",
        &[(
            "            --VARIANT-REFRESH--\n            ( model, Cmd.perform (File.readFile \"data/note.txt\") Refreshed )",
            "            ( model\n            , Cmd.batch\n                [ Cmd.perform (File.readFile \"data/note.txt\") Refreshed\n                , Cmd.perform connect GotHandshake\n                ]\n            )",
        )],
    );
    let err = match analyze(&dir) {
        Ok(_) => panic!("a branch mixing Noise and a file read must be refused"),
        Err(e) => e,
    };
    assert!(
        err.contains("branch `Refresh`")
            && err.contains("client-held crypto")
            && (err.contains("Kx") || err.contains("Noise")),
        "the refusal must name the branch and the crypto family, got:\n{err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_key_operation_in_init_is_refused() {
    let dir = variant(
        "init",
        &[(
            "    , Cmd.none\n    )\n\n\nupdate",
            "    , Cmd.perform connect GotHandshake\n    )\n\n\nupdate",
        )],
    );
    let err = match analyze(&dir) {
        Ok(_) => panic!("key operations in `init` must be refused (init also runs on the server)"),
        Err(e) => e,
    };
    assert!(
        err.contains("init reaches client-held crypto"),
        "the refusal must name init, got:\n{err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_key_on_the_wire_is_refused() {
    // The server branch now reads the handshake: its request would carry it.
    let dir = variant(
        "wire",
        &[(
            "            --VARIANT-REFRESH--\n            ( model, Cmd.perform (File.readFile \"data/note.txt\") Refreshed )",
            "            case model.handshake of\n                Just _ ->\n                    ( model, Cmd.perform (File.readFile \"data/note.txt\") Refreshed )\n\n                Nothing ->\n                    ( model, Cmd.none )",
        )],
    );
    let err = match generate(&dir, "wire") {
        Ok(_) => panic!("a server branch reading the device handshake must be refused"),
        Err(e) => e,
    };
    assert!(
        err.contains("Std.Crypto.Noise.Handshake") && err.contains("never crosses"),
        "the refusal must name the key type, got:\n{err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_key_field_that_is_not_a_maybe_is_refused() {
    let dir = variant(
        "field",
        &[
            (
                "    , handshake : Maybe Noise.Handshake\n",
                "    , handshake : Maybe Noise.Handshake\n    , keys : List Kx.SecretKey\n",
            ),
            (
                "handshake = Nothing, note = \"\" }",
                "handshake = Nothing, note = \"\", keys = [] }",
            ),
        ],
    );
    let err = match generate(&dir, "field") {
        Ok(_) => panic!("a `List Kx.SecretKey` model field must be refused"),
        Err(e) => e,
    };
    assert!(
        err.contains("model field `keys`") && err.contains("Maybe SecretKey"),
        "the refusal must name the field and the `Maybe` fix, got:\n{err}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn the_key_operations_stay_in_the_frontend_and_get_no_rpc() {
    let (out, r) =
        generate(&fixture_dir(), "routes").unwrap_or_else(|e| panic!("generate failed: {e}"));
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Noise.initiatorWith") && front.contains("Kx.generate"),
        "the key operations must be in the wasm client:\n{front}"
    );
    assert!(
        !back.contains("POST /_rpc/Connect") && !back.contains("POST /_rpc/GotHandshake"),
        "the backend must have no RPC for a key operation:\n{back}"
    );
    assert!(back.contains("POST /_rpc/Refresh"));
    assert!(
        r.server_branches.iter().all(|b| b.starts_with("Refresh")),
        "only Refresh is a server branch, got {:?}",
        r.server_branches
    );
    // The SSR first paint and the saved-model clearing of a `Maybe` key field
    // are emitted for a Std.App entry; rust/crates/sky/tests/spa_split_flow.rs
    // `client_crypto_std_app_builds_and_leaves_keys_out_of_the_first_paint`
    // covers them on the `--target web:app` build.
    let _ = std::fs::remove_dir_all(&out);
}

/// A copy of the Std.App fixture `name` with its `main` rewritten as a
/// hand-written `Spa.app` entry (the form `spa_split::generate` reads; the
/// `--target web:app` build derives the same entry), plus `edits`.
fn spa_variant(name: &str, tag: &str, spa_main: &str, edits: &[(&str, &str)]) -> PathBuf {
    let from = repo_root()
        .join("rust/crates/sky/tests/fixtures")
        .join(name);
    let dir = std::env::temp_dir().join(format!(
        "sky-spa-client-crypto-{}-{tag}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::copy(from.join("sky.toml"), dir.join("sky.toml")).unwrap();
    let src = std::fs::read_to_string(from.join("src/Main.sky")).unwrap();
    let cut = src
        .find("\nmain =")
        .or_else(|| src.find("\nappDef ="))
        .unwrap();
    let mut src = src[..cut].replacen(
        "import Std.App as App\n",
        "import Std.App as App\nimport Std.Spa as Spa\n",
        1,
    );
    for (from, to) in edits {
        assert_eq!(src.matches(from).count(), 1, "`{from}` must occur once");
        src = src.replace(from, to);
    }
    src.push_str(spa_main);
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
    dir
}

const RELAY_MAIN: &str = "\n\nmain : Task Error ()\nmain =\n    Spa.app\n        (Spa.config\n            { init = init, update = update, view = \\m -> Ui.layout [] (view m), subscriptions = \\_ -> Sub.none }\n            |> Spa.withClientCrypto\n        )\n";

/// Two relay steps of the same shape: each server arm forwards public hex and
/// returns `model`, and the client arm its result reaches does the Noise
/// operation. Both are client-result RPCs. Before, `SendEcho` → `GotEcho` was
/// settled as a server-internal chain (GotEcho ends with `Cmd.none`, while
/// GotMsg2 performs a client-dispatched Msg), so the chain's I/O held the
/// transport field `tr` and the build refused "branch `SendEcho`, field `tr`:
/// … never crosses between client and server".
#[test]
fn two_relay_steps_of_the_same_shape_are_both_client_result_rpcs() {
    let dir = spa_variant("spa-client-crypto-relay", "relay", RELAY_MAIN, &[]);
    let r = analyze(&dir).unwrap_or_else(|e| panic!("analyze: {e}"));
    let mut roots: Vec<(String, String)> = r.client_result.clone();
    roots.sort();
    assert_eq!(
        roots,
        vec![
            ("SendEcho".to_string(), "GotEcho".to_string()),
            ("SendHello".to_string(), "GotMsg2".to_string())
        ],
        "both relay steps answer the client with the result"
    );
    assert!(
        !r.server_internal
            .iter()
            .any(|m| m == "GotEcho" || m == "GotMsg2"),
        "a client arm holding the key is never settled on the server: {:?}",
        r.server_internal
    );
    let (out, _) = generate(&dir, "relay").unwrap_or_else(|e| panic!("generate failed: {e}"));
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("POST /_rpc/SendHello") && back.contains("POST /_rpc/SendEcho"),
        "{back}"
    );
    let _ = std::fs::remove_dir_all(&out);
    let _ = std::fs::remove_dir_all(&dir);
}

/// The refusal stays where a server arm really touches a key field: here the
/// relay arm also clears `tr`, so its response would carry the transport.
#[test]
fn a_relay_arm_that_writes_the_key_field_is_still_refused() {
    let dir = spa_variant(
        "spa-client-crypto-relay",
        "relay-writes-key",
        RELAY_MAIN,
        &[(
            "( model, Cmd.perform (relay \"/echo\" hex) GotEcho )",
            "( { model | tr = Nothing }, Cmd.perform (relay \"/echo\" hex) GotEcho )",
        )],
    );
    let Err(e) = generate(&dir, "relay-writes-key") else {
        panic!("a server arm writing `tr` must be refused");
    };
    assert!(e.contains("SendEcho") && e.contains("never crosses"), "{e}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// A relay root that also writes the model is not a client-result RPC: that
/// answer carries only the task result, so the write (`status = "sending"`)
/// would be lost. It takes the follow-up path. The write reads no server data,
/// so it runs in the client when `SendHello` runs, as on Sky.Live; the answer
/// applies no write (Msgs that ran meanwhile keep theirs) and dispatches the
/// result Msg.
#[test]
fn a_relay_root_that_writes_the_model_keeps_its_write() {
    let dir = spa_variant(
        "spa-client-crypto-relay",
        "relay-writes-status",
        RELAY_MAIN,
        &[(
            "( model, Cmd.perform (relay \"/handshake\" hex) GotMsg2 )",
            "( { model | status = \"sending\" }, Cmd.perform (relay \"/handshake\" hex) GotMsg2 )",
        )],
    );
    let r = analyze(&dir).unwrap_or_else(|e| panic!("analyze: {e}"));
    assert!(
        !r.client_result.iter().any(|(root, _)| root == "SendHello"),
        "{:?}",
        r.client_result
    );
    let (out, _) =
        generate(&dir, "relay-writes-status").unwrap_or_else(|e| panic!("generate failed: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let applied = front
        .split("AppliedSendHello (Ok resp) ->")
        .nth(1)
        .and_then(|r| r.split("AppliedSendHello (Err").next())
        .unwrap_or_default();
    assert!(
        !applied.contains("status = resp.status") && applied.contains("spaDecodeFollows_"),
        "the answer dispatches the follow-up and does not re-apply the write:\n{applied}"
    );
    // The client arm: from its pattern line to the RPC it sends.
    let rpc_at = front.find("\"/_rpc/SendHello\"").expect("SendHello RPC");
    let arm_start = front[..rpc_at]
        .rfind("\n        SendHello")
        .expect("SendHello arm");
    let arm = &front[arm_start..rpc_at];
    assert!(
        arm.contains("status = \"sending\"")
            && arm.contains("Spa.rpc ")
            && !arm.contains("Spa.rpcHold"),
        "the write runs in the client arm, with an async RPC:\n{arm}"
    );
    let _ = std::fs::remove_dir_all(&out);
    let _ = std::fs::remove_dir_all(&dir);
}
