package rt

// Defect 1 regression — "production cookies silently lose Secure on a
// substring collision".
//
// `securifyCookieAttrs` used to decide "is Secure already present?" with
//
//	strings.Contains(strings.ToLower(attrs), "secure")
//
// which is true for `Path=/secure`, `Domain=secure.example.com` and
// `Path=/insecure-area` — none of which is the Secure ATTRIBUTE. Any app
// whose cookie path or domain happens to contain the six letters "secure"
// shipped a production session cookie WITHOUT Secure, and nothing said so.
//
// Every assertion below goes through the STDLIB cookie parser
// (`(&http.Response{...}).Cookies()`), i.e. the browser's view of the
// header — never through the predicate under test. The old idempotency
// test asserted with `strings.Count(lower(sc), "secure") != 1`, the same
// broken predicate as the code, so it agreed with the bug.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// parseSetCookieLine parses one raw Set-Cookie value the way a browser
// (and Go's own client) does. Fails the test if the line is unparseable.
func parseSetCookieLine(t *testing.T, line string) *http.Cookie {
	t.Helper()
	resp := &http.Response{Header: http.Header{"Set-Cookie": []string{line}}}
	cs := resp.Cookies()
	if len(cs) != 1 {
		t.Fatalf("expected exactly one parseable cookie from %q, got %d", line, len(cs))
	}
	return cs[0]
}

// countSecureAttrs counts how many times the Secure ATTRIBUTE appears in
// a raw Set-Cookie line (used only to pin idempotency; presence itself is
// asserted through the parser).
func countSecureAttrs(line string) int {
	n := 0
	for i, seg := range strings.Split(line, ";") {
		if i == 0 {
			continue // name=value
		}
		seg = strings.TrimSpace(seg)
		if eq := strings.IndexByte(seg, '='); eq >= 0 {
			seg = strings.TrimSpace(seg[:eq])
		}
		if strings.EqualFold(seg, "secure") {
			n++
		}
	}
	return n
}

// withEnv sets ENV/SKY_ENV for the duration of the returned closure and
// restores both. Empty string means "unset".
func withEnvVars(t *testing.T, env, skyEnv string) func() {
	t.Helper()
	restoreEnv := setOrUnsetEnv(t, "ENV", env)
	restoreSky := setOrUnsetEnv(t, "SKY_ENV", skyEnv)
	return func() { restoreSky(); restoreEnv() }
}

func setOrUnsetEnv(t *testing.T, name, value string) func() {
	t.Helper()
	prev, had := os.LookupEnv(name)
	if value == "" {
		_ = os.Unsetenv(name)
	} else if err := os.Setenv(name, value); err != nil {
		t.Fatalf("setenv %s: %v", name, err)
	}
	return func() {
		if had {
			_ = os.Setenv(name, prev)
		} else {
			_ = os.Unsetenv(name)
		}
	}
}

// ── Defect 1a: the substring collision ───────────────────────────

func TestSecurifyCookieAttrs_SubstringCollisionStillGetsSecure(t *testing.T) {
	restore := withEnvVars(t, "production", "")
	defer restore()

	cases := []struct {
		name  string
		attrs string
	}{
		{"path contains 'secure'", "Path=/secure; HttpOnly; SameSite=Lax"},
		{"domain contains 'secure'", "Path=/; Domain=secure.example.com; HttpOnly"},
		{"path contains 'insecure'", "Path=/insecure-area; HttpOnly"},
		{"control — no collision", "Path=/; HttpOnly; SameSite=Lax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := asSkyResponse(Server_withCookie(
				"sky_session", "SECRET-SESSION-ID", tc.attrs, SkyResponse{Status: 200}))
			if !ok {
				t.Fatal("withCookie did not return a response")
			}
			lines := setCookieLines(out)
			if len(lines) != 1 {
				t.Fatalf("expected 1 Set-Cookie, got %d: %v", len(lines), lines)
			}
			c := parseSetCookieLine(t, lines[0])
			if !c.Secure {
				t.Fatalf("production cookie lost Secure for attrs %q → %q", tc.attrs, lines[0])
			}
		})
	}
}

// ── Defect 1b: idempotency, asserted on the PARSED cookie ────────

func TestSecurifyCookieAttrs_DoesNotDoubleAddSecure(t *testing.T) {
	restore := withEnvVars(t, "production", "")
	defer restore()

	for _, attrs := range []string{
		"Path=/; HttpOnly; Secure",
		"Path=/; HttpOnly; secure",
		"Path=/; HttpOnly; Secure=",   // valued form — still the attribute
		"Path=/;; HttpOnly; Secure; ", // empty segments
	} {
		out, _ := asSkyResponse(Server_withCookie("s", "v", attrs, SkyResponse{Status: 200}))
		lines := setCookieLines(out)
		if len(lines) != 1 {
			t.Fatalf("expected 1 Set-Cookie for %q, got %v", attrs, lines)
		}
		c := parseSetCookieLine(t, lines[0])
		if !c.Secure {
			t.Fatalf("attrs %q: parsed cookie is not Secure: %q", attrs, lines[0])
		}
		if n := countSecureAttrs(lines[0]); n != 1 {
			t.Fatalf("attrs %q: expected exactly one Secure attribute, got %d: %q",
				attrs, n, lines[0])
		}
	}
}

// ── Defect 1c: ONE predicate, not two ────────────────────────────
//
// `isProd()` read `<PREFIX>_ENV == "prod"` (exact match, namespaced var
// only). `productionFromEnv()` — the documented single source of truth
// behind the console/metrics gate — reads ENV first, then SKY_ENV, and
// treats ANY non-dev value as production. Under the production gate
// AGENTS.md actually documents (`ENV=production`) the cookie path was
// therefore still in DEV mode and appended no Secure at all.

func TestCookieSecurePredicate_MatchesProductionGate(t *testing.T) {
	cases := []struct {
		env, skyEnv string
		want        bool
	}{
		{"production", "", true},
		{"prod", "", true},
		{"staging", "", true},
		{"", "prod", true},
		{"", "production", true},
		{"dev", "", false},
		{"development", "", false},
		{"local", "", false},
		{"", "", false},
		{"dev", "production", false}, // ENV wins over SKY_ENV
	}
	for _, tc := range cases {
		restore := withEnvVars(t, tc.env, tc.skyEnv)
		got := strings.Contains(securifyCookieAttrs("Path=/; HttpOnly"), "Secure")
		gate := productionFromEnv()
		restore()
		if got != tc.want || gate != tc.want {
			t.Fatalf("ENV=%q SKY_ENV=%q: cookie-Secure=%v, production gate=%v, want %v for both",
				tc.env, tc.skyEnv, got, gate, tc.want)
		}
	}
}

// ── Defect 1d: every mint site runs the same predicate ───────────
//
// The Sky.Live session cookie was minted with `secure = false` unless
// SKY_LIVE_FRAME_ANCESTORS was set — so the session id of a production
// Sky.Live app (the PINNED DEFAULT app shape) rode without Secure.

func TestSessionCookie_SecureInProduction(t *testing.T) {
	restore := withEnvVars(t, "production", "")
	defer restore()

	w := httptest.NewRecorder()
	writeSessionCookie(httptest.NewRequest("GET", "/", nil), w, "sky_sid", "SID", time.Hour)
	lines := w.Result().Header.Values("Set-Cookie")
	if len(lines) != 1 {
		t.Fatalf("expected 1 Set-Cookie, got %v", lines)
	}
	if c := parseSetCookieLine(t, lines[0]); !c.Secure {
		t.Fatalf("production Sky.Live session cookie is not Secure: %q", lines[0])
	}
}

func TestSessionCookie_SecureOverTLSInDev(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	writeSessionCookie(req, w, "sky_sid", "SID", time.Hour)
	lines := w.Result().Header.Values("Set-Cookie")
	if c := parseSetCookieLine(t, lines[0]); !c.Secure {
		t.Fatalf("cookie served over HTTPS must be Secure: %q", lines[0])
	}
}

// The runtime used to hold its OWN cookies to a stricter rule than the
// one it applied to the user's: `sky_sid` saw the request and got Secure
// over HTTPS, while `Server.withCookie` (called from Sky with no request
// in hand) could only consult the production gate. A user setting an auth
// token over HTTPS on a staging deploy therefore got no Secure. The
// decision now also runs at EMISSION time, where the request exists.
func TestUserCookie_SecureOverTLSOutsideProduction(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()

	for _, form := range []struct {
		name string
		out  any
	}{
		{"withCookie/2", Server_withCookie(
			Server_cookie("auth", "TOKEN"), SkyResponse{Status: 200})},
		{"withCookie/3", Server_withCookie("auth", "TOKEN", SkyResponse{Status: 200})},
		{"withCookie/4", Server_withCookie("auth", "TOKEN",
			"Path=/; HttpOnly; SameSite=Lax", SkyResponse{Status: 200})},
	} {
		t.Run(form.name, func(t *testing.T) {
			sr, ok := asSkyResponse(form.out)
			if !ok {
				t.Fatal("withCookie did not return a response")
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Forwarded-Proto", "https")
			rec := httptest.NewRecorder()
			applySkyResponseHeaders(rec.Header(), req, sr)
			lines := rec.Result().Header.Values("Set-Cookie")
			if len(lines) != 1 {
				t.Fatalf("expected 1 Set-Cookie, got %v", lines)
			}
			if c := parseSetCookieLine(t, lines[0]); !c.Secure {
				t.Fatalf("user cookie set over HTTPS must be Secure — the runtime's own "+
					"sky_sid is, and the user's must meet the same bar: %q", lines[0])
			}
			if n := countSecureAttrs(lines[0]); n != 1 {
				t.Fatalf("expected exactly one Secure attribute, got %d: %q", n, lines[0])
			}
		})
	}
}

// Plain HTTP outside production stays plain: a Secure cookie on a
// `http://localhost` dev session is never sent back, which would break
// every local login.
func TestUserCookie_NoSecureOverPlainHttpInDev(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()

	sr, _ := asSkyResponse(Server_withCookie("auth", "TOKEN", SkyResponse{Status: 200}))
	rec := httptest.NewRecorder()
	applySkyResponseHeaders(rec.Header(), httptest.NewRequest("GET", "/", nil), sr)
	lines := rec.Result().Header.Values("Set-Cookie")
	if c := parseSetCookieLine(t, lines[0]); c.Secure {
		t.Fatalf("plain-HTTP dev cookie must not be Secure: %q", lines[0])
	}
}

func TestCsrfMiddlewareCookie_SecureInProduction(t *testing.T) {
	restore := withEnvVars(t, "production", "")
	defer restore()

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if !strings.HasPrefix(line, SkyCsrfCookieName+"=") {
			continue
		}
		if c := parseSetCookieLine(t, line); !c.Secure {
			t.Fatalf("production CSRF cookie is not Secure: %q", line)
		}
		return
	}
	t.Fatalf("no %s cookie issued: %v", SkyCsrfCookieName,
		w.Result().Header.Values("Set-Cookie"))
}

// Cookies whose NAME mandates Secure (the `__Host-` / `__Secure-`
// prefixes, RFC 6265bis §4.1.3.1–2) are Secure unconditionally — the
// only justified hardcoded `true`, and it must hold in dev too.
func TestHostPrefixedCookies_AlwaysSecure(t *testing.T) {
	restore := withEnvVars(t, "dev", "")
	defer restore()

	w := httptest.NewRecorder()
	setConsoleV2Cookie(w, []byte("0123456789abcdef0123456789abcdef"), "someone")
	lines := w.Result().Header.Values("Set-Cookie")
	if len(lines) != 1 {
		t.Fatalf("expected 1 Set-Cookie, got %v", lines)
	}
	if !strings.HasPrefix(lines[0], "__Host-") {
		t.Fatalf("expected a __Host- cookie, got %q", lines[0])
	}
	if c := parseSetCookieLine(t, lines[0]); !c.Secure {
		t.Fatalf("__Host- cookie must always be Secure: %q", lines[0])
	}
}
