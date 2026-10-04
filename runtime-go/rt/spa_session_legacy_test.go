//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// E-3 and A-2b (spa_session_legacy.go): a pre-v0.27.0 Sky.Spa token (no
// `sid`) is converted once, idempotently within a grace window, and refused
// after it; forged and expired tokens are never converted; the session moves
// from the shared `sky_sid` cookie to `sky_spa`.

// legacySpaToken signs a token the way v0.26.1 did: the projection claims and
// an expiry, no session id.
func legacySpaToken(t *testing.T, secret Secret, ttl int) string {
	t.Helper()
	r, ok := Auth_signToken(secret, spaSignedIn(), ttl).(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("Auth.signToken: %#v", r)
	}
	return r.OkValue.(string)
}

func spaRequestWith(cookies map[string]string) SkyRequest {
	return SkyRequest{Method: "POST", Path: "/_rpc/X", Cookies: cookies}
}

func resetSpaLegacyCache(t *testing.T) {
	t.Helper()
	spaLegacyMu.Lock()
	spaLegacyCache = map[string]spaLegacyEntry{}
	spaLegacyMu.Unlock()
	t.Cleanup(func() {
		spaLegacyMu.Lock()
		spaLegacyCache = map[string]spaLegacyEntry{}
		spaLegacyMu.Unlock()
	})
}

func TestSpaLegacyTokenIsConvertedOnceAndIdempotently(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	legacy := legacySpaToken(t, spaTestSecret, 3600)
	if _, ok := spaVerifyClaims(legacy); ok {
		t.Fatal("precondition: a token without a sid must not verify on its own")
	}
	req := spaRequestWith(map[string]string{"sky_sid": legacy})
	tok := Spa_sessionToken(spaTestSecret, req).(string)
	claims, ok := spaVerifyClaims(tok)
	if !ok {
		t.Fatalf("the converted token does not verify: %q", tok)
	}
	if claims["p0"] != spaSignedIn()["p0"] {
		t.Fatalf("the converted token lost the identity: %v", claims)
	}
	sid := claimString(claims, "sid")
	if sid == "" {
		t.Fatal("the converted token has no sid")
	}
	// The same request verifies once per identity field; parallel RPCs and
	// other tabs send the same cookie: one sid, one token, within the window.
	for i := 0; i < 3; i++ {
		again := Spa_sessionToken(spaTestSecret, req).(string)
		if again != tok {
			t.Fatalf("presentation %d converted to a different token", i+2)
		}
	}
	// Another process (a replica, or after a restart) within the window
	// converts to the same sid, from the store record.
	resetSpaLegacyCache(t)
	other := Spa_sessionToken(spaTestSecret, req).(string)
	oc, ok := spaVerifyClaims(other)
	if !ok || claimString(oc, "sid") != sid {
		t.Fatalf("a second process converted to sid %q, want %q", claimString(oc, "sid"), sid)
	}
	// The new sid can be signed out like any other.
	if r := spaEnd(t, tok); r.Tag != 0 {
		t.Fatalf("sign-out of the converted session failed: %#v", r)
	}
	if _, ok := spaVerifyClaims(tok); ok {
		t.Fatal("the converted session survived its sign-out")
	}
}

func TestSpaLegacyTokenIsRefusedAfterTheGraceWindow(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	prev := spaLegacyGrace
	spaLegacyGrace = 50 * time.Millisecond
	t.Cleanup(func() { spaLegacyGrace = prev })
	legacy := legacySpaToken(t, spaTestSecret, 3600)
	req := spaRequestWith(map[string]string{"sky_sid": legacy})
	if tok := Spa_sessionToken(spaTestSecret, req).(string); tok == "" {
		t.Fatal("first presentation was not converted")
	}
	time.Sleep(120 * time.Millisecond)
	if tok := Spa_sessionToken(spaTestSecret, req).(string); tok != "" {
		t.Fatalf("a legacy token replayed after the window was converted again: %q", tok)
	}
	resetSpaLegacyCache(t) // another process: the store record refuses too
	if tok := Spa_sessionToken(spaTestSecret, req).(string); tok != "" {
		t.Fatalf("a replayed legacy token was converted by another process: %q", tok)
	}
}

func TestSpaLegacyForgedAndExpiredTokensAreNotConverted(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	other := Secret{v: "ffffffffffffffffffffffffffffffffffffffffff"}
	forged := legacySpaToken(t, other, 3600)
	if tok := Spa_sessionToken(spaTestSecret, spaRequestWith(map[string]string{"sky_sid": forged})).(string); tok != "" {
		t.Fatalf("a token signed with another key was converted: %q", tok)
	}
	expired := legacySpaToken(t, spaTestSecret, -10)
	if tok := Spa_sessionToken(spaTestSecret, spaRequestWith(map[string]string{"sky_sid": expired})).(string); tok != "" {
		t.Fatalf("an expired token was converted: %q", tok)
	}
	// A Sky.Live session id in `sky_sid` (the same host) is not ours.
	live := newLiveSessionID()
	if tok := Spa_sessionToken(spaTestSecret, spaRequestWith(map[string]string{"sky_sid": live})).(string); tok != "" {
		t.Fatalf("a Sky.Live id was taken as a Spa token: %q", tok)
	}
}

func TestSpaLegacyConversionFailsClosedWhenTheStoreIsDown(t *testing.T) {
	resetSpaLegacyCache(t)
	restore := setSpaRevocationStoreForTest(nil, os.ErrPermission)
	t.Cleanup(restore)
	legacy := legacySpaToken(t, spaTestSecret, 3600)
	if tok := Spa_sessionToken(spaTestSecret, spaRequestWith(map[string]string{"sky_sid": legacy})).(string); tok != "" {
		t.Fatalf("a legacy token was converted with no store to record it: %q", tok)
	}
}

// A-2b: `sky_spa` wins, and a legacy-cookie session is moved to `sky_spa` on
// the response, with the legacy cookie expired.
func TestSpaSessionCookieMovesFromSkySidToSkySpa(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	current := spaSign(t, "", spaSignedIn())
	withBoth := spaRequestWith(map[string]string{"sky_spa": current, "sky_sid": newLiveSessionID()})
	if got := Spa_sessionToken(spaTestSecret, withBoth).(string); got != current {
		t.Fatalf("sky_spa must win: got %q", got)
	}
	if out := Spa_sessionCookies(spaTestSecret, withBoth, SkyResponse{Status: 200}).(SkyResponse); len(out.Cookies) != 0 {
		t.Fatalf("a request that already uses sky_spa got cookies: %v", out.Cookies)
	}
	// A v0.27 token still under the old name moves as it is.
	old := spaRequestWith(map[string]string{"sky_sid": current})
	if got := Spa_sessionToken(spaTestSecret, old).(string); got != current {
		t.Fatalf("a sid-bearing token under sky_sid was not read: %q", got)
	}
	out := Spa_sessionCookies(spaTestSecret, old, SkyResponse{Status: 200}).(SkyResponse)
	joined := strings.Join(out.Cookies, "\n")
	if !strings.Contains(joined, "sky_spa="+current) || !strings.Contains(joined, "sky_sid=;") || !strings.Contains(joined, "Max-Age=0") {
		t.Fatalf("response cookies do not move the session: %v", out.Cookies)
	}
	// A response that already sets sky_spa (the establishing branch) keeps it.
	pre := addSetCookie(SkyResponse{Status: 200}, "sky_spa=fresh; Path=/")
	out = Spa_sessionCookies(spaTestSecret, old, pre).(SkyResponse)
	if n := strings.Count(strings.Join(out.Cookies, "\n"), "sky_spa="); n != 1 {
		t.Fatalf("sky_spa set %d times: %v", n, out.Cookies)
	}
	// A leftover Spa token in sky_sid next to sky_spa (the conversion answer
	// set sky_spa on a path that did not expire sky_sid) is expired now, and
	// sky_spa is not rewritten.
	leftover := spaRequestWith(map[string]string{"sky_spa": current, "sky_sid": current})
	out = Spa_sessionCookies(spaTestSecret, leftover, SkyResponse{Status: 200}).(SkyResponse)
	joined = strings.Join(out.Cookies, "\n")
	if !strings.Contains(joined, "sky_sid=;") || !strings.Contains(joined, "Max-Age=0") || strings.Contains(joined, "sky_spa=") {
		t.Fatalf("a leftover sky_sid next to sky_spa was not expired alone: %v", out.Cookies)
	}
	// A Sky.Live id in sky_sid is never touched.
	live := spaRequestWith(map[string]string{"sky_sid": newLiveSessionID()})
	if out := Spa_sessionCookies(spaTestSecret, live, SkyResponse{Status: 200}).(SkyResponse); len(out.Cookies) != 0 {
		t.Fatalf("a Sky.Live cookie was expired by the Spa backend: %v", out.Cookies)
	}
}

// A-6: in production, no configured store and an unwritable data dir refuse
// every signed session (never a silent memory fallback), and the boot hook
// is an Err so the backend refuses to start.
func TestSpaRevocationStoreRefusesAnUnwritableDataDirInProduction(t *testing.T) {
	restore := setSpaRevocationStoreForTest(nil, nil)
	t.Cleanup(restore)
	spaRevMu.Lock()
	spaRev = spaRevState{}
	spaRevMu.Unlock()
	t.Setenv("SKY_LIVE_STORE", "")
	t.Setenv("SKY_LIVE_STORE_PATH", "")
	t.Setenv(spaSessionSecretEnv, string(spaTestSecret.v))
	t.Setenv("ENV", "production")
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKY_DATA_DIR", filepath.Join(file, "data"))
	if _, err := openSpaRevocationStore(); err == nil {
		t.Fatal("production opened a sign-out store with an unwritable data dir (memory fallback)")
	}
	res := Spa_sessionBoot(nil).(func() any)().(SkyResult[any, any])
	if res.Tag == 0 {
		t.Fatal("Spa_sessionBoot started a production backend that cannot record sign-outs")
	}
	if msg := errorMessage(res.ErrValue); !strings.Contains(msg, "SKY_DATA_DIR") ||
		!strings.Contains(msg, "see docs/migration/v0.27.md#spa-sign-out-store") {
		t.Fatalf("boot refusal does not say how to fix it: %q", msg)
	}
	// Development keeps the memory fallback.
	t.Setenv("ENV", "")
	st, err := openSpaRevocationStore()
	if err != nil {
		t.Fatalf("development refused a memory fallback: %v", err)
	}
	_ = st.Close()
}

// A-6: a configured store that is down at boot keeps the per-request retry;
// the boot hook does not refuse.
func TestSpaSessionBootToleratesAConfiguredStoreThatIsDown(t *testing.T) {
	restore := setSpaRevocationStoreForTest(nil, os.ErrDeadlineExceeded)
	t.Cleanup(restore)
	t.Setenv("SKY_LIVE_STORE", "postgres")
	t.Setenv("ENV", "production")
	t.Setenv(spaSessionSecretEnv, string(spaTestSecret.v))
	res := Spa_sessionBoot(nil).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("a configured store down at boot refused the start: %s", errorMessage(res.ErrValue))
	}
}

// A-7: a development key is `.dev`; a legacy key in development is renamed;
// production never reads `.dev` and never renames.
func TestSpaSessionSecretDevKeyIsNeverReadInProduction(t *testing.T) {
	t.Setenv(spaSessionSecretEnv, "")
	dir := t.TempDir()
	t.Setenv("SKY_DATA_DIR", dir)
	legacy := filepath.Join(dir, "spa-session-secret")
	devKey := strings.Repeat("d", 64)
	if err := os.WriteFile(legacy, []byte(devKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV", "")
	if got := resolveSpaSessionSecret(); got != devKey {
		t.Fatalf("development lost its existing key: %q", got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("the development key was not moved off the production name")
	}
	if b, _ := os.ReadFile(legacy + ".dev"); string(b) != devKey {
		t.Fatalf("the .dev key is %q", b)
	}
	t.Setenv("ENV", "production")
	prodKey := resolveSpaSessionSecret()
	if prodKey == devKey {
		t.Fatal("production signed with the development key")
	}
	if b, _ := os.ReadFile(legacy); string(b) != prodKey {
		t.Fatal("production did not persist its own key under the production name")
	}
	if b, _ := os.ReadFile(legacy + ".dev"); string(b) != devKey {
		t.Fatal("production touched the .dev key")
	}
	// A production key already under the production name is kept (no rename).
	if again := resolveSpaSessionSecret(); again != prodKey {
		t.Fatal("production did not re-read its persisted key")
	}
}

// A-7: production with no writable data dir says so in the start-up report.
func TestSpaSessionSecretUnpersistableInProductionIsLoud(t *testing.T) {
	t.Setenv(spaSessionSecretEnv, "")
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKY_DATA_DIR", filepath.Join(file, "data"))
	t.Setenv("ENV", "production")
	_ = resolveSpaSessionSecret()
	if w := strings.Join(startupWarningLines(), "\n"); !strings.Contains(w, "session key not persisted") ||
		!strings.Contains(w, "see docs/migration/v0.27.md#spa-session-key") {
		t.Fatalf("no start-up report line for an unpersistable production key: %v", startupWarningLines())
	}
}

// A Sky.Spa backend with no signed-session field keeps no server-side session:
// the model lives in the browser. Its boot hook says so, names the configured
// store it does not use, and opens no store. Before this, such a backend said
// nothing about a store at all, and the only store line in its log was the
// inline console's, which read as the app's store falling back to memory.
func TestSpaSessionBootWithoutASessionSaysTheStoreIsUnused(t *testing.T) {
	restore := setSpaRevocationStoreForTest(nil, nil)
	t.Cleanup(restore)
	spaRevMu.Lock()
	spaRev = spaRevState{}
	spaRevMu.Unlock()
	t.Setenv("ENV", "production")
	t.Setenv("SKY_LIVE_STORE", "postgres")
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	buf := captureLog(t)

	res := Spa_sessionBoot(false).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("a backend with no session refused to start: %s", errorMessage(res.ErrValue))
	}
	spaRevMu.Lock()
	opened := spaRev.opened
	spaRevMu.Unlock()
	if opened {
		t.Fatal("a backend with no session opened a sign-out store it never uses")
	}
	out := buf.String()
	for _, want := range []string{
		"[sky.spa] session store: none",
		"no server-side session",
		`SKY_LIVE_STORE ("postgres") is not used`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the boot report does not say %q:\n%s", want, out)
		}
	}
}

// With a signed session the boot hook opens the sign-out record store, and its
// banner says what the store holds.
func TestSpaSessionBootWithASessionNamesTheSignOutStore(t *testing.T) {
	restore := setSpaRevocationStoreForTest(nil, nil)
	t.Cleanup(restore)
	spaRevMu.Lock()
	spaRev = spaRevState{}
	spaRevMu.Unlock()
	t.Setenv("ENV", "")
	t.Setenv("SKY_LIVE_STORE", "memory")
	t.Setenv(spaSessionSecretEnv, string(spaTestSecret.v))
	buf := captureLog(t)

	res := Spa_sessionBoot(true).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("boot failed: %s", errorMessage(res.ErrValue))
	}
	if out := buf.String(); !strings.Contains(out, "[sky.spa] session store (sign-out records): memory (ttl=") {
		t.Fatalf("the sign-out store banner does not say what it holds:\n%s", out)
	}
}
