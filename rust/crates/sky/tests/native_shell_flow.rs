//! The native shells: purpose strings, secure storage, biometrics and release
//! packaging, driven through the real `sky` binary.
//!
//! Two kinds of test live here.
//!
//! * **Toolchain-free** (T1, every platform): the missing-purpose-string check
//!   runs before the toolchain probe and before any Go build, so the refusal is
//!   asserted on a bare runner.
//! * **Native smoke** (`#[ignore]`, macOS only): they build the iOS shell for
//!   the simulator (Xcode), boot a simulator and launch the app, and package a
//!   signed Android release (Android SDK + JDK). They need Go, Xcode and the
//!   Android SDK, and fail — never skip — without them (`live_gate`). The
//!   release workflow runs them on its macOS job (`gate-native`); the Linux
//!   `--ignored` leg does not compile them.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
#[allow(unused_imports)]
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-native-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"vault\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    dir
}

/// A Sky.Spa app that stores a token in the secure store and unlocks with
/// biometrics. `bundle_steps` are the `|> Bundle.with…` lines.
fn vault_app(dir: &Path, bundle_steps: &str) {
    let src = format!(
        r#"module Main exposing (main, bundle)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Secret as Secret exposing (Secret)
import Std.Bundle as Bundle exposing (Bundle)
import Std.Cmd as Cmd
import Std.Native as Native
import Std.Spa as Spa
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Html exposing (Html)


bundle : Bundle
bundle =
    Bundle.default
        |> Bundle.withId "com.example.vault"
{bundle_steps}


type alias Model =
    {{ status : String }}


type Msg
    = Save
    | Saved (Result Error ())
    | Load
    | Loaded (Result Error (Maybe Secret))
    | Unlock
    | Unlocked (Result Error Bool)


init : () -> ( Model, Cmd Msg )
init _ =
    ( {{ status = "ready" }}, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Save ->
            ( model, Cmd.perform (Native.secureSet "token" (Secret.fromString "abc")) Saved )

        Saved (Ok _) ->
            ( {{ model | status = "saved" }}, Cmd.none )

        Saved (Err _) ->
            ( {{ model | status = "save failed" }}, Cmd.none )

        Load ->
            ( model, Cmd.perform (Native.secureGet "token") Loaded )

        Loaded (Ok (Just s)) ->
            ( {{ model | status = "loaded " ++ String.fromInt (String.length (Secret.reveal s)) }}, Cmd.none )

        Loaded (Ok Nothing) ->
            ( {{ model | status = "none" }}, Cmd.none )

        Loaded (Err _) ->
            ( {{ model | status = "load failed" }}, Cmd.none )

        Unlock ->
            ( model, Cmd.perform (Native.authenticate "Unlock the vault") Unlocked )

        Unlocked (Ok True) ->
            ( {{ model | status = "unlocked" }}, Cmd.none )

        Unlocked (Ok False) ->
            ( {{ model | status = "no match" }}, Cmd.none )

        Unlocked (Err _) ->
            ( {{ model | status = "unavailable" }}, Cmd.none )


view : Model -> Html Msg
view model =
    Ui.layout [] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main : Task Error ()
main =
    Spa.app
        (Spa.config
            {{ init = init, update = update, view = view, subscriptions = subscriptions }}
        )
"#
    );
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
}

fn run(dir: &Path, args: &[&str], env: &[(&str, &str)]) -> (bool, String) {
    let mut cmd = Command::new(SKY);
    cmd.args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .env_remove("SKY_APP_URL")
        .env_remove("SKY_PACKAGE_RELEASE");
    for (k, v) in env {
        cmd.env(k, v);
    }
    let out = cmd.output().expect("spawn sky");
    (
        out.status.success(),
        format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        ),
    )
}

/// `Native.authenticate` needs Face ID's purpose string on iOS and
/// USE_BIOMETRIC on Android. An app that calls it without declaring
/// `Bundle.FaceId` fails `sky build` / `sky check` for both shells, naming the
/// builder that fixes it — before any toolchain or Go build is needed.
#[test]
fn a_capability_without_its_purpose_string_fails_the_native_build() {
    let dir = scratch("nousage");
    vault_app(&dir, "");
    for (verb, target) in [
        ("build", "mobile:ios"),
        ("check", "mobile:ios"),
        ("build", "mobile:android"),
    ] {
        let (ok, out) = run(&dir, &[verb, "--target", target, "src/Main.sky"], &[]);
        assert!(!ok, "sky {verb} --target {target} must refuse:\n{out}");
        assert!(
            out.contains("Native.authenticate") && out.contains("Bundle.withUsage Bundle.FaceId"),
            "sky {verb} --target {target}: the error must name the call and the fix:\n{out}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// A malformed typed entitlement is a build error for the native shells.
#[test]
fn a_malformed_entitlement_fails_the_native_build() {
    let dir = scratch("badent");
    vault_app(
        &dir,
        "        |> Bundle.withUsage Bundle.FaceId \"Unlocks your vault.\"\n        |> Bundle.withEntitlement (Bundle.AppGroup \"com.example.vault\")",
    );
    let (ok, out) = run(
        &dir,
        &["build", "--target", "mobile:ios", "src/Main.sky"],
        &[],
    );
    assert!(!ok, "a malformed app group must fail:\n{out}");
    assert!(out.contains("group."), "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

// ─────────────────────────────────────────────────────────────────────────────
// Native smoke (macOS; run by the release workflow's gate-native job)
// ─────────────────────────────────────────────────────────────────────────────

#[cfg(target_os = "macos")]
fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[cfg(target_os = "macos")]
fn have_xcode() -> bool {
    Command::new("xcrun")
        .args(["--sdk", "iphonesimulator", "--show-sdk-path"])
        .env(
            "DEVELOPER_DIR",
            "/Applications/Xcode.app/Contents/Developer",
        )
        .env_remove("SDKROOT")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[cfg(target_os = "macos")]
fn android_home() -> Option<PathBuf> {
    let home = std::env::var_os("ANDROID_HOME")
        .or_else(|| std::env::var_os("ANDROID_SDK_ROOT"))
        .map(PathBuf::from)
        .or_else(|| {
            std::env::var_os("HOME").map(|h| PathBuf::from(h).join("Library/Android/sdk"))
        })?;
    let has_tools = std::fs::read_dir(home.join("build-tools"))
        .map(|mut r| r.next().is_some())
        .unwrap_or(false);
    let has_jdk = Command::new("keytool").arg("-help").output().is_ok();
    (has_tools && has_jdk).then_some(home)
}

#[cfg(target_os = "macos")]
fn simctl(args: &[&str]) -> std::process::Output {
    Command::new("xcrun")
        .arg("simctl")
        .args(args)
        .env(
            "DEVELOPER_DIR",
            "/Applications/Xcode.app/Contents/Developer",
        )
        .env_remove("SDKROOT")
        .output()
        .expect("xcrun simctl")
}

/// The iOS simulator smoke test: the shell with the Keychain secure store and
/// the LocalAuthentication prompt compiles (swiftc), its Info.plist and
/// entitlements are valid property lists carrying the declared purpose string
/// and app group, the signature carries the entitlements, and the app installs
/// and stays running on a booted simulator.
#[cfg(target_os = "macos")]
#[test]
#[ignore = "native smoke: needs Go + Xcode + an iOS simulator runtime (release gate-native)"]
fn ios_simulator_shell_builds_installs_and_launches_with_secure_storage() {
    if !required(Need::Go, have_go()) || !required(Need::Xcode, have_xcode()) {
        return;
    }
    let dir = scratch("ios");
    vault_app(
        &dir,
        "        |> Bundle.withUsage Bundle.FaceId \"Unlocks your vault.\"\n        |> Bundle.withEntitlement (Bundle.AppGroup \"group.com.example.vault\")",
    );
    let (ok, out) = run(
        &dir,
        &["build", "--target", "mobile:ios", "src/Main.sky"],
        &[],
    );
    assert!(ok, "the iOS simulator build failed:\n{out}");
    let ios = dir.join(".split/frontend/sky-out/ios");
    let app = ios.join("build/Vault.app");
    assert!(app.join("Vault").is_file(), "no app binary:\n{out}");

    let lint = Command::new("plutil")
        .arg("-lint")
        .arg(app.join("Info.plist"))
        .arg(ios.join("Vault.entitlements"))
        .output()
        .unwrap();
    assert!(
        lint.status.success(),
        "plutil -lint: {}",
        String::from_utf8_lossy(&lint.stdout)
    );
    let info = std::fs::read_to_string(app.join("Info.plist")).unwrap();
    assert!(info.contains("NSFaceIDUsageDescription") && info.contains("Unlocks your vault."));
    assert_eq!(info.matches("<key>CFBundleIdentifier</key>").count(), 1);
    let sig = Command::new("codesign")
        .args(["-d", "--entitlements", "-"])
        .arg(&app)
        .output()
        .unwrap();
    let sig_text = format!(
        "{}{}",
        String::from_utf8_lossy(&sig.stdout),
        String::from_utf8_lossy(&sig.stderr)
    );
    assert!(
        sig_text.contains("group.com.example.vault"),
        "the simulator build must be signed with its entitlements:\n{sig_text}"
    );

    // Boot an available iPhone simulator, install, launch, and check the app is
    // still running a few seconds later (a Swift crash at start would end it).
    let list = simctl(&["list", "devices", "available", "-j"]);
    let json: serde_json::Value = serde_json::from_slice(&list.stdout).expect("simctl list json");
    let udid = json["devices"]
        .as_object()
        .into_iter()
        .flatten()
        .filter(|(rt, _)| rt.contains("iOS"))
        .flat_map(|(_, devs)| devs.as_array().cloned().unwrap_or_default())
        .find(|d| d["name"].as_str().unwrap_or("").starts_with("iPhone"))
        .and_then(|d| d["udid"].as_str().map(str::to_string));
    assert!(
        required(Need::Xcode, udid.is_some()),
        "no available iPhone simulator"
    );
    let udid = udid.unwrap();
    let booted_here = simctl(&["boot", &udid]).status.success();
    let _ = simctl(&["bootstatus", &udid, "-b"]);
    let inst = simctl(&["install", &udid, app.to_str().unwrap()]);
    assert!(
        inst.status.success(),
        "simctl install: {}",
        String::from_utf8_lossy(&inst.stderr)
    );
    let launch = simctl(&["launch", &udid, "com.example.vault"]);
    assert!(
        launch.status.success(),
        "simctl launch: {}",
        String::from_utf8_lossy(&launch.stderr)
    );
    std::thread::sleep(std::time::Duration::from_secs(5));
    let procs = simctl(&["spawn", &udid, "launchctl", "list"]);
    let running = String::from_utf8_lossy(&procs.stdout).contains("com.example.vault");
    let _ = simctl(&["terminate", &udid, "com.example.vault"]);
    let _ = simctl(&["uninstall", &udid, "com.example.vault"]);
    if booted_here {
        let _ = simctl(&["shutdown", &udid]);
    }
    assert!(running, "the app is not running 5 s after launch");
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky package --release --target mobile:android` signs with the upload key
/// from the environment (never the debug key) and the result verifies; the
/// Java shell with the Keystore secure store and BiometricPrompt compiles.
#[cfg(target_os = "macos")]
#[test]
#[ignore = "native smoke: needs Go + the Android SDK + a JDK (release gate-native)"]
fn android_release_is_signed_with_the_upload_key() {
    let home = android_home();
    if !required(Need::Go, have_go()) || !required(Need::AndroidSdk, home.is_some()) {
        return;
    }
    let home = home.unwrap();
    let dir = scratch("android");
    vault_app(
        &dir,
        "        |> Bundle.withUsage Bundle.FaceId \"Unlocks your vault.\"\n        |> Bundle.withBuild 7",
    );
    let ks = dir.join("upload.jks");
    let kt = Command::new("keytool")
        .args(["-genkeypair", "-keystore"])
        .arg(&ks)
        .args([
            "-storepass",
            "flowtest123",
            "-keypass",
            "flowtest123",
            "-alias",
            "upload",
            "-keyalg",
            "RSA",
            "-keysize",
            "2048",
            "-validity",
            "365",
            "-dname",
            "CN=Flow Test,O=Sky,C=GB",
        ])
        .output()
        .unwrap();
    assert!(
        kt.status.success(),
        "keytool: {}",
        String::from_utf8_lossy(&kt.stderr)
    );
    let ks_s = ks.to_string_lossy().into_owned();
    let home_s = home.to_string_lossy().into_owned();
    let (ok, out) = run(
        &dir,
        &["package", "--release", "--target", "mobile:android"],
        &[
            ("SKY_APP_URL", "https://app.example.test/"),
            ("SKY_ANDROID_KEYSTORE", &ks_s),
            ("SKY_ANDROID_KEYSTORE_PASSWORD", "flowtest123"),
            ("SKY_ANDROID_KEY_ALIAS", "upload"),
            ("ANDROID_HOME", &home_s),
        ],
    );
    assert!(ok, "sky package --release failed:\n{out}");
    let apk = dir.join("sky-out/release/vault.apk");
    assert!(apk.is_file(), "no release apk:\n{out}");
    assert!(!out.contains("debug.keystore"), "{out}");
    let bt = std::fs::read_dir(home.join("build-tools"))
        .unwrap()
        .flatten()
        .map(|e| e.path())
        .max()
        .unwrap();
    // `apksigner`'s own subcommand, spelled without the quoted verb token: the
    // coverage ledger reads a quoted `sky` verb name in a *_flow.rs file as a
    // test of that verb, and this is not a test of `sky verify`.
    let subcommand = ["ver", "ify"].concat();
    let verify = Command::new(bt.join("apksigner"))
        .arg(&subcommand)
        .arg("--print-certs")
        .arg(&apk)
        .output()
        .unwrap();
    let certs = String::from_utf8_lossy(&verify.stdout);
    assert!(verify.status.success(), "apksigner verify failed: {certs}");
    assert!(
        certs.contains("CN=Flow Test"),
        "signed with the upload key, not the debug key:\n{certs}"
    );
    let manifest = std::fs::read_to_string(
        dir.join(".split/frontend/sky-out/android/app/src/main/AndroidManifest.xml"),
    )
    .unwrap();
    assert!(manifest.contains("android.permission.USE_BIOMETRIC"));
    assert!(manifest.contains("android:versionCode=\"7\""));
    let _ = std::fs::remove_dir_all(&dir);
}
