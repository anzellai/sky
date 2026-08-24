import SwiftUI
import WebKit

/// SwiftUI wrapper over WKWebView. WKWebView runs JavaScript and WebAssembly by
/// default, so the Sky.Spa wasm client boots with no extra configuration.
struct WebView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.load(URLRequest(url: url))
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}
}
