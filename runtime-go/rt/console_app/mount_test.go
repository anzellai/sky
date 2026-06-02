package console_app

// PR 1 regression spec for the inline-mount path.
//
// Two layers under test:
//   1. console_app.MountInlineConsole — registers a handler, returns
//      nil error, the registered path serves HTTP 200 with HTML +
//      a sentinel header that PR 2/3 / external monitoring can grep
//      for ("X-Sky-Console-Mode: inline").
//   2. The init() in register.go pushed a non-nil hook into rt's
//      RegisterInlineConsoleHook — calling rt.MountInlineConsole
//      with the same mux yields the SAME registration as the
//      direct call.
//
// What's intentionally NOT tested here (deferred to PR 2/3):
//   - SSE event streaming
//   - POST /_sky/event dispatch
//   - production auth gate (consoleAccessAllowed)
//   - SKY_CONSOLE_MODE selection logic in maybeAutoMountConsole
//   - the body matching the OUTPUT of the bundled Sky source one-
//     for-one (regen drift detection is the right tool for that)

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rt "sky-app/rt"
)

// TestMountInlineConsole_Direct verifies the package-local API: a
// fresh mux, MountInlineConsole called against it, then a GET on
// /_sky/console returns 200 + the inline-mode sentinel header +
// a non-empty HTML body.
func TestMountInlineConsole_Direct(t *testing.T) {
	mux := http.NewServeMux()
	if err := MountInlineConsole(mux, ""); err != nil {
		t.Fatalf("MountInlineConsole returned unexpected error: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_sky/console")
	if err != nil {
		t.Fatalf("GET /_sky/console: %v", err)
	}
	defer resp.Body.Close()

	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if got, want := resp.Header.Get("X-Sky-Console-Mode"), "inline"; got != want {
		t.Errorf("X-Sky-Console-Mode header: got %q, want %q", got, want)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html...", ct)
	}

	bodyBytes, err := readBodyN(resp, 64*1024)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<title>Sky Console</title>",
		`<meta name="sky-console-mode" content="inline">`,
		"<body>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body (first 512 chars) ---\n%s\n", want, head(body, 512))
		}
	}

	// The bundled Sky source renders a Std.Ui-typed view. The render
	// produces a <div> tree with inline styles. We require at least
	// ONE <div> in the body — proves the Sky→Go pipeline ran end-to-
	// end at request time, not just that the static shell is served.
	if !strings.Contains(body, "<div") {
		t.Errorf("rendered body has no <div> — Std.Ui render likely short-circuited. Body head:\n%s", head(body, 1024))
	}
}

// TestMountInlineConsole_BasePath verifies the prefix logic: with
// basePath "/admin", the console mounts at /admin/_sky/console.
func TestMountInlineConsole_BasePath(t *testing.T) {
	mux := http.NewServeMux()
	if err := MountInlineConsole(mux, "/admin"); err != nil {
		t.Fatalf("MountInlineConsole returned unexpected error: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Root path should NOT serve.
	if resp, err := http.Get(srv.URL + "/_sky/console"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("/_sky/console served 200 despite basePath=/admin (should 404)")
		}
	}

	// Prefixed path SHOULD serve.
	resp, err := http.Get(srv.URL + "/admin/_sky/console")
	if err != nil {
		t.Fatalf("GET /admin/_sky/console: %v", err)
	}
	defer resp.Body.Close()
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("/admin/_sky/console status: got %d, want %d", got, want)
	}
}

// TestMountInlineConsole_HeadMethod verifies HEAD returns 200 with
// the inline sentinel header but no body. Some health-check probes
// + the dev-banner rely on a HEAD-200 check.
func TestMountInlineConsole_HeadMethod(t *testing.T) {
	mux := http.NewServeMux()
	if err := MountInlineConsole(mux, ""); err != nil {
		t.Fatalf("MountInlineConsole: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/_sky/console", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD status: got %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Sky-Console-Mode"); got != "inline" {
		t.Errorf("HEAD sentinel header: got %q, want %q", got, "inline")
	}
	body, _ := readBodyN(resp, 64)
	if len(body) != 0 {
		t.Errorf("HEAD body: got %d bytes, want 0", len(body))
	}
}

// TestMountInlineConsole_PostRejected verifies POST/PUT/DELETE
// return 405. PR 1 is read-only; the event POST path lives in PR 2.
func TestMountInlineConsole_PostRejected(t *testing.T) {
	mux := http.NewServeMux()
	if err := MountInlineConsole(mux, ""); err != nil {
		t.Fatalf("MountInlineConsole: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, srv.URL+"/_sky/console", strings.NewReader(""))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s status: got %d, want 405", method, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") {
			t.Errorf("%s Allow header: got %q, want GET listed", method, allow)
		}
	}
}

// TestRegisterInlineConsoleHook_Wired verifies that console_app's
// init() did the registration step against rt's shim. Calling
// rt.InlineConsoleAvailable should return true, AND
// rt.MountInlineConsole should produce a working mount equivalent
// to calling MountInlineConsole directly.
func TestRegisterInlineConsoleHook_Wired(t *testing.T) {
	if !rt.InlineConsoleAvailable() {
		t.Fatal("rt.InlineConsoleAvailable() is false — register.go's init() did not fire (build/link issue?)")
	}

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
	if got := resp.Header.Get("X-Sky-Console-Mode"); got != "inline" {
		t.Errorf("sentinel header: got %q, want %q", got, "inline")
	}
}

// ── helpers ────────────────────────────────────────────────────

func readBodyN(resp *http.Response, n int64) ([]byte, error) {
	buf := make([]byte, 0, n)
	tmp := make([]byte, 4096)
	var remaining int64 = n
	for remaining > 0 {
		toRead := int64(len(tmp))
		if toRead > remaining {
			toRead = remaining
		}
		r, err := resp.Body.Read(tmp[:toRead])
		if r > 0 {
			buf = append(buf, tmp[:r]...)
			remaining -= int64(r)
		}
		if err != nil {
			// io.EOF or genuine error — both end the loop. Return
			// what we have; caller checks length.
			break
		}
	}
	return buf, nil
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
