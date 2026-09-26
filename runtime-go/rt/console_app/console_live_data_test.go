//go:build !js

// console_live_data_test.go — the embedded console shows the host's live
// telemetry when console auth is on.
//
// The defect (v0.25.17 – v0.25.19): with SKY_CONSOLE_AUTH=token (and always
// under ENV=production) the console's header read "Sky — · dev · uptime 0s"
// for ever and every panel stayed empty. Its reads are loopback GETs to the
// host's /_sky/console/api/*, which since the F1 fix admit the in-process
// console only by the per-boot internal token. The console source never sent
// that token, every read answered 401 with the login page, the decode of the
// page failed into Model.lastError, and nothing rendered lastError. The
// existing console tests checked the JSON endpoints and the tab rendering, but
// never that a value in the running console changed.
//
// These tests drive the real mount (rt.MountEmbeddedConsole) in production
// token mode over HTTP.

package console_app

import (
	"bufio"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	rt "sky-app/rt"
	"sky-app/rt/telemetry"
)

var (
	consoleHostOnce sync.Once
	consoleHostURL  string
)

// consoleHost starts ONE host server with the embedded console mounted the
// way an app mounts it (a sub-app mount can happen once per process).
func consoleHost(t *testing.T) string {
	t.Helper()
	consoleHostOnce.Do(func() {
		os.Setenv("ENV", "production")
		os.Setenv("SKY_CONSOLE_AUTH", "token")
		os.Setenv("SKY_CONSOLE_TOKEN", "console-live-data-test-token-0123456789")
		os.Unsetenv("SKY_CONSOLE_INTERNAL_TOKEN")
		rt.SetProductionMode(true)
		rt.ResetConsoleAuthStateForTesting()
		telemetry.ResetDefault()

		mux := http.NewServeMux()
		srv := httptest.NewServer(rt.ObservabilityMiddleware(mux))
		// The console reads its host over loopback at SKY_PARENT_URL; the
		// runtime seeds it from the listen port, the test from the server.
		os.Setenv("SKY_PARENT_URL", srv.URL)
		rt.MountEmbeddedConsole(mux)
		rt.MountObservabilityEndpoints(mux)
		consoleHostURL = srv.URL
	})
	return consoleHostURL
}

// syntheticTraffic records what a running app records: requests, a log line, a
// trace span.
func syntheticTraffic() {
	st := telemetry.Default()
	for i := 0; i < 3; i++ {
		st.Inc("sky_live_requests_total", map[string]string{"status": "200", "route": "/"})
	}
	now := time.Now()
	st.AppendLog(telemetry.LogEntry{TS: now, Level: "info", Message: "console-live-data synthetic log"})
	st.AppendTrace(telemetry.TraceEntry{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		Name: "GET /synthetic", Kind: "server", StartTime: now, EndTime: now.Add(time.Millisecond),
		StatusCode: "OK",
	})
}

// The console's own reads (the Store its init builds) return the host's
// telemetry in production token mode.
func TestConsoleStore_ReadsHostTelemetryUnderTokenAuth(t *testing.T) {
	consoleHost(t)
	syntheticTraffic()
	time.Sleep(1100 * time.Millisecond) // uptime is whole seconds

	model := Main_init_(nil).V0
	if model.ParentUrl == "" {
		t.Fatalf("init did not pick up SKY_PARENT_URL; the console would show mock data")
	}

	ov := model.Store.ReadOverview(struct{}{})()
	if ov.Tag != 0 {
		t.Fatalf("overview read failed (the console would stay on its empty model): %+v", ov.ErrValue)
	}
	o := ov.OkValue
	if o.SkyVersion == "" || o.SkyVersion == "—" {
		t.Errorf("SkyVersion = %q, want the host's version", o.SkyVersion)
	}
	if o.UptimeSeconds < 1 {
		t.Errorf("UptimeSeconds = %d, want > 0", o.UptimeSeconds)
	}
	if o.RequestsTotal < 3 {
		t.Errorf("RequestsTotal = %d, want >= 3 after synthetic traffic", o.RequestsTotal)
	}
	if !o.ProductionMode {
		t.Errorf("ProductionMode = false under ENV=production; the header would say dev")
	}

	logs := model.Store.ReadLogs(State_emptyLogFilter())()
	if logs.Tag != 0 || len(logs.OkValue) == 0 {
		t.Errorf("logs read: tag=%d n=%d err=%+v, want the synthetic log line", logs.Tag, len(logs.OkValue), logs.ErrValue)
	}
	traces := model.Store.ReadTraces(struct{}{})()
	if traces.Tag != 0 || len(traces.OkValue) == 0 {
		t.Errorf("traces read: tag=%d n=%d err=%+v, want the synthetic span", traces.Tag, len(traces.OkValue), traces.ErrValue)
	}

	// The reply feeds update; the model the view renders holds the values.
	next := Main_update(State_Msg_GotOverview(any(ov)), model).V0
	if next.Overview.RequestsTotal != o.RequestsTotal || next.LastError != "" {
		t.Errorf("update did not take the overview: requests=%d lastError=%q", next.Overview.RequestsTotal, next.LastError)
	}
}

// The running console, signed in over HTTP, pushes the live header to the
// browser: production mode and a counting uptime, not "— · dev · uptime 0s".
func TestConsoleSSE_PushesLiveHeaderAfterLogin(t *testing.T) {
	base := consoleHost(t)
	syntheticTraffic()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(base+"/_sky/console/_login", url.Values{"token": {os.Getenv("SKY_CONSOLE_TOKEN")}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status %d, want 303", resp.StatusCode)
	}
	page, err := client.Get(base + "/_sky/console/")
	if err != nil {
		t.Fatalf("console page: %v", err)
	}
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("console page: status %d", page.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_sky/console/_sky/sse?tab=t1&sl=1&path=%2F_sky%2Fconsole%2F", nil)
	req.Header.Set("Accept", "text/event-stream")
	sse, err := client.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer sse.Body.Close()
	if sse.StatusCode != http.StatusOK {
		t.Fatalf("sse: status %d", sse.StatusCode)
	}

	live := regexp.MustCompile(`prod (·|\\u00b7) uptime [1-9]`)
	sc := bufio.NewScanner(sse.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var seen strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: session-lost") {
			t.Fatalf("the signed-in console's stream reported session-lost")
		}
		if strings.HasPrefix(line, "data: ") {
			seen.WriteString(line)
			if live.MatchString(line) {
				return
			}
		}
	}
	tail := seen.String()
	if len(tail) > 600 {
		tail = tail[len(tail)-600:]
	}
	t.Fatalf("no frame carried the live header (\"prod · uptime N\") within 12 s; last data: %s", tail)
}
