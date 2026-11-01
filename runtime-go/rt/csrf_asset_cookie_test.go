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
