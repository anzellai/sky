package rt

// prod_cookie_secure_test.go — `ENV=production` must put `Secure` on
// every session cookie the runtime mints.
//
// THE DEFECT these tests pin, in two independent places:
//
//  1. `isProd()` was `skyGetenv("ENV") == "prod"` — <PREFIX>_ENV only,
//     and only the literal string "prod". `ENV=production` — what
//     `sky init` scaffolds and what every doc promises — left it
//     false, so `securifyCookieAttrs` never appended "; Secure".
//
//  2. `writeSessionCookie` (the ACTUAL Sky.Live `sky_sid` session
//     cookie) never consulted a production predicate at all. It
//     hardcoded `secure := false` and flipped it true only under
//     `crossOriginIframeMode()`. So even the narrow `SKY_ENV=prod`
//     spelling left the session cookie without `Secure`.
//
// Every assertion below is on the COOKIE ATTRIBUTE as it appears on
// the wire, never on the predicate. A test that asserted `isProd()`
// would have passed while `Secure` was still missing one call away —
// which is precisely how (2) survived alongside (1).

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// clearEnvFlags removes every spelling of the production flag so a
// test starts from a known dev baseline regardless of the ambient
// shell (the predicate now reads plain ENV as well as <PREFIX>_ENV).
func clearEnvFlags(t *testing.T) {
	t.Helper()
	t.Setenv("ENV", "")
	t.Setenv("SKY_ENV", "")
	t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
}

// prodSpellings enumerates the values that MUST gate as production.
// "production" is the headline: it is what `sky init` writes into the
// scaffolded .env and what docs/sky-toml.md documents.
var prodSpellings = []struct {
	name  string
	key   string
	value string
}{
	{"ENV=production", "ENV", "production"},
	{"ENV=prod", "ENV", "prod"},
	{"ENV=staging", "ENV", "staging"},
	{"SKY_ENV=production", "SKY_ENV", "production"},
	{"SKY_ENV=prod", "SKY_ENV", "prod"},
	{"SKY_ENV=staging", "SKY_ENV", "staging"},
}

// TestSessionCookie_SecureInProd is the headline regression: the
// Sky.Live session cookie itself, over a plain-HTTP request, with the
// production flag set.
func TestSessionCookie_SecureInProd(t *testing.T) {
	for _, sp := range prodSpellings {
		t.Run(sp.name, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv(sp.key, sp.value)

			r := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			sessionID(r, w, 30*time.Minute)

			c := findSetCookie(w.Result().Header, "sky_sid")
			if c == nil {
				t.Fatalf("expected sky_sid cookie")
			}
			if !c.Secure {
				t.Errorf("%s: sky_sid Secure = false, want true "+
					"(session cookie sent in cleartext in production)", sp.name)
			}
			// HttpOnly is unconditional and must stay so.
			if !c.HttpOnly {
				t.Errorf("%s: sky_sid HttpOnly = false, want true", sp.name)
			}
		})
	}
}

// TestSessionCookie_NotSecureInDev pins the other side: plain HTTP dev
// with no production flag must NOT get Secure, or local development
// over http:// breaks (browser refuses to send the cookie).
func TestSessionCookie_NotSecureInDev(t *testing.T) {
	for _, devVal := range []string{"", "dev", "development", "local"} {
		t.Run("ENV="+devVal, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv("ENV", devVal)

			r := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			sessionID(r, w, 30*time.Minute)

			c := findSetCookie(w.Result().Header, "sky_sid")
			if c == nil {
				t.Fatalf("expected sky_sid cookie")
			}
			if c.Secure {
				t.Errorf("ENV=%q: sky_sid Secure = true, want false "+
					"(breaks plain-HTTP local dev)", devVal)
			}
		})
	}
}

// TestSessionCookie_SecureOnHTTPSRequest — an HTTPS request gets a
// Secure cookie whatever the env flag says. The request scheme is a
// strictly more accurate signal than an env var, and the CSRF
// middleware already derives Secure this way (csrf_middleware.go:224).
func TestSessionCookie_SecureOnHTTPSRequest(t *testing.T) {
	t.Run("direct TLS", func(t *testing.T) {
		clearEnvFlags(t)
		r := httptest.NewRequest("GET", "https://example.test/", nil)
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		sessionID(r, w, 30*time.Minute)

		c := findSetCookie(w.Result().Header, "sky_sid")
		if c == nil {
			t.Fatalf("expected sky_sid cookie")
		}
		if !c.Secure {
			t.Error("sky_sid over TLS: Secure = false, want true")
		}
	})

	t.Run("X-Forwarded-Proto https", func(t *testing.T) {
		clearEnvFlags(t)
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		sessionID(r, w, 30*time.Minute)

		c := findSetCookie(w.Result().Header, "sky_sid")
		if c == nil {
			t.Fatalf("expected sky_sid cookie")
		}
		if !c.Secure {
			t.Error("sky_sid behind TLS-terminating proxy: " +
				"Secure = false, want true")
		}
	})
}

// TestWithCookie_SecureForProductionSpellings covers the
// securifyCookieAttrs path — the OTHER predicate, asserted through
// the emitted Set-Cookie header rather than through isProd().
func TestWithCookie_SecureForProductionSpellings(t *testing.T) {
	for _, sp := range prodSpellings {
		t.Run(sp.name, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv(sp.key, sp.value)

			resp := SkyResponse{Status: 200}
			out := Server_withCookie("session", "tok123", resp).(SkyResponse)
			sc := out.Headers["Set-Cookie"]
			if !strings.Contains(strings.ToLower(sc), "secure") {
				t.Errorf("%s: Set-Cookie %q lacks Secure", sp.name, sc)
			}
			// The unconditional attributes must survive untouched.
			if !strings.Contains(sc, "HttpOnly") {
				t.Errorf("%s: Set-Cookie %q lacks HttpOnly", sp.name, sc)
			}
			if !strings.Contains(sc, "SameSite") {
				t.Errorf("%s: Set-Cookie %q lacks SameSite", sp.name, sc)
			}
		})
	}
}

// TestCsrfIssue_SecureForProductionSpellings — same for the
// Server.csrfIssue cookie.
func TestCsrfIssue_SecureForProductionSpellings(t *testing.T) {
	for _, sp := range prodSpellings {
		t.Run(sp.name, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv(sp.key, sp.value)

			resp := SkyResponse{Status: 200}
			out := Server_csrfIssue(resp).(SkyTuple2)
			updated := out.V1.(SkyResponse)
			sc := updated.Headers["Set-Cookie"]
			if !strings.Contains(strings.ToLower(sc), "secure") {
				t.Errorf("%s: csrf Set-Cookie %q lacks Secure", sp.name, sc)
			}
		})
	}
}

// TestProductionPredicate_HonoursEnvPrefix — with `[env] prefix =
// "FENCE"` in sky.toml the runtime reads FENCE_ENV, and the
// production gate must follow the prefix. `productionFromEnv` used to
// hardcode os.Getenv("SKY_ENV"), so a custom-prefix project could not
// turn the gate on via its own namespaced variable at all.
//
// Asserted on the cookie attribute, per the rule above.
func TestProductionPredicate_HonoursEnvPrefix(t *testing.T) {
	clearEnvFlags(t)
	t.Setenv("FENCE_ENV", "production")

	prev := EnvPrefix()
	SetEnvPrefix("FENCE")
	defer SetEnvPrefix(prev)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	sessionID(r, w, 30*time.Minute)

	c := findSetCookie(w.Result().Header, "sky_sid")
	if c == nil {
		t.Fatalf("expected sky_sid cookie")
	}
	if !c.Secure {
		t.Error("FENCE_ENV=production with prefix FENCE: " +
			"sky_sid Secure = false, want true")
	}
}

// TestOneProductionPredicate — the structural assertion. isProd() and
// productionFromEnv() must agree on every input, or a future change
// re-opens the split that caused this bug. Compared across the whole
// spelling matrix, dev and prod alike.
func TestOneProductionPredicate(t *testing.T) {
	cases := []struct {
		key, value string
		want       bool
	}{
		{"ENV", "", false},
		{"ENV", "dev", false},
		{"ENV", "development", false},
		{"ENV", "local", false},
		{"ENV", "DEV", false},
		{"ENV", "prod", true},
		{"ENV", "production", true},
		{"ENV", "Production", true},
		{"ENV", "staging", true},
		{"ENV", "qa", true},
		{"SKY_ENV", "prod", true},
		{"SKY_ENV", "production", true},
		{"SKY_ENV", "dev", false},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv(c.key, c.value)
			if got := productionFromEnv(); got != c.want {
				t.Errorf("productionFromEnv() = %v, want %v", got, c.want)
			}
			if isProd() != productionFromEnv() {
				t.Errorf("isProd() = %v disagrees with productionFromEnv() = %v "+
					"— there must be exactly ONE production predicate",
					isProd(), productionFromEnv())
			}
		})
	}
}
