package rt

import "testing"

// webviewURLRun's argument validation runs BEFORE webview.New (which needs a
// GUI backend), so these paths are headless-testable. The happy path (opening a
// real WKWebView window and navigating) is a GUI concern verified by running
// the desktop shell; here we lock the input contract.

func TestWebviewURLRequiresNonEmptyURL(t *testing.T) {
	cfg := map[string]any{"Title": "x", "Size": T2[any, any]{V0: 480, V1: 760}}
	res := webviewURLRun("", cfg)
	if !isErr(res) {
		t.Fatalf("empty url must be Err, got %#v", res)
	}
	res2 := webviewURLRun("   ", cfg)
	if !isErr(res2) {
		t.Fatalf("blank url must be Err, got %#v", res2)
	}
}

func TestWebviewURLRequiresWindowCfg(t *testing.T) {
	res := webviewURLRun("http://127.0.0.1:8951/", nil)
	if !isErr(res) {
		t.Fatalf("nil window cfg must be Err, got %#v", res)
	}
}
