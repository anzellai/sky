package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSkySidCookieSameSiteDefault — without SKY_LIVE_FRAME_ANCESTORS,
// the session cookie stays SameSite=Lax (browser default for same-
// origin nav), Secure off (works on plain-HTTP local dev).
func TestSkySidCookieSameSiteDefault(t *testing.T) {
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	sessionID(r, w, 30*time.Minute)

	c := findSetCookie(w.Result().Header, "sky_sid")
	if c == nil {
		t.Fatalf("expected sky_sid cookie")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("default sky_sid SameSite: got %v, want Lax", c.SameSite)
	}
	if c.Secure {
		t.Errorf("default sky_sid Secure: got true, want false")
	}
	if !c.HttpOnly {
		t.Errorf("sky_sid HttpOnly: got false, want true")
	}
}

// TestSkySidCookieCrossOriginIframe — with SKY_LIVE_FRAME_ANCESTORS
// set, the cookie MUST be SameSite=None; Secure or the browser drops
// it on every iframe-driven SSE/POST, sending the app into an endless
// reconnect loop. This is the regression #15 from the dev.skydeploy
// preview-iframe disconnection bug.
func TestSkySidCookieCrossOriginIframe(t *testing.T) {
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "https://dev.skydeploy.app")
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	sessionID(r, w, 30*time.Minute)

	c := findSetCookie(w.Result().Header, "sky_sid")
	if c == nil {
		t.Fatalf("expected sky_sid cookie")
	}
	if c.SameSite != http.SameSiteNoneMode {
		t.Errorf("iframe sky_sid SameSite: got %v, want None", c.SameSite)
	}
	if !c.Secure {
		t.Errorf("iframe sky_sid Secure: got false, want true (SameSite=None requires Secure)")
	}
}

// TestCsrfCookieSameSiteDefault — without SKY_LIVE_FRAME_ANCESTORS,
// CSRF cookie remains SameSite=Strict (its design baseline).
func TestCsrfCookieSameSiteDefault(t *testing.T) {
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := CSRFMiddleware(mux)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	c := findSetCookie(w.Result().Header, "__sky_csrf")
	if c == nil {
		t.Fatalf("expected __sky_csrf cookie")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("default __sky_csrf SameSite: got %v, want Strict", c.SameSite)
	}
}

// TestCsrfCookieCrossOriginIframe — with iframe mode, CSRF cookie
// drops to None+Secure. Strict would block the cookie on the iframed
// app's own POSTs (sky-id from cross-site context); None lets it
// through, and the X-Sky-Csrf header set by same-origin JS in the
// iframed app remains the actual CSRF gate.
func TestCsrfCookieCrossOriginIframe(t *testing.T) {
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "https://dev.skydeploy.app")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := CSRFMiddleware(mux)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	c := findSetCookie(w.Result().Header, "__sky_csrf")
	if c == nil {
		t.Fatalf("expected __sky_csrf cookie")
	}
	if c.SameSite != http.SameSiteNoneMode {
		t.Errorf("iframe __sky_csrf SameSite: got %v, want None", c.SameSite)
	}
	if !c.Secure {
		t.Errorf("iframe __sky_csrf Secure: got false, want true")
	}
}

// TestCsrfCookieXForwardedProtoMarksSecure — a TLS-terminated proxy
// (Cloud Run, Caddy, Cloudflare) presents the inner request as plain
// HTTP. X-Forwarded-Proto=https must promote the cookie to Secure so
// browsers accept it.
func TestCsrfCookieXForwardedProtoMarksSecure(t *testing.T) {
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := CSRFMiddleware(mux)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	c := findSetCookie(w.Result().Header, "__sky_csrf")
	if c == nil {
		t.Fatalf("expected __sky_csrf cookie")
	}
	if !c.Secure {
		t.Errorf("__sky_csrf Secure: got false, want true under X-Forwarded-Proto=https")
	}
}

func findSetCookie(h http.Header, name string) *http.Cookie {
	for _, raw := range h.Values("Set-Cookie") {
		// Use the standard library's request parser to decode the
		// header back into a cookie struct.
		req := &http.Request{Header: http.Header{"Cookie": []string{raw}}}
		for _, c := range req.Cookies() {
			if c.Name == name {
				// Cookie() doesn't carry attrs — parse them by hand
				// from the raw Set-Cookie string for SameSite/Secure.
				return parseSetCookieAttrs(raw, c)
			}
		}
	}
	return nil
}

func parseSetCookieAttrs(raw string, c *http.Cookie) *http.Cookie {
	for _, part := range strings.Split(raw, ";") {
		p := strings.TrimSpace(part)
		switch {
		case strings.EqualFold(p, "Secure"):
			c.Secure = true
		case strings.EqualFold(p, "HttpOnly"):
			c.HttpOnly = true
		case strings.HasPrefix(strings.ToLower(p), "samesite="):
			v := strings.ToLower(strings.TrimPrefix(strings.ToLower(p), "samesite="))
			switch v {
			case "lax":
				c.SameSite = http.SameSiteLaxMode
			case "strict":
				c.SameSite = http.SameSiteStrictMode
			case "none":
				c.SameSite = http.SameSiteNoneMode
			}
		}
	}
	return c
}
