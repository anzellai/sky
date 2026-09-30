//go:build !js

package rt

// A-2: two apps served in one process resolve the same session store from the
// environment, and a browser sends one cookie to every port of a host. Before
// v0.27.0 app B loaded app A's session for that cookie. These tests run two
// served apps over ONE sqlite store (the case the memory-store test in
// live_serve_test.go cannot see, because each app gets its own memory map).

import (
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serveDurableEnv is serveTestEnv with one sqlite session store shared by
// every app the test serves.
func serveDurableEnv(t *testing.T) {
	t.Helper()
	serveTestEnv(t)
	t.Setenv("SKY_LIVE_STORE", "sqlite")
	t.Setenv("SKY_LIVE_STORE_PATH", filepath.Join(t.TempDir(), "sessions.db"))
}

// namedServeCfg is serveTestCfg with a name.
func namedServeCfg(port int, label, name string) map[string]any {
	cfg := serveTestCfg(port, label)
	if name != "" {
		cfg["Name"] = name
	}
	return cfg
}

// getWithCookieHeader loads / with a raw Cookie header and returns the body
// and every Set-Cookie of the response.
func getWithCookieHeader(t *testing.T, addr, cookie string) (string, []*http.Cookie) {
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
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String(), resp.Cookies()
}

func sessionCookieOf(cs []*http.Cookie) *http.Cookie {
	for _, c := range cs {
		if strings.HasPrefix(c.Name, "sky_sid") || strings.HasPrefix(c.Name, "__Host-sky_sid") {
			return c
		}
	}
	return nil
}

func TestLiveServe_TwoAppsOnOneDurableStoreKeepTheirOwnSessions(t *testing.T) {
	serveDurableEnv(t)
	a := serveForTest(t, namedServeCfg(0, "alpha", "public"))
	b := serveForTest(t, namedServeCfg(0, "beta", "admin"))
	addrA, addrB := Live_address(a).(string), Live_address(b).(string)

	bodyA, setA := getWithCookieHeader(t, addrA, "")
	ca := sessionCookieOf(setA)
	if !strings.Contains(bodyA, "alpha") || ca == nil {
		t.Fatalf("app A did not render or set no session cookie: %v %s", setA, bodyA)
	}
	if ca.Name != "sky_sid_public" {
		t.Fatalf("served app A's session cookie is %q, want sky_sid_public", ca.Name)
	}
	if !strings.HasSuffix(ca.Value, ".public") {
		t.Fatalf("served app A's session id %q does not carry its namespace", ca.Value)
	}

	// The browser sends A's cookie to B (cookies ignore the port): B shows
	// its own init state, never A's model.
	jar := ca.Name + "=" + ca.Value
	bodyB, setB := getWithCookieHeader(t, addrB, jar)
	if strings.Contains(bodyB, "alpha") || !strings.Contains(bodyB, "beta") {
		t.Fatalf("app B rendered app A's session over one store: %s", bodyB)
	}
	cb := sessionCookieOf(setB)
	if cb == nil || cb.Name != "sky_sid_admin" {
		t.Fatalf("app B's session cookie: %v, want sky_sid_admin", setB)
	}

	// Planting A's id under B's cookie name does not reach A's row either.
	forged := "sky_sid_admin=" + ca.Value
	if body, _ := getWithCookieHeader(t, addrB, forged); strings.Contains(body, "alpha") {
		t.Fatalf("app B adopted app A's session id under its own cookie name: %s", body)
	}
	// Nor does the bare id of another namespace.
	bare := "sky_sid_admin=" + strings.TrimSuffix(ca.Value, ".public")
	if body, _ := getWithCookieHeader(t, addrB, bare); strings.Contains(body, "alpha") {
		t.Fatalf("app B adopted a bare session id: %s", body)
	}

	// B's own session keeps working.
	if body, _ := getWithCookieHeader(t, addrB, cb.Name+"="+cb.Value); !strings.Contains(body, "beta") {
		t.Fatalf("app B lost its own session: %s", body)
	}
}

func TestLiveServe_UnnamedServedAppIsNamespacedByItsPort(t *testing.T) {
	serveDurableEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ls := serveForTest(t, namedServeCfg(port, "gamma", ""))
	_, set := getWithCookieHeader(t, Live_address(ls).(string), "")
	c := sessionCookieOf(set)
	want := "sky_sid_port-" + strconv.Itoa(port)
	if c == nil || c.Name != want {
		t.Fatalf("unnamed served app's cookie: %v, want %s", set, want)
	}
}

func TestLiveServe_PortZeroWithADurableStoreNeedsAName(t *testing.T) {
	serveDurableEnv(t)
	res := Live_serve(namedServeCfg(0, "delta", "")).(func() any)().(SkyResult[any, any])
	if res.Tag == 0 {
		ls := res.OkValue.(*liveServer)
		_ = ls.stop(2 * time.Second)
		t.Fatal("a served app on port 0 with a sqlite store and no name started")
	}
	if msg := errorMessage(res.ErrValue); !strings.Contains(msg, "App.withName") ||
		!strings.Contains(msg, "see docs/migration/v0.27.md#served-app-namespace") {
		t.Fatalf("refusal does not name App.withName: %q", msg)
	}
	// With a name it starts.
	_ = serveForTest(t, namedServeCfg(0, "delta", "delta"))
}

func TestLiveServe_TwoServedAppsWithOneNameRefuse(t *testing.T) {
	serveDurableEnv(t)
	_ = serveForTest(t, namedServeCfg(0, "one", "same"))
	res := Live_serve(namedServeCfg(0, "two", "same")).(func() any)().(SkyResult[any, any])
	if res.Tag == 0 {
		ls := res.OkValue.(*liveServer)
		_ = ls.stop(2 * time.Second)
		t.Fatal("a second served app with the same name started")
	}
	if msg := errorMessage(res.ErrValue); !strings.Contains(msg, "same") {
		t.Fatalf("refusal does not name the namespace: %q", msg)
	}
}

// The process-owning app (App.run / Live.app) keeps the unprefixed cookie and
// 32-hex ids, so an existing deployment keeps its sessions.
func TestLiveApp_ProcessOwningAppStaysUnprefixed(t *testing.T) {
	serveDurableEnv(t)
	cfg := namedServeCfg(0, "owner", "ignored")
	ls, err := buildLiveServerFor(cfg, true, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = ls.stop(2 * time.Second) })
	go ls.run()
	_, set := getWithCookieHeader(t, ls.address(), "")
	c := sessionCookieOf(set)
	if c == nil || c.Name != "sky_sid" || !validSessionID(c.Value) {
		t.Fatalf("process-owning app's cookie: %v, want sky_sid with a 32-hex id", set)
	}
}

func TestLiveNamespace_OwnsOnlyItsOwnIDs(t *testing.T) {
	owner := &liveApp{}
	served := &liveApp{ns: "admin"}
	sid := newLiveSessionID()
	if !owner.ownsSID(sid) || owner.ownsSID(sid+".admin") {
		t.Fatal("the process-owning app must own exactly the 32-hex ids")
	}
	if served.ownsSID(sid) || !served.ownsSID(sid+".admin") || served.ownsSID(sid+".public") {
		t.Fatal("a served app must own exactly its own suffix")
	}
	if !served.ownsSID(served.newSID()) || !served.ownsSID(served.tokenSID("t")) {
		t.Fatal("a served app must own the ids it mints and derives")
	}
	if owner.tokenSID("t") == served.tokenSID("t") {
		t.Fatal("one header token names the same session in two apps")
	}
	for _, in := range []string{"Admin Panel", "ok_name", "a/b"} {
		if ns, ok := sanitiseLiveNamespace(in); !ok || !validNamespaceChars(ns) {
			t.Fatalf("sanitiseLiveNamespace(%q) = %q, %v", in, ns, ok)
		}
	}
	if _, ok := sanitiseLiveNamespace("///"); ok {
		t.Fatal("a name with no usable character was accepted")
	}
}
