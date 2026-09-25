//go:build cgo && darwin

package rt

import (
	"strings"
	"testing"
)

// The Sky.Webview shim is injected natively, not as a page script, but it must
// obey the same rule as every other runtime script: nothing evaluates a string.
// It lives here because webview.go builds only with cgo on darwin.
func TestWebviewSharedJSHasNoEval(t *testing.T) {
	for _, f := range []string{"new Function", "eval(", "data-sky-eval", `setTimeout("`, `setInterval("`, "document.write("} {
		if strings.Contains(webviewSharedJS, f) {
			t.Errorf("webviewSharedJS contains %q", f)
		}
	}
}
