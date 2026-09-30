//go:build !js

package rt

// Live.serve / Live.address / Live.stop (live_serve.go) and the audit of
// per-app state it depends on.
//
// The contract these tests lock:
//   - serve binds before it returns; port 0 picks a free port and address
//     names it; the app answers HTTP 200;
//   - stop releases the port (the same port binds again at once) and is
//     idempotent; a second app can then start in the same process;
//   - two apps served side by side keep their own sessions: a session of one
//     is unknown to the other, an event in one does not reach the other, and
//     stopping one leaves the other serving with its sessions intact;
//   - stop closes an open SSE stream of the app and returns within its bound;
//   - after stop no goroutine of the app is left (tickers, relays, the store
//     cleanup loop, the listener);
//   - stop removes every process-wide registration the app made (readiness
//     probe, shutdown hook, accept-stopper, release-phase closer, publish
//     target), and the inline console one app mounted is released for the
//     next app, which before v0.27 panicked on the second mount.

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// serveTestCfg is a counter-shaped app: the model is a label that gains a
// "!" per click, rendered in a button.
func serveTestCfg(port int, label string) map[string]any {
	return map[string]any{
		"Init": func(req any) any {
			return SkyTuple2{V0: label, V1: cmdT{kind: "none"}}
		},
		"Update": func(msg, model any) any {
			next := model
			if s, ok := model.(string); ok {
				next = s + "!"
			}
			return SkyTuple2{V0: next, V1: cmdT{kind: "none"}}
		},
		"View": func(model any) any {
			s, _ := model.(string)
			return velement("div", nil, []any{
				velement("button", []any{eventPair{name: "click", msg: "ClickMsg"}}, []any{vtext(s)}),
			})
		},
		"Subscriptions": func(any) any { return nil },
		"NotFound":      "not-found",
		"Routes":        []any{},
		"Port":          port,
	}
}

// serveForTest starts cfg with Live.serve and fails the test on an Err. The
// app is stopped at cleanup (stop is idempotent).
func serveForTest(t *testing.T, cfg map[string]any) *liveServer {
	t.Helper()
	res := Live_serve(cfg).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Live.serve: Err %s", errorMessage(res.ErrValue))
	}
	ls, ok := res.OkValue.(*liveServer)
	if !ok {
		t.Fatalf("Live.serve returned %T, want *liveServer", res.OkValue)
	}
	t.Cleanup(func() { _ = ls.stop(2 * time.Second) })
	return ls
}

func stopForTest(t *testing.T, ls *liveServer) {
	t.Helper()
	res := Live_stop(ls).(func() any)().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Live.stop: Err %s", errorMessage(res.ErrValue))
	}
}

// serveTestEnv isolates the process-wide settings a served app reads.
func serveTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SKY_PARENT_URL", "http://127.0.0.1:1") // never seeded by the app
	t.Setenv("SKY_LIVE_STORE", "memory")
	t.Setenv("SKY_CONSOLE_EMBED", "off")
	t.Setenv("ENV", "")
	SetCsrfEnabled(false)
	// Booting an app in this process sets process-wide state that other
	// tests read: the tracer (it would stamp trace ids on every request) and
	// the production-mode snapshot. Keep the first out and undo the second.
	prevTracing := liveInitTracing
	liveInitTracing = func() error { return nil }
	t.Cleanup(func() {
		refreshCsrfEnabled()
		liveInitTracing = prevTracing
		clearProductionMode()
	})
}

var serveTestClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{DisableKeepAlives: true},
}

// serveGetPage loads / from addr with cookie (may be ""), returning the response
// body and the session cookie the response set (or "").
func serveGetPage(t *testing.T, addr, cookie string) (string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := serveTestClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s/: %v", addr, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/: status %d, body %s", addr, resp.StatusCode, body)
	}
	set := ""
	for _, c := range resp.Cookies() {
		// A served app's cookie is sky_sid_<namespace> (live_namespace.go).
		if strings.HasPrefix(strings.TrimPrefix(c.Name, "__Host-"), "sky_sid") {
			set = c.Name + "=" + c.Value
		}
	}
	return string(body), set
}

func TestLiveServe_PortZeroServesThenStopReleasesThePort(t *testing.T) {
	serveTestEnv(t)
	ls := serveForTest(t, serveTestCfg(0, "first"))
	addr := Live_address(ls).(string)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "0" || port == "" {
		t.Fatalf("Live.address = %q, want a bound host:port", addr)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Live.address host = %q, want the development loopback bind", host)
	}
	body, cookie := serveGetPage(t, addr, "")
	if !strings.Contains(body, "first") || cookie == "" {
		t.Fatalf("page did not render the app (cookie %q): %s", cookie, body)
	}
	stopForTest(t, ls)
	stopForTest(t, ls) // idempotent
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatalf("port %s still accepts connections after Live.stop", addr)
	}
	// The same port binds again in the same process.
	var n int
	for _, ch := range port {
		n = n*10 + int(ch-'0')
	}
	again := serveForTest(t, serveTestCfg(n, "second"))
	if got := Live_address(again).(string); got != addr {
		t.Fatalf("restart bound %q, want %q", got, addr)
	}
	if body, _ := serveGetPage(t, addr, ""); !strings.Contains(body, "second") {
		t.Fatalf("restarted app did not serve: %s", body)
	}
	stopForTest(t, again)
}

func TestLiveServe_PortInUseIsTheTasksErr(t *testing.T) {
	serveTestEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	probesBefore := len(scopedProbeList())
	res := Live_serve(serveTestCfg(port, "x")).(func() any)().(SkyResult[any, any])
	if res.Tag == 0 {
		t.Fatal("Live.serve on a taken port returned Ok")
	}
	if msg := errorMessage(res.ErrValue); !strings.Contains(msg, "already in use") {
		t.Fatalf("Err message %q does not say the port is in use", msg)
	}
	// A failed start leaves no registration behind.
	if got := len(scopedProbeList()); got != probesBefore {
		t.Fatalf("readiness probes: %d after a failed start, want %d", got, probesBefore)
	}
}

func TestLiveServe_TwoAppsKeepTheirOwnSessions(t *testing.T) {
	serveTestEnv(t)
	a := serveForTest(t, serveTestCfg(0, "alpha"))
	b := serveForTest(t, serveTestCfg(0, "beta"))
	addrA, addrB := Live_address(a).(string), Live_address(b).(string)
	if addrA == addrB {
		t.Fatalf("both apps bound %s", addrA)
	}
	bodyA, cookieA := serveGetPage(t, addrA, "")
	bodyB, cookieB := serveGetPage(t, addrB, "")
	if !strings.Contains(bodyA, "alpha") || !strings.Contains(bodyB, "beta") {
		t.Fatalf("apps rendered each other's view:\nA: %s\nB: %s", bodyA, bodyB)
	}
	sidA := cookieA[strings.Index(cookieA, "=")+1:]
	if _, ok := b.app.store.Get(sidA); ok {
		t.Fatal("app A's session is visible in app B's store")
	}
	// A's cookie presented to B is an unknown session there: B serves its
	// own init, never A's model.
	if body, _ := serveGetPage(t, addrB, cookieA); strings.Contains(body, "alpha") {
		t.Fatalf("app B rendered app A's session: %s", body)
	}
	// An event in A changes A only.
	hid := clickHandlerID(t, a.app, sidA)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addrA+"/_sky/event",
		strings.NewReader(eventBody(sidA, hid)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookieA)
	resp, err := serveTestClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event on A: status %d", resp.StatusCode)
	}
	if got := modelOf(t, a.app, sidA); got != "alpha!" {
		t.Fatalf("A's model after a click = %q, want alpha!", got)
	}
	sidB := cookieB[strings.Index(cookieB, "=")+1:]
	if got := modelOf(t, b.app, sidB); got != "beta" {
		t.Fatalf("B's model changed to %q by an event in A", got)
	}
	// Stopping A leaves B serving, with its session.
	stopForTest(t, a)
	if body, _ := serveGetPage(t, addrB, cookieB); !strings.Contains(body, "beta") {
		t.Fatalf("app B stopped serving when A stopped: %s", body)
	}
	if _, ok := b.app.store.Get(sidB); !ok {
		t.Fatal("app B lost its session when A stopped")
	}
}

func TestLiveServe_StopClosesAnOpenSSEStream(t *testing.T) {
	serveTestEnv(t)
	ls := serveForTest(t, serveTestCfg(0, "sse"))
	addr := Live_address(ls).(string)
	_, cookie := serveGetPage(t, addr, "")
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/_sky/sse?tab=t1&sl=1", nil)
	req.Header.Set("Cookie", cookie)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	hello := false
	for !hello {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("SSE closed before hello: %v", err)
		}
		hello = strings.HasPrefix(line, "event: hello")
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, rd)
		close(done)
	}()
	start := time.Now()
	stopForTest(t, ls)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Live.stop took %v with an open SSE stream (want a prompt drain)", took)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the SSE stream stayed open after Live.stop")
	}
}

// settleGoroutines waits until the goroutine count is at most want.
func settleGoroutines(want int) int {
	deadline := time.Now().Add(5 * time.Second)
	n := runtime.NumGoroutine()
	for n > want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func TestLiveServe_StopLeavesNoGoroutineBehind(t *testing.T) {
	serveTestEnv(t)
	cycle := func() {
		ls := serveForTest(t, serveTestCfg(0, "leak"))
		addr := Live_address(ls).(string)
		_, cookie := serveGetPage(t, addr, "")
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/_sky/sse?tab=t1&sl=1", nil)
		req.Header.Set("Cookie", cookie)
		resp, err := serveTestClient.Do(req)
		if err == nil {
			rd := bufio.NewReader(resp.Body)
			_, _ = rd.ReadString('\n')
			go func() { _, _ = io.Copy(io.Discard, rd); resp.Body.Close() }()
		}
		stopForTest(t, ls)
	}
	// Warm up the process-lifetime goroutines (they start once).
	cycle()
	time.Sleep(300 * time.Millisecond)
	base := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		cycle()
	}
	// Tolerance 2: the runtime's own background goroutines come and go.
	if got := settleGoroutines(base + 2); got > base+2 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines: %d after three serve/stop cycles, baseline %d\n%s", got, base, buf[:n])
	}
}

func TestLiveServe_StopRemovesProcessWideRegistrations(t *testing.T) {
	serveTestEnv(t)
	// An earlier test may have run the shutdown chain, which closes the hook
	// registry for the process; start from an open one.
	resetShutdownHooksForTesting()
	t.Cleanup(resetShutdownHooksForTesting)
	count := func() (probes, hooks, stoppers, closers, brokers int) {
		probes = len(scopedProbeList())
		shutdownMu.Lock()
		hooks = len(shutdownHooks)
		shutdownMu.Unlock()
		acceptMu.Lock()
		stoppers = len(acceptStoppers)
		acceptMu.Unlock()
		releaseMu.Lock()
		closers = len(resourceClosers)
		releaseMu.Unlock()
		brokers = len(processBrokerApps())
		return
	}
	p0, h0, s0, c0, b0 := count()
	ls := serveForTest(t, serveTestCfg(0, "reg"))
	p1, h1, s1, c1, b1 := count()
	if p1 != p0+1 || h1 != h0+1 || s1 != s0+1 || c1 != c0+1 || b1 != b0+1 {
		t.Fatalf("serve registered probes %d→%d hooks %d→%d stoppers %d→%d closers %d→%d brokers %d→%d; want +1 each",
			p0, p1, h0, h1, s0, s1, c0, c1, b0, b1)
	}
	stopForTest(t, ls)
	p2, h2, s2, c2, b2 := count()
	if p2 != p0 || h2 != h0 || s2 != s0 || c2 != c0 || b2 != b0 {
		t.Fatalf("after stop: probes %d hooks %d stoppers %d closers %d brokers %d; want %d %d %d %d %d",
			p2, h2, s2, c2, b2, p0, h0, s0, c0, b0)
	}
}

func TestLiveServe_OneConsolePerProcessReleasedOnStop(t *testing.T) {
	serveTestEnv(t)
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_CONSOLE_AUTH", "")
	inlineConsoleCfgMu.RLock()
	prevProvider := inlineConsoleCfgProvider
	inlineConsoleCfgMu.RUnlock()
	RegisterInlineConsoleCfgProvider(func() any { return serveTestCfg(0, "console") })
	t.Cleanup(func() { RegisterInlineConsoleCfgProvider(prevProvider) })

	a := serveForTest(t, serveTestCfg(0, "a"))
	if a.console == nil || !consoleOwnedBy(a) {
		t.Fatal("the first served app did not mount the inline console")
	}
	// Before v0.27 this second mount panicked: sub-app already mounted at
	// /_sky/console.
	b := serveForTest(t, serveTestCfg(0, "b"))
	if b.console != nil {
		t.Fatal("a second app mounted a second console")
	}
	stopForTest(t, a)
	if LookupInProcessSubApp("/_sky/console") != nil {
		t.Fatal("the console sub-app is still registered after its owner stopped")
	}
	c := serveForTest(t, serveTestCfg(0, "c"))
	if c.console == nil {
		t.Fatal("a later app could not mount the console after the owner stopped")
	}
	stopForTest(t, c)
	stopForTest(t, b)
}

func TestLiveServe_RevocationGateIsPerApp(t *testing.T) {
	serveTestEnv(t)
	dbA := &SkyDb{}
	cfgA := serveTestCfg(0, "gated")
	cfgA["Revocation"] = dbA
	a := serveForTest(t, cfgA)
	b := serveForTest(t, serveTestCfg(0, "open"))
	if a.app.revocationGate() == nil || a.app.revocationGate().db != dbA {
		t.Fatal("app A does not carry its own revocation gate")
	}
	if b.app.revocationGate() != nil {
		t.Fatal("app B inherited app A's revocation gate")
	}
	if !revocationGateEnabled() {
		t.Fatal("revocationGateEnabled is false while a gated app runs")
	}
	stopForTest(t, a)
	if revocationGateEnabled() {
		t.Fatal("revocationGateEnabled stayed true after the gated app stopped")
	}
}

func TestLiveServe_ConflictingSlidingAuthRefusesToStart(t *testing.T) {
	serveTestEnv(t)
	cfgA := serveTestCfg(0, "a")
	cfgA["AuthSliding"] = map[string]any{"Cookie": "a_auth", "SecretEnv": "A_SECRET"}
	a := serveForTest(t, cfgA)
	cfgB := serveTestCfg(0, "b")
	cfgB["AuthSliding"] = map[string]any{"Cookie": "b_auth", "SecretEnv": "B_SECRET"}
	res := Live_serve(cfgB).(func() any)().(SkyResult[any, any])
	if res.Tag == 0 {
		ls := res.OkValue.(*liveServer)
		_ = ls.stop(time.Second)
		t.Fatal("a second app with a different sliding-auth config started")
	}
	// The same config is shared.
	cfgC := serveTestCfg(0, "c")
	cfgC["AuthSliding"] = map[string]any{"Cookie": "a_auth", "SecretEnv": "A_SECRET"}
	c := serveForTest(t, cfgC)
	stopForTest(t, a)
	if getAuthSlidingConfig() == nil {
		t.Fatal("the process sliding config was cleared while app C still uses it")
	}
	stopForTest(t, c)
	if getAuthSlidingConfig() != nil {
		t.Fatal("the process sliding config outlived every app that claimed it")
	}
}
