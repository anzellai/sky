//go:build !js

package rt

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// The Sky.Spa signed session (spa_session_revocation.go). Before v0.27.0 a
// copy of the `sky_sid` cookie taken before sign-out still signed the user in
// after sign-out, until its 30-day expiry. These tests pin the three kernels
// the generated backend calls; the end-to-end proof on a built backend is
// `spa_sign_out_revokes_the_signed_session_cookie` in
// rust/crates/sky/tests/spa_split_flow.rs.

var spaTestSecret = Secret{v: "0123456789abcdef0123456789abcdef0123456789"}

func spaSignedIn() map[string]any {
	return map[string]any{"p0": `{"userId":"u1","role":"admin"}`}
}

func spaSign(t *testing.T, prev string, claims map[string]any) string {
	t.Helper()
	r, ok := Spa_signSession(spaTestSecret, prev, claims).(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("Spa_signSession failed: %#v", r)
	}
	return r.OkValue.(string)
}

func spaVerifyClaims(tok string) (map[string]any, bool) {
	return okClaims(Spa_verifySession(spaTestSecret, tok))
}

func spaEnd(t *testing.T, tok string) SkyResult[any, any] {
	t.Helper()
	thunk, ok := Spa_endSession(spaTestSecret, tok).(func() any)
	if !ok {
		t.Fatalf("Spa_endSession must return a Task thunk")
	}
	return thunk().(SkyResult[any, any])
}

func withMemoryRevocations(t *testing.T) *memoryStore {
	t.Helper()
	st := newMemoryStore(time.Minute)
	restore := setSpaRevocationStoreForTest(st, nil)
	t.Cleanup(func() { restore(); _ = st.Close() })
	return st
}

func TestSpaSignOutRefusesThePreSignOutCookie(t *testing.T) {
	withMemoryRevocations(t)
	tok := spaSign(t, "", spaSignedIn())
	claims, ok := spaVerifyClaims(tok)
	if !ok {
		t.Fatal("a freshly signed session must verify")
	}
	if claimString(claims, "sid") == "" || claims["p0"] != spaSignedIn()["p0"] {
		t.Fatalf("the token must carry a sid and the projection: %#v", claims)
	}
	copied := tok // the attacker's copy, taken before sign-out
	if r := spaEnd(t, tok); r.Tag != 0 {
		t.Fatalf("sign-out must succeed: %#v", r)
	}
	if _, ok := spaVerifyClaims(copied); ok {
		t.Fatal("SECURITY: a cookie copied before sign-out must be refused after sign-out")
	}
	// A fresh sign-in is a new session and works.
	if _, ok := spaVerifyClaims(spaSign(t, "", spaSignedIn())); !ok {
		t.Fatal("a fresh sign-in after a sign-out must verify")
	}
}

func TestSpaSignKeepsTheIdForTheSameProjectionAndRotatesOnChange(t *testing.T) {
	withMemoryRevocations(t)
	first := spaSign(t, "", spaSignedIn())
	c1, _ := spaVerifyClaims(first)

	// Same projection re-issued (a branch that re-writes the same session):
	// the id is kept, both cookies stay valid.
	again := spaSign(t, first, spaSignedIn())
	c2, ok := spaVerifyClaims(again)
	if !ok || claimString(c2, "sid") != claimString(c1, "sid") {
		t.Fatalf("an unchanged projection must keep its sid: %v vs %v", c1["sid"], c2["sid"])
	}
	if _, ok := spaVerifyClaims(first); !ok {
		t.Fatal("an unchanged projection must not end the current cookie")
	}

	// A change of identity (here a server-side sign-out: p0 = null) mints a
	// new id and ends the old one at once.
	out := spaSign(t, again, map[string]any{"p0": "null"})
	c3, ok := spaVerifyClaims(out)
	if !ok || claimString(c3, "sid") == claimString(c1, "sid") {
		t.Fatalf("a changed projection must rotate the sid: %v vs %v", c1["sid"], c3["sid"])
	}
	for name, tok := range map[string]string{"first": first, "again": again} {
		if _, ok := spaVerifyClaims(tok); ok {
			t.Fatalf("SECURITY: the %s pre-change cookie must be refused after the identity changed", name)
		}
	}

	// Sign-in from a signed-out cookie also rotates (no fixation of the id).
	in := spaSign(t, out, spaSignedIn())
	c4, _ := spaVerifyClaims(in)
	if claimString(c4, "sid") == claimString(c3, "sid") {
		t.Fatal("sign-in must mint a new sid")
	}
	if _, ok := spaVerifyClaims(out); ok {
		t.Fatal("the signed-out cookie presented at sign-in must be ended")
	}
}

func TestSpaVerifyRefusesATokenWithoutASessionId(t *testing.T) {
	withMemoryRevocations(t)
	// The shape the backend signed before v0.27.0: projection claims, no sid.
	r := Auth_signToken(spaTestSecret, spaSignedIn(), 3600).(SkyResult[any, any])
	if _, ok := spaVerifyClaims(r.OkValue.(string)); ok {
		t.Fatal("a token with no session id cannot be signed out, so it must be refused")
	}
}

func TestSpaReservedClaimsCannotBeSetByTheProjection(t *testing.T) {
	withMemoryRevocations(t)
	tok := spaSign(t, "", map[string]any{"p0": "x", "sid": "chosen", "exp": 1})
	c, ok := spaVerifyClaims(tok)
	if !ok || claimString(c, "sid") == "chosen" {
		t.Fatalf("the runtime owns sid/exp: %#v", c)
	}
}

func TestSpaEndSessionIgnoresForgedAndEmptyCookies(t *testing.T) {
	st := withMemoryRevocations(t)
	other := Secret{v: "ffffffffffffffffffffffffffffffffffffffffff"}
	r := Spa_signSession(other, "", spaSignedIn()).(SkyResult[any, any])
	for _, tok := range []string{"", "not-a-jwt", r.OkValue.(string)} {
		if res := spaEnd(t, tok); res.Tag != 0 {
			t.Fatalf("sign-out of %q must succeed with nothing to end: %#v", tok, res)
		}
	}
	st.mu.RLock()
	n := len(st.aliases)
	st.mu.RUnlock()
	if n != 0 {
		t.Fatalf("a forged or empty cookie must write no record, got %d", n)
	}
}

func TestSpaSessionCheckFailsClosedWhenTheStoreIsUnavailable(t *testing.T) {
	st := newMemoryStore(time.Minute)
	restore := setSpaRevocationStoreForTest(st, nil)
	tok := spaSign(t, "", spaSignedIn())
	restore()
	_ = st.Close()

	restore = setSpaRevocationStoreForTest(nil, errors.New("store down"))
	defer restore()
	res := Spa_verifySession(spaTestSecret, tok).(SkyResult[any, any])
	if res.Tag == 0 {
		t.Fatal("SECURITY: when the store cannot answer, the cookie must be refused (fail closed)")
	}
	if r := spaEnd(t, tok); r.Tag == 0 {
		t.Fatal("a sign-out that could not be recorded must be an Err, not a silent success")
	}
}

// The record lives as long as the TOKEN (30 days), not the store TTL, and a
// durable store keeps it across a reopen (a restart, or another replica).
func TestSpaSignOutRecordOutlivesTheStoreTTLAndARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := newSQLiteStore(path, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	restore := setSpaRevocationStoreForTest(st, nil)
	tok := spaSign(t, "", spaSignedIn())
	if r := spaEnd(t, tok); r.Tag != 0 {
		t.Fatalf("sign-out: %#v", r)
	}
	restore()
	_ = st.Close()

	// A sweep an hour past the store TTL must not drop the record.
	st2, err := newSQLiteStore(path, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if err := st2.cleanupOnce(st2.db, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	restore = setSpaRevocationStoreForTest(st2, nil)
	defer restore()
	if _, ok := spaVerifyClaims(tok); ok {
		t.Fatal("SECURITY: the sign-out record must survive a reopen and a sweep past the store TTL")
	}
	// A still-valid, never-signed-out cookie verifies against the reopened store.
	if _, ok := spaVerifyClaims(spaSign(t, "", spaSignedIn())); !ok {
		t.Fatal("a valid cookie must verify against the reopened store")
	}
}

// An expired record is not "ended" any more (its token has expired too), and
// the memory sweep removes it.
func TestSpaSignOutRecordExpiresWithItsToken(t *testing.T) {
	st := withMemoryRevocations(t)
	if err := spaEndSid("gone", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.putAliasUntil(spaRevocationKeyPrefix+"old", sessionAlias{}, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if ended, _ := spaSessionEnded("old"); ended {
		t.Fatal("an expired record must not be read")
	}
	st.mu.Lock()
	st.reapAliasesLocked(time.Now())
	n := len(st.aliases)
	st.mu.Unlock()
	if n != 0 {
		t.Fatalf("the sweep must drop expired records, %d left", n)
	}
}

// A store that could not be opened is tried again after spaRevRetryAfter, so a
// store that was down at first use is picked up once it is back.
func TestSpaRevocationStoreRetriesAFailedOpen(t *testing.T) {
	restore := setSpaRevocationStoreForTest(nil, errors.New("store down"))
	defer restore()
	if _, err := spaRevocationStore(); err == nil {
		t.Fatal("the injected failure must be returned")
	}
	spaRevMu.Lock()
	spaRev.retryAt = time.Now().Add(time.Hour)
	spaRevMu.Unlock()
	if _, err := spaRevocationStore(); err == nil {
		t.Fatal("before retryAt the failure must stand (fail closed)")
	}
	// Past retryAt the open runs again; with no store configured in the test
	// environment it resolves the data-dir sqlite file (SKY_DATA_DIR).
	t.Setenv("SKY_DATA_DIR", t.TempDir())
	t.Setenv("SKY_LIVE_STORE", "")
	spaRevMu.Lock()
	spaRev.retryAt = time.Now().Add(-time.Second)
	spaRevMu.Unlock()
	got, err := spaRevocationStore()
	if err != nil || got == nil {
		t.Fatalf("past retryAt the store must be opened again: %v", err)
	}
	_ = got.Close()
}
