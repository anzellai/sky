//go:build !js

package rt

// The Sky Console's controls, at the API they drive (v0.27.8). Each test
// is the server half of a control scripts/console-controls-e2e.mjs operates
// in a browser:
//
//   - range chips  → ?range= on /logs, /traces, /errors, /analytics, applied
//     to the whole store BEFORE the row limit, comparing instants (a log
//     stamped in a non-UTC zone is in or out of a range by its instant);
//   - searches     → ?q= (repeatable, ANDed) on /logs, /traces, /errors;
//   - session pivot → ?session= on /logs;
//   - level toggles → ?level= (a set; "none" matches nothing);
//   - trace pivot  → ?q=<trace id> on /traces, which keeps the whole trace;
//   - a console link through the sign-in → the login form's redirect.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func consoleGetJSON(t *testing.T, h http.HandlerFunc, path string, out any) {
	t.Helper()
	resp := serveOnce(h, http.MethodGet, path)
	if resp.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, resp.Code)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func logMessages(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["Message"].(string))
	}
	return out
}

// seedConsoleLogs puts one old line per age into the ring, then `volume`
// newer lines, so the old lines are NOT among the newest `volume` rows.
func seedConsoleLogs(t *testing.T, volume int) {
	t.Helper()
	store := telemetry.Default()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v (the Go time-zone database is required)", err)
	}
	now := time.Now()
	for _, s := range []struct {
		age   time.Duration
		name  string
		level string
	}{
		{2 * time.Minute, "2m", "warn"},
		{30 * time.Minute, "30m", "info"},
		{5 * time.Hour, "5h", "error"},
		{3 * 24 * time.Hour, "3d", "info"},
		{10 * 24 * time.Hour, "10d", "error"},
	} {
		// Stamped in a non-UTC zone, as a server in that zone stamps its
		// own lines: the range is by instant, not by the string.
		store.AppendLog(telemetry.LogEntry{
			TS: now.Add(-s.age).In(ny), Level: s.level, Message: "seed-" + s.name,
			ReqID: "trace-" + s.name, Fields: map[string]string{"session_id": "sess-" + s.name},
		})
	}
	for i := 0; i < volume; i++ {
		store.AppendLog(telemetry.LogEntry{TS: now, Level: "info", Message: "volume"})
	}
}

func TestConsoleLogs_RangeAppliesToTheWholeRingBeforeTheLimit(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	seedConsoleLogs(t, 300)
	for _, c := range []struct {
		rng  string
		want []string
	}{
		{"15m", []string{"seed-2m"}},
		{"1h", []string{"seed-2m", "seed-30m"}},
		{"24h", []string{"seed-2m", "seed-30m", "seed-5h"}},
		{"7d", []string{"seed-2m", "seed-30m", "seed-5h", "seed-3d"}},
		{"all", []string{"seed-2m", "seed-30m", "seed-5h", "seed-3d", "seed-10d"}},
	} {
		var rows []map[string]any
		consoleGetJSON(t, HandleConsoleLogs, "/_sky/console/api/logs?limit=200&range="+c.rng+"&q=seed", &rows)
		if got := strings.Join(logMessages(rows), ","); got != strings.Join(c.want, ",") {
			t.Errorf("range=%s q=seed: got [%s], want [%s] (newest first, by time)", c.rng, got, strings.Join(c.want, ","))
		}
	}
	// Without a search the newest 200 are the volume lines: the range
	// still bounds them, and the limit holds.
	var rows []map[string]any
	consoleGetJSON(t, HandleConsoleLogs, "/_sky/console/api/logs?limit=200&range=15m", &rows)
	if len(rows) != 200 {
		t.Errorf("range=15m limit=200: got %d rows, want 200", len(rows))
	}
}

func TestConsoleLogs_SearchSessionAndLevelSet(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	seedConsoleLogs(t, 300)
	cases := []struct {
		query string
		want  string
	}{
		// Two q terms (the global search and the Logs tab's) both apply.
		{"q=seed&q=3d", "seed-3d"},
		// The search covers the request id and the session, not only the message.
		{"q=trace-5h", "seed-5h"},
		{"q=SESS-30M", "seed-30m"},
		{"session=sess-10d", "seed-10d"},
		{"q=seed&level=warn,error", "seed-2m,seed-5h,seed-10d"},
		{"q=seed&level=none", ""},
	}
	for _, c := range cases {
		var rows []map[string]any
		consoleGetJSON(t, HandleConsoleLogs, "/_sky/console/api/logs?limit=200&range=all&"+c.query, &rows)
		if got := strings.Join(logMessages(rows), ","); got != c.want {
			t.Errorf("%s: got [%s], want [%s]", c.query, got, c.want)
		}
	}
}

func TestConsoleTraces_RangeSearchKeepsWholeTracesAndPages(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	store := telemetry.Default()
	now := time.Now()
	add := func(trace, span, parent, name string, age time.Duration) {
		start := now.Add(-age)
		store.AppendTrace(telemetry.TraceEntry{TraceID: trace, SpanID: span, ParentID: parent, Name: name, StartTime: start, EndTime: start.Add(time.Millisecond)})
	}
	add("t-old", "s1", "", "root-old", 3*24*time.Hour)
	add("t-old", "s2", "s1", "db.query", 3*24*time.Hour-time.Second)
	add("t-new", "s3", "", "root-new", time.Minute)
	for i := 0; i < 150; i++ {
		add("t-vol", "v", "", "volume", 0)
	}
	names := func(path string) []string {
		var rows []map[string]any
		consoleGetJSON(t, HandleConsoleTraces, path, &rows)
		out := []string{}
		for _, r := range rows {
			out = append(out, r["name"].(string))
		}
		return out
	}
	// The trace pivot: an id older than the newest 100 spans is found, whole.
	if got := strings.Join(names("/_sky/console/api/traces?limit=100&range=all&q=t-old"), ","); got != "db.query,root-old" {
		t.Errorf("q=t-old: got [%s], want the whole trace [db.query,root-old]", got)
	}
	// A span-name match keeps its trace's other spans.
	if got := strings.Join(names("/_sky/console/api/traces?limit=100&range=all&q=db.query"), ","); got != "db.query,root-old" {
		t.Errorf("q=db.query: got [%s], want the whole trace", got)
	}
	if got := strings.Join(names("/_sky/console/api/traces?limit=100&range=1h&q=root"), ","); got != "root-new" {
		t.Errorf("range=1h q=root: got [%s], want [root-new]", got)
	}
	// offset pages (it used to be read and ignored).
	all := names("/_sky/console/api/traces?limit=1000&range=all")
	page := names("/_sky/console/api/traces?limit=2&offset=150&range=all")
	if strings.Join(page, ",") != strings.Join(all[150:152], ",") {
		t.Errorf("offset=150 limit=2: got %v, want %v", page, all[150:152])
	}
}

func TestConsoleErrors_RangeAndSearch(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	seedConsoleLogs(t, 10)
	msgs := func(path string) string {
		var rows []map[string]any
		consoleGetJSON(t, HandleConsoleErrors, path, &rows)
		out := []string{}
		for _, r := range rows {
			out = append(out, r["message"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := msgs("/_sky/console/api/errors?range=1h"); got != "seed-2m" {
		t.Errorf("range=1h: got [%s], want [seed-2m]", got)
	}
	if got := msgs("/_sky/console/api/errors?range=24h"); got != "seed-2m,seed-5h" {
		t.Errorf("range=24h: got [%s], want [seed-2m,seed-5h] (newest first on equal counts)", got)
	}
	if got := msgs("/_sky/console/api/errors?range=all&q=10d"); got != "seed-10d" {
		t.Errorf("range=all q=10d: got [%s], want [seed-10d]", got)
	}
}

func TestConsoleAnalyticsWindow_FollowsTheRange(t *testing.T) {
	for _, c := range []struct {
		key   string
		dur   time.Duration
		days  int
		label string
	}{
		{"15m", 15 * time.Minute, 0, "15 minutes"},
		{"1h", time.Hour, 0, "1 hour"},
		{"24h", 24 * time.Hour, 1, "24 hours"},
		{"7d", 7 * 24 * time.Hour, 7, "7 days"},
		{"all", consoleAnalyticsWindow, consoleAnalyticsWindowDays, "30 days"},
		{"", consoleAnalyticsWindow, consoleAnalyticsWindowDays, "30 days"},
	} {
		d, days, label := consoleAnalyticsWindowFor(c.key)
		if d != c.dur || days != c.days || label != c.label {
			t.Errorf("range %q: got (%v, %d, %q), want (%v, %d, %q)", c.key, d, days, label, c.dur, c.days, c.label)
		}
	}
}

func TestConsoleLogin_ReturnsToTheRequestedConsoleURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/_sky/console/?tab=logs&range=7d&q=a%22b", nil)
	page := renderConsoleLoginPage(consoleAuthModeToken, consoleReturnTo(r))
	if !strings.Contains(page, `name="redirect" value="/_sky/console/?tab=logs&amp;range=7d&amp;q=a%22b"`) {
		t.Fatalf("the login form does not carry the console URL it was shown for:\n%s", page)
	}
	// Not a destination: an API read, the login route, a POST.
	for _, p := range []string{"/_sky/console/api/logs?range=1h", "/_sky/console/_login"} {
		if got := consoleReturnTo(httptest.NewRequest(http.MethodGet, p, nil)); got != "" {
			t.Errorf("consoleReturnTo(%s) = %q, want \"\"", p, got)
		}
	}
	for raw, want := range map[string]string{
		"/_sky/console/?tab=errors&range=1h": "/_sky/console/?tab=errors&range=1h",
		"/_sky/console":                      "/_sky/console",
		"":                                   "/_sky/console",
		"https://evil.example/_sky/console":  "/_sky/console",
		"//evil.example/_sky/console":        "/_sky/console",
		"/_sky/console\\@evil.example":       "/_sky/console",
		"/_sky/consolex/../../elsewhere":     "/_sky/consolex/../../elsewhere",
		"/elsewhere":                         "/_sky/console",
	} {
		if got := consoleLoginDest(raw); got != want {
			t.Errorf("consoleLoginDest(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestConsoleLogin_PostRedirectsToTheFormsDestination(t *testing.T) {
	t.Setenv("SKY_CONSOLE_TOKEN", "tok-0123456789abcdef0123456789")
	st := &consoleAuthState{mode: consoleAuthModeToken, signKey: []byte("k0123456789abcdef0123456789abcde")}
	form := url.Values{"token": {"tok-0123456789abcdef0123456789"}, "redirect": {"/_sky/console/?tab=traces&range=1h"}}
	r := httptest.NewRequest(http.MethodPost, "/_sky/console/_login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleConsoleLogin(w, r, st)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/_sky/console/?tab=traces&range=1h" {
		t.Fatalf("login: %d Location=%q, want 303 to the console URL", w.Code, w.Header().Get("Location"))
	}
}

func TestConsoleQuietTelemetry_RecordsNoSpans(t *testing.T) {
	// The production sampler is ParentBased (telemetry/otel.go skySampler):
	// a local parent that is not sampled drops its children, as here.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithSyncer(exporter),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		// The process default (otel's own delegating provider) cannot be set
		// back as its own delegate; a no-op provider is the same state.
		if _, real := prev.(*sdktrace.TracerProvider); real {
			otel.SetTracerProvider(prev)
		} else {
			otel.SetTracerProvider(tracenoop.NewTracerProvider())
		}
	})

	WithMsgSpan("Loud", func() any { return nil })
	if got := len(exporter.GetSpans()); got != 1 {
		t.Fatalf("an ordinary dispatch recorded %d spans, want 1", got)
	}
	app := &liveApp{quietTelemetry: true}
	func() {
		defer app.quiet()()
		WithMsgSpan("GotLogs", func() any {
			return WithCmdSpan("perform", func() any {
				return WithHTTPClientSpan("GET", "http://127.0.0.1/_sky/console/api/logs", func() any { return nil })
			})
		})
	}()
	if got := len(exporter.GetSpans()); got != 1 {
		t.Fatalf("the console's own Msg, Cmd and HTTP spans were recorded: %d spans, want 1", got)
	}
	// The goroutine's previous context is back: spans record again.
	WithMsgSpan("LoudAgain", func() any { return nil })
	if got := len(exporter.GetSpans()); got != 2 {
		t.Fatalf("after the quiet section, %d spans, want 2", got)
	}
}
