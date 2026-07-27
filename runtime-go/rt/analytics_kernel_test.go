package rt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetAnalyticsSinks() {
	analyticsSinksMu.Lock()
	analyticsSinks = []analyticsSinkT{{kind: "stderr"}}
	analyticsSinksMu.Unlock()
}

// TestAnalyticsFileSink — configure a FileSink and confirm events are appended
// as JSONL (one event per line).
func TestAnalyticsFileSink(t *testing.T) {
	defer resetAnalyticsSinks()
	tmp := filepath.Join(t.TempDir(), "ev.jsonl")
	anyTaskInvoke(Analytics_configure([]any{SkyADT{SkyName: "FileSink", Fields: []any{tmp}}}))

	sess := &liveSession{sid: "fsink"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	analyticsEmit("e1", map[string]any{"k": "v"})
	analyticsEmit("e2", map[string]any{"n": int64(2)})

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read file sink: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d: %q", len(lines), data)
	}
	if !strings.Contains(lines[0], `"event":"e1"`) || !strings.Contains(lines[1], `"event":"e2"`) {
		t.Errorf("file sink lines wrong: %q", data)
	}
}

// TestAnalyticsCustomSink — a Custom sink hands each rendered JSON line to a
// user function (the userland-destination escape hatch).
func TestAnalyticsCustomSink(t *testing.T) {
	defer resetAnalyticsSinks()
	var got string
	fn := func(line any) any {
		return func() any {
			got = fmt.Sprintf("%v", line)
			return Ok[any, any](struct{}{})
		}
	}
	anyTaskInvoke(Analytics_configure([]any{SkyADT{SkyName: "Custom", Fields: []any{fn}}}))

	sess := &liveSession{sid: "csink"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	analyticsEmit("ce", map[string]any{})
	if !strings.Contains(got, `"event":"ce"`) {
		t.Errorf("custom sink did not receive the line: %q", got)
	}
}

func TestAnalyticsSnakeCase(t *testing.T) {
	cases := map[string]string{
		"ProductViewed": "product_viewed",
		"OrderId":       "order_id",
		"id":            "id",
		"AppOpened":     "app_opened",
		"Purchased":     "purchased",
	}
	for in, want := range cases {
		if got := analyticsSnakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyticsRenderGoValue_RedactsPiiAndKeepsScalars(t *testing.T) {
	// A Pii value renders to its KIND, never its value.
	pii := SkyADT{SkyName: "PiiEmail", Fields: []any{"secret@example.com"}}
	if got := analyticsRenderGoValue(pii); got != "<pii:email>" {
		t.Errorf("pii render = %v, want <pii:email>", got)
	}
	// A plain string is NOT treated as PII.
	if got := analyticsRenderGoValue("hello"); got != "hello" {
		t.Errorf("string render = %v, want hello", got)
	}
	if got := analyticsRenderGoValue(int64(7)); got != int64(7) {
		t.Errorf("int render = %v, want 7", got)
	}
	if got := analyticsRenderGoValue(true); got != true {
		t.Errorf("bool render = %v, want true", got)
	}
}

// TestAnalyticsTrackEvent_DerivesTypedProps — the reflective-derive path: an
// app event value (constructor + record payload) becomes a snake_case event
// name + typed props, with PII redacted and the plain field kept verbatim.
func TestAnalyticsTrackEvent_DerivesTypedProps(t *testing.T) {
	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	// ProductViewed { id : String, qty : Int, email : Pii } — as the SkyADT
	// representation (a record payload in Fields[0]).
	ev := SkyADT{Tag: 0, SkyName: "ProductViewed", Fields: []any{
		map[string]any{
			"id":    "SKU-42",
			"qty":   int64(2),
			"email": SkyADT{SkyName: "PiiEmail", Fields: []any{"alice@example.com"}},
		},
	}}
	anyTaskInvoke(Analytics_trackEvent(ev))

	out := buf.String()
	if !strings.Contains(out, `"event":"product_viewed"`) {
		t.Errorf("constructor not snake_cased into event name: %s", out)
	}
	if !strings.Contains(out, `"id":"SKU-42"`) || !strings.Contains(out, `"qty":2`) {
		t.Errorf("record fields not derived into props: %s", out)
	}
	if !strings.Contains(out, `"email":"<pii:email>"`) {
		t.Errorf("Pii field not redacted: %s", out)
	}
	if strings.Contains(out, "alice@example.com") {
		t.Errorf("PII VALUE LEAKED into the sink: %s", out)
	}
}

// TestAnalyticsConsentGate — the default-safe posture: anonymous by default
// (anon id, no user_id), identify records but does NOT attach identity until
// consent is Granted, and a Denied consent drops capture entirely.
func TestAnalyticsConsentGate(t *testing.T) {
	sess := &liveSession{sid: "gate-test"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	analyticsEmit("e1_default_anon", map[string]any{})   // Anonymous default
	anyTaskInvoke(Analytics_identify("user-9", nil))     // records, must not attach
	analyticsEmit("e2_after_identify", map[string]any{}) // still anonymous
	anyTaskInvoke(Analytics_setConsent(1))               // Granted (tag 1)
	analyticsEmit("e3_granted", map[string]any{})        // now attaches user_id
	anyTaskInvoke(Analytics_setConsent(2))               // Denied (tag 2)
	analyticsEmit("e4_denied_dropped", map[string]any{}) // dropped

	out := buf.String()
	if !strings.Contains(out, "e1_default_anon") || !strings.Contains(out, "anonymous_id") {
		t.Errorf("default event missing / no anon id: %s", out)
	}
	// Identity must NOT appear before consent is Granted.
	if idx := strings.Index(out, "e2_after_identify"); idx >= 0 {
		line := out[idx:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		if strings.Contains(line, "user_id") {
			t.Errorf("user_id LEAKED before consent: %s", line)
		}
	}
	if !strings.Contains(out, `"event":"e3_granted"`) || !strings.Contains(out, `"user_id":"user-9"`) {
		t.Errorf("user_id not attached after Granted: %s", out)
	}
	if strings.Contains(out, "e4_denied_dropped") {
		t.Errorf("event captured after Denied — consent gate breached: %s", out)
	}
}

// TestAnalyticsSessionIsolation — the architectural crux: analytics state is
// SESSION-scoped (via the goroutine-local session stamp), so one Sky.Live
// user's consent/identity never bleeds into another's. Two sessions must keep
// independent consent, user id, and anon id.
func TestAnalyticsSessionIsolation(t *testing.T) {
	a := &liveSession{sid: "sess-A"}
	b := &liveSession{sid: "sess-B"}

	setGoroutineLiveSession(a)
	anyTaskInvoke(Analytics_setConsent(1)) // A: Granted
	anyTaskInvoke(Analytics_identify("alice", nil))
	_, anonA, _ := currentAnalyticsState().snapshot()
	clearGoroutineLiveSession()

	setGoroutineLiveSession(b)
	anyTaskInvoke(Analytics_setConsent(2)) // B: Denied
	_, anonB, _ := currentAnalyticsState().snapshot()
	clearGoroutineLiveSession()

	setGoroutineLiveSession(a)
	ca, _, ua := currentAnalyticsState().snapshot()
	clearGoroutineLiveSession()
	if ca != consentGranted || ua != "alice" {
		t.Errorf("session A state wrong (leak?): consent=%v user=%q", ca, ua)
	}

	setGoroutineLiveSession(b)
	cb, _, ub := currentAnalyticsState().snapshot()
	clearGoroutineLiveSession()
	if cb != consentDenied || ub != "" {
		t.Errorf("session B state wrong (leak from A?): consent=%v user=%q", cb, ub)
	}
	if anonA == anonB || anonA == "" || anonB == "" {
		t.Errorf("anon ids must be distinct + non-empty per session: A=%q B=%q", anonA, anonB)
	}
}

// TestAnalyticsAnonIP — IP anonymisation: raw client IPs are truncated to
// their network prefix before they can reach an event (GDPR-style).
func TestAnalyticsAnonIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:5678":     "1.2.3.0",
		"1.2.3.4":          "1.2.3.0",
		"203.0.113.42:443": "203.0.113.0",
		"not-an-ip":        "",
		"":                 "",
	}
	for in, want := range cases {
		if got := analyticsAnonIP(in); got != want {
			t.Errorf("anonIP(%q) = %q, want %q", in, got, want)
		}
	}
	// IPv6: last 80 bits zeroed (keep the /48).
	if got := analyticsAnonIP("2001:db8:1234:5678:9abc:def0:1234:5678"); !strings.HasPrefix(got, "2001:db8:1234:") || strings.Contains(got, "9abc") {
		t.Errorf("ipv6 anon = %q, want the host bits zeroed", got)
	}
}

// TestAnalyticsSetContext — device + anonymised IP land on the session context;
// the raw IP is never stored.
func TestAnalyticsSetContext(t *testing.T) {
	sess := &liveSession{sid: "ctx"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	analyticsSetContext("UA-X", "9.8.7.6:1234")
	ctx := currentAnalyticsState().contextSnapshot()
	if ctx["user_agent"] != "UA-X" {
		t.Errorf("user_agent not captured: %v", ctx)
	}
	if ctx["ip"] != "9.8.7.0" {
		t.Errorf("ip not anonymised (raw IP leaked?): %v", ctx)
	}
}

// TestAnalyticsTrackPageView — the Sky.Live auto page-view path emits a
// consent-gated `page_view` (path + referrer), anonymous by default.
func TestAnalyticsTrackPageView(t *testing.T) {
	sess := &liveSession{sid: "pv"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	analyticsTrackPageView("/about", "http://ref/")

	out := buf.String()
	if !strings.Contains(out, `"event":"page_view"`) ||
		!strings.Contains(out, `"path":"/about"`) ||
		!strings.Contains(out, `"referrer":"http://ref/"`) {
		t.Errorf("page_view not emitted correctly: %s", out)
	}
	if strings.Contains(out, "user_id") {
		t.Errorf("auto page_view must be anonymous by default: %s", out)
	}
}

// TestAnalyticsTrackEvent_NullaryVariant — a nullary variant emits the name
// with empty props.
func TestAnalyticsTrackEvent_NullaryVariant(t *testing.T) {
	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	anyTaskInvoke(Analytics_trackEvent(SkyADT{Tag: 0, SkyName: "AppOpened"}))

	out := buf.String()
	if !strings.Contains(out, `"event":"app_opened"`) {
		t.Errorf("nullary event name wrong: %s", out)
	}
	if !strings.Contains(out, `"props":{}`) {
		t.Errorf("nullary variant should have empty props: %s", out)
	}
}
