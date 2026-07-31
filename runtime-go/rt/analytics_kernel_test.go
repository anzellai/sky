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
// The auto-page-view identity resolver (analytics = { identify = … }): a
// `Just id` stamps the session user id; nil / Nothing leave it anonymous.
func TestAnalyticsApplyIdentity(t *testing.T) {
	run := func(sid string, resolver any) string {
		sess := &liveSession{sid: sid}
		setGoroutineLiveSession(sess)
		defer clearGoroutineLiveSession()
		analyticsApplyIdentity(resolver, map[string]any{"currentUser": "x"})
		_, _, uid := currentAnalyticsState().snapshot()
		return uid
	}
	if got := run("id-just", func(any) any { return SkyMaybe[any]{Tag: 0, JustValue: "u42"} }); got != "u42" {
		t.Errorf("Just resolver: want u42, got %q", got)
	}
	if got := run("id-nothing", func(any) any { return SkyMaybe[any]{Tag: 1} }); got != "" {
		t.Errorf("Nothing resolver: want empty, got %q", got)
	}
	if got := run("id-nil", nil); got != "" {
		t.Errorf("nil resolver: want empty, got %q", got)
	}
}

// TestAnalyticsApplyIdentityClearsOnSignOut is the sign-out regression: a session
// that was identified (resolver returned Just id on earlier renders) must revert
// to anonymous once the resolver returns Nothing — otherwise a signed-OUT session
// keeps attributing events to the previous user (and handleInitial persists that
// stale id to the session store). The resolver is the identity authority; Nothing
// un-identifies. The fresh-session cases in TestAnalyticsApplyIdentity can't catch
// this because their user id starts empty.
func TestAnalyticsApplyIdentityClearsOnSignOut(t *testing.T) {
	sess := &liveSession{sid: "signout"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	signedIn := func(any) any { return SkyMaybe[any]{Tag: 0, JustValue: "u42"} }
	signedOut := func(any) any { return SkyMaybe[any]{Tag: 1} }
	model := map[string]any{"currentUser": "x"}

	// Signed in → identified across renders.
	analyticsApplyIdentity(signedIn, model)
	if _, _, uid := currentAnalyticsState().snapshot(); uid != "u42" {
		t.Fatalf("after sign-in: want u42, got %q", uid)
	}
	// Sign out → the resolver now returns Nothing; the user id MUST clear.
	analyticsApplyIdentity(signedOut, model)
	if _, _, uid := currentAnalyticsState().snapshot(); uid != "" {
		t.Fatalf("after sign-out: user id not cleared, still attributing to %q", uid)
	}
	// An empty `Just ""` (e.g. a not-yet-loaded id) also un-identifies.
	analyticsApplyIdentity(signedIn, model) // re-identify
	analyticsApplyIdentity(func(any) any { return SkyMaybe[any]{Tag: 0, JustValue: ""} }, model)
	if _, _, uid := currentAnalyticsState().snapshot(); uid != "" {
		t.Fatalf("Just \"\": want cleared, got %q", uid)
	}
}

func TestAnalyticsConsentGate(t *testing.T) {
	sess := &liveSession{sid: "gate-test"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	// Default posture is Granted (v0.19.1): capture is on, and `identify`
	// attaches the user id immediately.
	analyticsEmit("e1_default", map[string]any{})        // Granted default, no identify yet → anon only
	anyTaskInvoke(Analytics_identify("user-9", nil))     // attaches immediately under Granted
	analyticsEmit("e2_after_identify", map[string]any{}) // now carries user_id
	anyTaskInvoke(Analytics_setConsent(0))               // Anonymous (tag 0) — drop identity
	analyticsEmit("e3_anonymous", map[string]any{})      // anonymous: no user_id
	anyTaskInvoke(Analytics_setConsent(2))               // Denied (tag 2)
	analyticsEmit("e4_denied_dropped", map[string]any{}) // dropped

	out := buf.String()
	lineHasUserID := func(event string) bool {
		idx := strings.Index(out, event)
		if idx < 0 {
			return false
		}
		line := out[idx:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		return strings.Contains(line, `"user_id":"user-9"`)
	}
	if !strings.Contains(out, "e1_default") || !strings.Contains(out, "anonymous_id") {
		t.Errorf("default event missing / no anon id: %s", out)
	}
	// Granted-by-default: identity attaches immediately after identify.
	if !lineHasUserID("e2_after_identify") {
		t.Errorf("user_id not attached under Granted default: %s", out)
	}
	// Explicit Anonymous consent drops identity from subsequent events.
	if lineHasUserID("e3_anonymous") {
		t.Errorf("user_id LEAKED under Anonymous consent: %s", out)
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

// TestRestoreAnalyticsConsentDefaultMigration: a session persisted with a
// non-explicit consent (rode the framework default) adopts the CURRENT default
// (Granted) on restore, so a default change reaches already-persisted sessions
// and a stale default is never mistaken for a user's choice. An EXPLICIT posture
// (setConsent, e.g. behind a consent banner) is restored verbatim.
func TestRestoreAnalyticsConsentDefaultMigration(t *testing.T) {
	// Persisted Anonymous(0) but NOT explicit → current default (Granted).
	if got := restoreAnalyticsState(int(consentAnonymous), false, "anon1", "u1").consent; got != consentGranted {
		t.Fatalf("non-explicit Anonymous should restore as Granted, got %v", got)
	}
	// Explicit Anonymous → respected.
	if got := restoreAnalyticsState(int(consentAnonymous), true, "anon2", "u2").consent; got != consentAnonymous {
		t.Fatalf("explicit Anonymous should restore as Anonymous, got %v", got)
	}
	// Explicit Denied → respected.
	if got := restoreAnalyticsState(int(consentDenied), true, "anon3", "").consent; got != consentDenied {
		t.Fatalf("explicit Denied should restore as Denied, got %v", got)
	}
	// Identity + anon id always carried through regardless of consent origin.
	st := restoreAnalyticsState(int(consentAnonymous), false, "anonX", "userX")
	if st.userID != "userX" || st.anonID != "anonX" {
		t.Fatalf("identity/anon id not preserved: %q / %q", st.userID, st.anonID)
	}
	// setConsent flips the explicit bit (so it persists + restores verbatim).
	fresh := newAnalyticsState()
	if fresh.consentExplicit() {
		t.Fatal("fresh state should not be explicit")
	}
	fresh.setConsent(consentAnonymous)
	if !fresh.consentExplicit() {
		t.Fatal("setConsent should mark consent explicit")
	}
}
