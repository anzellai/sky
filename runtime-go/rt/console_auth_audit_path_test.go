//go:build !js

package rt

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"sky-app/rt/telemetry"
)

// The console auth audit log keeps the full client address (a security
// audit log needs it, docs/observability.md), and the path it records is
// only ever a /_sky/ console path. A request that reaches the gate on any
// other path records a fixed marker, never its raw path.
func TestConsoleAuthAuditLogsOnlyAConsolePath(t *testing.T) {
	for i, c := range []struct{ path, want string }{
		{"/_sky/console/api/logs", "/_sky/console/api/logs"},
		{"/reset/3f9a77c2secret", consoleAuditOtherPath},
		{"/_skyx/token123", consoleAuditOtherPath},
	} {
		remote := "203.0.113.77:4100" + strconv.Itoa(i)
		r := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: c.path}, RemoteAddr: remote}
		recordConsoleAuthEvent(r, "denied", "")
		var got map[string]string
		for _, e := range telemetry.Default().RecentLogs(0) {
			if e.Message == "console.auth" && e.Fields["remote"] == remote {
				got = e.Fields
			}
		}
		if got == nil {
			t.Fatalf("no console.auth line with the full remote address for %s", c.path)
		}
		if got["path"] != c.want {
			t.Fatalf("console.auth path for %s = %q, want %q", c.path, got["path"], c.want)
		}
	}
}
