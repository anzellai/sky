package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A console cookie is not a bearer token for its whole 4-hour life.
//
// Regression: under SKY_CONSOLE_AUTH=app the console accepted any
// HMAC-valid `__Host-sky_console` cookie until it expired, without
// re-running the app's `App.withConsoleAuth` check, and `_logout` only
// cleared the browser's copy. So an admin who signed out of the app, or
// lost the admin role, kept the console for up to 4 hours, and so did
// anyone holding a copy of the cookie.
//
// Now: each cookie carries a random id; `_logout` revokes the id on the
// server; in app mode the app's check re-runs at most every
// consoleAppRecheckInterval per cookie id, and a `Nothing` refuses with
// 403, revokes the id and clears the cookie.

// consoleTestClock pins consoleNow to a movable instant.
type consoleTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *consoleTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *consoleTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func withConsoleTestClock(t *testing.T) *consoleTestClock {
	t.Helper()
	c := &consoleTestClock{now: time.Unix(1_790_000_000, 0)}
	prev := consoleNow
	consoleNow = c.Now
	t.Cleanup(func() { consoleNow = prev })
	return c
}

// switchableConsoleCallback is a `Request -> Task Error (Maybe Identity)`
// that admits while `admin` is true (or the request carries the app cookie
// `session=admin-session`, when `viaAppCookie` is set) and counts its calls.
type switchableConsoleCallback struct {
	admin        atomic.Bool
	viaAppCookie bool
	calls        atomic.Int32
}

func (s *switchableConsoleCallback) fn() any {
	return func(req any) any {
		return func() any {
			s.calls.Add(1)
			ok := s.admin.Load()
			if s.viaAppCookie {
				ok = false
				if r, isReq := req.(SkyRequest); isReq {
					ok = r.Cookies["session"] == "admin-session"
				}
			}
			if ok {
				return Ok[any, any](Just[any](consoleTestIdentity{Subject: "admin-1", Email: "a@example.test", Claims: map[string]string{}}))
			}
			return Ok[any, any](Nothing[any]())
		}
	}
}

func setupConsoleAppMode(t *testing.T, cb *switchableConsoleCallback) {
	t.Helper()
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	SetConsoleAuthCallback(cb.fn())
	t.Cleanup(func() { SetConsoleAuthCallback(nil); ResetConsoleAuthStateForTesting() })
}

// consoleGet sends GET /_sky/console/ with the given cookies and returns
// the verdict, the status, and the console cookie the response set
// ("" when none, "-" when it cleared it).
func consoleGet(t *testing.T, cookies ...*http.Cookie) (bool, int, string) {
	t.Helper()
	r := httptest.NewRequest("GET", "/_sky/console/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	ok := evaluateConsoleAuth(w, r)
	set := ""
	for _, c := range w.Result().Cookies() {
		if c.Name == consoleAuthCookieV2Name {
			if c.MaxAge < 0 || c.Value == "" {
				set = "-"
			} else {
				set = c.Value
			}
		}
	}
	return ok, w.Code, set
}

func consoleCookie(v string) *http.Cookie {
	return &http.Cookie{Name: consoleAuthCookieV2Name, Value: v}
}

func appCookie(v string) *http.Cookie { return &http.Cookie{Name: "session", Value: v} }

// (1) After the app's check flips to Nothing, the old cookie is refused once
// the re-check window has passed.
func TestConsoleAppMode_CookieRefusedAfterAppSignOut(t *testing.T) {
	clock := withConsoleTestClock(t)
	cb := &switchableConsoleCallback{}
	cb.admin.Store(true)
	setupConsoleAppMode(t, cb)

	ok, _, issued := consoleGet(t)
	if !ok || issued == "" || issued == "-" {
		t.Fatalf("admin: allowed=%v cookie=%q, want allowed with a console cookie", ok, issued)
	}
	cb.admin.Store(false) // the admin signs out of the app

	clock.Advance(consoleAppRecheckInterval + time.Second)
	ok, status, set := consoleGet(t, consoleCookie(issued))
	if ok || status != http.StatusForbidden {
		t.Fatalf("%v after app sign-out: allowed=%v status=%d, want 403", consoleAppRecheckInterval+time.Second, ok, status)
	}
	if set != "-" {
		t.Fatalf("refusal after sign-out must clear the console cookie, Set-Cookie=%q", set)
	}
	// The id is revoked: even an admin check again would not revive THIS
	// cookie without re-running the app's check.
	if !consoleCookieRevoked(issued) {
		t.Fatalf("refusal after sign-out must revoke the cookie id on the server")
	}
}

// (2) After `_logout` the same cookie is refused at once, in app mode (a
// copied cookie without the app's own session) and in token mode.
func TestConsoleLogout_RevokesCookieOnServer(t *testing.T) {
	t.Run("app", func(t *testing.T) {
		withConsoleTestClock(t)
		cb := &switchableConsoleCallback{viaAppCookie: true}
		setupConsoleAppMode(t, cb)

		ok, _, issued := consoleGet(t, appCookie("admin-session"))
		if !ok || issued == "" || issued == "-" {
			t.Fatalf("admin: allowed=%v cookie=%q", ok, issued)
		}
		// A copy of the console cookie alone opens the console before logout.
		if ok, status, _ := consoleGet(t, consoleCookie(issued)); !ok {
			t.Fatalf("copied cookie before logout: refused (%d), want allowed inside the window", status)
		}
		consoleLogout(t, issued)
		ok, status, _ := consoleGet(t, consoleCookie(issued))
		if ok || status != http.StatusForbidden {
			t.Fatalf("copied cookie right after _logout: allowed=%v status=%d, want 403", ok, status)
		}
	})
	t.Run("token", func(t *testing.T) {
		withConsoleTestClock(t)
		t.Setenv("SKY_CONSOLE_AUTH", "token")
		t.Setenv("SKY_CONSOLE_TOKEN", "32-byte-token-cccccccccccccccccccc")
		ResetConsoleAuthStateForTesting()
		t.Cleanup(ResetConsoleAuthStateForTesting)
		withServerlessEnv(t, nil)
		st := loadConsoleAuthState()
		w := httptest.NewRecorder()
		setConsoleV2Cookie(w, st.signKey, "token-auth")
		issued := w.Result().Cookies()[0].Value
		if ok, status, _ := consoleGet(t, consoleCookie(issued)); !ok {
			t.Fatalf("token cookie before logout: refused (%d)", status)
		}
		consoleLogout(t, issued)
		ok, status, _ := consoleGet(t, consoleCookie(issued))
		if ok || status != http.StatusUnauthorized {
			t.Fatalf("token cookie right after _logout: allowed=%v status=%d, want 401 (login form)", ok, status)
		}
	})
}

func consoleLogout(t *testing.T, cookie string) {
	t.Helper()
	mux := http.NewServeMux()
	mountConsoleAuthRoutes(mux)
	r := httptest.NewRequest("GET", "/_sky/console/_logout", nil)
	r.AddCookie(consoleCookie(cookie))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("_logout answered %d, want 303", w.Code)
	}
	if sc := w.Result().Header.Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("_logout must still clear the browser copy, Set-Cookie=%q", sc)
	}
}

// (3) A demoted identity is refused within the window of its demotion.
func TestConsoleAppMode_DemotedAdminRefusedWithinWindow(t *testing.T) {
	clock := withConsoleTestClock(t)
	cb := &switchableConsoleCallback{}
	cb.admin.Store(true)
	setupConsoleAppMode(t, cb)

	_, _, issued := consoleGet(t)
	clock.Advance(10 * time.Second)
	cb.admin.Store(false) // demoted 10 s after the cookie was issued
	clock.Advance(consoleAppRecheckInterval)
	ok, status, set := consoleGet(t, consoleCookie(issued))
	if ok || status != http.StatusForbidden || set != "-" {
		t.Fatalf("%v after demotion: allowed=%v status=%d set=%q, want 403 + cleared cookie", consoleAppRecheckInterval, ok, status, set)
	}
}

// (4) An untouched admin keeps access across many windows: the re-check runs,
// says yes, and nothing is cleared.
func TestConsoleAppMode_UntouchedAdminKeepsAccess(t *testing.T) {
	clock := withConsoleTestClock(t)
	cb := &switchableConsoleCallback{}
	cb.admin.Store(true)
	setupConsoleAppMode(t, cb)

	_, _, issued := consoleGet(t)
	for i := 0; i < 8; i++ {
		clock.Advance(consoleAppRecheckInterval/2 + time.Second)
		ok, status, set := consoleGet(t, consoleCookie(issued))
		if !ok {
			t.Fatalf("step %d: an admin who changed nothing was refused (%d)", i, status)
		}
		if set == "-" {
			t.Fatalf("step %d: an admin who changed nothing had the console cookie cleared", i)
		}
	}
	// 8 steps of (window/2 + 1 s) cross the window at least 4 times; the
	// check must have re-run (it is not a 4-hour bearer token) but not on
	// every request.
	if n := cb.calls.Load(); n < 4 || n > 6 {
		t.Fatalf("app check ran %d times over 8 requests across ~4 windows, want 4..6", n)
	}
}
