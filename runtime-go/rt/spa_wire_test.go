//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// E-4 (spa_wire.go): the handshake verdicts and the reload guard.

func withWireHash(t *testing.T, h string) {
	t.Helper()
	prev := currentSpaWireHash()
	Spa_setWireHash(h).(func() any)()
	t.Cleanup(func() { spaWireHash.Store(prev) })
}

func TestSpaWireCheckVerdicts(t *testing.T) {
	withWireHash(t, "")
	if spaWireCheck("anything") != spaWireOK || spaWireCheck("") != spaWireOK {
		t.Fatal("with no wire hash registered nothing is checked")
	}
	withWireHash(t, "abc123")
	if spaWireCheck("abc123") != spaWireOK {
		t.Fatal("the same schema must pass")
	}
	if spaWireCheck("old999") != spaWireMismatch {
		t.Fatal("another schema must be told to reload")
	}
	if spaWireCheck("") != spaWireLegacy {
		t.Fatal("no header is a pre-v0.27 tab")
	}
	req := SkyRequest{Headers: map[string]any{"x-sky-wire": "abc123"}}
	if Spa_isLegacyRpc(req).(bool) {
		t.Fatal("a request with the header is not legacy")
	}
	if !Spa_isLegacyRpc(SkyRequest{Headers: map[string]any{}}).(bool) {
		t.Fatal("a request without the header is legacy")
	}
}

// Two replicas on different schemas behind round robin: the page comes from A
// and the RPC goes to B, then the reverse. The tab reloads once, not in a loop.
func TestSpaWireReloadGuardReloadsOnceNotInALoop(t *testing.T) {
	now := int64(1_000_000)
	if !spaWireShouldReload(0, now) {
		t.Fatal("the first mismatch must reload")
	}
	last := now
	for _, dt := range []int64{200, 1_500, 10_000, 29_999} {
		if spaWireShouldReload(last, now+dt) {
			t.Fatalf("a second mismatch %d ms after a reload reloaded again (a loop)", dt)
		}
	}
	if !spaWireShouldReload(last, now+spaWireReloadEvery) {
		t.Fatal("after the guard window a new deploy must reload again")
	}
	if !spaWireShouldReload(now+60_000, now) {
		t.Fatal("a future timestamp (clock change) must not block reloads for ever")
	}
}

func TestRpcGuardAnswersAWireMismatchWithReload(t *testing.T) {
	withWireHash(t, "new-schema")
	for _, c := range []struct {
		wire string
		want int
	}{{"old-schema", 409}, {"new-schema", 200}, {"", 200}} {
		req := httptest.NewRequest(http.MethodPost, "/_rpc/AddToBasket?rid=1", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if c.wire != "" {
			req.Header.Set(spaWireHeader, c.wire)
		}
		rr := httptest.NewRecorder()
		ok := rpcRequestGuard(rr, req, "POST")
		code := 200
		if !ok {
			code = rr.Code
		}
		if code != c.want {
			t.Fatalf("X-Sky-Wire %q: %d, want %d", c.wire, code, c.want)
		}
		if c.want == 409 && rr.Header().Get("X-Sky-Status") != "reload" {
			t.Fatalf("a wire mismatch must carry X-Sky-Status: reload, got %q", rr.Header().Get("X-Sky-Status"))
		}
	}
}
