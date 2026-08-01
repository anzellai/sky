package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// L2 regression — the session cookie must SLIDE. On a request that already
// carries sky_sid, sessionIDNamed must re-issue the cookie with a fresh MaxAge
// so the browser's expiry window tracks the server-side TTL (which slides on
// activity). Pre-fix it returned the value WITHOUT a Set-Cookie, so the cookie
// died at its original fixed window while the server session kept sliding — an
// actively-used session past that window lost its cookie → new session → `init`
// wiped the Model (cart/auth/form) mid-use.
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
	if !strings.Contains(setCookie, "Max-Age=1800") {
		t.Fatalf("re-issued cookie should carry a fresh Max-Age keyed to the TTL (30m=1800s): %q", setCookie)
	}
}
