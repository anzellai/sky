import SwiftUI

// The native iOS/iPadOS shell for the Sky.Spa Todos client.
//
// A thin WKWebView that loads the SAME client the web / desktop / Android builds
// use: the Sky TEA loop (Model / Msg / update / view) compiled to wasm, served
// over HTTP by its own stateless backend. Client and server stay SEPARATE; only
// the shell is native — exactly like `Std.Webview.url` (macOS/desktop) and the
// Android WebView shell. Same view, same Model/Msg, same server.
//
// The iOS SIMULATOR shares the host's network, so `http://localhost:8951/` (the
// dev backend from `../run.sh`) works as-is. A REAL device cannot see the host's
// localhost — point APP_URL at the deployed backend over https (and prefer https
// so no ATS exception is needed; see Info.plist note in README.md).
@main
struct SkySpaTodosApp: App {
    // Dev: the local backend. Prod: your deployed https URL.
    static let appURL = URL(string: "http://localhost:8951/")!

    var body: some Scene {
        WindowGroup {
            WebView(url: Self.appURL)
                .ignoresSafeArea()
        }
    }
}
