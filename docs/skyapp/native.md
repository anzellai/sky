# Native shells: permissions, entitlements, secure storage, release

The `mobile:ios`, `mobile:android` and `desktop:mac` targets wrap the Sky.Spa
client in a native web view (see [Client targets](overview.md#client-targets--same-source-no-stdspa-entry)).
This page covers what a native app needs beyond the web build: the purpose
strings the operating system shows in its permission prompts, the Apple
entitlements, the device's secure store, the biometric prompt, and the store
artefact that `sky package --release` makes. The last section is a recipe that
scans and generates QR codes.

Everything here is declared in code, in the optional `bundle` binding of the
entry module. `sky.toml` gains no keys. The build reads the declarations
statically, so each argument is a literal; a value computed at run time is a
build error that names the builder.

```elm
-- doc-example: skip  (fragment — the rest of the app is elided)
bundle : Bundle
bundle =
    Bundle.default
        |> Bundle.withId "com.acme.vault"
        |> Bundle.withVersion "1.4.0"
        |> Bundle.withBuild 12
        |> Bundle.withUsage Bundle.Camera "Scans the pairing code on your other device."
        |> Bundle.withUsage Bundle.FaceId "Unlocks your saved sign-in."
        |> Bundle.withEntitlement (Bundle.AppGroup "group.com.acme.vault")
```

## Identity and version

| Builder | Writes | Default |
|---|---|---|
| `withName` | CFBundleDisplayName, `android:label`, window title | the project name |
| `withId` | CFBundleIdentifier, the Android package | `sky.spa.<name>` (with a note) |
| `withVersion` | CFBundleShortVersionString, `android:versionName` | `1.0` |
| `withBuild` | CFBundleVersion, `android:versionCode` | `1` |
| `withIcon` | the app icon set (rendered with `sips`) | the platform icon |

A store refuses a second upload with the same build number, so raise
`withBuild` for each release. Before v0.27.0 the build number was always 1.

## Permissions and purpose strings — `withPermission`, `withUsage`

`Bundle.withPermission P` declares that the app needs a permission, with a
generic purpose string. `Bundle.withUsage P "text"` declares it with the text
the operating system shows in its prompt. Say why the app needs it, in the
user's terms.

| Constructor | iOS `Info.plist` key | Android permission | macOS key |
|---|---|---|---|
| `Location` | NSLocationWhenInUseUsageDescription | ACCESS_FINE_LOCATION, ACCESS_COARSE_LOCATION | NSLocationUsageDescription |
| `LocationAlways` | + NSLocationAlwaysAndWhenInUseUsageDescription | + ACCESS_BACKGROUND_LOCATION | NSLocationUsageDescription |
| `Camera` | NSCameraUsageDescription | CAMERA | NSCameraUsageDescription |
| `Microphone` | NSMicrophoneUsageDescription | RECORD_AUDIO | NSMicrophoneUsageDescription |
| `Notifications` | (none) | POST_NOTIFICATIONS | (none) |
| `PhotoLibrary` | NSPhotoLibraryUsageDescription, NSPhotoLibraryAddUsageDescription | READ_MEDIA_IMAGES | NSPhotoLibraryUsageDescription |
| `Contacts` | NSContactsUsageDescription | READ_CONTACTS | NSContactsUsageDescription |
| `FaceId` | NSFaceIDUsageDescription | USE_BIOMETRIC | (none: Touch ID needs no key) |
| `LocalNetwork` | NSLocalNetworkUsageDescription | (none) | NSLocalNetworkUsageDescription |
| `Bluetooth` | NSBluetoothAlwaysUsageDescription | BLUETOOTH_CONNECT, BLUETOOTH_SCAN | NSBluetoothAlwaysUsageDescription |

Android permissions that need a grant at run time are requested when the app
starts; `USE_BIOMETRIC` is a normal permission and is not.

**A capability without its permission is a build error.** The iOS and Android
builds read every source file for the `Std.Native` calls whose operating-system
API refuses to run without a permission, and fail when the permission is not
declared:

| Call | Needs |
|---|---|
| `Native.authenticate` | `FaceId` |
| `Native.capturePhoto` | `Camera` |
| `Native.geolocation` | `Location` or `LocationAlways` |

```
sky build --target mobile:ios: missing permission purpose string:
  - the app calls `Native.authenticate`, which needs NSFaceIDUsageDescription on iOS.
    Add `|> Bundle.withUsage Bundle.FaceId "<why the app needs it>"` to the
    `bundle` binding in the entry module.
```

The check runs before the toolchain probe and before any compile, and `sky
check --target …` runs it too, so check and build agree. It finds a call
through the module's import alias (`Native.authenticate`) or through a name the
import exposes explicitly. A permission that another capability needs (a
widget that calls `getUserMedia`, as in the QR recipe below) is not detected:
declare it yourself.

A release (`sky package --release`) also refuses a permission declared with
`withPermission` alone when the platform shows its purpose string, because App
Review rejects a generic purpose string. Use `withUsage`.

## Typed entitlements — `withEntitlement`

`Bundle.withEntitlement` writes an Apple entitlement into the signed app's
`.entitlements` file (iOS and the macOS `.app`; Android has none):

| Constructor | Entitlement key |
|---|---|
| `KeychainAccessGroup "TEAMID.com.acme.shared"` | `keychain-access-groups` (the team prefix is part of the value) |
| `AppGroup "group.com.acme.app"` | `com.apple.security.application-groups` |
| `AssociatedDomain "applinks:acme.com"` | `com.apple.developer.associated-domains` (`webcredentials:` for password autofill) |
| `PushNotifications PushProduction` | `aps-environment` (`PushDevelopment` for a development build) |
| `ICloudContainer "iCloud.com.acme.app"` | `com.apple.developer.icloud-container-identifiers` + `icloud-services: CloudKit` |

The build checks the shape of each value (an app group starts with `group.`,
an associated domain names its service, an iCloud container starts with
`iCloud.`). A signed release also checks that the provisioning profile grants
every entitlement the app asks for, and names the ones it does not.

The simulator build is signed ad hoc with the entitlements, so the Keychain
works there.

## Native fragments and how they merge

A project or a Sky library can still ship native files under
`native/<platform>/`: Swift and Java bridge handlers (see `Std.Native.bridge`),
`native/ios/Info.plist.append`, `native/ios/app.entitlements`,
`native/macos/Info.plist.append`, `native/macos/app.entitlements` and
`native/android/permissions.xml`.

The build reads each fragment as a tree and merges the trees; it never joins
them as text. Before v0.27.0 it did, and two defects followed: two
`app.entitlements` files that were each a whole property list became two XML
documents in one file, which `codesign` rejects, and a key set by two sources
(a library's `NSCameraUsageDescription` next to the one `withPermission Camera`
writes) appeared twice in one `<dict>`.

The precedence, highest first:

1. The keys Sky generates (identity, version, device family, transport
   security, and for a signed release the provisioning profile's identity
   keys). A fragment that sets one of them to another value is a build error.
2. The app's own declarations (`withUsage`, `withPermission`,
   `withEntitlement`).
3. The project's own `native/<platform>/` fragment.
4. Each dependency's fragment. Dependencies share one rank.

Two dictionaries merge key by key. Two arrays are unioned, the higher source's
items first. Equal values are one value. When two sources set a key to
different values, the higher source wins and the build prints a note naming
both, except that two dependencies that disagree are an error: set the key in
the project's own fragment, which outranks every dependency. A fragment may be
a whole property list, a bare `<dict>`, or a run of `<key>`/value pairs.

`native/android/permissions.xml` elements merge the same way:
identical elements appear once, a `<uses-permission>` that `withPermission`
already declares is dropped, and two `<uses-permission>` / `<uses-feature>`
elements for the same name with different attributes are a conflict (the
project's own wins over a dependency; two dependencies are an error).

## Secure storage — `Native.secureSet`, `secureGet`, `secureRemove`

```elm
-- doc-example: skip  (fragment)
secureSet : String -> Secret -> Task Error ()
secureGet : String -> Task Error (Maybe Secret)
secureRemove : String -> Task Error ()
```

The device's secure store, for a refresh token, a device key or anything that
must not sit in `localStorage`:

| Shell | Store |
|---|---|
| iOS | the Keychain (generic password, service = the bundle id, `WhenUnlockedThisDeviceOnly`) |
| Android | AES-256-GCM under a key generated inside the Android Keystore; the ciphertext is in private SharedPreferences, bound to its key name |
| macOS desktop (`desktop:mac`) | the Keychain (service = the bundle id of the packaged `.app`) |
| a browser, a server, a CLI, Windows / Linux desktop | none: `Err Unavailable` |

There is no fallback. A value the app asked to keep secret is never written to
`localStorage` instead. The value is a `Sky.Core.Secret.Secret`, so it redacts
itself in every log and print path; wrap a string with `Secret.fromString` and
unwrap it with `Secret.reveal` where it is used. A key is 1 to 256 bytes with no
control characters (`Err InvalidInput` otherwise). On Android, an entry whose
Keystore key is gone (the app data was restored onto another device) cannot be
decrypted by anyone; it is removed and reads as `Nothing`.

On Sky.Spa these calls run in the wasm client, through the native shell: the
server has no user's Keychain. `Secret.fromString` and `Secret.reveal` are pure
and also run in the client; `Secret.fromEnv` reads the server's environment and
stays on the server. A `Secret` has no codec, so it never crosses the wire: to
send a stored token to the backend, reveal it in a client branch and pass the
string in the RPC `Msg`.

## Biometrics — `Native.authenticate`

```elm
-- doc-example: skip  (fragment)
authenticate : String -> Task Error Bool
```

The reason string appears in the prompt. The shell uses LocalAuthentication
(Face ID or Touch ID) on iOS and macOS and the system `BiometricPrompt` on
Android 9 and later. The four outcomes are distinct:

| Result | Meaning |
|---|---|
| `Ok True` | the user authenticated |
| `Ok False` | the biometric did not match and the system gave up (offer another way in) |
| `Err PermissionDenied` | the user or the system cancelled the prompt |
| `Err Unavailable` | no biometric hardware, none enrolled, locked out, or no native shell |

Declare `Bundle.withUsage Bundle.FaceId "…"`: iOS needs the purpose string and
Android the `USE_BIOMETRIC` permission. The build refuses the call without it.

## Release packaging — `sky package --release`

```bash
SKY_APP_URL=https://app.acme.com/ \
SKY_IOS_SIGN_IDENTITY="Apple Distribution: Acme Ltd (TEAMID)" \
SKY_IOS_PROVISIONING_PROFILE=~/profiles/vault_appstore.mobileprovision \
  sky package --release --target mobile:ios src/Main.sky
```

`sky package --release --target <t>` runs the ordinary `sky build --target <t>`
in release mode and copies the artefact to `sky-out/release/`:

| Target | Artefact | Signing (environment only) |
|---|---|---|
| `mobile:ios` | `<App>.ipa` built for devices (arm64, iphoneos SDK) | `SKY_IOS_SIGN_IDENTITY` + `SKY_IOS_PROVISIONING_PROFILE`. Without both: `<App>-unsigned.ipa`, with a note that it does not install on a device. |
| `mobile:android` | a release `.apk` signed with your upload key, plus an `.aab` when `bundletool` is on PATH | `SKY_ANDROID_KEYSTORE`, `SKY_ANDROID_KEYSTORE_PASSWORD`, `SKY_ANDROID_KEY_ALIAS`, optional `SKY_ANDROID_KEY_PASSWORD` (defaults to the keystore password). Required. |
| `desktop:mac` | `<App>.app` and `<App>.dmg` | `SKY_MACOS_SIGN_IDENTITY` (a Developer ID Application identity; hardened runtime). Without it the `.app` is signed ad hoc, with a note. |

The passwords are passed to `apksigner` and `jarsigner` by environment-variable
name, never on a command line. A release differs from a development build: the
iOS shell is built for devices, the web view is not inspectable (Safari's
Develop menu, `chrome://inspect`, the desktop window), the Android code is
compiled with `d8 --release`, and the Android signing key is yours, never the
debug key.

`sky package` refuses, before any build and with the fix named:

- no `--release`, or a target that is not a native shell (`web`, `tablet`,
  `terminal`), or a desktop target other than `desktop:mac`;
- a backend address that is the development default or a local host (set
  `App.withAppUrl "https://…"` or `SKY_APP_URL`), or plain `http` (serve the
  backend over https);
- a declared permission whose purpose string is the generic default;
- an Android release without its signing variables, or an iOS identity
  without a provisioning profile (or the reverse);
- entitlements the provisioning profile does not grant.

A notarised `.dmg` needs `xcrun notarytool submit --wait` and `xcrun stapler
staple` after packaging, with your Apple credentials. An App Store upload of the
`.ipa` uses Transporter or `xcrun altool`.

The release workflow's `gate-native` job builds the iOS shell for the
simulator, launches it on a booted simulator, and packages and verifies a
signed Android release on a macOS runner.

## Recipe — scan and show a QR code

A pairing flow: one device shows a QR code; the other scans it with the camera
and turns the text into a typed value. Generation is `Std.Qr`, which is pure
and runs anywhere. Scanning runs the camera in a widget island
([Widget islands](../skyui/overview.md#widget-islands--third-party-js-widgets)),
which reports each decoded code to `update` as a typed `Msg`.

```elm
module Main exposing (main, bundle)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Json.Decode as Decode
import Sky.Core.Json.Encode as Encode
import Sky.Core.String as String
import Std.App as App
import Std.Bundle as Bundle exposing (Bundle)
import Std.Cmd as Cmd
import Std.Html as Html
import Std.Html.Attributes as Attr
import Std.Qr as Qr
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


-- The camera purpose string: iOS shows it in the permission prompt, and the
-- Android build adds the CAMERA permission. `sky package --release` refuses a
-- camera app without it.
bundle : Bundle
bundle =
    Bundle.default
        |> Bundle.withId "com.example.pair"
        |> Bundle.withUsage Bundle.Camera "Scans the pairing code shown on your other device."


type Page
    = Home
    | NotFound


-- What a scanned code means, decoded from its text. Anything else is refused.
type alias Pairing =
    { device : String
    , key : String
    }


type alias Model =
    { scanning : Bool
    , paired : Maybe Pairing
    , problem : String
    }


type Msg
    = StartScan
    | StopScan
    | Scanned String
    | ScanFailed String


-- A pairing code is the text "sky-pair:<device>:<key>".
parsePairing : String -> Maybe Pairing
parsePairing text =
    case String.split ":" (String.trim text) of
        [ "sky-pair", device, key ] ->
            if String.isEmpty device || String.isEmpty key then
                Nothing

            else
                Just { device = device, key = key }

        _ ->
            Nothing


pairingText : Pairing -> String
pairingText p =
    "sky-pair:" ++ p.device ++ ":" ++ p.key


init : () -> ( Model, Cmd Msg )
init _ =
    ( { scanning = False, paired = Nothing, problem = "" }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        StartScan ->
            ( { model | scanning = True, problem = "" }, Cmd.none )

        StopScan ->
            ( { model | scanning = False }, Cmd.toIsland "pair-scanner" "stop" (Encode.object []) )

        Scanned text ->
            case parsePairing text of
                Just pairing ->
                    ( { model | scanning = False, paired = Just pairing }
                    , Cmd.toIsland "pair-scanner" "stop" (Encode.object [])
                    )

                Nothing ->
                    ( { model | problem = "That QR code is not a pairing code." }, Cmd.none )

        ScanFailed reason ->
            ( { model | scanning = False, problem = reason }, Cmd.none )


-- The scanner island: a JavaScript widget that runs the camera and reports
-- each decoded code as a "scanned" event with { text }.
scanner : Element Msg
scanner =
    Ui.island
        { name = "qr-scanner", id = "pair-scanner", props = Encode.object [] }
        [ Ui.width Ui.fill
        , Ui.height (Ui.px 320)
        , Ui.onIslandEvent "scanned" (Decode.field "text" Decode.string) Scanned
        , Ui.onIslandEvent "failed" (Decode.field "reason" Decode.string) ScanFailed
        ]


-- Show this device's own pairing code as a QR code (Std.Qr).
myCode : Element Msg
myCode =
    case Qr.encode Qr.Medium (pairingText { device = "tablet-1", key = "k3y" }) of
        Ok code ->
            Qr.view 4 code

        Err _ ->
            Ui.text "Could not draw the pairing code."


view : Model -> Element Msg
view model =
    Ui.column [ Ui.spacing 16, Ui.padding 24 ]
        [ myCode
        , case model.paired of
            Just p ->
                Ui.text ("Paired with " ++ p.device)

            Nothing ->
                if model.scanning then
                    Ui.column [ Ui.spacing 8 ]
                        [ scanner, Ui.button [] { onPress = Just StopScan, label = Ui.text "Cancel" } ]

                else
                    Ui.button [] { onPress = Just StartScan, label = Ui.text "Scan a pairing code" }
        , Ui.text model.problem
        ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


-- The widget file is a same-origin script, loaded after the Sky client.
head : Model -> List (Html.Html Msg)
head _ =
    [ Html.node "script" [ Attr.src "/static/qr-scanner.js", Attr.attribute "defer" "" ] [] ]


main : Task Error ()
main =
    App.run
        (App.app { init = init, update = update, view = view, subscriptions = subscriptions }
            |> App.withRoutes [ App.route "/" Home ]
            |> App.withNotFound NotFound
            |> App.withHead head
            |> App.withConfig (App.WebConfig { App.webDefaults | static = Just "static" })
            |> App.withAppUrl "https://pair.example.test/"
        )
```

The widget file, `static/qr-scanner.js`:

```js
// A camera QR scanner as a widget island. It reports "scanned" { text } for
// each code it decodes and "failed" { reason } when it cannot run.
window.Sky.island("qr-scanner", {
  async mount(el, props, send) {
    this.video = document.createElement("video");
    this.video.setAttribute("playsinline", "");
    this.video.muted = true;
    this.video.style.width = "100%";
    el.appendChild(this.video);
    if (!("BarcodeDetector" in window)) {
      send("failed", { reason: "This web view has no QR decoder." });
      return;
    }
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: "environment" },
      });
    } catch (e) {
      send("failed", { reason: "The camera is not available: " + e.message });
      return;
    }
    this.video.srcObject = this.stream;
    await this.video.play();
    const detector = new BarcodeDetector({ formats: ["qr_code"] });
    const tick = async () => {
      if (!this.stream) return;
      try {
        const codes = await detector.detect(this.video);
        if (codes.length > 0) send("scanned", { text: codes[0].rawValue });
      } catch (_) {}
      this.timer = setTimeout(tick, 250);
    };
    tick();
  },
  command(name) {
    if (name === "stop") this.destroy();
  },
  destroy() {
    clearTimeout(this.timer);
    if (this.stream) this.stream.getTracks().forEach((t) => t.stop());
    this.stream = null;
  },
});
```

What to know:

- **The camera permission.** The widget calls `getUserMedia`, which the build
  cannot see, so the recipe declares `Bundle.withUsage Bundle.Camera "…"`
  itself. The iOS shell grants the web view's media-capture request and iOS
  shows the purpose string; the Android shell declares `CAMERA`, requests it at
  start, and grants the web view's request.
- **The decoder.** `BarcodeDetector` exists in Chrome and the Android
  System WebView. WKWebView (iOS, macOS) has none: bundle a decoder, for
  example jsQR, into the same same-origin file and run it on a canvas frame of
  the video. The file must stay same-origin with no `eval` (strict CSP).
- **A native scanner.** To scan with AVFoundation or ML Kit instead, ship a
  `native/ios/QrScan.swift` / `native/android/QrScan.java` handler and call it
  with `Native.bridge "qrScan" "{}"`, then decode the JSON reply into the same
  `Scanned` Msg.
- **Typed decoding.** The island event is decoded with `Decode.field "text"
  Decode.string`; `parsePairing` turns the text into a `Pairing` or refuses it.
  A payload the decoder rejects is logged and dropped; it never reaches
  `update`.
