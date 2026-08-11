package rt

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// L2 regression — the session cookie must SLIDE. On a request that already
// carries sky_sid, sessionIDNamed must re-issue the cookie with a fresh MaxAge,
// so an actively-browsed session always holds a young cookie. Pre-fix it
// returned the value WITHOUT a Set-Cookie, so the cookie died at its original
// fixed window while the server session kept sliding — an actively-used session
// past that window lost its cookie → new session → `init` wiped the Model
// (cart/auth/form) mid-use.
//
// v0.20.0: this test used to additionally assert `Max-Age=1800` — i.e. that the
// cookie lifetime was KEYED to the 30m TTL. That assertion pinned the defect
// rather than the invariant: an idle tab issues no request at all, so no
// re-issue happens, and a TTL-keyed Max-Age expired the cookie out from under a
// session the SSE heartbeat was still sliding (see
// TestSessionCookieMaxAgeOutlivesShortIdleTTL and
// TestIdleUnderLiveSSE_NextEventStillDispatches). What L2 is actually about is
// that the existing-cookie path RE-ISSUES; the lifetime rule lives in
// slidingCookieMaxAgeSeconds and is asserted there. So the Max-Age check below
// now pins the correct property — a fresh Max-Age that OUTLIVES the TTL.
func TestSessionCookieSlidesOnExisting(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "existing-sid"})
	rr := httptest.NewRecorder()

	sid := sessionIDNamed(req, rr, 30*time.Minute, "sky_sid")
	if sid != "existing-sid" {
		t.Fatalf("sid = %q, want the existing sid preserved", sid)
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if setCookie == "" {
		t.Fatal("L2: existing-cookie path must re-issue Set-Cookie (sliding window), got none")
	}
	if !strings.Contains(setCookie, "sky_sid=existing-sid") {
		t.Fatalf("re-issued cookie should carry the same sid: %q", setCookie)
	}
	// A fresh, PERSISTENT Max-Age that outlives the 30m TTL it guards.
	maxAge := maxAgeFromSetCookie(t, setCookie)
	if maxAge <= 0 {
		t.Fatalf("re-issued cookie must carry a persistent Max-Age: %q", setCookie)
	}
	if ttl := int((30 * time.Minute).Seconds()); maxAge <= ttl {
		t.Fatalf("re-issued cookie's Max-Age (%ds) must OUTLIVE the %ds TTL — a "+
			"TTL-keyed lifetime expires the cookie out from under a session the "+
			"SSE heartbeat is still sliding: %q", maxAge, ttl, setCookie)
	}
}

// maxAgeFromSetCookie pulls the Max-Age (seconds) out of a raw Set-Cookie
// header, returning 0 when absent (i.e. a non-persistent session cookie).
func maxAgeFromSetCookie(t *testing.T, setCookie string) int {
	t.Helper()
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "Max-Age=") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(part, "Max-Age="))
		if err != nil {
			t.Fatalf("unparseable Max-Age in %q: %v", setCookie, err)
		}
		return n
	}
	return 0
}
