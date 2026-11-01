package rt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func csrfSetCookie(resp *httptest.ResponseRecorder) string {
	for _, sc := range resp.Header().Values("Set-Cookie") {
		if strings.HasPrefix(sc, SkyCsrfCookieName+"=") {
			return sc
		}
	}
	return ""
}

func serveCsrfWith(method, path string, h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	resp := httptest.NewRecorder()
	CSRFMiddleware(h).ServeHTTP(resp, req)
	return resp
}

// A static or immutable asset response carries no CSRF cookie. It used to
// get one on every GET, so the response (the hashed Sky.Spa wasm and JS,
// /_sky/live.<hash>.js, a static mount) carried Set-Cookie and a CDN would
// not cache it. Its own Cache-Control is kept. An HTML page still gets the
// cookie.
func TestCSRF_AssetResponsesCarryNoCookie(t *testing.T) {
	resetCsrf(t)
	asset := func(ct, cc, enc string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			if cc != "" {
				w.Header().Set("Cache-Control", cc)
			}
			if enc != "" {
				w.Header().Set("Content-Encoding", enc)
			}
			_, _ = w.Write([]byte("x"))
		})
	}
	immutable := "public, max-age=31536000, immutable"
	cases := []struct {
		path string
		h    http.Handler
	}{
		{"/_sky/live.0123abcd.js", asset("text/javascript; charset=utf-8", immutable, "")},
		{"/spa-boot.0123abcd.js", asset("application/javascript", immutable, "")},
		{"/main.0123abcd.wasm", asset("application/wasm", immutable, "br")},
		{"/assets/app.css", asset("text/css; charset=utf-8", "", "")},
		{"/logo", asset("image/png", "", "")},
		{"/fonts/x", asset("font/woff2", "", "")},
		// An extensionless route with an immutable Cache-Control.
		{"/blob/abc", asset("application/octet-stream", immutable, "")},
	}
	for _, c := range cases {
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			resp := serveCsrfWith(m, c.path, c.h)
			if sc := csrfSetCookie(resp); sc != "" {
				t.Errorf("%s %s: an asset response must not set the CSRF cookie, got %q", m, c.path, sc)
			}
			if c.h != nil && strings.Contains(c.path, "0123abcd") && resp.Header().Get("Cache-Control") != immutable {
				t.Errorf("%s %s: Cache-Control %q, want its own %q", m, c.path, resp.Header().Get("Cache-Control"), immutable)
			}
		}
	}

	// A static mount (http.FileServer): the file's type comes from its name.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.0123abcd.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.0123abcd.wasm.gz"), []byte{0x1f, 0x8b, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	fs := http.FileServer(http.Dir(dir))
	for _, p := range []string{"/app.0123abcd.js", "/main.0123abcd.wasm.gz"} {
		if sc := csrfSetCookie(serveCsrfWith(http.MethodGet, p, fs)); sc != "" {
			t.Errorf("static mount %s: must not set the CSRF cookie, got %q", p, sc)
		}
	}

	// An HTML page, with or without a declared type, still gets the cookie.
	page := asset("text/html; charset=utf-8", "", "")
	sniffed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><html><body>hi</body></html>"))
	})
	for _, c := range []struct {
		path string
		h    http.Handler
	}{{"/", page}, {"/users/jane.doe", page}, {"/index.html", page}, {"/about", sniffed}} {
		if csrfSetCookie(serveCsrfWith(http.MethodGet, c.path, c.h)) == "" {
			t.Errorf("GET %s: an HTML page must still set the CSRF cookie", c.path)
		}
	}
	// A POST response still issues it (a flow that starts with a POST).
	if csrfSetCookie(serveCsrfWith(http.MethodPost, "/_sky/x.js", asset("text/javascript", immutable, ""))) == "" {
		t.Error("a POST response must still issue the CSRF cookie")
	}
}

// The merged rule of the v0.29.1 CSRF changes: a response is marked private
// (`Vary: Cookie`, `Cache-Control: private`) exactly when it sets the CSRF
// cookie. A page GET gets the cookie and goes out private, with the app's own
// directives less `public` and `s-maxage`. An asset GET gets no cookie and is
// not marked: its own Cache-Control goes out as sent, so a CDN can cache it.
// A POST issues the cookie and goes out private. Both wrappers keep
// http.Flusher, http.Hijacker and Unwrap, so SSE and a WebSocket upgrade
// behind the middleware still work.
func TestCSRF_PrivateExactlyWhenCookieIsSet(t *testing.T) {
	resetCsrf(t)
	immutable := "public, max-age=31536000, immutable"
	withCT := func(ct, cc string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", cc)
			_, _ = w.Write([]byte("x"))
		})
	}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		page := serveCsrfWith(m, "/about", withCT("text/html; charset=utf-8", "public, max-age=60, s-maxage=600"))
		if csrfSetCookie(page) == "" {
			t.Errorf("%s page: no CSRF cookie", m)
		}
		if got := page.Header().Get("Cache-Control"); got != "private, max-age=60" {
			t.Errorf("%s page: Cache-Control %q, want %q", m, got, "private, max-age=60")
		}
		if !headerHasToken(page.Header().Values("Vary"), "Cookie") {
			t.Errorf("%s page: no Vary: Cookie: %v", m, page.Header().Values("Vary"))
		}

		for _, a := range []struct{ path, ct string }{
			{"/main.0123abcd.wasm", "application/wasm"},
			{"/_sky/live.0123abcd.js", "text/javascript; charset=utf-8"},
			{"/assets/app.0123abcd.css", "text/css; charset=utf-8"},
			{"/blob/abc", "application/json"},
		} {
			asset := serveCsrfWith(m, a.path, withCT(a.ct, immutable))
			if sc := csrfSetCookie(asset); sc != "" {
				t.Errorf("%s %s: an asset set the CSRF cookie %q", m, a.path, sc)
			}
			if got := asset.Header().Get("Cache-Control"); got != immutable {
				t.Errorf("%s %s: Cache-Control %q, want its own %q", m, a.path, got, immutable)
			}
			if headerHasToken(asset.Header().Values("Vary"), "Cookie") {
				t.Errorf("%s %s: an asset with no cookie is marked Vary: Cookie", m, a.path)
			}
		}
	}

	// A POST issues the cookie (it fails the token check) and is private.
	post := serveCsrfWith(http.MethodPost, "/x", withCT("application/json", "public, max-age=60"))
	if csrfSetCookie(post) == "" {
		t.Error("POST: no CSRF cookie")
	}
	if got := post.Header().Get("Cache-Control"); !strings.HasPrefix(got, "private") {
		t.Errorf("POST: Cache-Control %q, want private", got)
	}

	// SSE and WebSocket pass-through on both wrappers, over a real server.
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("%s /sse: Flush through the middleware: %v", r.Method, err)
			}
			_, _ = w.Write([]byte("data: 1\n\n"))
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("%s /ws: Hijack through the middleware: %v", r.Method, err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(m, srv.URL+"/ws", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		if m == http.MethodPost {
			// A mutating request must pass the token check to reach the handler.
			req.Header.Set(SkyCsrfHeaderName, "tok")
			req.AddCookie(&http.Cookie{Name: SkyCsrfCookieName, Value: "tok"})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s /ws: %v", m, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Errorf("%s /ws: status %d, want 101", m, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Set-Cookie") == "" {
		t.Errorf("GET /sse: status %d, Set-Cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	if !strings.HasPrefix(resp.Header.Get("Cache-Control"), "private") {
		t.Errorf("GET /sse: Cache-Control %q, want private", resp.Header.Get("Cache-Control"))
	}
}
