package rt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// L5 regression — the CSRF cookie must be a PERSISTENT (MaxAge) cookie, not a
// session cookie. Pre-fix it had no MaxAge, so browsers that clear session
// cookies on tab-discard / sleep-wake (Safari/ITP, Chrome tab discard) dropped
// it while a long-lived Sky.Live SPA stayed open; the next POST regenerated a
// NEW cookie but the page still sent the OLD baked header → 403 forever (the SPA
// never reloads to re-seed). A persistent, re-issued cookie survives that.
func TestCsrfCookieIsPersistent(t *testing.T) {
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	defer csrfEnabled.Store(prev)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	sc := rr.Header().Get("Set-Cookie")
	if !strings.Contains(sc, SkyCsrfCookieName) {
		t.Fatalf("expected a CSRF Set-Cookie, got %q", sc)
	}
	if !strings.Contains(sc, "Max-Age=") {
		t.Fatalf("L5: CSRF cookie must be persistent (Max-Age=…), got a session cookie: %q", sc)
	}
}

// Bug #11 regression — the darraghstudio incident. The CSRF cookie's Max-Age must
// OUTLIVE a session that keeps SLIDING on the SSE heartbeat while the tab sits
// IDLE (no GET/POST re-issues the cookie during idle). Keying Max-Age to a short
// SKY_LIVE_TTL (the documented production pattern, e.g. 30m) made the cookie
// expire after 30m of idle while the server session lived on → next event POST
// 403 → reconnecting banner → strand until manual refresh. Max-Age must be a long
// floor regardless of a short TTL.
func TestCsrfCookieMaxAgeOutlivesShortIdleTTL(t *testing.T) {
	t.Setenv("SKY_LIVE_TTL", "30m")
	const day = 24 * 3600
	if got := csrfCookieMaxAgeSeconds(); got < 7*day {
		t.Fatalf("bug #11: CSRF cookie Max-Age (%ds) must outlive an idle-sliding "+
			"session; a 30m SKY_LIVE_TTL must NOT shrink it below a multi-day floor", got)
	}
}

// The CSRF cookie's Max-Age must derive from the RESOLVED session TTL, not a
// second independent LIVE_TTL default. §1.7 recorded the CSRF path re-resolving
// LIVE_TTL with a 30-DAY default while the session used 30 minutes — two
// readers of one variable disagreeing on the default. Reconciled: the CSRF
// window now derives from `resolveSessionTTL()`, the same resolution live.go
// applies (default `defaultSessionTTL`), and the long idle-slide window comes
// from slidingCookieMaxAgeSeconds's floor rather than from a TTL default.
//
// Falsifier (declared for the harness): revert the reconciled default — change
// resolveSessionTTL's default from `defaultSessionTTL` back to `30*24*time.Hour`
// (the independent 30-day default §1.7 recorded) — and case A below reddens,
// because resolveSessionTTL() would then return 30 days with LIVE_TTL unset.
//
// A subtlety worth stating: the 30-day floor in slidingCookieMaxAgeSeconds makes
// the two defaults produce an IDENTICAL cookie Max-Age (both floor to 30 days),
// so the reconciliation is not observable at the cookie's output — it is
// observable only in the resolved TTL that FEEDS the floor, which is why the
// mutation-catcher asserts on resolveSessionTTL() directly rather than on the
// floored Max-Age. Case B (a TTL above the floor) then proves the two windows
// TRACK once the floor no longer masks them.
func TestCsrfCookieDerivesFromResolvedSessionTTL(t *testing.T) {
	name := skyEnvName("LIVE_TTL")

	// Case A — LIVE_TTL unset: the CSRF cookie derives its TTL from the session
	// default (30m), NOT a second 30-day default. This is the mutation-catcher.
	orig, had := os.LookupEnv(name)
	os.Unsetenv(name)
	if got := resolveSessionTTL(); got != defaultSessionTTL {
		if had {
			os.Setenv(name, orig)
		}
		t.Fatalf("with LIVE_TTL unset the CSRF cookie derives its TTL from %v; "+
			"want the session default %v, not a second independent 30-day default",
			got, defaultSessionTTL)
	}
	// The Max-Age is that resolved TTL run through the sliding floor — computed
	// identically to the session cookie (writeSessionCookie also floors the
	// resolved session TTL). At 30m the floor makes it a multi-day cookie
	// (bug #11), which is the floor's job, not a TTL default's.
	if got, want := csrfCookieMaxAgeSeconds(), slidingCookieMaxAgeSeconds(resolveSessionTTL()); got != want {
		if had {
			os.Setenv(name, orig)
		}
		t.Fatalf("csrfCookieMaxAgeSeconds()=%d must equal slidingCookieMaxAgeSeconds(resolveSessionTTL())=%d", got, want)
	}
	if had {
		os.Setenv(name, orig)
	} else {
		os.Unsetenv(name)
	}

	// Case B — an operator sets SKY_LIVE_TTL longer than the sliding floor. The
	// CSRF cookie must then key to that SAME resolved TTL as the session, above
	// the floor — proving the window TRACKS the session rather than pinning to a
	// fixed default. 1000h (~41d) exceeds the 30-day floor, so the two must equal
	// 1000h in seconds, NOT the 30-day floor.
	t.Setenv(name, "1000h")
	sessionTTL := resolveSessionTTL()
	if sessionTTL != 1000*time.Hour {
		t.Fatalf("operator SKY_LIVE_TTL=1000h must win: resolveSessionTTL()=%v", sessionTTL)
	}
	if got, want := csrfCookieMaxAgeSeconds(), slidingCookieMaxAgeSeconds(sessionTTL); got != want {
		t.Fatalf("with SKY_LIVE_TTL=1000h the CSRF Max-Age=%d must equal the session's %d (tracking, not 30 days)", got, want)
	}
	if want := int((1000 * time.Hour).Seconds()); csrfCookieMaxAgeSeconds() != want {
		t.Fatalf("with a TTL above the floor the CSRF Max-Age must be the resolved TTL (%ds), got %d", want, csrfCookieMaxAgeSeconds())
	}
}
