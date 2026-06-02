package rt_test

// Regression spec for the inline-console shim from the rt side.
//
// rt itself cannot import sky-app/rt/console_app at production-code
// level — that would form an import cycle (console_app imports rt).
// And it CAN'T import console_app from an internal `package rt` test
// either: Go's compiler bundles internal _test.go into the package,
// which keeps the cycle ("import cycle not allowed in test"). The
// fix is an EXTERNAL test package (`package rt_test`), which Go
// compiles as a separate unit that imports BOTH rt and console_app
// from the outside — no cycle.
//
// What's covered:
//   - Linking console_app DOES register the hook (via its init()).
//   - rt.InlineConsoleAvailable() reports true once the hook is
//     present.
//   - rt.MountInlineConsole(mux, "") serves the inline console at
//     <basePath>/_sky/console with the sentinel header.
//
// What's NOT covered here (deferred to PR 2):
//   - rt's maybeAutoMountConsole choosing between subprocess and
//     inline based on SKY_CONSOLE_MODE.
//   - The legacy subprocess fallback. PR 1 leaves the old path
//     untouched and additive — both paths coexist.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rt "sky-app/rt"
	// Blank-import the console_app subpackage so its init() registers
	// the inline-console hook into rt's shim. This is the same wire-
	// in PR 2's user-codegen path will perform (compiler injects
	// `import _ "sky-app/rt/console_app"` into generated main.go).
	_ "sky-app/rt/console_app"
)

func TestInlineConsole_Available_AfterImport(t *testing.T) {
	if !rt.InlineConsoleAvailable() {
		t.Fatal("InlineConsoleAvailable() returned false after blank-import of console_app — package init() did not fire")
	}
}

func TestInlineConsole_ServesViaShim(t *testing.T) {
	mux := http.NewServeMux()
	if err := rt.MountInlineConsole(mux, ""); err != nil {
		t.Fatalf("rt.MountInlineConsole: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_sky/console")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get("X-Sky-Console-Mode"), "inline"; got != want {
		t.Errorf("X-Sky-Console-Mode header: got %q, want %q", got, want)
	}

	bodyBytes := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			bodyBytes = append(bodyBytes, tmp[:n]...)
		}
		if err != nil {
			break
		}
		if len(bodyBytes) >= 64*1024 {
			break // safety cap
		}
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "<title>Sky Console</title>") {
		t.Errorf("body missing <title>Sky Console</title>; first 256 chars: %s", truncForLog(body, 256))
	}
}

// truncForLog keeps test failure messages from spraying 10kb of HTML.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
