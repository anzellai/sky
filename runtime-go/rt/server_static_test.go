package rt

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cycle 4 follow-up — Sky.Http.Server.static was previously a stub
// that returned the literal string "static:<dir>" as the body. This
// test pins the new behaviour: Server_static returns a SkyRoute with
// StaticDir set; Server_listen registers http.StripPrefix +
// http.FileServer under the URL prefix, so:
//
//   - GET /<prefix>/file.js returns the file body with the right
//     Content-Type (mime.TypeByExtension).
//   - GET /<prefix>/missing returns 404.
//   - GET /<prefix>/../../etc/passwd returns 400 (http.Dir refuses
//     paths containing ..).
//   - GET /<prefix>/subdir/nested.css works (prefix matching, not
//     exact).
//
// The Sky-handler dispatch path is NOT exercised for these routes —
// they go straight to http.FileServer for stdlib-grade serving.

func Test_ServerStatic_NormalisesPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/static", "/static/"},
		{"/static/", "/static/"},
		{"static", "/static/"},
		{"static/", "/static/"},
		{"/assets/v1", "/assets/v1/"},
	}
	for _, c := range cases {
		r := Server_static(c.in, "wherever").(SkyRoute)
		if r.Path != c.want {
			t.Errorf("Server_static(%q): got Path=%q, want %q", c.in, r.Path, c.want)
		}
		if r.StaticDir != "wherever" {
			t.Errorf("Server_static(%q): StaticDir=%q, want %q", c.in, r.StaticDir, "wherever")
		}
		if r.Handler != nil {
			t.Errorf("Server_static(%q): Handler should be nil (Server_listen handles static directly), got %T", c.in, r.Handler)
		}
		if r.Method != "GET" {
			t.Errorf("Server_static(%q): Method=%q, want GET", c.in, r.Method)
		}
	}
}

// End-to-end via a real *http.ServeMux — same shape Server_listen
// builds.  We don't spin up a TCP server; httptest.NewRecorder
// directly invokes the mux's ServeHTTP.
func Test_ServerStatic_ServesFiles_WithCorrectContentType(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "app.js", "console.log('hello');")
	mustWrite(t, dir, "style.css", "body { color: red; }")
	mustWrite(t, dir, "sub/nested.svg", "<svg/>")

	mux := http.NewServeMux()
	r := Server_static("/static", dir).(SkyRoute)
	// Mirror what Server_listen does for static routes.
	stripPattern := r.Path
	if len(stripPattern) > 1 && stripPattern[len(stripPattern)-1] == '/' {
		stripPattern = stripPattern[:len(stripPattern)-1]
	}
	mux.Handle(r.Path, http.StripPrefix(stripPattern, http.FileServer(http.Dir(r.StaticDir))))

	cases := []struct {
		url             string
		wantStatus      int
		wantContentType string // prefix match
		wantBodySub     string
	}{
		{"/static/app.js", 200, "text/javascript", "console.log"},
		{"/static/style.css", 200, "text/css", "color: red"},
		{"/static/sub/nested.svg", 200, "image/svg", "<svg"},
		{"/static/missing.txt", 404, "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.url, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.wantStatus {
			t.Errorf("%s: got status %d, want %d", c.url, w.Code, c.wantStatus)
		}
		ct := w.Header().Get("Content-Type")
		if c.wantContentType != "" && !strings.HasPrefix(ct, c.wantContentType) {
			t.Errorf("%s: Content-Type %q does not start with %q", c.url, ct, c.wantContentType)
		}
		if c.wantBodySub != "" {
			body, _ := io.ReadAll(w.Body)
			if !strings.Contains(string(body), c.wantBodySub) {
				t.Errorf("%s: body does not contain %q\nbody: %s", c.url, c.wantBodySub, body)
			}
		}
	}
}

// Path traversal: ServeMux normalises `/static/../...` paths BEFORE
// the route handler ever sees them — typically by emitting a 301
// redirect to the cleaned URL, which then takes a different route
// (or 404). Either way the malicious path never reaches our
// FileServer + http.Dir, which itself also refuses `..` segments.
// We just assert the request does NOT succeed with the file body
// of an unrelated route.
func Test_ServerStatic_RefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "ok.txt", "served")

	mux := http.NewServeMux()
	r := Server_static("/static", dir).(SkyRoute)
	stripPattern := r.Path[:len(r.Path)-1]
	mux.Handle(r.Path, http.StripPrefix(stripPattern, http.FileServer(http.Dir(r.StaticDir))))

	req := httptest.NewRequest("GET", "/static/../etc/passwd", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// The status MUST be redirect-or-not-found, never 200.
	if w.Code == 200 {
		body, _ := io.ReadAll(w.Body)
		t.Errorf("path traversal returned 200 with body %q (security regression)", body)
	}
}

// ────────────────────────────────────────────────────────────────
// helpers

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}
