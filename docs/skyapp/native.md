# Native shells: permissions, entitlements, secure storage, release

The `mobile:ios`, `mobile:android`, `tablet:ipad`, `tablet:android` and
`desktop:mac` targets wrap the Sky.Spa client in a native web view (see
[Client targets](overview.md#client-targets--same-source-no-stdspa-entry)). A
tablet build is the phone's shell: the iOS app declares both device families,
and the Android app has no form-factor split. Before v0.27.0 `tablet:ipad` and
`tablet:android` built the responsive web bundle, with no native capabilities.
This page covers what a native app needs beyond the web build: the purpose
strings the operating system shows in its permission prompts, the Apple
entitlements, the device's secure store, the biometric prompt, and the store
artefact that `sky package --release` makes (and its upload to TestFlight),
and the camera code scanner. The
last section is a recipe that scans and generates QR codes.

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
| `withName` | CFBundleDisplayName, CFBundleName, `android:label`, window title; the product name | the project name |
| `withId` | CFBundleIdentifier, the Android package | `sky.spa.<name>` (with a note) |
| `withVersion` | CFBundleShortVersionString, `android:versionName` | `1.0` |
| `withBuild` | CFBundleVersion, `android:versionCode` | `1` |
| `withIcon` | the app icon set (rendered with `sips`) | the platform icon |

A store refuses a second upload with the same build number, so raise
`withBuild` for each release. Before v0.27.0 the build number was always 1.

The product name (the executable, the `.app`, the `.apk`) is made from the whole
display name, as Xcode makes a module name: each character that is not an ASCII
letter, digit or `_` becomes `_`, and a leading digit gets a `_` prefix.
`withName "Sky Probe"` builds `Sky_Probe.app` with CFBundleName `Sky Probe`.
Before v0.27.0 the product name was the last part of the bundle id (`Probe`
for `com.example.probe`), and CFBundleName was that part too.

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
| `Native.notify` | `Notifications` (Android only: iOS asks with no key) |
| `Native.scanCode` | `Camera` |

```
sky build --target mobile:ios: missing permission purpose string:
  - the app calls `Native.authenticate` at src/Main.sky:42, which needs
    NSFaceIDUsageDescription on iOS. Add `|> Bundle.withUsage Bundle.FaceId
    "<why the app needs it>"` to the `bundle` binding at src/Main.sky:14.
```

The error names the user's own files: the line that calls the capability and
the `bundle` binding to fix (or, when there is none, how to add one). An
`App.app` entry is checked before the build derives its client entry, so the
error never points at the derived files.

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
`.entitlements` file (iOS and the macOS `.app`). Android has no entitlements;
an associated domain maps onto its Android equivalent (see "Links into the
app" below):

| Constructor | Entitlement key |
|---|---|
| `KeychainAccessGroup "TEAMID.com.acme.shared"` | `keychain-access-groups` (the team prefix is part of the value) |
| `AppGroup "group.com.acme.app"` | `com.apple.security.application-groups` |
| `AssociatedDomain "applinks:acme.com"` | `com.apple.developer.associated-domains` (`webcredentials:` for password autofill) |
| `PushNotifications PushProduction` | `aps-environment` (`PushDevelopment` for a development build) |
| `ICloudContainer "iCloud.com.acme.app"` | `com.apple.developer.icloud-container-identifiers` + `icloud-services: CloudKit` |

The build checks the shape of each value (an app group starts with `group.`,
an associated domain is `<service>:<host>` with an optional `:<port>` and
`?mode=developer`, an iCloud container starts with `iCloud.`). A signed release also checks that the provisioning profile grants
every entitlement the app asks for, and names the ones it does not.

**The simulator build.** The iOS simulator build carries its entitlements the
way Xcode's does: they are written into the executable's `__TEXT,__entitlements`
and `__TEXT,__ents_der` sections, with the application identifier
(`<team>.<bundle id>`) and, when the app declares none, a keychain access group
for the app's own id. The signature is ad hoc and carries no entitlements. The
simulator reads the sections, so the Keychain works with no declared
entitlement, and a declared one (a keychain access group, an associated domain,
push, iCloud) never stops the launch. The team prefix is the one the app's
first keychain access group carries, else `SKYSIMTEAM`.

Before v0.27.0 the simulator build was signed ad hoc WITH the entitlements. The
kernel does not launch an ad hoc signed binary that asks for a restricted
entitlement (`simctl launch` reported "No such process"), and without one every
Keychain call failed with -34018 (errSecMissingEntitlement).

**The macOS app.** Signed ad hoc (no `SKY_MACOS_SIGN_IDENTITY`), the `.app`
keeps only the entitlements an ad hoc signature may carry (an app group) and
the build names the ones it leaves out; the app runs on this Mac and reaches
the login Keychain without an access group. Signed with a Developer ID
identity, a restricted entitlement needs `SKY_MACOS_PROVISIONING_PROFILE`: the
profile is embedded in the `.app` and must grant every entitlement the app asks
for.

## Links into the app — `AssociatedDomain "applinks:…"`

`Bundle.withEntitlement (Bundle.AssociatedDomain "applinks:example.com")`
lets a link to `https://example.com/…` open the app instead of the browser,
on the link's page.

| Platform | What the build does |
|---|---|
| iOS / iPadOS | The associated-domains entitlement. The shell takes the universal link (`onOpenURL`, `NSUserActivityTypeBrowsingWeb`) and opens the link's path, query and fragment on the backend's own address, where the app's router shows the page. |
| Android | An App Links intent filter on the activity: `android:autoVerify="true"`, `VIEW`, `DEFAULT` + `BROWSABLE`, `https` and the host (and port). The activity is `singleTask`. `onCreate` opens a link that starts the app; `onNewIntent` navigates the running app in place (`history.pushState` + `popstate`, as Back does), so its state and an open scanner stay. |
| macOS | The entitlement, and the hosts in the app's `Info.plist` (`SkyLinkHosts`). The desktop window takes a universal link (`application:continueUserActivity:`, `NSUserActivityTypeBrowsingWeb`) and a link sent to the app (the GetURL event, `open -a <app> <url>`). A link that launches the app is its first page; a link to the running app navigates in place (`history.pushState` + `popstate`). Only `sky package --release --target desktop:mac` makes an `.app`, so only the packaged app takes links. |

Only a declared host is routed. A `*.example.com` domain matches its
subdomains. The link's host is not the backend's: the app loads the link's
path from the backend it always talks to (`App.withAppUrl`), so an app whose
backend is `https://app.example.com/` and whose domain is `example.com` shows
`https://app.example.com/orders/7` for `https://example.com/orders/7`.

Other services on Android:

- `webcredentials:<host>` (password autofill) maps onto Android's shared
  sign-in: the app gets `asset_statements` (a `<meta-data>` entry and a
  string resource that points at the site's `assetlinks.json`), and the
  site's file gets the `delegate_permission/common.get_login_creds`
  relation.
- `activitycontinuation:` and `appclips:` have no Android equivalent. The
  Android build leaves them out and prints a note naming each one.

**The site must vouch for the app.** Android verifies an App Link against
`https://<host>/.well-known/assetlinks.json`; iOS and macOS against the site's
`apple-app-site-association` file. On macOS the app also needs a signature
that carries the associated-domains entitlement (`SKY_MACOS_SIGN_IDENTITY`
and a provisioning profile that grants it): an ad hoc signed app leaves the
entitlement out, and macOS then hands it no universal link (the build says
so). A link sent to the app with `open -a` still opens its page. The Android build writes the exact
`assetlinks.json` for the app, with the SHA-256 digest of the certificate that
signed the APK, and prints where it is and where to serve it:

- `sky build --target mobile:android` → `…/android/build/assetlinks.json`,
  signed with the debug key (for testing on a device or an emulator).
- `sky package --release --target mobile:android` →
  `sky-out/release/assetlinks.json`, signed with the upload key.

```json
[
  {
    "relation": ["delegate_permission/common.handle_all_urls"],
    "target": {
      "namespace": "android_app",
      "package_name": "com.example.probe",
      "sha256_cert_fingerprints": ["0A:1B:…:F9"]
    }
  }
]
```

Serve it on every declared host, as `application/json`, with no redirect. A
Sky backend on the same host can serve it from an `App.api
"/.well-known/assetlinks.json"` handler, or a static host from its web root.
Google Play re-signs an app with its own key when Play App Signing is on: then
add the app signing key's SHA-256 (Play Console → App integrity) to
`sha256_cert_fingerprints` beside the upload key's.

To test on an emulator before the site serves the file, approve the domain
for the app (`adb shell pm set-app-links-user-selection --user cur --package
<id> true <host>`) and send the link (`adb shell am start -a
android.intent.action.VIEW -c android.intent.category.BROWSABLE -d
"https://<host>/some/path" <id>`). The Android emulator release gate does
this: a link that starts the app, and one sent to the running app, each open
their page.

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
`localStorage` instead. A WebCrypto store in IndexedDB was considered for the
plain browser and not built: script in the page could still read every value,
and the key sits on disk beside the ciphertext (the threat analysis is in
`docs/skyspa/client-crypto.md`, "Why a plain browser has no secure store"). The value is a `Sky.Core.Secret.Secret`, so it redacts
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

## Notifications — `Native.notify`

```elm
-- doc-example: skip  (fragment)
notify : String -> String -> Task Error ()
```

The shell posts a local notification with the title and body:
`UNUserNotificationCenter` on iOS, `NotificationManager` on Android. The first
call asks the user for permission, and the Task waits for the answer:

| Result | Meaning |
|---|---|
| `Ok ()` | the notification is posted |
| `Err PermissionDenied` | the user refused, or turned the app's notifications off in Settings |
| `Err Io` | the system refused to post it |

On Android 13 and later POST_NOTIFICATIONS is a run-time permission, which
the shell asks for when the app starts. A notify made while that prompt shows
waits for the answer through the same permission broker as `Native.scanCode`,
so a notification at first launch is posted after Allow, not dropped. A
refusal holds for the rest of the run: a `Native.notify` (or `scanCode`,
camera or location request) made after the user pressed "Don't allow" is
`Err PermissionDenied` at once, not a second prompt. The next start asks
again, and a grant in Settings counts at once.
Declare `Bundle.withPermission Bundle.Notifications`: the Android build
refuses the call without it. iOS needs no declaration. In a browser and in
the desktop window, `Native.notify` uses the Web Notification API.

## Scanning codes — `Native.scanCode`

```elm
-- doc-example: skip  (fragment)
type CodeFormat = Qr | Aztec | DataMatrix | Pdf417 | Ean8 | Ean13 | UpcE | Code39 | Code93 | Code128 | Itf | Codabar
type alias ScanOptions = { formats : List CodeFormat, prompt : String }
type alias ScannedCode = { format : CodeFormat, text : String }

scanCode : ScanOptions -> Task Error (Maybe ScannedCode)
```

The native shell shows a full-screen camera scanner with the prompt and a
Cancel button, and returns the first code of one of the asked formats (an empty
list asks for all of them).

| Shell | Scanner |
|---|---|
| iOS and iPadOS | VisionKit's `DataScannerViewController` (iOS 16 and later; the shell targets iOS 17) |
| Android | the camera (Camera2) with the ZXing decoder |
| a browser, the desktop window, a server | none: `Err Unavailable` (use a widget island, see the recipe) |

| Result | Meaning |
|---|---|
| `Ok (Just code)` | the camera read a code |
| `Ok Nothing` | the user closed the scanner (Cancel, or Back on Android) |
| `Err PermissionDenied` | the user refused the camera |
| `Err Unavailable` | no camera scanner: no camera, the iOS simulator, a browser |

Declare `Bundle.withUsage Bundle.Camera "…"`: iOS shows the text when it asks
for the camera, and Android asks for CAMERA. The build refuses the call without
it. On Android a scan started while the camera prompt shows (the app asks for
its declared permissions when it starts, so a scan in `init` does) waits for
the user's answer: Allow opens the scanner, and only Don't allow is `Err
PermissionDenied`. The same rule holds for the page's camera and microphone
(`getUserMedia`) and location requests. Android shows one permission prompt at
a time, and the shell asks for a permission only when no prompt shows. A UPC-A code is reported as `Ean13` (its 13-digit form, with a leading 0) on
both platforms, as Apple's Vision reports it.

**Android and ZXing.** The Android scanner decodes with
[ZXing](https://github.com/zxing/zxing) core 3.5.3 (Apache License 2.0). It is
a plain Java jar, so the shell's SDK-tools build (`javac` + `d8`, no Gradle)
compiles it in as it is. An app that calls `Native.scanCode` gets it; the build
fetches it once from Maven Central, checks its pinned SHA-256
(`8d8064c1…c109fd82`) and caches it in `~/.cache/sky/android/`. Without network,
put the jar there yourself. An app that does not call `scanCode` builds without
it. An app that ships ZXing states the Apache License 2.0 (for example in its
licences screen).

**Testing on a device.** VisionKit's scanner never runs on the iOS simulator
(`Err Unavailable` there), so test the iOS scanner on a device: `sky package
--release --target mobile:ios` with your signing identity, then install the
`.ipa` with Xcode's Devices window or `xcrun devicectl device install app`. The
Android emulator has an emulated back camera, so the scanner opens there.

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
| `desktop:mac` | `<App>.app` and `<App>.dmg` | `SKY_MACOS_SIGN_IDENTITY` (a Developer ID Application identity; hardened runtime), plus `SKY_MACOS_PROVISIONING_PROFILE` when the app asks for a restricted entitlement. Without the identity the `.app` is signed ad hoc, with a note, and leaves restricted entitlements out. |

The passwords are passed to `apksigner` and `jarsigner` by environment-variable
name, never on a command line. A release differs from a development build: the
iOS shell is built for devices, the web view is not inspectable (Safari's
Develop menu, `chrome://inspect`, the desktop window), the Android code is
compiled with `d8 --release`, and the Android signing key is yours, never the
debug key.

`sky package` refuses, before any build and with the fix named:

- no `--release`, or a target that is not a native shell (`web`, bare
  `tablet`, `tablet:windows`, `terminal`), or a desktop target other than
  `desktop:mac`;
- a backend address that is the development default or a local host (set
  `App.withAppUrl "https://…"` or `SKY_APP_URL`), or plain `http` (serve the
  backend over https);
- a declared permission whose purpose string is the generic default;
- an Android release without its signing variables, or an iOS identity
  without a provisioning profile (or the reverse);
- entitlements the provisioning profile does not grant, and a macOS Developer
  ID build that asks for a restricted entitlement without
  `SKY_MACOS_PROVISIONING_PROFILE`.

A notarised `.dmg` needs `xcrun notarytool submit --wait` and `xcrun stapler
staple` after packaging, with your Apple credentials. An iOS or iPadOS build
uploads to TestFlight with `--upload testflight` (next section).

## Upload to TestFlight — `sky package --upload testflight`

`sky package --release --target mobile:ios --upload testflight` (or
`--target tablet:ipad`) builds the signed `.ipa`, then sends it to App Store
Connect with Apple's own tool, `xcrun altool`: it validates the build first and
uploads it only when validation passes. The build then appears in TestFlight
when Apple has processed it. The upload runs on macOS with Xcode installed (the
Command Line Tools alone do not have `altool`).

**One-time setup in App Store Connect.**

1. Register the bundle id in the Apple Developer portal (Certificates,
   Identifiers & Profiles → Identifiers → +), for example `com.acme.vault`.
2. Create the app record in App Store Connect (Apps → + → New App) and choose
   that bundle id.
3. Create an "Apple Distribution" certificate and an **App Store Connect**
   distribution provisioning profile for the bundle id (Profiles → + →
   Distribution → App Store Connect). A development or ad hoc profile does not
   work for TestFlight.
4. Create an API key: Users and Access → Integrations → App Store Connect API
   → Team Keys → +, with the **App Manager** role. Note the key id (10
   characters) and the issuer id (a UUID at the top of the page). Download
   `AuthKey_<KEY_ID>.p8`. Apple lets you download it once. Keep it out of the
   repository.

**The app.** The `bundle` binding sets the id and a build number:

```elm
-- doc-example: skip  (fragment — the rest of the app is elided)
bundle : Bundle
bundle =
    Bundle.default
        |> Bundle.withId "com.acme.vault"
        |> Bundle.withVersion "1.0"
        |> Bundle.withBuild 7
```

App Store Connect refuses a build number it already has for the version, so
raise `Bundle.withBuild` for every upload.

**The command.**

```bash
SKY_APP_URL=https://app.acme.com/ \
SKY_IOS_SIGN_IDENTITY="Apple Distribution: Acme Ltd (TEAMID)" \
SKY_IOS_PROVISIONING_PROFILE=~/profiles/vault_appstore.mobileprovision \
SKY_ASC_KEY_ID=ABC123DEF4 \
SKY_ASC_ISSUER_ID=57246542-96fe-1a63-e053-0824d011072a \
SKY_ASC_KEY_PATH=~/keys/AuthKey_ABC123DEF4.p8 \
  sky package --release --target mobile:ios --upload testflight src/Main.sky
```

To upload an `.ipa` packaged earlier (for example after a network failure),
add `--ipa sky-out/release/Vault.ipa`. Sky then builds nothing and uploads that
file. The signing variables are not needed, because the file is already signed.

| Variable | Value |
|---|---|
| `SKY_ASC_KEY_ID` | The API key id, e.g. `ABC123DEF4`. |
| `SKY_ASC_ISSUER_ID` | The issuer id (a UUID). |
| `SKY_ASC_KEY_PATH` | The path to the downloaded `AuthKey_<KEY_ID>.p8`. |

**How the key is passed.** The key id and the issuer id are identifiers, not
secrets, and go to `altool` as `--api-key` and `--api-issuer`. The key file
never appears on a command line, so `ps` does not show it: `altool` reads
`AuthKey_<KEY_ID>.p8` from the directory in `API_PRIVATE_KEYS_DIR`, which is
one of the places `xcrun altool --help` documents. When your file already has
that name Sky points at its directory. Otherwise Sky copies it into a private
temporary directory (mode 0700, the file 0600) and removes the copy when the
upload ends. Sky never prints the key.

**Refused before any build or network call**, each with the fix:

- `--upload` with a destination other than `testflight`, or with a target
  other than `mobile:ios` / `tablet:ipad`; `--ipa` without `--upload`;
- no `Bundle.withId` (the default `sky.spa.…` id is for development builds),
  or no `Bundle.withBuild`;
- a missing `SKY_ASC_*` variable, or a key path that is not a `.p8` key file;
- a build that would not be signed for distribution: the signing variables
  unset (the build would be the unsigned `-unsigned.ipa`), an "Apple
  Development" identity, or a profile that lists devices (development or ad
  hoc), is an enterprise profile, or allows a debugger (`get-task-allow`);
- with `--ipa`: an `-unsigned.ipa`, an `.ipa` with no code signature or no
  embedded profile, or an embedded profile that is not an App Store profile;
- no `xcrun altool` (install Xcode and run `sudo xcode-select -s
  /Applications/Xcode.app`, or upload with Apple's Transporter app).

**A successful upload prints** the file, the bundle id and the build number,
Apple's success message and the delivery UUID:

```text
Uploaded sky-out/release/Vault.ipa to App Store Connect (bundle id com.acme.vault, build 7).
  Apple: No errors uploading 'sky-out/release/Vault.ipa'
  Delivery UUID: 8d1c2f3a-5b6e-4c7d-9e0f-112233445566
```

Apple then processes the build. It appears under TestFlight in App Store
Connect when processing finishes, usually within 30 minutes, and Apple emails
the account holder. Add testers there.

**When Apple refuses.** Sky prints each Apple error as Apple wrote it
(`Apple: …`), then the fix for the errors a Sky app can meet (`fix: …`), and
exits 1. A failed validation uploads nothing.

| Apple says | Fix |
|---|---|
| "The bundle version must be higher than the previously uploaded version", "Redundant Binary Upload" | Raise `Bundle.withBuild` and package again. |
| ITMS-90062, `CFBundleShortVersionString` must be higher | The version is closed for new builds: raise `Bundle.withVersion`. |
| "No suitable application records were found" | Create the app record for the bundle id in App Store Connect (setup step 2). |
| "Unable to authenticate", `NOT_AUTHORIZED` | Check `SKY_ASC_KEY_ID` and `SKY_ASC_ISSUER_ID`, that the key is not revoked, and that it has the App Manager role. |
| ITMS-90161 "Invalid Provisioning Profile" | Sign with an App Store distribution profile for this bundle id. |
| ITMS-90683 missing purpose string | Declare the permission with `Bundle.withUsage`. |
| ITMS-90022 / ITMS-90704 missing icon | Set `Bundle.withIcon` to a 1024×1024 PNG without transparency. |

**Testing.** The flow tests prove the argument construction, every refusal, the
success path and Apple's error reports with a fake `xcrun`, through
`SKY_XCRUN`. That variable is **for tests only**: it names an executable run in
place of `xcrun`. Leave it unset. No test in this repository reaches Apple: the
first real upload is yours to run, with your own App Store Connect key.

The release workflow's `gate-native` job builds a probe app for the iOS
simulator (`mobile:ios`, and `tablet:ipad` with restricted entitlements
declared), launches each on a booted simulator, and reads the app's own results
back through its backend: the Keychain round-trips a value, and
`Native.scanCode` and `Native.authenticate` are `Err Unavailable` (the simulator
has no scanner and no enrolled biometric). It also packages and verifies a
signed Android release. Its `gate-native-android` job does the same on an
Android emulator (a Linux runner with KVM): `mobile:android` and
`tablet:android` launch, the Keystore round-trips a value, the scanner opens
and Back closes it (`Ok Nothing`), and `Native.authenticate` with no enrolled
finger is `Err Unavailable`.

## Recipe — scan and show a QR code

A pairing flow: one device shows a QR code; the other scans it with the camera
and turns the text into a typed value. Generation is `Std.Qr`, which is pure
and runs anywhere. Scanning asks the native shell first (`Native.scanCode`: the
iOS and Android apps). Where there is no native scanner (`Err Unavailable`: a
browser, the desktop window) it runs the camera in a widget island
([Widget islands](../skyui/overview.md#widget-islands--third-party-js-widgets)),
which reports each decoded code to `update` as a typed `Msg`. Both paths end in
the same `accept` step.

```elm
module Main exposing (main, bundle)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error as Error exposing (Error(..), ErrorKind(..))
import Sky.Core.Json.Decode as Decode
import Sky.Core.Json.Encode as Encode
import Sky.Core.String as String
import Std.App as App
import Std.Bundle as Bundle exposing (Bundle)
import Std.Cmd as Cmd
import Std.Html as Html
import Std.Html.Attributes as Attr
import Std.Native as Native
import Std.Qr as Qr
import Std.Sub as Sub
import Std.Ui as Ui exposing (Element)


-- The camera purpose string: iOS shows it in the permission prompt, and the
-- Android build adds the CAMERA permission. The native build refuses
-- `Native.scanCode` without it.
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
    | NativeScanned (Result Error (Maybe Native.ScannedCode))
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


-- A scanned text, from either scanner: a pairing, or a problem to show.
accept : String -> Model -> Model
accept text model =
    case parsePairing text of
        Just pairing ->
            { model | scanning = False, paired = Just pairing }

        Nothing ->
            { model | problem = "That QR code is not a pairing code." }


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        -- The native scanner first (the iOS and Android apps).
        StartScan ->
            ( { model | problem = "" }
            , Cmd.perform
                (Native.scanCode { formats = [ Native.Qr ], prompt = "Scan the pairing code on your other device" })
                NativeScanned
            )

        NativeScanned (Ok (Just code)) ->
            ( accept code.text model, Cmd.none )

        NativeScanned (Ok Nothing) ->
            ( model, Cmd.none )

        -- No native scanner here (a browser): run the widget island instead.
        NativeScanned (Err (Error Unavailable _)) ->
            ( { model | scanning = True }, Cmd.none )

        NativeScanned (Err e) ->
            ( { model | problem = Error.toString e }, Cmd.none )

        StopScan ->
            ( { model | scanning = False }, Cmd.toIsland "pair-scanner" "stop" (Encode.object []) )

        Scanned text ->
            ( accept text model, Cmd.toIsland "pair-scanner" "stop" (Encode.object []) )

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

- **Native first.** `Native.scanCode` is the scanner in the iOS and Android
  apps: VisionKit on iOS, the camera and ZXing on Android. It is `Err
  Unavailable` where no native scanner exists, and the recipe then shows the
  island. On the iOS simulator it is also `Err Unavailable`, so the island runs
  there (WKWebView has no `BarcodeDetector`, so it reports "failed").
- **The camera permission.** `Native.scanCode` needs `Bundle.withUsage
  Bundle.Camera "…"`, and the build refuses the call without it. The island's
  `getUserMedia` needs it too, but the build cannot see that call. The iOS shell grants the web view's media-capture request and iOS
  shows the purpose string; the Android shell declares `CAMERA`, requests it at
  start, and grants the web view's request.
- **The decoder.** `BarcodeDetector` exists in Chrome and the Android
  System WebView. WKWebView (iOS, macOS) has none: bundle a decoder, for
  example jsQR, into the same same-origin file and run it on a canvas frame of
  the video. The file must stay same-origin with no `eval` (strict CSP).
- **Another native scanner.** To scan with a different library, ship a
  `native/ios/QrScan.swift` / `native/android/QrScan.java` handler and call it
  with `Native.bridge "qrScan" "{}"`, then decode the JSON reply into the same
  `Scanned` Msg.
- **Typed decoding.** The island event is decoded with `Decode.field "text"
  Decode.string`; `parsePairing` turns the text into a `Pairing` or refuses it.
  A payload the decoder rejects is logged and dropped; it never reaches
  `update`.
