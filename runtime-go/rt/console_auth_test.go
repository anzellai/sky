package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestConsoleAuthAcceptsUrlToken — first-hit flow: ?token=<JWT>
// verifies, sets sky_console_sid cookie, 302-redirects to the
// same path with the token stripped.
//
// v0.16.0 PR 3: the URL handshake is opt-in via
// SKY_CONSOLE_EMBED_ORIGIN. We set the env var + an Origin header
// matching it so the hardened gate accepts the redeem.
func TestConsoleAuthAcceptsUrlToken(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	tok, err := MintConsoleUrlToken(secret, "anzel@test", "42", 10*time.Minute)
	if err != nil {
		t.Fatalf("MintConsoleUrlToken: %v", err)
	}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	gate := consoleTokenAuth(secret, inner)

	r := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r.Header.Set("Origin", "https://dev.example")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)

	if w.Result().StatusCode != http.StatusFound {
		t.Errorf("expected 302 redirect on first-hit, got %d", w.Result().StatusCode)
	}
	loc := w.Result().Header.Get("Location")
	if strings.Contains(loc, "token=") {
		t.Errorf("redirect should strip token from URL, got %q", loc)
	}
	if !strings.HasPrefix(loc, "/_sky/console") {
		t.Errorf("redirect should stay under /_sky/console, got %q", loc)
	}
	if called {
		t.Errorf("inner handler should NOT be called during the redirect step")
	}
	c := findSetCookie(w.Result().Header, consoleAuthCookieName)
	if c == nil {
		t.Fatalf("expected %s cookie to be set", consoleAuthCookieName)
	}
	if c.SameSite != http.SameSiteNoneMode {
		t.Errorf("cookie SameSite: got %v, want None (cross-origin iframe)", c.SameSite)
	}
	if !c.Secure {
		t.Errorf("cookie Secure: false; required because SameSite=None")
	}
	if !c.HttpOnly {
		t.Errorf("cookie HttpOnly: false; required to defend against XSS")
	}
}

// TestConsoleAuthAcceptsCookieOnSubsequentRequest — after the
// first hit set the session cookie, the inner handler is reached
// directly on subsequent requests with no token in the URL.
func TestConsoleAuthAcceptsCookieOnSubsequentRequest(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	// Build a session cookie by going through the issue path.
	urlTok, _ := MintConsoleUrlToken(secret, "anzel@test", "42", 10*time.Minute)
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	r1 := httptest.NewRequest("GET", "/_sky/console/?token="+urlTok, nil)
	r1.Header.Set("Origin", "https://dev.example")
	w1 := httptest.NewRecorder()
	gate.ServeHTTP(w1, r1)
	cookie := findSetCookie(w1.Result().Header, consoleAuthCookieName)
	if cookie == nil {
		t.Fatalf("setup: expected session cookie")
	}

	// Now make a subsequent request carrying ONLY the cookie.
	called := false
	gate2 := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	r2 := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	r2.AddCookie(&http.Cookie{Name: consoleAuthCookieName, Value: cookie.Value})
	w2 := httptest.NewRecorder()
	gate2.ServeHTTP(w2, r2)

	if !called {
		t.Errorf("inner handler not reached — cookie path is broken")
	}
	if w2.Result().StatusCode != http.StatusOK {
		t.Errorf("subsequent request status: %d", w2.Result().StatusCode)
	}
}

// TestConsoleAuthRejectsBadToken — wrong signature on the URL
// token must 401 + render the landing page, never reach inner.
func TestConsoleAuthRejectsBadToken(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	// Token signed with a DIFFERENT secret.
	badTok, _ := MintConsoleUrlToken("different-secret-32-bytes-or-more", "x", "1", 10*time.Minute)

	called := false
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("GET", "/_sky/console/?token="+badTok, nil)
	r.Header.Set("Origin", "https://dev.example")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)

	if called {
		t.Errorf("inner handler reached with bad token")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("bad-token status: got %d, want 401", w.Result().StatusCode)
	}
	if findSetCookie(w.Result().Header, consoleAuthCookieName) != nil {
		t.Errorf("session cookie should NOT be set on a bad token")
	}
}

// TestConsoleAuthRejectsExpiredToken — exp claim in the past.
func TestConsoleAuthRejectsExpiredToken(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	tok, _ := MintConsoleUrlToken(secret, "x", "1", -1*time.Minute)

	called := false
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r.Header.Set("Origin", "https://dev.example")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)

	if called {
		t.Errorf("inner handler reached with expired token")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("expired-token status: got %d, want 401", w.Result().StatusCode)
	}
}

// TestConsoleAuthRejectsNoCredentials — no token, no cookie.
func TestConsoleAuthRejectsNoCredentials(t *testing.T) {
	secret := "a-32-byte-or-longer-test-secret-key"
	called := false
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)

	if called {
		t.Errorf("inner handler reached with no credentials")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("no-creds status: got %d, want 401", w.Result().StatusCode)
	}
}


// TestAdminTokenSecretPrefersAdminToken — the canonical
// SKY_ADMIN_TOKEN beats both legacy aliases.
func TestAdminTokenSecretPrefersAdminToken(t *testing.T) {
	t.Setenv("SKY_ADMIN_TOKEN", "canonical-admin")
	t.Setenv("SKY_METRICS_TOKEN", "v14_21-alias")
	t.Setenv("SKY_CONSOLE_TOKEN_SECRET", "v14_20-alias")
	if got := adminTokenSecret(); got != "canonical-admin" {
		t.Errorf("with all three set, expected SKY_ADMIN_TOKEN to win; got %q", got)
	}
}

// TestAdminTokenSecretFallsBackToMetricsToken — v0.14.21-seeded
// tenants with only SKY_METRICS_TOKEN provisioned still work.
func TestAdminTokenSecretFallsBackToMetricsToken(t *testing.T) {
	t.Setenv("SKY_ADMIN_TOKEN", "")
	t.Setenv("SKY_METRICS_TOKEN", "v14_21-alias")
	t.Setenv("SKY_CONSOLE_TOKEN_SECRET", "v14_20-alias")
	if got := adminTokenSecret(); got != "v14_21-alias" {
		t.Errorf("with admin unset + metrics set, got %q want v14_21-alias", got)
	}
}

// TestAdminTokenSecretFallsBackToConsoleSecret — v0.14.20-seeded
// tenants with only SKY_CONSOLE_TOKEN_SECRET still work.
func TestAdminTokenSecretFallsBackToConsoleSecret(t *testing.T) {
	t.Setenv("SKY_ADMIN_TOKEN", "")
	t.Setenv("SKY_METRICS_TOKEN", "")
	t.Setenv("SKY_CONSOLE_TOKEN_SECRET", "v14_20-alias")
	if got := adminTokenSecret(); got != "v14_20-alias" {
		t.Errorf("with only legacy var set, got %q want v14_20-alias", got)
	}
}

// TestAdminTokenSecretEmptyWhenUnset — none set → admin surface
// stays locked; the deploy falls back to dev-mode rules.
func TestAdminTokenSecretEmptyWhenUnset(t *testing.T) {
	t.Setenv("SKY_ADMIN_TOKEN", "")
	t.Setenv("SKY_METRICS_TOKEN", "")
	t.Setenv("SKY_CONSOLE_TOKEN_SECRET", "")
	if got := adminTokenSecret(); got != "" {
		t.Errorf("nothing set, got %q want \"\"", got)
	}
}

// TestConsoleAdminSecretAlias — the v0.14.21 helper name is kept
// as a thin alias so external callers don't break.
func TestConsoleAdminSecretAlias(t *testing.T) {
	t.Setenv("SKY_ADMIN_TOKEN", "via-admin")
	t.Setenv("SKY_METRICS_TOKEN", "")
	t.Setenv("SKY_CONSOLE_TOKEN_SECRET", "")
	if got := consoleAdminSecret(); got != "via-admin" {
		t.Errorf("alias should resolve same as adminTokenSecret; got %q", got)
	}
}
