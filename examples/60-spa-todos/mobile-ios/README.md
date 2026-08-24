# Sky.Spa Todos — native iOS / iPadOS shell

The native iPhone/iPad shell for the [Sky.Spa Todos](..) client — a thin
`WKWebView` (SwiftUI) that loads the **same** client the web, macOS-desktop, and
Android builds use (wasm TEA loop + `Std.Ui` view + stateless backend). Client
and server stay **separate**; only the shell is native, exactly like
`Std.Webview.url` on macOS. One Sky.Spa app spans web, desktop, and mobile with
no per-platform app logic.

> macOS (MacBook) is already covered by the **desktop** target
> ([`../desktop`](../desktop)) — `Std.Webview.url` opens a native **WKWebView**
> window, which is macOS's system webview. This folder is only for **iPhone /
> iPad**, where the WebView is hosted by an iOS app target.

## Build (CLI, no .xcodeproj)

```bash
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer ./build-app.sh
# -> build/SkySpaTodos.app  (compiles SwiftUI + WKWebView for the iOS simulator)
```

`build-app.sh` drives `swiftc` against the iOS simulator SDK directly, so it
needs full **Xcode** (not just Command Line Tools). Prefer Xcode's GUI? create an
iOS App project and drop in `SkySpaTodos/*.swift` + `Info.plist`.

## Run in the simulator

```bash
# a simulator runtime must be installed once (large, ~8.5 GB):
xcodebuild -downloadPlatform iOS
xcrun simctl create SkyPhone "iPhone 15" com.apple.CoreSimulator.SimRuntimeType.iOS-<ver>
xcrun simctl boot SkyPhone && open -a Simulator
# start the host backend first: (cd .. && ./run.sh)
xcrun simctl install booted build/SkySpaTodos.app
xcrun simctl launch booted dev.sky.spatodos
```

## localhost / ATS notes

- **Simulator** → `http://localhost:8951/` works (shares the host network); the
  bundled `Info.plist` sets `NSAllowsLocalNetworking` for the dev cleartext URL.
- **Real device** → cannot reach the host's localhost; point
  `SkySpaTodosApp.appURL` at the deployed backend over **https** (then remove the
  `NSAppTransportSecurity` exception). iOS 14+ also shows a one-time **Local
  Network** permission prompt when a real device talks to a LAN backend.
