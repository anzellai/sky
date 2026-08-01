package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
