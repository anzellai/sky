// Sky.Webview spike — the webview_go side.
//
// Tiny program that opens a webview pointed at the Sky.Http.Server
// running on http://localhost:8765 (the Sky side of this same
// example dir). Run order:
//
//	# terminal 1
//	cd examples/29-webview-threejs-spike
//	sky run src/Main.sky          # starts the server on :8765
//
//	# terminal 2
//	cd examples/29-webview-threejs-spike/webview_shim
//	go mod download
//	go run main.go                # opens the native webview
//
// What we're validating:
//
//   - webview_go can spawn a window on this OS.
//   - The system webview (WKWebView / WebView2 / WebKitGTK) loads
//     Three.js + WebGL2 + runs the animation loop at ~60fps.
//   - The HUD shows the GL renderer string + WebGL version — read
//     that to confirm hardware acceleration vs software rasterisation.
//   - Click handlers (pause rotation, toggle wireframe) work — proves
//     JS event dispatch is live inside the webview.
//
// Expected results for a green spike (per docs/skywebview/PLAN.md
// risks #2 and #3):
//
//   - macOS: WebGL 2, "Apple M*" or "AMD Radeon Pro …" renderer, 60fps.
//   - Windows 11: WebGL 2, "ANGLE (… Direct3D11 …)" renderer, 60fps.
//   - Ubuntu 22.04+: WebGL 2, "Mesa …" renderer (hw) OR "Mesa Off-screen"
//     (sw — bad; install libwebkit2gtk + DRI drivers).
//
// Resize the window to confirm the canvas reflows. Close the window
// to exit the program cleanly. No tray, no always-on-top, no
// transparency — those are v0.2 of the real Sky.Webview backend,
// out of scope for this spike.

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/webview/webview_go"
)

func main() {
	url := flag.String("url", "http://localhost:8765", "URL to open in the webview")
	debug := flag.Bool("debug", true, "enable webview devtools (right-click to inspect)")
	width := flag.Int("w", 1024, "initial window width (logical px)")
	height := flag.Int("h", 720, "initial window height (logical px)")
	title := flag.String("title", "Sky.Webview spike — three.js", "window title")
	flag.Parse()

	w := webview.New(*debug)
	if w == nil {
		fmt.Fprintln(os.Stderr, "webview.New returned nil — your OS lacks the system webview backend.")
		fmt.Fprintln(os.Stderr, "  macOS: builtin WKWebView — should always work")
		fmt.Fprintln(os.Stderr, "  Windows: install the Edge WebView2 runtime")
		fmt.Fprintln(os.Stderr, "  Linux: install libwebkit2gtk-4.0-37 (Ubuntu) or webkit2gtk4.0 (Fedora)")
		os.Exit(1)
	}
	defer w.Destroy()

	w.SetTitle(*title)
	w.SetSize(*width, *height, webview.HintNone)
	w.Navigate(*url)

	fmt.Fprintf(os.Stderr, "[sky-webview-spike] webview opened on %s\n", *url)
	fmt.Fprintln(os.Stderr, "                    HUD top-left shows GL renderer + WebGL version.")
	fmt.Fprintln(os.Stderr, "                    close the window to exit.")

	w.Run()
}
