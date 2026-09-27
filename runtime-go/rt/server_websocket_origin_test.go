//go:build !js

package rt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsOriginServer serves serveWebSocketUpgrade with the given origin patterns.
// The handle closes the socket as soon as it connects, so each dial is short.
func wsOriginServer(t *testing.T, patterns []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWebSocketUpgrade(w, r, webSocketUpgradeCfg{
			maxMessageBytes: 1 << 16,
			originPatterns:  patterns,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsDial dials the test server with an explicit Origin (empty = none) and Host
// (empty = the server's own address). It returns the HTTP status of the
// handshake: 101 on success.
func wsDial(t *testing.T, srv *httptest.Server, origin, host string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr, Host: host})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return http.StatusSwitchingProtocols, ""
	}
	if resp == nil {
		t.Fatalf("dial failed without a response: %v", err)
	}
	body := ""
	if resp.Body != nil {
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
	}
	return resp.StatusCode, body
}

func serverPort(srv *httptest.Server) string {
	return srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

// TestDevWebSocketOriginCheck — in dev with no `withOriginPatterns`, the
// upgrade accepts a client that sends no Origin (a native client), a
// same-host page and a loopback dev server on another port (a Vite front end),
// and refuses every other site. Before the fix it skipped the Origin check, so
// any website open in the developer's browser could drive a local socket.
func TestDevWebSocketOriginCheck(t *testing.T) {
	clearHostGuardEnv(t)
	srv := wsOriginServer(t, nil)
	port := serverPort(srv)

	for _, origin := range []string{
		"",
		"http://127.0.0.1:" + port,
		"http://localhost:5173",
		"http://127.0.0.1:3000",
		"http://[::1]:5173",
		"http://localhost",
	} {
		if code, body := wsDial(t, srv, origin, ""); code != http.StatusSwitchingProtocols {
			t.Fatalf("dev origin %q refused: %d %q", origin, code, body)
		}
	}
	for _, origin := range []string{"http://evil.example", "https://evil.example:8443"} {
		if code, _ := wsDial(t, srv, origin, ""); code != http.StatusForbidden {
			t.Fatalf("dev origin %q accepted (status %d), want 403", origin, code)
		}
	}
}

// TestDevWebSocketRefusesDNSRebinding — a rebinding page sends Host AND Origin
// both as evil.example:PORT, which passes the library's same-host rule. On a
// loopback bind the Host guard refuses it.
func TestDevWebSocketRefusesDNSRebinding(t *testing.T) {
	clearHostGuardEnv(t)
	srv := wsOriginServer(t, nil)
	evil := "evil.example:" + serverPort(srv)
	if code, _ := wsDial(t, srv, "http://"+evil, evil); code != http.StatusForbidden {
		t.Fatalf("rebinding upgrade accepted (status %d), want 403", code)
	}
}

// TestDevWebSocketAllowsListedHosts — an origin on a host listed in
// SKY_ALLOWED_HOSTS (a dev proxy name) is accepted without withOriginPatterns.
func TestDevWebSocketAllowsListedHosts(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("SKY_ALLOWED_HOSTS", "app.test,*.app.github.dev")
	srv := wsOriginServer(t, nil)
	for _, origin := range []string{"http://app.test", "https://app.test:8443", "https://x-8000.app.github.dev"} {
		if code, body := wsDial(t, srv, origin, ""); code != http.StatusSwitchingProtocols {
			t.Fatalf("listed origin %q refused: %d %q", origin, code, body)
		}
	}
	if code, _ := wsDial(t, srv, "http://evil.example", ""); code != http.StatusForbidden {
		t.Fatalf("unlisted origin accepted: %d", code)
	}
}

// TestProductionWebSocketNeedsOriginPatterns — production refuses an upgrade
// with no patterns, and the refusal names the builder that fixes it; with a
// pattern, a matching origin connects.
func TestProductionWebSocketNeedsOriginPatterns(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("ENV", "production")

	bare := wsOriginServer(t, nil)
	code, body := wsDial(t, bare, "https://app.example", "")
	if code != http.StatusForbidden {
		t.Fatalf("production upgrade without patterns: status %d, want 403", code)
	}
	if !strings.Contains(body, "withOriginPatterns") {
		t.Fatalf("production 403 does not name withOriginPatterns: %q", body)
	}

	withPattern := wsOriginServer(t, []string{"app.example"})
	if code, body := wsDial(t, withPattern, "https://app.example", ""); code != http.StatusSwitchingProtocols {
		t.Fatalf("production upgrade with a matching pattern refused: %d %q", code, body)
	}
	if code, _ := wsDial(t, withPattern, "https://evil.example", ""); code != http.StatusForbidden {
		t.Fatalf("production upgrade from a non-matching origin accepted: %d", code)
	}
}

// TestNoEnvWideBindNoLongerAcceptsAnyOrigin — a deploy that forgot ENV and
// binds 0.0.0.0 used to take the dev branch and skip the Origin check, which is
// cross-site WebSocket hijacking on a reachable server.
func TestNoEnvWideBindNoLongerAcceptsAnyOrigin(t *testing.T) {
	clearHostGuardEnv(t)
	os.Setenv("SKY_HOST", "0.0.0.0")
	srv := wsOriginServer(t, nil)
	if code, _ := wsDial(t, srv, "http://evil.example", ""); code != http.StatusForbidden {
		t.Fatalf("no-ENV 0.0.0.0 upgrade accepted a foreign origin (status %d), want 403", code)
	}
	if code, body := wsDial(t, srv, "", ""); code != http.StatusSwitchingProtocols {
		t.Fatalf("no-ENV 0.0.0.0 upgrade refused a no-Origin client: %d %q", code, body)
	}
}
