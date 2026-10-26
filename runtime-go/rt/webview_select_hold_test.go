//go:build cgo && darwin

package rt

import "testing"

// The Sky.Webview shim applies the same server-diffed patches as the Sky.Live
// client, and had the same open-select skip plus a silent drop of a missing
// target (live_js_select_hold_test.go).
func TestWebviewJS_OpenSelectHoldsPatches(t *testing.T) {
	runSelectHoldHarness(t, webviewSharedJS, "webview")
}
