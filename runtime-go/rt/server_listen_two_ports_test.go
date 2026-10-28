//go:build !js

package rt

// One process, several servers (v0.28.0).
//
// A Sky program may serve two ports from one process: a public API and an
// admin API, each started with `Task.spawn (Server.listen …)`, or a
// Server.listen next to an embedded Sky.Live app. Before the fix every
// Server.listen shared ONE console owner token, so the second listener took
// itself for the console's owner, mounted the console sub-app again and
// panicked ("sub-app already mounted at /_sky/console"). The second server
// never bound, and under Task.spawn nothing reached the program.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// listenRig runs Server.listen Tasks the way `Task.spawn` does and stops
// every server it started at cleanup, waiting for each Task to end, so the
// console a server owns is released before the next test.
type listenRig struct {
	t     *testing.T
	mu    sync.Mutex
	srvs  []*http.Server
	dones []<-chan any
}

func newListenRig(t *testing.T) *listenRig {
	t.Helper()
	clearHostGuardEnv(t)
	t.Setenv("ENV", "")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("SKY_PARENT_URL", "http://127.0.0.1:1") // never seeded by the server
	ResetConsoleAuthStateForTesting()
	inlineConsoleCfgMu.RLock()
	prevProvider := inlineConsoleCfgProvider
	inlineConsoleCfgMu.RUnlock()
	RegisterInlineConsoleCfgProvider(func() any { return serveTestCfg(0, "console") })
	prevTracing := liveInitTracing
	liveInitTracing = func() error { return nil }

	rig := &listenRig{t: t}
	serverListenStartedHook = func(s *http.Server) {
		rig.mu.Lock()
		rig.srvs = append(rig.srvs, s)
		rig.mu.Unlock()
	}
	t.Cleanup(func() {
		rig.mu.Lock()
		srvs, dones := rig.srvs, rig.dones
		rig.mu.Unlock()
		for _, s := range srvs {
			_ = s.Close()
		}
		for _, d := range dones {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Errorf("a Server.listen Task did not end within 5s of its server closing")
			}
		}
		serverListenStartedHook = nil
		RegisterInlineConsoleCfgProvider(prevProvider)
		liveInitTracing = prevTracing
		clearProductionMode()
		ResetConsoleAuthStateForTesting()
	})
	return rig
}

// start runs `Server.listen port routes` on its own goroutine. The returned
// channel carries the Task's result, or a recovered panic as an error.
func (rig *listenRig) start(port int, routes []any) <-chan any {
	done := make(chan any, 1)
	relay := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("Server_listen panicked: %v", r)
			}
		}()
		done <- anyTaskInvoke(Server_listen(port, routes))
	}()
	// The test reads `relay`; cleanup waits on `waited`, which closes once the
	// Task's result has arrived, whoever read it first.
	waited := make(chan any)
	go func() {
		r := <-done
		relay <- r
		close(waited)
	}()
	rig.mu.Lock()
	rig.dones = append(rig.dones, waited)
	rig.mu.Unlock()
	return relay
}

// stop closes the server listening on port and returns its Task's result.
func (rig *listenRig) stop(port int, done <-chan any) any {
	rig.t.Helper()
	rig.mu.Lock()
	for _, s := range rig.srvs {
		if strings.HasSuffix(s.Addr, fmt.Sprintf(":%d", port)) {
			_ = s.Close()
		}
	}
	rig.mu.Unlock()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		rig.t.Fatalf("Server.listen on :%d did not end within 5s of its server closing", port)
		return nil
	}
}

// getWithin polls url until it answers or the budget runs out. When the
// server's Task ends first, its result is the failure: a server that cannot
// start must say so through its Task, never by going quiet.
func getWithin(t *testing.T, url string, done <-chan any, budget time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		select {
		case res := <-done:
			t.Fatalf("%s: the server's Task ended before it answered: %#v", url, res)
		default:
		}
		resp, err := serveTestClient.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode, string(body)
		}
		if time.Now().After(deadline) {
			_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
			t.Fatalf("%s never answered: %v", url, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func portURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

// Two Server.listen calls in one process, each on its own port with its own
// routes (a public API and an admin API), both serve. The first owns the Sky
// Console, the second serves none, and each keeps its own routes.
func TestServerListen_TwoPortsInOneProcessBothServe(t *testing.T) {
	rig := newListenRig(t)
	pubPort, admPort := freePort(t), freePort(t)
	pubDone := rig.start(pubPort, []any{
		SkyRoute{Method: "GET", Path: "/public", Handler: fakeHandler("from-public")},
	})
	code, body := getWithin(t, portURL(pubPort, "/public"), pubDone, 5*time.Second)
	if code != 200 || !strings.Contains(body, "from-public") {
		t.Fatalf("public: status=%d body=%q", code, body)
	}

	admDone := rig.start(admPort, []any{
		SkyRoute{Method: "GET", Path: "/admin", Handler: fakeHandler("from-admin")},
	})
	code, body = getWithin(t, portURL(admPort, "/admin"), admDone, 5*time.Second)
	if code != 200 || !strings.Contains(body, "from-admin") {
		t.Fatalf("admin: status=%d body=%q", code, body)
	}

	// Each server keeps its own routes: neither serves the other's.
	if code, _ := getWithin(t, portURL(pubPort, "/admin"), pubDone, 2*time.Second); code != 404 {
		t.Errorf("public port served /admin: status=%d, want 404", code)
	}
	if code, _ := getWithin(t, portURL(admPort, "/public"), admDone, 2*time.Second); code != 404 {
		t.Errorf("admin port served /public: status=%d, want 404", code)
	}
	// The first is still up after the second started.
	if code, body := getWithin(t, portURL(pubPort, "/public"), pubDone, 2*time.Second); code != 200 || !strings.Contains(body, "from-public") {
		t.Errorf("public after admin start: status=%d body=%q", code, body)
	}
	// One console per process: the first listener serves it, the second not.
	if code, _ := getWithin(t, portURL(pubPort, "/_sky/console"), pubDone, 2*time.Second); code == 404 {
		t.Errorf("the first listener serves no console: /_sky/console is 404")
	}
	if code, _ := getWithin(t, portURL(admPort, "/_sky/console"), admDone, 2*time.Second); code != 404 {
		t.Errorf("the second listener serves a console too: /_sky/console status=%d, want 404", code)
	}

	// When the owner stops, its Task ends with Ok and the console is
	// released: the next listener in the process mounts it.
	if r := rig.stop(pubPort, pubDone); !isOkResult(r) {
		t.Fatalf("closing the public server: Task result %#v, want Ok", r)
	}
	if LookupInProcessSubApp("/_sky/console") != nil {
		t.Fatal("the console sub-app is still registered after its owner stopped")
	}
	nextPort := freePort(t)
	nextDone := rig.start(nextPort, []any{
		SkyRoute{Method: "GET", Path: "/next", Handler: fakeHandler("from-next")},
	})
	if code, _ := getWithin(t, portURL(nextPort, "/_sky/console"), nextDone, 5*time.Second); code == 404 {
		t.Errorf("a later listener could not mount the console after the owner stopped")
	}
}

// A Server.listen that cannot bind (the port is held) ends its Task with
// Err, so `Task.spawn … |> Task.onError` and the entry's Err path see it. It
// does not exit the process and does not hang.
func TestServerListen_PortInUseIsTheTasksErr(t *testing.T) {
	rig := newListenRig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	done := rig.start(port, []any{
		SkyRoute{Method: "GET", Path: "/x", Handler: fakeHandler("x")},
	})
	var res any
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Server.listen on a held port did not end its Task within 10s")
	}
	if e, ok := res.(error); ok {
		t.Fatalf("Server.listen on a held port: %v", e)
	}
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 1 {
		t.Fatalf("Server.listen on a held port: got %#v, want Err", res)
	}
	if msg := errorMessage(r.ErrValue); !strings.Contains(msg, fmt.Sprintf("port %d is already in use", port)) {
		t.Errorf("Err does not say the port is in use: %s", msg)
	}
	// A server that never started holds nothing process-wide.
	if LookupInProcessSubApp("/_sky/console") != nil {
		t.Error("a Server.listen that failed to bind left the console mounted")
	}
}

// SKY_CONSOLE_AUTH=token with no console to mount is a refusal to start. It
// is the Task's Err (the entry's Err path exits 1), not an exit from inside
// a server that may be one of several, and the port is released.
func TestServerListen_ConsoleBootCheckIsTheTasksErr(t *testing.T) {
	rig := newListenRig(t)
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "test-console-token-0123456789abcdef")
	t.Setenv("SKY_CONSOLE_DISABLED", "1") // no legacy shell either
	RegisterInlineConsoleCfgProvider(func() any { return nil })
	ResetConsoleAuthStateForTesting()
	ResetConsoleHealthFlagsForTesting()
	port := freePort(t)
	done := rig.start(port, []any{SkyRoute{Method: "GET", Path: "/x", Handler: fakeHandler("x")}})
	var res any
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Server.listen with no console to mount did not end its Task within 10s")
	}
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 1 {
		t.Fatalf("got %#v, want Err", res)
	}
	if msg := errorMessage(r.ErrValue); !strings.Contains(msg, "SKY_CONSOLE_AUTH=token") {
		t.Errorf("Err does not name the console setting: %s", msg)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("the refused server kept its port: %v", err)
	}
	_ = ln.Close()
}

// A Server.listen and an embedded Sky.Live app (Live.serve) in one process,
// in both start orders: both serve, and exactly one of them owns the console.
func TestServerListen_NextToLiveServeBothServe(t *testing.T) {
	for _, serverFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("serverFirst=%v", serverFirst), func(t *testing.T) {
			rig := newListenRig(t)
			serveTestEnv(t)
			t.Setenv("SKY_CONSOLE_EMBED", "")
			port := freePort(t)
			routes := []any{SkyRoute{Method: "GET", Path: "/api", Handler: fakeHandler("from-api")}}

			var done <-chan any
			var ls *liveServer
			if serverFirst {
				done = rig.start(port, routes)
				if code, body := getWithin(t, portURL(port, "/api"), done, 5*time.Second); code != 200 || !strings.Contains(body, "from-api") {
					t.Fatalf("server: status=%d body=%q", code, body)
				}
				ls = serveForTest(t, serveTestCfg(0, "live-app"))
			} else {
				ls = serveForTest(t, serveTestCfg(0, "live-app"))
				done = rig.start(port, routes)
				if code, body := getWithin(t, portURL(port, "/api"), done, 5*time.Second); code != 200 || !strings.Contains(body, "from-api") {
					t.Fatalf("server: status=%d body=%q", code, body)
				}
			}
			page, _ := serveGetPage(t, fmt.Sprintf("127.0.0.1:%d", ls.port), "")
			if !strings.Contains(page, "live-app") {
				t.Errorf("the Live app did not serve its page next to Server.listen: %q", page)
			}
			serverConsole, _ := getWithin(t, portURL(port, "/_sky/console"), done, 2*time.Second)
			if serverFirst {
				if serverConsole == 404 || ls.console != nil {
					t.Errorf("server first: server console status=%d, live console mounted=%v; want the server to own it",
						serverConsole, ls.console != nil)
				}
			} else {
				if serverConsole != 404 || ls.console == nil {
					t.Errorf("live first: server console status=%d, live console mounted=%v; want the Live app to own it",
						serverConsole, ls.console != nil)
				}
			}
			stopForTest(t, ls)
			if r := rig.stop(port, done); !isOkResult(r) {
				t.Fatalf("closing the server: Task result %#v, want Ok", r)
			}
		})
	}
}

// Two Server.listen servers that each serve a WebSocket route: an upgrade on
// either port reaches that server's own handler.
func TestServerListen_TwoPortsEachServeTheirWebSocket(t *testing.T) {
	rig := newListenRig(t)
	var mu sync.Mutex
	got := map[string]string{}
	ok := func() any { return Ok[any, any](skyUnit()) }
	wsRoute := func(label string) SkyRoute {
		cfg := map[string]any{
			"onConnect": func(_ any) any { return ok },
			"onMessage": func(_ any) any {
				return func(msg any) any {
					mu.Lock()
					got[label] = msg.(string)
					mu.Unlock()
					return ok
				}
			},
			"onFrame":         func(_ any) any { return func(_ any) any { return ok } },
			"frameMode":       false,
			"onClose":         func(_ any) any { return ok },
			"onError":         func(_ any) any { return func(_ any) any { return ok } },
			"maxMessageBytes": 1 << 16,
			"originPatterns":  []any{},
		}
		return SkyRoute{Method: "GET", Path: "/ws", Handler: func(req SkyRequest) any {
			return ServerWebSocket_upgrade(req, cfg)
		}}
	}
	ports := map[string]int{"one": freePort(t), "two": freePort(t)}
	for _, label := range []string{"one", "two"} {
		port := ports[label]
		done := rig.start(port, []any{
			wsRoute(label),
			SkyRoute{Method: "GET", Path: "/up", Handler: fakeHandler("up")},
		})
		getWithin(t, portURL(port, "/up"), done, 5*time.Second)
	}
	for _, label := range []string{"one", "two"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws", ports[label]), nil)
		if err != nil {
			cancel()
			t.Fatalf("dial %s: %v", label, err)
		}
		if err := c.Write(ctx, websocket.MessageText, []byte("hello-"+label)); err != nil {
			t.Fatalf("write %s: %v", label, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			v := got[label]
			mu.Unlock()
			if v != "" {
				if v != "hello-"+label {
					t.Errorf("server %s received %q, want %q", label, v, "hello-"+label)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("server %s never received its WebSocket message", label)
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
		cancel()
	}
}
