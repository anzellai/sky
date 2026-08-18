package rt

// auth_sliding_test.go — the grill-required gates for the sliding-auth feature
// (auth_sliding.go + Auth_signSlidingToken in db_auth.go). Each test is written
// so its named MUTATION (the way the code could regress) flips it red — see the
// per-test comment. Assertions read cookies through the STDLIB parser (the
// browser's view), never the predicate under test.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const slidingTestSecret = "sliding-test-secret-at-least-32-bytes-long!!"
const slidingTestEnv = "SKY_TEST_SLIDING_SECRET"
const slidingTestCookie = "sky_auth"

// slidingSign signs an arbitrary claim map with the shared HMAC path — the
// SAME signHS256Claims the production kernels use — so a test can plant exact
// iat/exp/aexp/w values (including the impossible exp>aexp a crafted attacker
// token could carry) that time.Now-based helpers could not.
func slidingSign(t *testing.T, claims map[string]any) string {
	t.Helper()
	kb, errRes := coerceAuthSecret(slidingTestSecret, "test")
	if errRes != nil {
		t.Fatalf("coerceAuthSecret: %v", errRes)
	}
	res := signHS256Claims(kb, claims, "test")
	tag, okv, _ := anyResultView(res)
	if tag != 0 {
		t.Fatalf("signHS256Claims returned Err")
	}
	tok, ok := okv.(string)
	if !ok || tok == "" {
		t.Fatalf("signHS256Claims produced no token")
	}
	return tok
}

// slidingClaimsOf verifies a token and returns its claims (fails the test on a
// verify error — used to read the re-issued token the middleware emitted).
func slidingClaimsOf(t *testing.T, token string) map[string]any {
	t.Helper()
	claims, ok := slidingVerifiedClaims(slidingTestSecret, token)
	if !ok {
		t.Fatalf("expected a verifiable token, got a verify failure")
	}
	return claims
}

// runSliding drives one request through AuthSlidingMiddleware with the given
// config + presented cookie, and returns the Set-Cookie lines on the response
// plus whether the inner handler ran (it always should — the middleware never
// blocks). secretPresent=false leaves the env var unset (fail-open path).
func runSliding(t *testing.T, cfg *authSlidingConfig, cookieName, cookieVal string, secretPresent bool) (setCookies []string, handlerRan bool) {
	t.Helper()
	if secretPresent {
		os.Setenv(slidingTestEnv, slidingTestSecret)
		defer os.Unsetenv(slidingTestEnv)
	} else {
		os.Unsetenv(slidingTestEnv)
	}
	authSlidingCfg.Store(cfg)
	defer ResetAuthSlidingConfig()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		w.WriteHeader(200)
	})
	h := AuthSlidingMiddleware(next)
	req := httptest.NewRequest("POST", "/_sky/event", nil)
	if cookieName != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieVal})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Header["Set-Cookie"], handlerRan
}

// reissuedCookie returns the re-issued auth cookie (parsed) if the response set
// one for cfgCookie, else nil. Uses the stdlib parser.
func reissuedCookie(t *testing.T, lines []string, cfgCookie string) *http.Cookie {
	t.Helper()
	for _, line := range lines {
		resp := &http.Response{Header: http.Header{"Set-Cookie": []string{line}}}
		for _, c := range resp.Cookies() {
			if c.Name == cfgCookie {
				return c
			}
		}
	}
	return nil
}

func stdCfg() *authSlidingConfig {
	return &authSlidingConfig{cookie: slidingTestCookie, secretEnv: slidingTestEnv, sameSite: "Strict"}
}

// Gate 1 — idle-timeout / no-resurrection: an EXPIRED token gets NO re-issue.
// MUTATION: skip the verify (step 3) → an expired token's claims decode and it
// re-issues → this test catches it (a re-issue cookie appears).
func TestSliding_ExpiredTokenNoReissue(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub":  "u1",
		"iat":  now - 3600,
		"exp":  now - 100, // EXPIRED
		"aexp": now + 100000,
		"w":    int64(900),
	})
	lines, ran := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	if !ran {
		t.Fatal("handler must still run (middleware never blocks)")
	}
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("expired token was resurrected: re-issue cookie %q set", c.Value)
	}
}

// Gate 2 — absolute cap: a verifiable token whose now>=aexp gets NO re-issue,
// and a token whose window would overshoot the cap is clamped to exp'=aexp.
// MUTATION: drop the `now >= aexp` check (step 5) → a past-cap token re-issues.
func TestSliding_AbsoluteCapNoReissue(t *testing.T) {
	now := time.Now().Unix()
	// exp>now so it VERIFIES, but aexp already passed → cap reached.
	tok := slidingSign(t, map[string]any{
		"sub":  "u1",
		"iat":  now - 3600,
		"exp":  now + 100, // verifies
		"aexp": now - 10,  // cap already passed
		"w":    int64(900),
	})
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("token past its absolute cap was re-issued: %q", c.Value)
	}
}

func TestSliding_ClampsExpToCap(t *testing.T) {
	now := time.Now().Unix()
	aexp := now + 300 // now+w (=now+900) overshoots this
	tok := slidingSign(t, map[string]any{
		"sub":  "u1",
		"iat":  now - 3600, // past half-life
		"exp":  now + 100,  // verifies
		"aexp": aexp,
		"w":    int64(900),
	})
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	c := reissuedCookie(t, lines, slidingTestCookie)
	if c == nil {
		t.Fatal("expected a re-issue (past half-life, under cap)")
	}
	claims := slidingClaimsOf(t, c.Value)
	gotExp, _ := slidingClaimFloat(claims, "exp")
	if int64(gotExp) != aexp {
		t.Fatalf("exp' should clamp to aexp=%d, got %d", aexp, int64(gotExp))
	}
	if gotExp > float64(aexp) {
		t.Fatalf("sum of windows pushed exp past aexp: exp=%v aexp=%d", gotExp, aexp)
	}
}

// Gate 3 — cap verbatim: aexp is byte-stable across a re-issue, and exp never
// exceeds aexp. MUTATION: recompute aexp at re-issue (e.g. now+maxLife) → the
// re-issued aexp differs from the original.
func TestSliding_CapVerbatimAcrossReissue(t *testing.T) {
	now := time.Now().Unix()
	aexp := now + 100000
	tok := slidingSign(t, map[string]any{
		"sub":  "u1",
		"iat":  now - 3600,
		"exp":  now + 100,
		"aexp": aexp,
		"w":    int64(900),
	})
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	c := reissuedCookie(t, lines, slidingTestCookie)
	if c == nil {
		t.Fatal("expected a re-issue")
	}
	claims := slidingClaimsOf(t, c.Value)
	gotAexp, _ := slidingClaimFloat(claims, "aexp")
	if int64(gotAexp) != aexp {
		t.Fatalf("aexp not carried verbatim: want %d, got %d", aexp, int64(gotAexp))
	}
	gotExp, _ := slidingClaimFloat(claims, "exp")
	if gotExp > gotAexp {
		t.Fatalf("exp %v exceeds aexp %v after re-issue", gotExp, gotAexp)
	}
	// w carried verbatim too (the idle window must not shrink).
	gotW, _ := slidingClaimFloat(claims, "w")
	if int64(gotW) != 900 {
		t.Fatalf("w not carried verbatim: want 900, got %d", int64(gotW))
	}
}

// Gate 4 — tamper: flipping a signature byte makes verify fail → NO re-issue.
// MUTATION: verify-after-decode ordering (read claims before verify) → the
// tampered token's claims are used and it re-issues.
func TestSliding_TamperedTokenNoReissue(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	// Flip a MIDDLE byte of the signature segment. NOT the last char: a
	// base64url signature's final char carries only its top bits (a 43-char
	// segment encodes 32 bytes = 256 significant bits of 258), so {A,B,C,D}
	// all decode to the same trailing byte — flipping the last char to 'A'/'B'
	// leaves ~6.25% of tokens byte-identical, which verifies and makes this
	// gate both flaky-RED and vacuous. A middle byte always changes the
	// decoded signature. Assert the token string actually changed as a guard.
	i := strings.LastIndexByte(tok, '.')
	if i < 0 || i+3 >= len(tok) {
		t.Fatal("unexpected JWT shape")
	}
	mid := i + 1 + (len(tok)-i-1)/2 // middle of the signature segment
	orig := tok[mid]
	repl := byte('A')
	if orig == 'A' {
		repl = 'B'
	}
	tampered := tok[:mid] + string(repl) + tok[mid+1:]
	if tampered == tok {
		t.Fatal("tamper did not change the token — the mutation is vacuous")
	}
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tampered, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("tampered token was re-issued: %q", c.Value)
	}
}

// Gate 5 — revocation: revokedCheck true → NO re-issue; false → re-issue.
// MUTATION: consult the hook at the wrong time / ignore its result → a revoked
// user's token keeps sliding.
func TestSliding_RevokedNoReissue(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	cfg := stdCfg()
	cfg.revokedCheck = func(sub any) any { return func() any { return Ok[any, any](true) } } // revoked
	lines, _ := runSliding(t, cfg, slidingTestCookie, tok, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("revoked user's token was re-issued: %q", c.Value)
	}
}

func TestSliding_NotRevokedReissues(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	cfg := stdCfg()
	cfg.revokedCheck = func(sub any) any { return func() any { return Ok[any, any](false) } } // active
	lines, _ := runSliding(t, cfg, slidingTestCookie, tok, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c == nil {
		t.Fatal("active user's token should have been re-issued")
	}
}

// revokedCheck ERROR fails closed (no re-issue) — a DB blip must not let a
// possibly-revoked token keep sliding.
func TestSliding_RevokedCheckErrorFailsClosed(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	cfg := stdCfg()
	cfg.revokedCheck = func(sub any) any {
		return func() any { return Err[any, any](ErrFfi("db down")) }
	}
	lines, _ := runSliding(t, cfg, slidingTestCookie, tok, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("revocation-check error should block re-issue, got %q", c.Value)
	}
}

// Gate 6 — fail-closed claims: each of {malformed aexp, missing aexp, missing
// iat, missing exp, missing w} → NO re-issue. MUTATION: default a missing claim
// to 0 instead of bailing → a cap-less legacy token slides forever.
func TestSliding_FailClosedClaims(t *testing.T) {
	now := time.Now().Unix()
	base := func() map[string]any {
		return map[string]any{
			"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
		}
	}
	cases := map[string]func(map[string]any){
		"malformed-aexp": func(m map[string]any) { m["aexp"] = "not-a-number" },
		"missing-aexp":   func(m map[string]any) { delete(m, "aexp") },
		"missing-iat":    func(m map[string]any) { delete(m, "iat") },
		"missing-exp":    func(m map[string]any) { delete(m, "exp") },
		"missing-w":      func(m map[string]any) { delete(m, "w") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			claims := base()
			mut(claims)
			// A token with no exp verifies (no expiry to check); with exp it must
			// still be in-date so we exercise the CLAIM gate, not the verify gate.
			tok := slidingSign(t, claims)
			lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
			if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
				t.Fatalf("%s should fail closed (no re-issue), got %q", name, c.Value)
			}
		})
	}
}

// Gate 7 — window>maxLifetime rejected at signSlidingToken.
// MUTATION: drop the gate → a token with exp>aexp is issued.
func TestSliding_SignRejectsWindowOverCap(t *testing.T) {
	res := Auth_signSlidingToken(slidingTestSecret, map[string]any{"sub": "u1"},
		map[string]any{"WindowSeconds": 100000, "MaxLifetimeSeconds": 900})
	if tag, _, _ := anyResultView(res); tag != 1 {
		t.Fatal("signSlidingToken must reject windowSeconds > maxLifetimeSeconds")
	}
}

func TestSliding_SignAcceptsWindowUnderCapAndStampsClaims(t *testing.T) {
	res := Auth_signSlidingToken(slidingTestSecret, map[string]any{"sub": "u1"},
		map[string]any{"WindowSeconds": 900, "MaxLifetimeSeconds": 86400})
	tag, okv, _ := anyResultView(res)
	if tag != 0 {
		t.Fatal("signSlidingToken should accept window <= cap")
	}
	tok, _ := okv.(string)
	claims := slidingClaimsOf(t, tok)
	for _, k := range []string{"iat", "exp", "aexp", "w"} {
		if _, ok := slidingClaimFloat(claims, k); !ok {
			t.Fatalf("expected numeric claim %q on the issued token", k)
		}
	}
	iat, _ := slidingClaimFloat(claims, "iat")
	exp, _ := slidingClaimFloat(claims, "exp")
	aexp, _ := slidingClaimFloat(claims, "aexp")
	w, _ := slidingClaimFloat(claims, "w")
	if int64(exp-iat) != 900 {
		t.Fatalf("exp-iat should equal window 900, got %d", int64(exp-iat))
	}
	if int64(aexp-iat) != 86400 {
		t.Fatalf("aexp-iat should equal maxLifetime 86400, got %d", int64(aexp-iat))
	}
	if int64(w) != 900 {
		t.Fatalf("w claim should equal window 900, got %d", int64(w))
	}
}

// Record-claims preservation: a claims RECORD (Go struct, as codegen emits
// `{ sub = "u1" }`) must keep its fields through signSlidingToken — the sliding
// middleware's revocation hook needs `sub`. MUTATION: drop struct support in
// authClaimsToMap → sub vanishes and revocation can never identify the user.
func TestSliding_RecordClaimsPreserveSub(t *testing.T) {
	claims := struct{ Sub string }{Sub: "u1"} // mirrors the emitted struct{ Sub string }
	res := Auth_signSlidingToken(slidingTestSecret, claims,
		map[string]any{"WindowSeconds": 900, "MaxLifetimeSeconds": 86400})
	tag, okv, _ := anyResultView(res)
	if tag != 0 {
		t.Fatal("signSlidingToken should accept a record-claims value")
	}
	tok, _ := okv.(string)
	c := slidingClaimsOf(t, tok)
	sub, ok := slidingClaimString(c, "sub")
	if !ok || sub != "u1" {
		t.Fatalf("record claim `sub` should survive signing, got %q ok=%v", sub, ok)
	}
}

// Gate 8 — cookie attributes + G4 no-drift. The re-issue cookie carries
// HttpOnly, Path=/, SameSite=<builder default Strict>, and the SAME Secure the
// shared cookieSecureFor yields (prod → Secure, localhost → not). The login
// setter and the re-issue build IDENTICAL attributes (both via
// buildSlidingAuthCookie). MUTATION: inline attributes at one site → they drift.
func TestSliding_ReissueCookieAttributes(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	c := reissuedCookie(t, lines, slidingTestCookie)
	if c == nil {
		t.Fatal("expected a re-issue cookie")
	}
	if !c.HttpOnly {
		t.Error("re-issue cookie must be HttpOnly")
	}
	if c.Path != "/" {
		t.Errorf("re-issue cookie Path = %q, want /", c.Path)
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("re-issue cookie SameSite = %v, want Strict (builder default)", c.SameSite)
	}
	if c.MaxAge != slidingCookieMaxAgeSeconds(resolveSessionTTL()) {
		t.Errorf("re-issue cookie MaxAge = %d, want sliding floor %d", c.MaxAge, slidingCookieMaxAgeSeconds(resolveSessionTTL()))
	}
}

func TestSliding_SecureFollowsSharedHelper(t *testing.T) {
	// localhost, no prod gate → not Secure.
	os.Unsetenv("ENV")
	os.Unsetenv("SKY_ENV")
	rLocal := httptest.NewRequest("POST", "http://localhost/_sky/event", nil)
	cLocal := buildSlidingAuthCookie(rLocal, "sky_auth", "v", "Strict")
	if cLocal.Secure {
		t.Error("localhost cookie must not be Secure")
	}
	// production gate on → Secure even over plain HTTP.
	os.Setenv("ENV", "production")
	defer os.Unsetenv("ENV")
	cProd := buildSlidingAuthCookie(rLocal, "sky_auth", "v", "Strict")
	if !cProd.Secure {
		t.Error("production cookie must be Secure")
	}
}

// G4 no-drift proof: the login setter and the re-issue produce byte-identical
// cookie attributes because both flow through buildSlidingAuthCookie.
func TestSliding_LoginAndReissueAttributesIdentical(t *testing.T) {
	os.Setenv("ENV", "production")
	defer os.Unsetenv("ENV")
	r := httptest.NewRequest("POST", "https://app.example/_sky/event", nil)
	login := buildSlidingAuthCookie(r, "sky_auth", "tokA", "Strict")
	reissue := buildSlidingAuthCookie(r, "sky_auth", "tokB", "Strict")
	if login.Name != reissue.Name || login.Path != reissue.Path ||
		login.HttpOnly != reissue.HttpOnly || login.SameSite != reissue.SameSite ||
		login.MaxAge != reissue.MaxAge || login.Secure != reissue.Secure {
		t.Fatalf("login vs re-issue attribute drift:\n login=%+v\n reissue=%+v", login, reissue)
	}
}

// Gate 9 — safe half-configured states.
// (a) wrong cookie name presented → no 500, no stray mint, handler still runs.
func TestSliding_WrongCookieNameNoMint(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	// Present the token under the WRONG cookie name.
	lines, ran := runSliding(t, stdCfg(), "some_other_cookie", tok, true)
	if !ran {
		t.Fatal("handler must run")
	}
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("wrong cookie name should not mint auth cookie, got %q", c.Value)
	}
}

// (b) a half-configured builder (missing secretEnv) registers nothing — inert.
func TestSliding_HalfConfiguredIsInert(t *testing.T) {
	if parseAuthSlidingConfig(map[string]any{"Cookie": "sky_auth"}) != nil {
		t.Fatal("record missing secretEnv must yield no config (inert)")
	}
	if parseAuthSlidingConfig(map[string]any{"SecretEnv": "X"}) != nil {
		t.Fatal("record missing cookie must yield no config (inert)")
	}
}

// (c) signSlidingToken WITHOUT a registered middleware is an inert fixed-exp
// token: no config → the middleware passes through and mints nothing even for a
// valid sliding token.
func TestSliding_NoConfigNoReissue(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	// nil config.
	lines, ran := runSliding(t, nil, slidingTestCookie, tok, true)
	if !ran {
		t.Fatal("handler must run with no config")
	}
	if len(lines) != 0 {
		t.Fatalf("no config should set no cookies, got %v", lines)
	}
}

// secret env unset → fail-open on the read (no mint, no crash).
func TestSliding_SecretUnsetFailsOpen(t *testing.T) {
	now := time.Now().Unix()
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now - 3600, "exp": now + 100, "aexp": now + 100000, "w": int64(900),
	})
	lines, ran := runSliding(t, stdCfg(), slidingTestCookie, tok, false /* secret unset */)
	if !ran {
		t.Fatal("handler must run when secret is unset")
	}
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("must not mint when the secret env is unset, got %q", c.Value)
	}
}

// Gate 10 — no re-issue storm: a token WITHIN its half-life window is not
// re-issued. MUTATION: re-issue every request → a fresh token re-issues again.
func TestSliding_NoReissueWithinHalfLife(t *testing.T) {
	now := time.Now().Unix()
	// iat = now → now < iat + w/2, so no re-issue yet.
	tok := slidingSign(t, map[string]any{
		"sub": "u1", "iat": now, "exp": now + 900, "aexp": now + 100000, "w": int64(900),
	})
	lines, _ := runSliding(t, stdCfg(), slidingTestCookie, tok, true)
	if c := reissuedCookie(t, lines, slidingTestCookie); c != nil {
		t.Fatalf("token within half-life must not re-issue (storm), got %q", c.Value)
	}
}
