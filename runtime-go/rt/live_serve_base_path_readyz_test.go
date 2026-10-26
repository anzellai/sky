//go:build !js

package rt

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A standalone Sky.Live app run with SKY_LIVE_BASE_PATH behind a proxy that
// strips the prefix (the documented reverse-proxy setup) owns its process and
// its listener, so it must answer /_sky/healthz and /_sky/readyz like any other
// app. Before this fix buildLiveServerFor read "base path set" as "I am a
// fork+exec sub-app whose parent owns the health surface" (a mode deleted in
// 175dfbb83), mounted neither endpoint and registered no session-store probe:
// /_sky/readyz answered 404 through the proxy, so a readiness probe never
// passed. Found by the strict-CSP e2e (caddy-base), which now waits for the
// backend's /_sky/readyz in every topology.
func TestLiveServe_BasePathAppServesHealthAndReadiness(t *testing.T) {
	serveDurableEnv(t)
	resetReadiness(t)
	t.Setenv("SKY_LIVE_BASE_PATH", "/app")
	cfg := namedServeCfg(0, "basepath", "ignored")
	probesBefore := len(scopedProbeList())
	ls, err := buildLiveServerFor(cfg, true, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = ls.stop(2 * time.Second) })
	go ls.run()

	get := func(path string) (int, string) {
		t.Helper()
		var last error
		for i := 0; i < 40; i++ {
			resp, err := http.Get("http://" + ls.address() + path)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				return resp.StatusCode, string(b)
			}
			last = err
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("GET %s: %v", path, last)
		return 0, ""
	}
	// The proxy strips /app, so the backend sees the unprefixed path.
	if st, body := get("/_sky/healthz"); st != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("/_sky/healthz under SKY_LIVE_BASE_PATH = %d %q, want 200 ok", st, body)
	}
	if st, body := get("/_sky/readyz"); st != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Fatalf("/_sky/readyz under SKY_LIVE_BASE_PATH = %d %q, want 200 ready", st, body)
	}
	// The session store is wired into readiness, as for an app without a base
	// path, so an unreachable store makes the app not ready.
	if got := len(scopedProbeList()); got != probesBefore+1 {
		t.Fatalf("readiness probes: %d before the app, %d after, want the session-store probe added", probesBefore, got)
	}
}
