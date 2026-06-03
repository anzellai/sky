package rt

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v0.16.0 PR 3 — new auth gate tests.
//
// Coverage:
//   - resolveConsoleAuthMode env dispatch
//   - __Host-sky_console cookie attributes
//   - HKDF signing key determinism
//   - signCookieValue / verifyCookieValue round-trip + tamper detection
//   - one-shot JTI replay deny
//   - aud-claim mismatch deny (URL handshake hardening)
//   - login POST flow
//   - production + auth-unset → mount declines

// ─── Mode resolution ────────────────────────────────────────────

func TestResolveConsoleAuthMode_ExplicitOff(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "off")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeOff {
		t.Errorf("got %v, want consoleAuthModeOff", m)
	}
}

func TestResolveConsoleAuthMode_Token(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeToken {
		t.Errorf("got %v, want consoleAuthModeToken", m)
	}
}

func TestResolveConsoleAuthMode_App(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeApp {
		t.Errorf("got %v, want consoleAuthModeApp", m)
	}
}

func TestResolveConsoleAuthMode_ProductionUnset(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("ENV", "production")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeUnsetProd {
		t.Errorf("ENV=production + SKY_CONSOLE_AUTH unset → got %v, want consoleAuthModeUnsetProd", m)
	}
}

func TestResolveConsoleAuthMode_DevUnset(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("ENV", "")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeDevOpen {
		t.Errorf("dev + SKY_CONSOLE_AUTH unset → got %v, want consoleAuthModeDevOpen", m)
	}
}

func TestResolveConsoleAuthMode_UnknownValue(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "yolo")
	ResetConsoleAuthStateForTesting()
	if m := resolveConsoleAuthMode(); m != consoleAuthModeOff {
		t.Errorf("unknown value should fail-closed to off, got %v", m)
	}
}

// ─── Cookie attributes ──────────────────────────────────────────

func TestSetConsoleV2Cookie_AttributesAreCorrect(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	w := httptest.NewRecorder()
	setConsoleV2Cookie(w, key, "user-1")
	c := findSetCookie(w.Result().Header, consoleAuthCookieV2Name)
	if c == nil {
		t.Fatalf("expected %s cookie to be set", consoleAuthCookieV2Name)
	}
	if !strings.HasPrefix(c.Name, "__Host-") {
		t.Errorf("cookie name must begin with __Host- prefix; got %q", c.Name)
	}
	if !c.HttpOnly {
		t.Error("cookie HttpOnly: false; XSS defence requires HttpOnly")
	}
	if !c.Secure {
		t.Error("cookie Secure: false; __Host- prefix requires Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie SameSite: got %v, want Strict", c.SameSite)
	}
	// findSetCookie's parseSetCookieAttrs helper only knows Secure /
	// HttpOnly / SameSite — Path + MaxAge stay on the raw header.
	// Scan the raw Set-Cookie line directly for those attrs.
	raw := ""
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, consoleAuthCookieV2Name+"=") {
			raw = line
			break
		}
	}
	// Cookie Path is "/" per RFC 6265 §5.2.4 — __Host- prefix
	// REQUIRES Path=/ (commit 61cf4c3c, v0.16.1 Item 1). A
	// non-"/" path on a __Host- cookie is rejected by browsers and
	// causes the cookie to silently never persist.
	if !strings.Contains(raw, "Path=/;") && !strings.HasSuffix(strings.TrimSpace(raw), "Path=/") {
		// Allow either `Path=/;` (followed by more attrs) or trailing `Path=/`.
		t.Errorf("cookie Path: raw header does not contain Path=/ (RFC: __Host- requires Path=/): %q", raw)
	}
	wantMaxAge := fmt.Sprintf("Max-Age=%d", int(consoleAuthCookieV2MaxAge.Seconds()))
	if !strings.Contains(raw, wantMaxAge) {
		t.Errorf("cookie MaxAge: raw header missing %q: %q", wantMaxAge, raw)
	}
}

// ─── HKDF determinism ───────────────────────────────────────────

func TestDeriveConsoleSigningKey_DeterministicForFixedInputs(t *testing.T) {
	t.Setenv("SKY_CONSOLE_TOKEN", "fixed-secret-32-bytes-of-test-data-x")
	k1 := deriveConsoleSigningKey()
	k2 := deriveConsoleSigningKey()
	if len(k1) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(k1))
	}
	if string(k1) != string(k2) {
		t.Errorf("HKDF output should be deterministic for fixed (secret, salt, info)")
	}
}

func TestDeriveConsoleSigningKey_DifferentSecretsYieldDifferentKeys(t *testing.T) {
	t.Setenv("SKY_CONSOLE_TOKEN", "secret-A-32-bytes-aaaaaaaaaaaaaaaaa")
	kA := deriveConsoleSigningKey()
	t.Setenv("SKY_CONSOLE_TOKEN", "secret-B-32-bytes-bbbbbbbbbbbbbbbbb")
	kB := deriveConsoleSigningKey()
	if string(kA) == string(kB) {
		t.Errorf("different secrets must yield different keys")
	}
}

// ─── Cookie HMAC round-trip + tamper ────────────────────────────

func TestSignVerifyCookieValue_RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cookie := signCookieValue(key, "anzel@test", time.Hour)
	sub, ok := verifyCookieValue(key, cookie)
	if !ok {
		t.Fatalf("verifyCookieValue rejected a freshly-signed cookie")
	}
	if sub != "anzel@test" {
		t.Errorf("subject round-trip lost: got %q, want anzel@test", sub)
	}
}

func TestVerifyCookieValue_TamperedSignatureRejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cookie := signCookieValue(key, "anzel@test", time.Hour)
	// Replace the last 4 chars (the signature suffix).
	tampered := cookie[:len(cookie)-4] + "AAAA"
	if _, ok := verifyCookieValue(key, tampered); ok {
		t.Errorf("verifyCookieValue accepted a tampered signature")
	}
}

func TestVerifyCookieValue_DifferentKeyRejected(t *testing.T) {
	keyA := []byte("0123456789abcdef0123456789abcdef")
	keyB := []byte("ffffffffffffffffffffffffffffffff")
	cookie := signCookieValue(keyA, "x", time.Hour)
	if _, ok := verifyCookieValue(keyB, cookie); ok {
		t.Errorf("verifyCookieValue accepted a cookie signed with a different key")
	}
}

func TestVerifyCookieValue_ExpiredRejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cookie := signCookieValue(key, "x", -1*time.Minute)
	if _, ok := verifyCookieValue(key, cookie); ok {
		t.Errorf("verifyCookieValue accepted an expired cookie")
	}
}

// ─── JTI one-shot ───────────────────────────────────────────────

func TestRememberConsumedJTI_FirstWinsReplayLoses(t *testing.T) {
	// Fresh JTI value per test (sync.Map is global)
	jti := "test-jti-" + t.Name()
	exp := time.Now().Add(10 * time.Minute).Unix()
	if !rememberConsumedJTI(jti, exp) {
		t.Fatalf("first consume should succeed")
	}
	if rememberConsumedJTI(jti, exp) {
		t.Errorf("second consume of the same jti should fail (replay)")
	}
}

// ─── URL-handshake opt-in gate ──────────────────────────────────

func TestConsoleEmbedAllowed_OffByDefault(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "")
	if consoleEmbedAllowed() {
		t.Errorf("URL handshake should be disabled when SKY_CONSOLE_EMBED_ORIGIN unset")
	}
}

func TestConsoleEmbedAllowed_OnWhenOriginSet(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	if !consoleEmbedAllowed() {
		t.Errorf("URL handshake should be enabled when SKY_CONSOLE_EMBED_ORIGIN is set")
	}
}

func TestURLHandshake_BlockedWhenEmbedOriginUnset(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "")
	secret := "a-32-byte-or-longer-test-secret-key"
	tok, _ := MintConsoleUrlToken(secret, "x", "1", 10*time.Minute)
	called := false
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r.Header.Set("Origin", "https://dev.example") // even with origin, off → block
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if called {
		t.Errorf("inner reached despite URL handshake being off (SKY_CONSOLE_EMBED_ORIGIN unset)")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", w.Result().StatusCode)
	}
}

func TestURLHandshake_BlockedWhenOriginMismatch(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://allowed.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	tok, _ := MintConsoleUrlToken(secret, "x", "1", 10*time.Minute)
	called := false
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if called {
		t.Errorf("inner reached with origin mismatch")
	}
}

// ─── JTI replay against the live consoleTokenAuth ───────────────

func TestURLHandshake_OneShotJTI(t *testing.T) {
	t.Setenv("SKY_CONSOLE_EMBED_ORIGIN", "https://dev.example")
	secret := "a-32-byte-or-longer-test-secret-key"
	tok, _ := MintConsoleUrlToken(secret, "x", "build-1", 10*time.Minute)
	gate := consoleTokenAuth(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// First redemption succeeds (302 redirect with session cookie).
	r1 := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r1.Header.Set("Origin", "https://dev.example")
	w1 := httptest.NewRecorder()
	gate.ServeHTTP(w1, r1)
	if w1.Result().StatusCode != http.StatusFound {
		t.Fatalf("first redeem: got %d, want 302", w1.Result().StatusCode)
	}

	// Second redemption of the SAME token must fail (one-shot).
	r2 := httptest.NewRequest("GET", "/_sky/console/?token="+tok, nil)
	r2.Header.Set("Origin", "https://dev.example")
	w2 := httptest.NewRecorder()
	gate.ServeHTTP(w2, r2)
	if w2.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("jti replay: got %d, want 401", w2.Result().StatusCode)
	}
}

// ─── Login POST flow ────────────────────────────────────────────

func TestConsoleLogin_AcceptsCorrectToken(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "my-test-token-32-bytes-of-data-xxx")
	ResetConsoleAuthStateForTesting()
	st := loadConsoleAuthState()

	form := strings.NewReader("token=my-test-token-32-bytes-of-data-xxx")
	r := httptest.NewRequest("POST", "/_sky/console/_login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleConsoleLogin(w, r, st)

	if w.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("good token login: got %d, want 303", w.Result().StatusCode)
	}
	if findSetCookie(w.Result().Header, consoleAuthCookieV2Name) == nil {
		t.Errorf("good token login should set the __Host- cookie")
	}
}

func TestConsoleLogin_RejectsBadToken(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "my-test-token-32-bytes-of-data-xxx")
	ResetConsoleAuthStateForTesting()
	st := loadConsoleAuthState()

	form := strings.NewReader("token=wrong-token")
	r := httptest.NewRequest("POST", "/_sky/console/_login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleConsoleLogin(w, r, st)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token login: got %d, want 401", w.Result().StatusCode)
	}
	if findSetCookie(w.Result().Header, consoleAuthCookieV2Name) != nil {
		t.Errorf("bad token login should NOT set the __Host- cookie")
	}
}

func TestConsoleLogin_RedirectStaysUnderConsolePath(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "my-test-token-32-bytes-of-data-xxx")
	ResetConsoleAuthStateForTesting()
	st := loadConsoleAuthState()

	// Attempt to set the redirect outside /_sky/console.
	form := strings.NewReader("token=my-test-token-32-bytes-of-data-xxx&redirect=https%3A%2F%2Fattacker.example")
	r := httptest.NewRequest("POST", "/_sky/console/_login", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleConsoleLogin(w, r, st)

	loc := w.Result().Header.Get("Location")
	if loc != "/_sky/console" {
		t.Errorf("login redirect should clamp to /_sky/console, got %q (open-redirect surface)", loc)
	}
}

// ─── Mode dispatch via evaluateConsoleAuth ──────────────────────

func TestEvaluateConsoleAuth_TokenMode_NoCookie_Returns401(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-token-aaaaaaaaaaaaaaaaaaaa")
	ResetConsoleAuthStateForTesting()
	// avoid serverless env interference
	withServerlessEnv(t, nil)
	r := httptest.NewRequest("GET", "/_sky/console", nil)
	w := httptest.NewRecorder()
	if ok := evaluateConsoleAuth(w, r); ok {
		t.Errorf("expected token-mode + no cookie → deny, got pass")
	}
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", w.Result().StatusCode)
	}
}

func TestEvaluateConsoleAuth_TokenMode_ValidCookie_Allows(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-token-bbbbbbbbbbbbbbbbbbbb")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	st := loadConsoleAuthState()
	cookieVal := signCookieValue(st.signKey, "user", time.Hour)

	r := httptest.NewRequest("GET", "/_sky/console", nil)
	r.AddCookie(&http.Cookie{Name: consoleAuthCookieV2Name, Value: cookieVal})
	w := httptest.NewRecorder()
	if ok := evaluateConsoleAuth(w, r); !ok {
		t.Errorf("expected token-mode + valid cookie → allow, got deny (status %d)", w.Result().StatusCode)
	}
}

func TestEvaluateConsoleAuth_AppMode_NoCallback_Denies(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	SetConsoleAuthCallback(nil)
	r := httptest.NewRequest("GET", "/_sky/console", nil)
	w := httptest.NewRecorder()
	if ok := evaluateConsoleAuth(w, r); ok {
		t.Errorf("app-mode + no callback → expected deny, got pass")
	}
	if w.Result().StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", w.Result().StatusCode)
	}
}

func TestEvaluateConsoleAuth_UnsetProd_Returns503(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("ENV", "production")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	r := httptest.NewRequest("GET", "/_sky/console", nil)
	w := httptest.NewRecorder()
	if ok := evaluateConsoleAuth(w, r); ok {
		t.Errorf("unset-prod → expected deny, got pass")
	}
	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", w.Result().StatusCode)
	}
	if !strings.Contains(w.Body.String(), "SKY_CONSOLE_AUTH") {
		t.Errorf("503 body should reference the SKY_CONSOLE_AUTH env var")
	}
}

// ─── Mount declines path ────────────────────────────────────────

func TestMountEmbeddedConsole_ProductionUnsetDeclines(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("ENV", "production")
	ResetConsoleAuthStateForTesting()
	mux := http.NewServeMux()
	MountEmbeddedConsole(mux)
	// Verify nothing answered: GET /_sky/console/_login returns 404 (no handler).
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_sky/console/_login", nil)
	mux.ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusNotFound {
		t.Errorf("production + unset → /_sky/console/_login should be 404 (not mounted), got %d", w.Result().StatusCode)
	}
}

func TestMountEmbeddedConsole_OffModeDeclines(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "off")
	ResetConsoleAuthStateForTesting()
	mux := http.NewServeMux()
	MountEmbeddedConsole(mux)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_sky/console/_login", nil)
	mux.ServeHTTP(w, r)
	if w.Result().StatusCode != http.StatusNotFound {
		t.Errorf("off mode → _login should be 404, got %d", w.Result().StatusCode)
	}
}
