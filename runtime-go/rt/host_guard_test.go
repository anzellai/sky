//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// clearHostGuardEnv unsets every variable the Host guard and the bind
// resolution read. Env-based, so these tests must NOT run t.Parallel().
func clearHostGuardEnv(t *testing.T) {
	t.Helper()
	clearBindEnv(t)
	for _, k := range []string{"SKY_ALLOWED_HOSTS", "SKY_APP_URL"} {
		if old, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { os.Setenv(k, old) })
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
		os.Unsetenv(k)
	}
}

// guardStatus sends one GET with the given Host header through the guard.
func guardStatus(t *testing.T, h http.Handler, host string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestHostGuardRejectsAForeignHostOnALoopbackBind is the DNS-rebinding case: a
// page on evil.example re-points its name at 127.0.0.1, and the browser then
// sends `Host: evil.example:8000` to the loopback dev listener. Every route on
// that listener (the open dev console included) must refuse it.
func TestHostGuardRejectsAForeignHostOnALoopbackBind(t *testing.T) {
	clearHostGuardEnv(t)
	h := hostGuardMiddleware("127.0.0.1", okHandler())
	for _, host := range []string{
		"evil.example:8000",
		"evil.example",
		"localhost.evil.example:8000", // a suffix trick, not a *.localhost name
		"evillocalhost:8000",
		"192.168.1.20:8000", // a LAN address is not allowed until listed
	} {
		code, body := guardStatus(t, h, host)
		if code != http.StatusForbidden {
			t.Fatalf("Host %q on a loopback bind: status %d, want 403", host, code)
		}
		if !strings.Contains(body, "SKY_ALLOWED_HOSTS") {
			t.Fatalf("Host %q: the 403 body does not name SKY_ALLOWED_HOSTS: %q", host, body)
		}
	}
}

// TestHostGuardAcceptsEveryDefaultAllowedHost — the guard must not break any
// ordinary local flow: a browser on localhost / 127.0.0.1 / [::1], a
// *.localhost name, the Android emulator's alias for the host, the bind
// address itself, and a client that sends no Host at all (HTTP/1.0).
func TestHostGuardAcceptsEveryDefaultAllowedHost(t *testing.T) {
	clearHostGuardEnv(t)
	h := hostGuardMiddleware("127.0.0.1", okHandler())
	for _, host := range []string{
		"localhost:8000",
		"localhost",
		"LOCALHOST:8000",
		"localhost.:8000",
		"127.0.0.1:8000",
		"127.0.0.1",
		"[::1]:8000",
		"[::1]",
		"app.localhost:8000",
		"a.b.localhost",
		"10.0.2.2:8951",
		"",
	} {
		if code, body := guardStatus(t, h, host); code != http.StatusOK {
			t.Fatalf("default-allowed Host %q rejected: %d %q", host, code, body)
		}
	}
	// The bind address is allowed even when it is not 127.0.0.1.
	h6 := hostGuardMiddleware("::1", okHandler())
	if code, _ := guardStatus(t, h6, "[::1]:8000"); code != http.StatusOK {
		t.Fatalf("bind address [::1] rejected on a ::1 bind: %d", code)
	}
}

// TestHostGuardAcceptsTheAppURLHost — the host of SKY_APP_URL (the address a
// native shell loads) is allowed without listing it again.
func TestHostGuardAcceptsTheAppURLHost(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("SKY_APP_URL", "http://devbox.test:9000/")
	h := hostGuardMiddleware("127.0.0.1", okHandler())
	if code, _ := guardStatus(t, h, "devbox.test:8000"); code != http.StatusOK {
		t.Fatalf("SKY_APP_URL host rejected: %d", code)
	}
	if code, _ := guardStatus(t, h, "evil.example:8000"); code != http.StatusForbidden {
		t.Fatalf("SKY_APP_URL opened the guard to every host: %d", code)
	}
}

// TestHostGuardHonoursSkyAllowedHosts — LAN phones, a dev proxy name and
// Codespaces-style wildcard names are allowed once listed.
func TestHostGuardHonoursSkyAllowedHosts(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("SKY_ALLOWED_HOSTS", " app.test , *.app.github.dev,192.168.1.20:8000 ,.corp.test")
	h := hostGuardMiddleware("127.0.0.1", okHandler())
	for _, host := range []string{
		"app.test",
		"app.test:8000",
		"fuzzy-space-8000.app.github.dev",
		"192.168.1.20:8000",
		"192.168.1.20:9999", // a listed entry names a host; the port is not part of it
		"x.corp.test",
	} {
		if code, body := guardStatus(t, h, host); code != http.StatusOK {
			t.Fatalf("listed Host %q rejected: %d %q", host, code, body)
		}
	}
	for _, host := range []string{"app.github.dev", "evil.example", "notapp.test"} {
		if code, _ := guardStatus(t, h, host); code != http.StatusForbidden {
			t.Fatalf("unlisted Host %q accepted: %d", host, code)
		}
	}
}

// TestHostGuardStarDisablesTheCheck — `SKY_ALLOWED_HOSTS=*` is the explicit
// opt-out.
func TestHostGuardStarDisablesTheCheck(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("SKY_ALLOWED_HOSTS", "*")
	h := hostGuardMiddleware("127.0.0.1", okHandler())
	if code, _ := guardStatus(t, h, "evil.example:8000"); code != http.StatusOK {
		t.Fatalf("SKY_ALLOWED_HOSTS=* did not disable the guard: %d", code)
	}
}

// TestHostGuardIsNotAppliedOnANonLoopbackBind — production binds all
// interfaces behind a proxy that may rewrite Host, and an operator's SKY_HOST
// names a real interface. The guard is for the loopback dev listener only.
func TestHostGuardIsNotAppliedOnANonLoopbackBind(t *testing.T) {
	clearHostGuardEnv(t)
	for _, bind := range []string{"", "0.0.0.0", "::", "10.0.0.5"} {
		h := hostGuardMiddleware(bind, okHandler())
		if code, _ := guardStatus(t, h, "evil.example:8000"); code != http.StatusOK {
			t.Fatalf("bind %q: guard applied on a non-loopback bind (status %d)", bind, code)
		}
	}
}

// TestBothListenersApplyTheHostGuard — the guard is wired into the handler
// chain of BOTH listeners (Sky.Live and Sky.Http.Server), in front of every
// route, including the console and a user route. It is tested through the
// real chain builders, not through the middleware alone.
func TestBothListenersApplyTheHostGuard(t *testing.T) {
	clearHostGuardEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for name, h := range map[string]http.Handler{
		"Sky.Live":        liveListenerHandler(mux, "127.0.0.1"),
		"Sky.Http.Server": serverListenerHandler(mux, nil, "127.0.0.1"),
	} {
		for _, path := range []string{"/", "/_sky/console", "/_sky/sse", "/api/thing"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = "evil.example:8000"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s with a foreign Host: status %d, want 403", name, path, rec.Code)
			}
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "localhost:8000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: localhost request rejected: %d %q", name, rec.Code, rec.Body.String())
		}
	}
}
