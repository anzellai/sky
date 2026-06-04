package rt

// Regression test for #466 — Server_listen must register routes with
// Go 1.22+ method-aware ServeMux pattern when a specific Method is
// set on the SkyRoute. Pre-fix, two routes on the SAME path but with
// DIFFERENT methods panicked at boot ("pattern conflicts with pattern").

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// fakeHandler returns a handler value the runtime accepts.
// Shape: func(SkyRequest) any returning a func() any (the SkyTask
// the dispatcher executes).
func fakeHandler(label string) any {
	return func(req SkyRequest) any {
		return func() any {
			return SkyResponse{Status: 200, Body: label, ContentType: "text/plain"}
		}
	}
}

// waitForBind retries Get against the URL for up to 2s. Returns the
// first non-error response, or fails the test.
func waitForBind(t *testing.T, port int, path string) *http.Response {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never bound on :%d (%s): %v", port, path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServerListen_SamePathDifferentMethodsCoexist_466(t *testing.T) {
	port := freePort(t)
	// CSRF gate would 403 the POST otherwise; the routing fix is what's
	// under test, not the CSRF defaults.
	WithoutCsrf("/api/x")

	routes := []any{
		SkyRoute{Method: "GET", Path: "/api/x", Handler: fakeHandler("got-get")},
		SkyRoute{Method: "POST", Path: "/api/x", Handler: fakeHandler("got-post")},
	}

	// Server_listen blocks; trap panics to surface them as test failures.
	var serverErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				serverErr = fmt.Errorf("Server_listen panicked: %v", r)
			}
		}()
		Server_listen(port, routes)
	}()

	// Probe GET — wait for bind.
	resp := waitForBind(t, port, "/api/x")
	if serverErr != nil {
		t.Fatal(serverErr)
	}
	gBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(gBody), "got-get") {
		t.Errorf("GET: status=%d body=%q want 200 + got-get", resp.StatusCode, string(gBody))
	}

	// Probe POST — both should coexist on the same path.
	url := fmt.Sprintf("http://127.0.0.1:%d/api/x", port)
	pResp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	pBody, _ := io.ReadAll(pResp.Body)
	_ = pResp.Body.Close()
	if pResp.StatusCode != 200 || !strings.Contains(string(pBody), "got-post") {
		t.Errorf("POST: status=%d body=%q want 200 + got-post", pResp.StatusCode, string(pBody))
	}
}
