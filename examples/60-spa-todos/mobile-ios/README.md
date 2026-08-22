# Sky.Spa Todos — native iOS / iPadOS shell

The native iPhone/iPad shell for the [Sky.Spa Todos](..) client — a thin
`WKWebView` that loads the **same** client the web, macOS-desktop, and Android
builds use (wasm TEA loop + `Std.Ui` view + stateless backend). Client and
server stay **separate**; only the shell is native, exactly like
`Std.Webview.url` on macOS. One Sky.Spa app spans web, desktop, and mobile with
no per-platform app logic.

> macOS (MacBook) is already covered by the **desktop** target
> ([`../desktop`](../desktop)) — `Std.Webview.url` opens a native **WKWebView**
> window, which is macOS's system webview. This folder is only for **iPhone /
> iPad**, where the WebView must be hosted by an iOS app target.

## Build + run (Xcode — needed for iOS; not buildable from the CLI here)

1. Start the backend on the host: `cd .. && ./run.sh` (serves the client + API
   on `:8951`).
2. In Xcode: **File ▸ New ▸ Project ▸ iOS App** (SwiftUI, name `SkySpaTodos`).
3. Replace the generated `…App.swift` and add `WebView.swift` with the two files
   in `SkySpaTodos/`.
4. Run on the **iOS Simulator** — the simulator shares the host network, so
   `http://localhost:8951/` loads the dev backend directly.

## localhost / ATS notes

- **Simulator** → `http://localhost:8951/` works (shares the host network).
- **Real device** → it cannot reach the host's localhost; point
  `SkySpaTodosApp.appURL` at the deployed backend. Prefer **https** so no App
  Transport Security exception is needed.
- To allow **cleartext http** for local testing on a device, add to `Info.plist`:
  `NSAppTransportSecurity → NSAllowsLocalNetworking = YES` (local networking) or
  a scoped `NSExceptionDomains` entry. Production should be https, so this is
  dev-only.
