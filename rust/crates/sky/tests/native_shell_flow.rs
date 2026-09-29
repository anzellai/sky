//! The native shells: purpose strings, secure storage, biometrics and release
//! packaging, driven through the real `sky` binary.
//!
//! Two kinds of test live here.
//!
//! * **Toolchain-free** (T1, every platform): the missing-purpose-string check
//!   runs before the toolchain probe and before any Go build, so the refusal is
//!   asserted on a bare runner.
//! * **Native smoke** (`#[ignore]`): the iOS tests (macOS only) build a
//!   probe app for the simulator, boot a simulator, LAUNCH the app and read
//!   its own results (the Keychain round trip, the scanner, the biometric
//!   prompt) back through its backend, and package a signed Android release.
//!   The Android emulator test (any Unix) does the same on a running emulator.
//!   They need Go, Xcode, the Android SDK and an emulator, and fail — never
//!   skip — without them (`live_gate`). The release workflow runs the iOS and
//!   release tests on its macOS job (`gate-native`) and the emulator test on a
//!   Linux job with KVM (`gate-native-android`); the other Linux `--ignored`
//!   legs skip `android_emulator` by name.

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

/// An `App.app` entry that calls `Native.scanCode` without the camera purpose
/// string fails the native build with an error that points at the USER's
/// files: the line that calls it and the `bundle` binding to fix. Before
/// v0.27.0 the check ran only in the frontend leg, on the client entry the
/// build derives, and the error ended with "the failure above is in the
/// SYNTHESISED client entry", pointing away from the binding.
#[test]
fn an_app_entry_missing_a_purpose_string_names_its_own_bundle_binding() {
    let dir = scratch("appusage");
    probe_app(&dir, "");
    for target in [
        "mobile:ios",
        "mobile:android",
        "tablet:ipad",
        "tablet:android",
    ] {
        let (ok, out) = run(&dir, &["build", "--target", target, "src/Main.sky"], &[]);
        assert!(!ok, "sky build --target {target} must refuse:\n{out}");
        assert!(
            out.contains("`Native.scanCode` at src/Main.sky:")
                && out.contains("Bundle.withUsage Bundle.Camera")
                && out.contains("`bundle` binding at src/Main.sky:17"),
            "the error must name the call, the fix and the bundle binding:\n{out}"
        );
        assert!(!out.contains("SYNTHESISED"), "{out}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// `Native.notify` needs `Bundle.withPermission Bundle.Notifications` on
/// Android: Android 13 and later refuses an undeclared POST_NOTIFICATIONS
/// without a prompt, so every notification was lost. The build refuses the
/// call without it, naming the fix, before any toolchain is needed.
#[test]
fn native_notify_without_the_notifications_permission_fails_the_android_build() {
    let dir = scratch("notifyperm");
    probe_app_with(&dir, "", Flow::Notify);
    for target in ["mobile:android", "tablet:android"] {
        let (ok, out) = run(&dir, &["build", "--target", target, "src/Main.sky"], &[]);
        assert!(!ok, "sky build --target {target} must refuse:\n{out}");
        assert!(
            out.contains("`Native.notify` at src/Main.sky:")
                && out.contains("android.permission.POST_NOTIFICATIONS")
                && out.contains("Bundle.withPermission Bundle.Notifications"),
            "the error must name the call and the fix:\n{out}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

// ─────────────────────────────────────────────────────────────────────────────
// Native smoke (macOS; run by the release workflow's gate-native job)
// ─────────────────────────────────────────────────────────────────────────────

#[cfg(unix)]
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

#[cfg(unix)]
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

/// A `Std.App` probe for the native shells, built the way an app is: it runs
/// `Native.secureSet` then `Native.secureGet` at start, then `Native.scanCode`,
/// and reports each result through a SERVER branch that prints a
/// `SKY-PROBE <result>` line on the backend. It also reports each route it
/// navigates to (`route=home`, `route=probe:<name>` for `/probe/<name>`), so
/// a test can see which page an App Link opened. The test reads the lines from
/// the backend's output, so it asserts what the app on the device saw.
/// `bundle_steps` are the `|> Bundle.with…` lines after the name and id.
fn probe_app(dir: &Path, bundle_steps: &str) {
    probe_app_with(dir, bundle_steps, Flow::Native);
}

/// What the probe does after it starts.
#[derive(Clone, Copy, PartialEq)]
enum Flow {
    /// The secure-store round trip, then `Native.scanCode`, then
    /// `Native.authenticate` (the full native-capability probe).
    Native,
    /// The secure-store round trip, then `Native.notify` (`notify=ok` or
    /// `notify=err:<error>`).
    Notify,
    /// No native capability: `start=ok` when the client starts, and each
    /// route. A reload of the page reports a second `start=ok`.
    Links,
}

fn probe_app_with(dir: &Path, bundle_steps: &str, flow: Flow) {
    let init_cmd = match flow {
        Flow::Links => "Cmd.perform (Task.succeed \"start=ok\") Report",
        _ => "Cmd.perform roundTrip Got",
    };
    let got_next = match flow {
        Flow::Native => "\n                , Cmd.perform (Native.scanCode { formats = [ Native.Qr ], prompt = \"Scan the probe code\" }) Scanned",
        Flow::Notify => "\n                , Cmd.perform (Native.notify \"Sky Probe\" \"probe-notification\") Notified",
        Flow::Links => "",
    };
    let scanned_next = match flow {
        Flow::Native => {
            "\n                , Cmd.perform (Native.authenticate \"Confirm the probe\") Authed"
        }
        _ => "",
    };
    let src = format!(
        r#"module Main exposing (main, bundle)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Secret as Secret exposing (Secret)
import Sky.Core.Task as Task
import Std.App as App
import Std.Bundle as Bundle exposing (Bundle)
import Std.Cmd as Cmd
import Std.Log as Log
import Std.Native as Native
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


bundle : Bundle
bundle =
    Bundle.default
        |> Bundle.withName "Sky Probe"
        |> Bundle.withId "com.example.probe"
{bundle_steps}


type alias Model =
    {{ status : String, page : String }}


type Msg
    = Navigated String
    | Got (Result Error (Maybe Secret))
    | Scanned (Result Error (Maybe Native.ScannedCode))
    | Authed (Result Error Bool)
    | Report (Result Error String)
    | Reported (Result Error ())
    | Notified (Result Error ())


roundTrip : Task Error (Maybe Secret)
roundTrip =
    Native.secureSet "probe-key" (Secret.fromString "probe-value")
        |> Task.andThen (\_ -> Native.secureGet "probe-key")


secureText : Result Error (Maybe Secret) -> String
secureText r =
    case r of
        Ok (Just s) ->
            "secure=ok:" ++ Secret.reveal s

        Ok Nothing ->
            "secure=none"

        Err e ->
            "secure=err:" ++ Error.toString e


scanText : Result Error (Maybe Native.ScannedCode) -> String
scanText r =
    case r of
        Ok (Just c) ->
            "scan=code:" ++ c.text

        Ok Nothing ->
            "scan=cancelled"

        Err e ->
            "scan=err:" ++ Error.toString e


notifyText : Result Error () -> String
notifyText r =
    case r of
        Ok _ ->
            "notify=ok"

        Err e ->
            "notify=err:" ++ Error.toString e


authText : Result Error Bool -> String
authText r =
    case r of
        Ok True ->
            "auth=ok"

        Ok False ->
            "auth=nomatch"

        Err e ->
            "auth=err:" ++ Error.toString e


init : () -> ( Model, Cmd.Cmd Msg )
init _ =
    ( {{ status = "running", page = "home" }}, {init_cmd} )


update : Msg -> Model -> ( Model, Cmd.Cmd Msg )
update msg model =
    case msg of
        Navigated p ->
            ( {{ model | page = p }}, Cmd.perform (Task.succeed ("route=" ++ p)) Report )

        Got r ->
            ( {{ model | status = secureText r }}
            , Cmd.batch
                [ Cmd.perform (Task.succeed (secureText r)) Report{got_next}
                ]
            )

        Scanned r ->
            ( {{ model | status = scanText r }}
            , Cmd.batch
                [ Cmd.perform (Task.succeed (scanText r)) Report{scanned_next}
                ]
            )

        Authed r ->
            ( {{ model | status = authText r }}, Cmd.perform (Task.succeed (authText r)) Report )

        Report result ->
            case result of
                Ok line ->
                    ( model, Cmd.perform (Log.println ("SKY-PROBE " ++ line)) Reported )

                Err _ ->
                    ( model, Cmd.none )

        Reported _ ->
            ( model, Cmd.none )

        Notified r ->
            ( {{ model | status = notifyText r }}, Cmd.perform (Task.succeed (notifyText r)) Report )


view : Model -> Element Msg
view model =
    Ui.el [ Ui.padding 40 ] (Ui.text model.status)


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


appDef =
    App.app {{ init = init, update = update, view = view, subscriptions = subscriptions }}
        |> App.withRoutes
            [ App.route "/" "home"
            , App.routeParam "/probe/:name" (\n -> "probe:" ++ n)
            ]
        |> App.withNotFound "home"
        |> App.withOnNavigate Navigated


main =
    App.run appDef
"#
    );
    std::fs::write(dir.join("src/Main.sky"), src).unwrap();
}

/// The probe's purpose strings: the camera (`Native.scanCode`) and Face ID /
/// USE_BIOMETRIC (`Native.authenticate`).
const USAGES: &str = "        |> Bundle.withUsage Bundle.Camera \"Scans the pairing code.\"\n        |> Bundle.withUsage Bundle.FaceId \"Confirms it is you.\"";

/// The restricted Apple entitlements the "declared" variant asks for. They must
/// never stop a launch on either platform. Android has no entitlements; the
/// associated domain becomes an App Links filter there, and a link to it opens
/// the app on the link's page.
const DECLARED: &str = "        |> Bundle.withEntitlement (Bundle.KeychainAccessGroup \"ABCDE12345.com.example.probe\")\n        |> Bundle.withEntitlement (Bundle.AssociatedDomain \"applinks:example.com\")";

/// A port nothing listens on, for the probe's backend.
#[cfg(unix)]
fn free_port() -> u16 {
    std::net::TcpListener::bind(("127.0.0.1", 0))
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// The probe's backend, running; its output lines are collected. Dropping it
/// stops it.
#[cfg(unix)]
struct Backend {
    child: std::process::Child,
    lines: std::sync::Arc<std::sync::Mutex<Vec<String>>>,
}

#[cfg(unix)]
impl Backend {
    fn start(split: &Path, port: u16) -> Backend {
        use std::io::BufRead;
        let dir = split.join("backend");
        let mut child = Command::new(dir.join("sky-out/app"))
            .current_dir(&dir)
            .env("PORT", port.to_string())
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .expect("start the probe backend");
        let lines = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        for stream in [
            Box::new(child.stdout.take().unwrap()) as Box<dyn std::io::Read + Send>,
            Box::new(child.stderr.take().unwrap()),
        ] {
            let lines = lines.clone();
            std::thread::spawn(move || {
                for l in std::io::BufReader::new(stream)
                    .lines()
                    .map_while(Result::ok)
                {
                    lines.lock().unwrap().push(l);
                }
            });
        }
        // Wait until it answers.
        for _ in 0..100 {
            if std::net::TcpStream::connect(("127.0.0.1", port)).is_ok() {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(100));
        }
        Backend { child, lines }
    }

    /// The first `SKY-PROBE <kind>=…` line, waiting up to `secs`.
    fn probe_line(&self, kind: &str, secs: u64) -> Option<String> {
        let want = format!("SKY-PROBE {kind}=");
        let until = std::time::Instant::now() + std::time::Duration::from_secs(secs);
        while std::time::Instant::now() < until {
            if let Some(l) = self.lines.lock().unwrap().iter().find_map(|l| {
                l.find(&want)
                    .map(|i| l[i + "SKY-PROBE ".len()..].to_string())
            }) {
                return Some(l);
            }
            std::thread::sleep(std::time::Duration::from_millis(250));
        }
        None
    }

    /// Whether the backend printed `SKY-PROBE <text>` exactly, waiting up to
    /// `secs`.
    fn saw(&self, text: &str, secs: u64) -> bool {
        let want = format!("SKY-PROBE {text}");
        let until = std::time::Instant::now() + std::time::Duration::from_secs(secs);
        while std::time::Instant::now() < until {
            if self
                .lines
                .lock()
                .unwrap()
                .iter()
                .any(|l| l.trim_end().ends_with(&want))
            {
                return true;
            }
            std::thread::sleep(std::time::Duration::from_millis(250));
        }
        false
    }

    fn output(&self) -> String {
        self.lines.lock().unwrap().join("\n")
    }
}

#[cfg(unix)]
impl Drop for Backend {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// An available iPhone simulator's UDID, booted; `true` when this test booted
/// it (and so shuts it down).
#[cfg(target_os = "macos")]
fn boot_iphone() -> Option<(String, bool)> {
    let list = simctl(&["list", "devices", "available", "-j"]);
    let json: serde_json::Value = serde_json::from_slice(&list.stdout).ok()?;
    let devices: Vec<serde_json::Value> = json["devices"]
        .as_object()
        .into_iter()
        .flatten()
        .filter(|(rt, _)| rt.contains("iOS"))
        .flat_map(|(_, devs)| devs.as_array().cloned().unwrap_or_default())
        .filter(|d| d["name"].as_str().unwrap_or("").starts_with("iPhone"))
        .collect();
    // An iPhone that is already booted is used as it is.
    if let Some(d) = devices.iter().find(|d| d["state"] == "Booted") {
        return Some((d["udid"].as_str()?.to_string(), false));
    }
    let udid = devices.first()?["udid"].as_str()?.to_string();
    let booted_here = simctl(&["boot", &udid]).status.success();
    let _ = simctl(&["bootstatus", &udid, "-b"]);
    Some((udid, booted_here))
}

/// The iOS simulator gate: the app LAUNCHES and the Keychain round-trips a
/// value, with no declared entitlement and with restricted ones declared (a
/// keychain access group and an associated domain), and `Native.scanCode`
/// answers `Err Unavailable` (VisionKit's scanner needs a device camera). The
/// results are read back from the app itself, through its backend.
///
/// Before v0.27.0 the simulator build was signed ad hoc WITH the entitlements:
/// a restricted one stopped the launch ("No such process": AMFI refuses an ad
/// hoc signature that carries one), and without one every Keychain call failed
/// with -34018. This test only launched the app, with an app group (which is
/// not restricted), so it caught neither. The build now does what Xcode does
/// for the simulator: the entitlements go into the executable's
/// `__TEXT,__entitlements` / `__ents_der` sections with the application
/// identifier and a keychain access group added, and the signature carries
/// none.
#[cfg(target_os = "macos")]
#[test]
#[ignore = "native smoke: needs Go + Xcode + an iOS simulator runtime (release gate-native)"]
fn ios_simulator_app_launches_and_round_trips_the_keychain() {
    if !required(Need::Go, have_go()) || !required(Need::Xcode, have_xcode()) {
        return;
    }
    let sim = boot_iphone();
    assert!(
        required(Need::Xcode, sim.is_some()),
        "no available iPhone simulator"
    );
    let (udid, booted_here) = sim.unwrap();
    let declared = format!("{USAGES}\n{DECLARED}");
    // The declared variant is built as `tablet:ipad`: an iPad build is the
    // same universal iOS shell.
    let variants = [
        ("plain", USAGES, "mobile:ios", "mobile-ios"),
        ("declared", declared.as_str(), "tablet:ipad", "tablet-ipad"),
    ];
    let mut failures = Vec::new();
    for (tag, steps, target, out_dir) in variants {
        let dir = scratch(&format!("ios-{tag}"));
        probe_app(&dir, steps);
        let port = free_port();
        let port_s = port.to_string();
        let (ok, out) = run(
            &dir,
            &["build", "--target", target, "src/Main.sky"],
            &[("PORT", &port_s)],
        );
        assert!(ok, "{tag}: the iOS simulator build failed:\n{out}");
        let split = dir.join(".skyapp").join(out_dir).join(".split");
        let ios = split.join("frontend/sky-out/ios");
        // The product name is built from the whole display name.
        let app = ios.join("build/Sky_Probe.app");
        let exe = app.join("Sky_Probe");
        assert!(exe.is_file(), "{tag}: no app binary Sky_Probe:\n{out}");
        let lint = Command::new("plutil")
            .arg("-lint")
            .arg(app.join("Info.plist"))
            .arg(ios.join("Sky_Probe-Simulated.entitlements"))
            .output()
            .unwrap();
        assert!(
            lint.status.success(),
            "plutil -lint: {}",
            String::from_utf8_lossy(&lint.stdout)
        );
        let info = std::fs::read_to_string(app.join("Info.plist")).unwrap();
        assert!(
            info.contains("<string>Sky Probe</string>")
                && info.contains("NSCameraUsageDescription"),
            "{info}"
        );
        // The signature carries no entitlements; the executable's section does.
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
            !sig_text.contains("keychain-access-groups")
                && !sig_text.contains("associated-domains"),
            "{tag}: an ad hoc signature must not carry a restricted entitlement:\n{sig_text}"
        );
        let bin = std::fs::read(&exe).unwrap();
        let has = |needle: &str| bin.windows(needle.len()).any(|w| w == needle.as_bytes());
        let app_id = if tag == "declared" {
            "ABCDE12345.com.example.probe"
        } else {
            "SKYSIMTEAM.com.example.probe"
        };
        assert!(
            has("application-identifier") && has(app_id),
            "{tag}: the __TEXT,__entitlements section must name {app_id}"
        );
        if tag == "declared" {
            assert!(
                has("applinks:example.com"),
                "the declared domain is embedded"
            );
        }
        // A universal link to a declared `applinks:` host opens its page in
        // the web view (App.swift); the plain variant takes none.
        let app_swift = std::fs::read_to_string(ios.join("Sky_Probe/App.swift")).unwrap();
        let hosts = if tag == "declared" {
            "static let linkHosts: [String] = [\"example.com\"]"
        } else {
            "static let linkHosts: [String] = []"
        };
        assert!(
            app_swift.contains(hosts) && app_swift.contains(".onOpenURL"),
            "{tag}: {app_swift}"
        );

        let backend = Backend::start(&split, port);
        let _ = simctl(&["uninstall", &udid, "com.example.probe"]);
        let inst = simctl(&["install", &udid, app.to_str().unwrap()]);
        assert!(
            inst.status.success(),
            "simctl install: {}",
            String::from_utf8_lossy(&inst.stderr)
        );
        let launch = simctl(&["launch", &udid, "com.example.probe"]);
        let launched = launch.status.success();
        let secure = backend.probe_line("secure", 120);
        let scan = backend.probe_line("scan", 30);
        let auth = backend.probe_line("auth", 30);
        let _ = simctl(&["terminate", &udid, "com.example.probe"]);
        let _ = simctl(&["uninstall", &udid, "com.example.probe"]);
        if !launched {
            failures.push(format!(
                "{tag}: simctl launch failed: {}",
                String::from_utf8_lossy(&launch.stderr)
            ));
        } else if secure.as_deref() != Some("secure=ok:probe-value") {
            failures.push(format!(
                "{tag}: the Keychain round trip read {secure:?}, want secure=ok:probe-value\n{}",
                backend.output()
            ));
        } else if !scan
            .as_deref()
            .is_some_and(|s| s.starts_with("scan=err:Unavailable"))
        {
            failures.push(format!(
                "{tag}: Native.scanCode on the simulator read {scan:?}, want Err Unavailable"
            ));
        } else if !auth
            .as_deref()
            .is_some_and(|s| s.starts_with("auth=err:Unavailable"))
        {
            // The simulator has no enrolled biometric: LocalAuthentication
            // cannot evaluate the policy, which is Err Unavailable.
            failures.push(format!(
                "{tag}: Native.authenticate on the simulator read {auth:?}, want Err Unavailable"
            ));
        }
        drop(backend);
        let _ = std::fs::remove_dir_all(&dir);
    }
    if booted_here {
        let _ = simctl(&["shutdown", &udid]);
    }
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

/// The first `.app` bundle under `root`.
#[cfg(target_os = "macos")]
fn find_app_bundle(root: &Path) -> Option<PathBuf> {
    let mut stack = vec![root.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).ok()?.flatten() {
            let p = e.path();
            if !p.is_dir() {
                continue;
            }
            if p.extension().is_some_and(|x| x == "app") {
                return Some(p);
            }
            stack.push(p);
        }
    }
    None
}

/// The macOS desktop app, launched from its `.app` bundle with `open`; it is
/// stopped (by its own executable path) when dropped.
#[cfg(target_os = "macos")]
struct MacApp {
    app: PathBuf,
    exe: PathBuf,
}

#[cfg(target_os = "macos")]
impl MacApp {
    /// `open -a <app> --env …` with the links to hand it, if any.
    fn open(&self, env: &[(&str, &str)], links: &[&str], log: &Path) -> std::process::Output {
        let mut cmd = Command::new("open");
        cmd.arg("-a").arg(&self.app);
        for (k, v) in env {
            cmd.arg("--env").arg(format!("{k}={v}"));
        }
        cmd.arg("--stdout").arg(log).arg("--stderr").arg(log);
        cmd.args(links).output().expect("open")
    }

    fn stop(&self) {
        let _ = Command::new("pkill")
            .arg("-f")
            .arg(self.exe.to_str().unwrap())
            .output();
        for _ in 0..40 {
            let alive = Command::new("pgrep")
                .arg("-f")
                .arg(self.exe.to_str().unwrap())
                .output()
                .is_ok_and(|o| o.status.success());
            if !alive {
                return;
            }
            std::thread::sleep(std::time::Duration::from_millis(250));
        }
        let _ = Command::new("pkill")
            .args(["-9", "-f", self.exe.to_str().unwrap()])
            .output();
    }
}

#[cfg(target_os = "macos")]
impl Drop for MacApp {
    fn drop(&mut self) {
        self.stop();
    }
}

/// A `desktop:mac` app that declares `Bundle.AssociatedDomain
/// "applinks:example.com"` opens a link to that host on the link's page, as
/// the iOS and Android shells do. Before v0.27.0 the desktop window took no
/// link at all: the app opened on its first page and the release build
/// printed a note saying so.
///
/// Both ways a link reaches a macOS app are driven on the packaged `.app`:
///
/// * `application:openURLs:` — `open -a <app> <url>`. A link the app is
///   launched with is its first page (the app never shows `/` first); a link
///   sent to the running app navigates in place (pushState + popstate: the
///   client does not restart); a link to a host the app did not declare is
///   ignored.
/// * `application:continueUserActivity:restorationHandler:` — a universal
///   link. macOS delivers one only to an app signed with the
///   associated-domains entitlement after Apple has checked the site, which
///   an ad hoc test build cannot have, so the shell is built with
///   `-tags skytest_links` and a test hook sends the app delegate the same
///   NSUserActivityTypeBrowsingWeb activity macOS sends
///   (runtime-go/rt/native_desktop_links_testhook_darwin.go): one while the
///   app starts, one while it runs.
///
/// Each result is the route the app reports through its backend.
#[cfg(target_os = "macos")]
#[test]
#[ignore = "native smoke: needs Go + Xcode and a macOS desktop session (release gate-native)"]
fn macos_desktop_app_opens_a_universal_link_on_its_page() {
    if !required(Need::Go, have_go()) || !required(Need::Xcode, have_xcode()) {
        return;
    }
    let dir = scratch("macos-links");
    probe_app_with(&dir, DECLARED, Flow::Links);
    let (ok, out) = run(
        &dir,
        &["package", "--release", "--target", "desktop:mac"],
        &[
            ("SKY_APP_URL", "https://probe.example.test/"),
            ("GOFLAGS", "-tags=skytest_links"),
        ],
    );
    assert!(
        ok,
        "sky package --release --target desktop:mac failed:\n{out}"
    );
    assert!(
        !out.contains("opens the app on its first page"),
        "the build must not say links are not routed:\n{out}"
    );
    let app = find_app_bundle(&dir.join(".skyapp")).expect("no .app bundle");
    let info = std::fs::read_to_string(app.join("Contents/Info.plist")).unwrap();
    assert!(
        info.contains("<key>SkyLinkHosts</key>") && info.contains("<string>example.com</string>"),
        "Info.plist must name the applinks hosts:\n{info}"
    );
    let exe_name = std::fs::read_dir(app.join("Contents/MacOS"))
        .unwrap()
        .flatten()
        .next()
        .unwrap()
        .path();
    let mac = MacApp {
        app: app.clone(),
        exe: exe_name,
    };
    let split = dir.join(".skyapp/desktop-mac/.split");
    let log = dir.join("app.log");
    let mut failures = Vec::new();

    // 1. `application:openURLs:` at launch, while running, and for a host the
    //    app did not declare.
    {
        let port = free_port();
        let backend = Backend::start(&split, port);
        let base = format!("http://127.0.0.1:{port}/");
        let o = mac.open(
            &[("SKY_APP_URL", &base)],
            &["https://example.com/probe/deep?x=1"],
            &log,
        );
        let deep = backend.saw("route=probe:deep", 120);
        let again = deep && {
            std::thread::sleep(std::time::Duration::from_secs(2));
            mac.open(&[], &["https://example.com/probe/again"], &log);
            backend.saw("route=probe:again", 60)
        };
        let nope = again && {
            mac.open(&[], &["https://other.example.org/probe/nope"], &log);
            backend.saw("route=probe:nope", 8)
        };
        mac.stop();
        let output = backend.output();
        let starts = output.matches("SKY-PROBE start=ok").count();
        let applog = std::fs::read_to_string(&log).unwrap_or_default();
        if !o.status.success() {
            failures.push(format!(
                "open failed: {}",
                String::from_utf8_lossy(&o.stderr)
            ));
        } else if !deep {
            failures.push(format!(
                "a link the app is launched with must open /probe/deep:\n{output}\n{applog}"
            ));
        } else if output.contains("SKY-PROBE route=home") {
            failures.push(format!(
                "a link the app is launched with is its first page, not `/`:\n{output}"
            ));
        } else if !again {
            failures.push(format!(
                "a link sent to the running app must open /probe/again:\n{output}\n{applog}"
            ));
        } else if starts != 1 {
            failures.push(format!(
                "a link sent to the running app navigates in place, the client must start \
                 once, started {starts} times:\n{output}"
            ));
        } else if nope {
            failures.push(format!(
                "a link to a host the app did not declare must be ignored:\n{output}"
            ));
        }
    }

    // 2. `application:continueUserActivity:restorationHandler:`: a universal
    //    link while the app starts, and one while it runs.
    {
        let port = free_port();
        let backend = Backend::start(&split, port);
        let base = format!("http://127.0.0.1:{port}/");
        let o = mac.open(
            &[
                ("SKY_APP_URL", &base),
                (
                    "SKYTEST_LINK_ACTIVITIES",
                    "https://example.com/probe/act1,https://example.com/probe/act2",
                ),
            ],
            &[],
            &log,
        );
        let act1 = backend.saw("route=probe:act1", 120);
        let starts_at_act1 = backend.output().matches("SKY-PROBE start=ok").count();
        let act2 = act1 && backend.saw("route=probe:act2", 60);
        mac.stop();
        let output = backend.output();
        let starts = output.matches("SKY-PROBE start=ok").count();
        let applog = std::fs::read_to_string(&log).unwrap_or_default();
        if !o.status.success() {
            failures.push(format!(
                "open failed: {}",
                String::from_utf8_lossy(&o.stderr)
            ));
        } else if !act1 {
            failures.push(format!(
                "a universal link while the app starts must open /probe/act1:\n{output}\n{applog}"
            ));
        } else if !act2 {
            failures.push(format!(
                "a universal link to the running app must open /probe/act2:\n{output}\n{applog}"
            ));
        } else if starts != starts_at_act1 {
            failures.push(format!(
                "a universal link to the running app navigates in place, not a reload:\n{output}"
            ));
        }
    }
    drop(mac);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
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
        "        |> Bundle.withName \"Vault\"\n        |> Bundle.withUsage Bundle.FaceId \"Unlocks your vault.\"\n        |> Bundle.withBuild 7\n        |> Bundle.withEntitlement (Bundle.AssociatedDomain \"applinks:app.example.test\")",
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
    let verify = Command::new(bt.join("apksigner"))
        .args(["verify", "--print-certs"])
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
    // App Links: the release writes the site-association file with the
    // UPLOAD key's certificate digest, and says where to serve it.
    assert!(
        manifest.contains("android:host=\"app.example.test\""),
        "{manifest}"
    );
    let digest = certs
        .lines()
        .find_map(|l| l.split_once("certificate SHA-256 digest:"))
        .map(|(_, d)| d.trim().to_ascii_uppercase())
        .expect("apksigner prints the SHA-256 digest");
    let links: serde_json::Value = serde_json::from_str(
        &std::fs::read_to_string(dir.join("sky-out/release/assetlinks.json"))
            .expect("sky-out/release/assetlinks.json"),
    )
    .unwrap();
    let fp = links[0]["target"]["sha256_cert_fingerprints"][0]
        .as_str()
        .unwrap_or_default()
        .to_string();
    assert_eq!(fp.replace(':', ""), digest, "{links}");
    assert_eq!(links[0]["target"]["package_name"], "com.example.vault");
    assert!(
        out.contains("https://app.example.test/.well-known/assetlinks.json")
            && out.contains("upload key"),
        "{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// The emulator tests install the same package on one emulator: one at a
/// time.
#[cfg(unix)]
static EMULATOR_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

/// The first AVD `emulator -list-avds` names.
#[cfg(unix)]
fn first_avd(home: &Path) -> Option<String> {
    let out = Command::new(home.join("emulator/emulator"))
        .arg("-list-avds")
        .output()
        .ok()?;
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .map(str::trim)
        .find(|l| !l.is_empty() && !l.starts_with("INFO"))
        .map(str::to_string)
}

/// The Android emulator the test runs on: one that is already running (the
/// CI runner's, or one a developer started), used as it is, or one this test
/// starts from the first AVD. Dropping it stops only an emulator this test
/// started, by its serial.
#[cfg(unix)]
struct Emulator {
    started: Option<std::process::Child>,
    adb: PathBuf,
    serial: String,
}

#[cfg(unix)]
impl Emulator {
    fn adb(&self, args: &[&str]) -> std::process::Output {
        Command::new(&self.adb)
            .arg("-s")
            .arg(&self.serial)
            .args(args)
            .output()
            .expect("adb")
    }

    /// A running emulator's serial, from `adb devices`.
    fn running(adb: &Path) -> Option<String> {
        let out = Command::new(adb).arg("devices").output().ok()?;
        String::from_utf8_lossy(&out.stdout).lines().find_map(|l| {
            let mut parts = l.split_whitespace();
            match (parts.next(), parts.next()) {
                (Some(serial), Some("device")) if serial.starts_with("emulator-") => {
                    Some(serial.to_string())
                }
                _ => None,
            }
        })
    }

    /// Attach to a running emulator, or start one from `avd`; `None` when there
    /// is neither.
    fn attach_or_start(home: &Path, avd: Option<&str>) -> Option<Emulator> {
        let adb = home.join("platform-tools/adb");
        if let Some(serial) = Self::running(&adb) {
            return Some(Emulator {
                started: None,
                adb,
                serial,
            });
        }
        let port = 5584;
        let child = Command::new(home.join("emulator/emulator"))
            .args(["-avd", avd?, "-port", &port.to_string()])
            .args([
                "-no-window",
                "-no-audio",
                "-no-boot-anim",
                "-no-snapshot-save",
            ])
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .ok()?;
        Some(Emulator {
            started: Some(child),
            adb,
            serial: format!("emulator-{port}"),
        })
    }

    /// Wait until Android is ready for an app.
    ///
    /// `sys.boot_completed` alone is not ready: on a cold boot the package
    /// manager and the launcher are still starting, and SystemUI can show an
    /// "isn't responding" dialog that covers the app, so a UI step (the
    /// permission prompt, Back to close the scanner) went to the dialog and
    /// the gate failed by luck of the boot. So this also waits for the package
    /// manager and a resumed launcher, turns the animations off, and dismisses
    /// a system dialog.
    fn wait_booted(&self) -> bool {
        let _ = self.adb(&["wait-for-device"]);
        let until = std::time::Instant::now() + std::time::Duration::from_secs(600);
        let wait_for = |what: &dyn Fn() -> bool| -> bool {
            while std::time::Instant::now() < until {
                if what() {
                    return true;
                }
                std::thread::sleep(std::time::Duration::from_secs(2));
            }
            false
        };
        if !wait_for(&|| self.shell_text(&["getprop", "sys.boot_completed"]).trim() == "1") {
            eprintln!("the emulator did not set sys.boot_completed");
            return false;
        }
        if !wait_for(&|| {
            self.shell_text(&["pm", "path", "android"])
                .contains("package:")
        }) {
            eprintln!("the emulator's package manager did not answer");
            return false;
        }
        // The launcher: the package of the HOME activity is the resumed one.
        let mut home = String::new();
        let resolved = wait_for(&|| {
            self.shell_text(&[
                "cmd",
                "package",
                "resolve-activity",
                "--brief",
                "-a",
                "android.intent.action.MAIN",
                "-c",
                "android.intent.category.HOME",
            ])
            .lines()
            .any(|l| l.contains('/'))
        });
        if resolved {
            home = self
                .shell_text(&[
                    "cmd",
                    "package",
                    "resolve-activity",
                    "--brief",
                    "-a",
                    "android.intent.action.MAIN",
                    "-c",
                    "android.intent.category.HOME",
                ])
                .lines()
                .rev()
                .find(|l| l.contains('/'))
                .and_then(|l| l.trim().split('/').next().map(str::to_string))
                .unwrap_or_default();
        }
        if home.is_empty()
            || !wait_for(&|| {
                self.shell_text(&["dumpsys", "activity", "activities"])
                    .lines()
                    .any(|l| l.contains("ResumedActivity") && l.contains(&home))
            })
        {
            eprintln!("the emulator's launcher ({home:?}) did not resume");
            return false;
        }
        for key in [
            "window_animation_scale",
            "transition_animation_scale",
            "animator_duration_scale",
        ] {
            let _ = self.adb(&["shell", "settings", "put", "global", key, "0"]);
        }
        self.dismiss_system_dialogs();
        true
    }

    fn shell_text(&self, args: &[&str]) -> String {
        let mut all = vec!["shell"];
        all.extend_from_slice(args);
        String::from_utf8_lossy(&self.adb(&all).stdout).into_owned()
    }

    /// The system dialog that has the focus, if any: an "isn't responding"
    /// (ANR) or crash dialog, read from the focused window (`dumpsys window`).
    fn system_dialog(&self) -> Option<String> {
        let focus = self
            .shell_text(&["dumpsys", "window"])
            .lines()
            .find(|l| l.contains("mCurrentFocus="))?
            .trim()
            .to_string();
        let lower = focus.to_lowercase();
        (lower.contains("not responding")
            || lower.contains("isn't responding")
            || lower.contains("application error"))
        .then_some(focus)
    }

    /// Dismiss the system dialogs that have the focus: "Wait" on an ANR
    /// dialog, "Close app" on a crash dialog, else the system's close-dialogs
    /// broadcast. Returns whether there was one.
    fn dismiss_system_dialogs(&self) -> bool {
        let mut seen = false;
        for _ in 0..5 {
            let Some(focus) = self.system_dialog() else {
                break;
            };
            eprintln!("dismissing a system dialog: {focus}");
            seen = true;
            match ui_node_centre(self, &["aerr_wait", "aerr_close"]) {
                Some((x, y)) => {
                    let _ = self.adb(&["shell", "input", "tap", &x.to_string(), &y.to_string()]);
                }
                None => {
                    let _ = self.adb(&[
                        "shell",
                        "am",
                        "broadcast",
                        "-a",
                        "android.intent.action.CLOSE_SYSTEM_DIALOGS",
                    ]);
                }
            }
            std::thread::sleep(std::time::Duration::from_secs(2));
        }
        seen
    }

    /// The centre of the first node whose `resource-id` ends with one of
    /// `ids`. When it is not on screen because a system dialog covers it, the
    /// dialog is dismissed and the lookup runs once more; there is no retry
    /// without a detected dialog.
    fn find_ui(&self, ids: &[&str]) -> Option<(i32, i32)> {
        if let Some(c) = ui_node_centre(self, ids) {
            return Some(c);
        }
        if self.dismiss_system_dialogs() {
            return ui_node_centre(self, ids);
        }
        None
    }

    /// Evidence for a failed emulator step, taken while the device is still
    /// in the failed state: the log lines of the app, the shell's permission
    /// broker (`SkyPermissions`, `SkyNative`), ActivityManager and the
    /// permission controller; the focused window; a screenshot and the UI
    /// tree; and the backend's report log. Saved under SKYTEST_EVIDENCE_DIR
    /// (else the temp dir); the returned text names the folder and carries
    /// the focus and the broker's log.
    fn capture_evidence(&self, tag: &str, backend: &str) -> String {
        let root = std::env::var_os("SKYTEST_EVIDENCE_DIR")
            .map(PathBuf::from)
            .unwrap_or_else(std::env::temp_dir);
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis())
            .unwrap_or(0);
        let dir = root.join(format!("sky-native-evidence-{tag}-{stamp}"));
        let _ = std::fs::create_dir_all(&dir);
        let pid = self
            .shell_text(&["pidof", "com.example.probe"])
            .trim()
            .to_string();
        let full = self.shell_text(&["logcat", "-d", "-v", "threadtime"]);
        let keep = |l: &str| {
            [
                "SkyPermissions",
                "SkyNative",
                "ActivityManager",
                "ActivityTaskManager",
                "PermissionController",
                "permissioncontroller",
                "GrantPermissions",
                "com.example.probe",
            ]
            .iter()
            .any(|k| l.contains(k))
                || (!pid.is_empty() && l.split_whitespace().nth(2) == Some(pid.as_str()))
        };
        let filtered: Vec<&str> = full.lines().filter(|l| keep(l)).collect();
        let broker: Vec<&str> = full
            .lines()
            .filter(|l| l.contains("SkyPermissions") || l.contains("SkyNative"))
            .collect();
        let focus: Vec<String> = self
            .shell_text(&["dumpsys", "window"])
            .lines()
            .filter(|l| l.contains("mCurrentFocus=") || l.contains("mFocusedApp="))
            .map(|l| l.trim().to_string())
            .collect();
        let _ = std::fs::write(dir.join("logcat.txt"), filtered.join("\n"));
        let _ = std::fs::write(dir.join("logcat-full.txt"), &full);
        let _ = std::fs::write(dir.join("focus.txt"), focus.join("\n"));
        let _ = std::fs::write(dir.join("backend.txt"), backend);
        let shot = self.adb(&["exec-out", "screencap", "-p"]);
        let _ = std::fs::write(dir.join("screen.png"), &shot.stdout);
        let _ = std::fs::write(dir.join("ui.xml"), ui_dump(self).unwrap_or_default());
        format!(
            "evidence in {}\n--- focus\n{}\n--- SkyPermissions / SkyNative\n{}\n--- backend\n{}",
            dir.display(),
            focus.join("\n"),
            broker.join("\n"),
            backend
        )
    }

    /// Send a key to the app, after dismissing a system dialog that would
    /// take it instead.
    fn key(&self, key: &str) {
        self.dismiss_system_dialogs();
        let _ = self.adb(&["shell", "input", "keyevent", key]);
    }
}

#[cfg(unix)]
impl Drop for Emulator {
    fn drop(&mut self) {
        let Some(child) = self.started.as_mut() else {
            return; // not ours: leave it running
        };
        let _ = Command::new(&self.adb)
            .args(["-s", &self.serial, "emu", "kill"])
            .output();
        for _ in 0..60 {
            if let Ok(Some(_)) = child.try_wait() {
                return;
            }
            std::thread::sleep(std::time::Duration::from_millis(500));
        }
        let _ = child.kill();
        let _ = child.wait();
    }
}

/// The Android gate, the counterpart of the iOS simulator one: the same probe,
/// with no entitlement and with the restricted Apple ones declared (the
/// associated domain becomes an App Links filter; the link checks are below),
/// built for
/// `mobile:android`, installed on a running emulator and LAUNCHED. The app's
/// own results are read back through its backend: the Keystore-backed secure
/// store round-trips a value, `Native.scanCode` opens the camera scanner and
/// Back closes it (`Ok Nothing`), and `Native.authenticate` with no enrolled
/// biometric is `Err Unavailable`. The APK carries the ZXing decoder because
/// the app calls `Native.scanCode`, and its label is the full display name.
///
/// It uses an emulator that is already running (the release workflow's
/// `gate-native-android` job starts one on a Linux runner with KVM), or starts
/// the first AVD and stops it afterwards.
#[cfg(unix)]
#[test]
#[ignore = "native emulator: needs Go + the Android SDK + a running emulator or an AVD (release gate-native-android)"]
fn android_emulator_app_launches_and_round_trips_the_keystore_scanner_and_biometrics() {
    let _emulator = EMULATOR_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let home = android_home();
    if !required(Need::Go, have_go()) || !required(Need::AndroidSdk, home.is_some()) {
        return;
    }
    let home = home.unwrap();
    let home_s = home.to_string_lossy().into_owned();
    let declared = format!("{USAGES}\n{DECLARED}");
    // Build both variants before the emulator is up.
    let mut builds = Vec::new();
    // The declared variant is built as `tablet:android`: a tablet build is the
    // same native shell as the phone's.
    for (tag, steps, target, out_dir) in [
        ("plain", USAGES, "mobile:android", "mobile-android"),
        (
            "declared",
            declared.as_str(),
            "tablet:android",
            "tablet-android",
        ),
    ] {
        let dir = scratch(&format!("android-emu-{tag}"));
        probe_app(&dir, steps);
        let port = free_port();
        let port_s = port.to_string();
        let (ok, out) = run(
            &dir,
            &["build", "--target", target, "src/Main.sky"],
            &[("PORT", &port_s), ("ANDROID_HOME", &home_s)],
        );
        assert!(ok, "{tag}: the Android build failed:\n{out}");
        let split = dir.join(".skyapp").join(out_dir).join(".split");
        let android = split.join("frontend/sky-out/android");
        assert!(
            android.join("app/libs/zxing-core-3.5.3.jar").is_file(),
            "{tag}: an app that calls Native.scanCode builds with the ZXing decoder"
        );
        let manifest =
            std::fs::read_to_string(android.join("app/src/main/AndroidManifest.xml")).unwrap();
        assert!(
            manifest.contains("android:label=\"Sky Probe\"")
                && manifest.contains("android.permission.CAMERA")
                && manifest.contains("android.permission.USE_BIOMETRIC"),
            "{tag}: {manifest}"
        );
        // `applinks:example.com` is an App Links filter; the plain variant has
        // none.
        assert_eq!(
            manifest.matches("android:autoVerify=\"true\"").count(),
            usize::from(tag == "declared"),
            "{tag}: {manifest}"
        );
        assert_eq!(
            manifest.contains("android:host=\"example.com\""),
            tag == "declared",
            "{tag}: {manifest}"
        );
        if tag == "declared" {
            assert!(
                android.join("build/assetlinks.json").is_file()
                    && out.contains("/.well-known/assetlinks.json"),
                "the build writes assetlinks.json and says where to serve it:\n{out}"
            );
        }
        // The APK is named from the whole display name ("Sky Probe").
        let apk = android.join("build/skyprobe.apk");
        assert!(apk.is_file(), "{tag}: no skyprobe.apk:\n{out}");
        builds.push((tag, dir, split, apk, port));
    }

    let emu = Emulator::attach_or_start(&home, first_avd(&home).as_deref());
    if !required(Need::AndroidEmulator, emu.is_some()) {
        return;
    }
    let emu = emu.unwrap();
    assert!(emu.wait_booted(), "the emulator did not finish booting");
    let mut failures = Vec::new();
    for (tag, dir, split, apk, port) in &builds {
        let _ = emu.adb(&["uninstall", "com.example.probe"]);
        let inst = emu.adb(&["install", "-r", "-g", apk.to_str().unwrap()]);
        assert!(
            inst.status.success(),
            "{tag}: adb install: {}{}",
            String::from_utf8_lossy(&inst.stdout),
            String::from_utf8_lossy(&inst.stderr)
        );
        let backend = Backend::start(split, *port);
        emu.dismiss_system_dialogs();
        let start = emu.adb(&[
            "shell",
            "am",
            "start",
            "-W",
            "-n",
            "com.example.probe/.MainActivity",
        ]);
        let secure = backend.probe_line("secure", 180);
        // The scanner is open over the app: Back closes it without a code.
        std::thread::sleep(std::time::Duration::from_secs(5));
        emu.key("KEYCODE_BACK");
        let scan = backend.probe_line("scan", 60);
        let auth = backend.probe_line("auth", 60);
        let _ = emu.adb(&["shell", "am", "force-stop", "com.example.probe"]);
        // App Links. The declared variant carries `Bundle.AssociatedDomain
        // "applinks:example.com"`: a link to that host opens the app on the
        // link's page, both when it starts the app (onCreate) and when the app
        // is running (onNewIntent, which navigates in place). example.com serves no assetlinks.json for
        // this app, so the test approves the domain for the app, as a user
        // does in the app's settings. The plain variant declares no domain and
        // must not take the link.
        let open_link = |path: &str| {
            emu.dismiss_system_dialogs();
            let o = emu.adb(&[
                "shell",
                "am",
                "start",
                "-W",
                "-a",
                "android.intent.action.VIEW",
                "-c",
                "android.intent.category.BROWSABLE",
                "-d",
                &format!("https://example.com{path}"),
                "com.example.probe",
            ]);
            format!(
                "{}{}",
                String::from_utf8_lossy(&o.stdout),
                String::from_utf8_lossy(&o.stderr)
            )
        };
        let mut link_failure = None;
        if *tag == "declared" {
            let state = emu.adb(&["shell", "pm", "get-app-links", "com.example.probe"]);
            let state = String::from_utf8_lossy(&state.stdout).into_owned();
            let _ = emu.adb(&[
                "shell",
                "pm",
                "set-app-links-user-selection",
                "--user",
                "cur",
                "--package",
                "com.example.probe",
                "true",
                "example.com",
            ]);
            let cold = open_link("/probe/deep");
            let deep = backend.saw("route=probe:deep", 180);
            let warm = open_link("/probe/again");
            let again = backend.saw("route=probe:again", 120);
            let _ = emu.adb(&["shell", "am", "force-stop", "com.example.probe"]);
            if !state.contains("example.com") {
                link_failure = Some(format!(
                    "the installed app declares no App Link domain:\n{state}"
                ));
            } else if !deep {
                link_failure = Some(format!(
                    "an App Link that starts the app must open /probe/deep:\n{cold}\n{}",
                    backend.output()
                ));
            } else if !again {
                link_failure = Some(format!(
                    "an App Link to the running app must open /probe/again:\n{warm}\n{}",
                    backend.output()
                ));
            } else if backend.output().contains("already open") {
                // The running app navigates in place: the scanner the cold
                // start opened is still its own, not an orphan a reload left.
                link_failure = Some(format!(
                    "an App Link to the running app must not reload it:\n{}",
                    backend.output()
                ));
            }
        } else {
            let cold = open_link("/probe/deep");
            std::thread::sleep(std::time::Duration::from_secs(5));
            let _ = emu.adb(&["shell", "am", "force-stop", "com.example.probe"]);
            if backend.saw("route=probe:deep", 1) || !cold.contains("unable to resolve") {
                link_failure = Some(format!(
                    "an app that declares no associated domain must not take the link:\n{cold}"
                ));
            }
        }
        let _ = emu.adb(&["uninstall", "com.example.probe"]);
        let output = backend.output();
        drop(backend);
        if !start.status.success() {
            failures.push(format!(
                "{tag}: am start failed: {}",
                String::from_utf8_lossy(&start.stderr)
            ));
        } else if secure.as_deref() != Some("secure=ok:probe-value") {
            failures.push(format!(
                "{tag}: the Keystore round trip read {secure:?}, want secure=ok:probe-value\n{output}"
            ));
        } else if scan.as_deref() != Some("scan=cancelled") {
            failures.push(format!(
                "{tag}: Native.scanCode closed with Back read {scan:?}, want scan=cancelled\n{output}"
            ));
        } else if !auth
            .as_deref()
            .is_some_and(|s| s.starts_with("auth=err:Unavailable"))
        {
            failures.push(format!(
                "{tag}: Native.authenticate with no enrolled biometric read {auth:?}, want Err Unavailable\n{output}"
            ));
        } else if let Some(f) = link_failure {
            failures.push(format!("{tag}: {f}"));
        }
        let _ = std::fs::remove_dir_all(dir);
    }
    drop(emu);
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

/// The emulator's UI tree, from `uiautomator dump`. A dump can fail (on a
/// freshly booted emulator it can time out waiting for the UI to go idle),
/// and it then leaves the previous file behind: reading that file gave the
/// last screen's nodes, or none, and a lookup reported an element missing
/// that was on screen. So the old file is removed first, and a failed dump
/// is taken again (up to three times); `None` only when every dump failed.
#[cfg(unix)]
fn ui_dump(emu: &Emulator) -> Option<String> {
    for attempt in 0..3 {
        if attempt > 0 {
            std::thread::sleep(std::time::Duration::from_secs(1));
        }
        let _ = emu.adb(&["shell", "rm", "-f", "/sdcard/sky-ui.xml"]);
        let _ = emu.adb(&["shell", "uiautomator", "dump", "/sdcard/sky-ui.xml"]);
        let xml = emu.shell_text(&["cat", "/sdcard/sky-ui.xml"]);
        if xml.contains("<hierarchy") {
            return Some(xml);
        }
        eprintln!(
            "uiautomator dump failed (attempt {}), dumping again",
            attempt + 1
        );
    }
    None
}

/// The centre of the first node in the emulator's UI whose `resource-id`
/// ends with one of `ids`, from a uiautomator dump.
#[cfg(unix)]
fn ui_node_centre(emu: &Emulator, ids: &[&str]) -> Option<(i32, i32)> {
    let xml = ui_dump(emu)?;
    for node in xml.split("<node ") {
        let attr = |name: &str| -> Option<String> {
            let key = format!("{name}=\"");
            let i = node.find(&key)? + key.len();
            Some(node[i..].split('"').next()?.to_string())
        };
        let Some(id) = attr("resource-id") else {
            continue;
        };
        if !ids.iter().any(|want| id.ends_with(want)) {
            continue;
        }
        // bounds="[x1,y1][x2,y2]"
        let b = attr("bounds")?;
        let nums: Vec<i32> = b
            .split(|c: char| !c.is_ascii_digit())
            .filter(|s| !s.is_empty())
            .filter_map(|s| s.parse().ok())
            .collect();
        if nums.len() == 4 {
            return Some(((nums[0] + nums[2]) / 2, (nums[1] + nums[3]) / 2));
        }
    }
    None
}

/// `Native.scanCode` at first launch waits for the camera prompt's answer.
/// The shell asks for the declared run-time permissions when it starts, and
/// the probe calls `scanCode` right after its secure-store round trip, while
/// that prompt still shows. Android answers a second request made while a
/// prompt shows at once with empty arrays, and the scanner read that as a
/// denial: `Err PermissionDenied` before the user had answered, and the next
/// launch scanned (found downstream on an Android 16 device). The shell's
/// permission broker (`sky.perm.SkyPermissions`) now parks the scan until the
/// start-up prompt is answered: "While using the app" opens the scanner (Back
/// closes it, `Ok Nothing`), and "Don't allow" is the only way to
/// `Err PermissionDenied`. The app is installed without `-g`, so the camera
/// is not granted.
#[cfg(unix)]
#[test]
#[ignore = "native emulator: needs Go + the Android SDK + a running emulator or an AVD (release gate-native-android)"]
fn android_emulator_scan_at_first_launch_waits_for_the_camera_prompt() {
    let _emulator = EMULATOR_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let home = android_home();
    if !required(Need::Go, have_go()) || !required(Need::AndroidSdk, home.is_some()) {
        return;
    }
    let home = home.unwrap();
    let home_s = home.to_string_lossy().into_owned();
    let dir = scratch("android-first-launch");
    probe_app(&dir, USAGES);
    let port = free_port();
    let port_s = port.to_string();
    let (ok, out) = run(
        &dir,
        &["build", "--target", "mobile:android", "src/Main.sky"],
        &[("PORT", &port_s), ("ANDROID_HOME", &home_s)],
    );
    assert!(ok, "the Android build failed:\n{out}");
    let split = dir.join(".skyapp/mobile-android/.split");
    let apk = split.join("frontend/sky-out/android/build/skyprobe.apk");
    let emu = Emulator::attach_or_start(&home, first_avd(&home).as_deref());
    if !required(Need::AndroidEmulator, emu.is_some()) {
        return;
    }
    let emu = emu.unwrap();
    assert!(emu.wait_booted(), "the emulator did not finish booting");
    let mut failures = Vec::new();
    // (the prompt button to press, the scan result it must lead to)
    for (button, want) in [
        ("permission_allow_foreground_only_button", "scan=cancelled"),
        ("permission_deny_button", "scan=err:PermissionDenied"),
    ] {
        let _ = emu.adb(&["uninstall", "com.example.probe"]);
        let inst = emu.adb(&["install", "-r", apk.to_str().unwrap()]);
        assert!(
            inst.status.success(),
            "adb install: {}",
            String::from_utf8_lossy(&inst.stderr)
        );
        let _ = emu.adb(&[
            "shell",
            "pm",
            "revoke",
            "com.example.probe",
            "android.permission.CAMERA",
        ]);
        let backend = Backend::start(&split, port);
        emu.dismiss_system_dialogs();
        let _ = emu.adb(&[
            "shell",
            "am",
            "start",
            "-W",
            "-n",
            "com.example.probe/.MainActivity",
        ]);
        let secure = backend.probe_line("secure", 180);
        // scanCode has been called; the prompt is still up, so it must not
        // have answered yet.
        std::thread::sleep(std::time::Duration::from_secs(5));
        let early = backend.probe_line("scan", 1);
        let tap = emu.find_ui(&[button]);
        if let Some((x, y)) = tap {
            let _ = emu.adb(&["shell", "input", "tap", &x.to_string(), &y.to_string()]);
        }
        if want == "scan=cancelled" {
            // The scanner opens after Allow: Back closes it without a code.
            std::thread::sleep(std::time::Duration::from_secs(5));
            emu.key("KEYCODE_BACK");
        }
        let scan = backend.probe_line("scan", 60);
        let _ = emu.adb(&["shell", "am", "force-stop", "com.example.probe"]);
        let output = backend.output();
        drop(backend);
        if secure.as_deref() != Some("secure=ok:probe-value") {
            failures.push(format!(
                "{button}: the app did not start: {secure:?}\n{output}"
            ));
        } else if early.is_some() {
            failures.push(format!(
                "{button}: scanCode answered {early:?} while the camera prompt showed\n{output}"
            ));
        } else if tap.is_none() {
            failures.push(format!(
                "{button}: the camera prompt was not on screen\n{output}"
            ));
        } else if !scan.as_deref().is_some_and(|s| s.starts_with(want)) {
            failures.push(format!(
                "{button}: scanCode read {scan:?}, want {want}\n{output}"
            ));
        }
    }
    let _ = emu.adb(&["uninstall", "com.example.probe"]);
    drop(emu);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

/// The probe's notification permission (`Native.notify`).
const NOTIFY: &str = "        |> Bundle.withPermission Bundle.Notifications";

/// `Native.notify` at first launch waits for the notification prompt's
/// answer. On Android 13 and later POST_NOTIFICATIONS is a run-time
/// permission: the shell asks for it when it starts, and the probe calls
/// `Native.notify` right after its secure-store round trip, while that prompt
/// still shows. The bridge used to be a synchronous call that answered `Ok`
/// at once, and NotificationManager dropped the notification (no permission
/// yet), so the first notification was lost with no error. The shell now
/// routes `sky:notify` through the permission broker: "Allow" posts the
/// notification (it is in `dumpsys notification`) and answers `Ok`, and
/// "Don't allow" answers `Err PermissionDenied`. The app is installed without
/// `-g`, and the permission revoked, so it is not granted.
///
/// "Don't allow" is also pressed the moment the prompt shows, usually before
/// the app has called notify. This case used to hang: the broker asked again
/// for a permission the user had just refused, a second prompt showed, and
/// notify waited on it (the gate's one unexplained failure, reproduced by
/// tapping at once, SkyPermissions log: `result 23730 … [-1]`, then `ensure …
/// waiting`, then `request 23731`). A refusal now holds for the rest of the
/// run, so notify answers `Err PermissionDenied` whichever came first, and no
/// second prompt shows. On a failure the test saves evidence (the app's,
/// ActivityManager's and the permission controller's log lines, the focused
/// window, a screenshot, the UI tree and the backend's log) under
/// SKYTEST_EVIDENCE_DIR, else the temp dir.
#[cfg(unix)]
#[test]
#[ignore = "native emulator: needs Go + the Android SDK + a running emulator or an AVD (release gate-native-android)"]
fn android_emulator_notify_at_first_launch_waits_for_the_notification_prompt() {
    let _emulator = EMULATOR_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let home = android_home();
    if !required(Need::Go, have_go()) || !required(Need::AndroidSdk, home.is_some()) {
        return;
    }
    let home = home.unwrap();
    let home_s = home.to_string_lossy().into_owned();
    let dir = scratch("android-notify");
    probe_app_with(&dir, NOTIFY, Flow::Notify);
    let port = free_port();
    let port_s = port.to_string();
    let (ok, out) = run(
        &dir,
        &["build", "--target", "mobile:android", "src/Main.sky"],
        &[("PORT", &port_s), ("ANDROID_HOME", &home_s)],
    );
    assert!(ok, "the Android build failed:\n{out}");
    let split = dir.join(".skyapp/mobile-android/.split");
    let apk = split.join("frontend/sky-out/android/build/skyprobe.apk");
    let emu = Emulator::attach_or_start(&home, first_avd(&home).as_deref());
    if !required(Need::AndroidEmulator, emu.is_some()) {
        return;
    }
    let emu = emu.unwrap();
    assert!(emu.wait_booted(), "the emulator did not finish booting");
    let sdk: u32 = emu
        .shell_text(&["getprop", "ro.build.version.sdk"])
        .trim()
        .parse()
        .unwrap_or(0);
    assert!(
        sdk >= 33,
        "the notification prompt needs Android 13 (API 33) or later; the emulator runs API {sdk}"
    );
    let mut failures = Vec::new();
    // (the prompt button to press, the notify result it must lead to, and
    // whether the button is pressed the moment the prompt shows, which is
    // usually before the app has called notify)
    for (button, want, race) in [
        ("permission_allow_button", "notify=ok", false),
        (
            "permission_deny_button",
            "notify=err:PermissionDenied",
            false,
        ),
        (
            "permission_deny_button",
            "notify=err:PermissionDenied",
            true,
        ),
    ] {
        let case = format!("{button}{}", if race { " (at once)" } else { "" });
        let _ = emu.adb(&["uninstall", "com.example.probe"]);
        let inst = emu.adb(&["install", "-r", apk.to_str().unwrap()]);
        assert!(
            inst.status.success(),
            "adb install: {}",
            String::from_utf8_lossy(&inst.stderr)
        );
        let _ = emu.adb(&[
            "shell",
            "pm",
            "revoke",
            "com.example.probe",
            "android.permission.POST_NOTIFICATIONS",
        ]);
        let backend = Backend::start(&split, port);
        emu.dismiss_system_dialogs();
        let _ = emu.adb(&["logcat", "-c"]);
        let _ = emu.adb(&[
            "shell",
            "am",
            "start",
            "-W",
            "-n",
            "com.example.probe/.MainActivity",
        ]);
        let (secure, early, tap) = if race {
            // Tap the moment the prompt is on screen: the answer can come
            // before the app has called notify.
            let until = std::time::Instant::now() + std::time::Duration::from_secs(60);
            let mut tap = None;
            while tap.is_none() && std::time::Instant::now() < until {
                tap = ui_node_centre(&emu, &[button]);
            }
            if let Some((x, y)) = tap {
                let _ = emu.adb(&["shell", "input", "tap", &x.to_string(), &y.to_string()]);
            }
            (backend.probe_line("secure", 180), None, tap)
        } else {
            let secure = backend.probe_line("secure", 180);
            // notify has been called; the prompt is still up, so it must not
            // have answered yet.
            std::thread::sleep(std::time::Duration::from_secs(5));
            let early = backend.probe_line("notify", 1);
            let tap = emu.find_ui(&[button]);
            if let Some((x, y)) = tap {
                let _ = emu.adb(&["shell", "input", "tap", &x.to_string(), &y.to_string()]);
            }
            (secure, early, tap)
        };
        let notify = backend.probe_line("notify", 60);
        // An answer is final for the run: no second prompt follows it.
        std::thread::sleep(std::time::Duration::from_secs(2));
        let prompt_again = emu
            .shell_text(&["dumpsys", "window"])
            .lines()
            .any(|l| l.contains("mCurrentFocus=") && l.contains("GrantPermissionsActivity"));
        // The posted notification, as the system holds it.
        let posted = emu
            .shell_text(&["dumpsys", "notification", "--noredact"])
            .lines()
            .any(|l| l.contains("probe-notification"));
        let output = backend.output();
        let failure = if secure.as_deref() != Some("secure=ok:probe-value") {
            Some(format!("the app did not start: {secure:?}"))
        } else if early.is_some() {
            Some(format!(
                "notify answered {early:?} while the notification prompt showed"
            ))
        } else if tap.is_none() {
            Some("the notification prompt was not on screen".to_string())
        } else if !notify.as_deref().is_some_and(|s| s.starts_with(want)) {
            Some(format!("notify read {notify:?}, want {want}"))
        } else if prompt_again {
            Some("the permission prompt showed again after the user answered it".to_string())
        } else if posted != (want == "notify=ok") {
            Some(format!(
                "the notification is {} in `dumpsys notification`",
                if posted { "posted" } else { "not posted" }
            ))
        } else {
            None
        };
        if let Some(f) = failure {
            // Taken before the app is stopped, so it shows the stuck state.
            let evidence = emu.capture_evidence(&format!("notify-{button}-{race}"), &output);
            failures.push(format!("{case}: {f}\n{evidence}"));
        }
        let _ = emu.adb(&["shell", "am", "force-stop", "com.example.probe"]);
        drop(backend);
    }
    let _ = emu.adb(&["uninstall", "com.example.probe"]);
    drop(emu);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

/// The emulator helpers find and dismiss a system dialog that covers the app.
/// A cold boot can leave a SystemUI "isn't responding" dialog in front, and a
/// UI step then went to the dialog. This raises a real one (a crash dialog for
/// Settings, with the first-crash dialog turned on) and checks that it is seen
/// as a system dialog and dismissed.
#[cfg(unix)]
#[test]
#[ignore = "native emulator: needs the Android SDK + a running emulator or an AVD (release gate-native-android)"]
fn android_emulator_a_system_dialog_is_dismissed_before_app_steps() {
    let _emulator = EMULATOR_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let home = android_home();
    if !required(Need::AndroidSdk, home.is_some()) {
        return;
    }
    let home = home.unwrap();
    let emu = Emulator::attach_or_start(&home, first_avd(&home).as_deref());
    if !required(Need::AndroidEmulator, emu.is_some()) {
        return;
    }
    let emu = emu.unwrap();
    assert!(emu.wait_booted(), "the emulator did not finish booting");
    assert!(
        emu.system_dialog().is_none(),
        "wait_booted must leave no system dialog in front"
    );
    let _ = emu.adb(&[
        "shell",
        "settings",
        "put",
        "global",
        "show_first_crash_dialog",
        "1",
    ]);
    let _ = emu.adb(&[
        "shell",
        "settings",
        "put",
        "secure",
        "show_first_crash_dialog_dev_option",
        "1",
    ]);
    // Android shows no crash dialog for an app that crashed less than a
    // minute before (it marks it as crashing repeatedly), so a second try
    // waits that minute out.
    let mut shown = None;
    for attempt in 0..2 {
        if attempt > 0 {
            std::thread::sleep(std::time::Duration::from_secs(65));
        }
        let _ = emu.adb(&[
            "shell",
            "am",
            "start",
            "-W",
            "-n",
            "com.android.settings/.Settings",
        ]);
        let _ = emu.adb(&["shell", "am", "crash", "com.android.settings"]);
        for _ in 0..20 {
            shown = emu.system_dialog();
            if shown.is_some() {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(500));
        }
        if shown.is_some() {
            break;
        }
    }
    let dismissed = emu.dismiss_system_dialogs();
    let after = emu.system_dialog();
    let _ = emu.adb(&[
        "shell",
        "settings",
        "put",
        "global",
        "show_first_crash_dialog",
        "0",
    ]);
    let _ = emu.adb(&[
        "shell",
        "settings",
        "delete",
        "secure",
        "show_first_crash_dialog_dev_option",
    ]);
    let _ = emu.adb(&["shell", "am", "force-stop", "com.android.settings"]);
    drop(emu);
    let shown = shown.expect("the crash dialog did not take the focus");
    assert!(
        shown.contains("Application Error"),
        "a crash dialog is a system dialog: {shown}"
    );
    assert!(dismissed, "the dialog was there, dismiss must say so");
    assert!(after.is_none(), "the dialog is still in front: {after:?}");
}
