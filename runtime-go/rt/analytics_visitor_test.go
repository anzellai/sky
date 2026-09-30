//go:build !js

package rt

import (
	"bytes"
	"container/list"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// H-4: a Sky.Spa backend runs every `/_rpc` call as a plain HTTP handler (no
// Sky.Live session), and Std.Analytics used ONE process-wide state there: every
// visitor got the same anonymous id, and one visitor's consent applied to all.
// Analytics state is now per visitor for any Sky HTTP handler.

// analyticsHandler is a Sky handler that runs `Analytics.track` (or
// `setConsent Denied` when the request path says so), as a Spa RPC does.
func analyticsHandler(req any) any {
	return func() any {
		if sr, ok := req.(SkyRequest); ok && sr.Path == "/deny" {
			anyTaskInvoke(Analytics_setConsent(2))
		} else {
			anyTaskInvoke(Analytics_track(map[string]any{"Name": "rpc_event", "Props": []any{}}))
		}
		return Ok[any, any](Server_text("ok"))
	}
}

func analyticsCall(t *testing.T, sink *bytes.Buffer, path, csrf string) (anonID string, emitted bool) {
	t.Helper()
	before := sink.Len()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	if csrf != "" {
		req.AddCookie(&http.Cookie{Name: SkyCsrfCookieName, Value: csrf})
	}
	w := httptest.NewRecorder()
	dispatchSkyHandler(w, req, analyticsHandler, nil)
	if w.Code != 200 {
		t.Fatalf("%s: status %d", path, w.Code)
	}
	line := strings.TrimSpace(sink.String()[before:])
	if line == "" {
		return "", false
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "[analytics] ")), &ev); err != nil {
		t.Fatalf("event line %q: %v", line, err)
	}
	id, _ := ev["anonymous_id"].(string)
	return id, true
}

func TestAnalyticsHttpRequestsArePerVisitor(t *testing.T) {
	var sink bytes.Buffer
	prev := analyticsSink
	analyticsSink = &sink
	resetAnalyticsSinks()
	// A fresh visitor table: a previous run (-count=N) left visitor-a denied.
	prevVisitors := analyticsVisitors
	analyticsVisitors = &analyticsVisitorTable{byKey: map[string]*list.Element{}, lru: list.New()}
	t.Cleanup(func() { analyticsSink = prev; analyticsVisitors = prevVisitors })

	a1, _ := analyticsCall(t, &sink, "/_rpc/Track", "visitor-a")
	a2, _ := analyticsCall(t, &sink, "/_rpc/Track", "visitor-a")
	b1, _ := analyticsCall(t, &sink, "/_rpc/Track", "visitor-b")
	if a1 == "" || a1 != a2 {
		t.Fatalf("one visitor must keep one anonymous id: %q %q", a1, a2)
	}
	if a1 == b1 {
		t.Fatalf("two visitors must get two anonymous ids, both got %q", a1)
	}
	if strings.Contains(a1, "visitor-a") {
		t.Fatalf("the cookie value must never be the anonymous id: %q", a1)
	}

	// Visitor A denies consent: A's events stop, B's continue.
	analyticsCall(t, &sink, "/deny", "visitor-a")
	if _, emitted := analyticsCall(t, &sink, "/_rpc/Track", "visitor-a"); emitted {
		t.Fatal("visitor A denied consent; its event must be dropped")
	}
	if id, emitted := analyticsCall(t, &sink, "/_rpc/Track", "visitor-b"); !emitted || id != b1 {
		t.Fatalf("visitor B's consent must not follow A's: emitted=%v id=%q", emitted, id)
	}

	// A request with no visitor cookie shares no state with anyone.
	n1, _ := analyticsCall(t, &sink, "/_rpc/Track", "")
	n2, _ := analyticsCall(t, &sink, "/_rpc/Track", "")
	if n1 == "" || n1 == n2 || n1 == a1 || n1 == b1 {
		t.Fatalf("cookie-less requests must not share a visitor: %q %q", n1, n2)
	}
}

func TestAnalyticsVisitorTableIsBounded(t *testing.T) {
	prevMax := analyticsVisitorMax
	analyticsVisitorMax = 2
	fresh := &analyticsVisitorTable{byKey: map[string]*list.Element{}, lru: list.New()}
	t.Cleanup(func() { analyticsVisitorMax = prevMax })
	a := fresh.get("a")
	fresh.get("b")
	if fresh.get("a") != a {
		t.Fatal("a seen visitor keeps its state")
	}
	fresh.get("c") // evicts b, the least recently seen
	if fresh.lru.Len() != 2 {
		t.Fatalf("want 2 visitors kept, got %d", fresh.lru.Len())
	}
	if _, ok := fresh.byKey["b"]; ok {
		t.Fatal("the least recently seen visitor must be dropped first")
	}
}
