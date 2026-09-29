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
    ( {{ status = "running", page = "home" }}, Cmd.perform roundTrip Got )


update : Msg -> Model -> ( Model, Cmd.Cmd Msg )
update msg model =
    case msg of
        Navigated p ->
            ( {{ model | page = p }}, Cmd.perform (Task.succeed ("route=" ++ p)) Report )

        Got r ->
            ( {{ model | status = secureText r }}
            , Cmd.batch
                [ Cmd.perform (Task.succeed (secureText r)) Report
                , Cmd.perform (Native.scanCode {{ formats = [ Native.Qr ], prompt = "Scan the probe code" }}) Scanned
                ]
            )

        Scanned r ->
            ( {{ model | status = scanText r }}
            , Cmd.batch
                [ Cmd.perform (Task.succeed (scanText r)) Report
                , Cmd.perform (Native.authenticate "Confirm the probe") Authed
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

    /// Wait until Android has finished booting.
    fn wait_booted(&self) -> bool {
        let _ = self.adb(&["wait-for-device"]);
        let until = std::time::Instant::now() + std::time::Duration::from_secs(600);
        while std::time::Instant::now() < until {
            let o = self.adb(&["shell", "getprop", "sys.boot_completed"]);
            if String::from_utf8_lossy(&o.stdout).trim() == "1" {
                return true;
            }
            std::thread::sleep(std::time::Duration::from_secs(2));
        }
        false
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
        let _ = emu.adb(&["shell", "input", "keyevent", "KEYCODE_BACK"]);
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

/// The centre of the first node in the emulator's UI whose `resource-id`
/// ends with one of `ids`, from a uiautomator dump.
#[cfg(unix)]
fn ui_node_centre(emu: &Emulator, ids: &[&str]) -> Option<(i32, i32)> {
    let _ = emu.adb(&["shell", "uiautomator", "dump", "/sdcard/sky-ui.xml"]);
    let xml = String::from_utf8_lossy(&emu.adb(&["shell", "cat", "/sdcard/sky-ui.xml"]).stdout)
        .into_owned();
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
        let tap = ui_node_centre(&emu, &[button]);
        if let Some((x, y)) = tap {
            let _ = emu.adb(&["shell", "input", "tap", &x.to_string(), &y.to_string()]);
        }
        if want == "scan=cancelled" {
            // The scanner opens after Allow: Back closes it without a code.
            std::thread::sleep(std::time::Duration::from_secs(5));
            let _ = emu.adb(&["shell", "input", "keyevent", "KEYCODE_BACK"]);
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
