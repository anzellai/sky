//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func postViewport(t *testing.T, app *liveApp, sid string, width int, fresh bool) *httptest.ResponseRecorder {
	t.Helper()
	if fresh {
		app.store.Set(sid, &liveSession{
			sseCh:     make(chan sseFrame, 4),
			cancelSub: make(chan struct{}),
			model:     "",
			handlers:  map[string]any{},
		})
	}
	body := strings.NewReader(`{"sessionId":"` + sid + `","msg":"__skyViewport","args":[` + strconv.Itoa(width) + `]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "sky_sid="+sid)
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	return rr
}

// Sub.onBreakpoint on Sky.Live: the reported width reaches update as the
// breakpoint the SERVER computes from the app's own widths; a second report
// in the same breakpoint dispatches nothing (204), a crossing dispatches again.
func TestLiveViewportReportReachesTheBreakpointSub(t *testing.T) {
	app := fragmentTestApp(func(model any) any {
		return Sub_batch([]any{Sub_every(60000, "Tick"), Sub_onBreakpoint([]any{600, 900}, func(b any) any { return "bp:" + strconv.Itoa(AsInt(b)) })})
	})
	rr := postViewport(t, app, "sid-vp", 700, true)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "FRAG=bp:600") {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Sky-Breakpoints"); got != "600,900" {
		t.Fatalf("X-Sky-Breakpoints = %q, want 600,900", got)
	}
	rr = postViewport(t, app, "sid-vp", 850, false)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("a report in the same breakpoint must be a no-op, got %d", rr.Code)
	}
	rr = postViewport(t, app, "sid-vp", 320, false)
	sess, _ := app.store.Get("sid-vp")
	if rr.Code != 200 || sess.model != "bp:0" {
		t.Fatalf("crossing below every width must deliver 0: %d model=%v", rr.Code, sess.model)
	}
}

// Without a Sub.onBreakpoint leaf the report is a 204 no-op, model unchanged,
// and the empty header tells the client to stop reporting.
func TestLiveViewportReportWithoutASubIsANoOp(t *testing.T) {
	app := fragmentTestApp(func(model any) any { return Sub_none() })
	rr := postViewport(t, app, "sid-novp", 1000, true)
	if rr.Code != http.StatusNoContent || rr.Body.Len() != 0 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if h, ok := rr.Header()["X-Sky-Breakpoints"]; !ok || len(h) != 1 || h[0] != "" {
		t.Fatalf("want an empty X-Sky-Breakpoints header, got %v", rr.Header())
	}
	if sess, _ := app.store.Get("sid-novp"); sess.model != "" {
		t.Fatalf("model changed to %v without a Sub.onBreakpoint", sess.model)
	}
}

// The page hands the client the widths: in the boot config of a full load
// and in the X-Sky-Breakpoints header a sky-nav fetch reads.
func TestLivePageCarriesTheBreakpointWidths(t *testing.T) {
	app := newMirrorTestApp()
	app.subscriptions = func(model any) any { return Sub_onBreakpoint([]any{900, 600}, func(b any) any { return b }) }
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Header().Get("X-Sky-Breakpoints") != "600,900" {
		t.Fatalf("page header = %q", rr.Header().Get("X-Sky-Breakpoints"))
	}
	if !strings.Contains(rr.Body.String(), `"bp":[600,900]`) {
		t.Fatalf("the boot config must carry the widths:\n%s", rr.Body.String())
	}
	plain := newMirrorTestApp()
	rr = httptest.NewRecorder()
	plain.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rr.Body.String(), `"bp":`) || rr.Header().Get("X-Sky-Breakpoints") != "" {
		t.Fatal("an app without the leaf carries no widths")
	}
}

// Sub.onQuery on Sky.Live: the query of the page request reaches update in
// the same step as the route, so the page already renders it; a navigation
// that keeps the query delivers nothing, one that changes it (or removes it)
// delivers again.
func TestLiveQueryReachesTheQuerySubOnEveryPageRequest(t *testing.T) {
	var delivered []string
	app := newMirrorTestApp()
	app.subscriptions = func(model any) any {
		return Sub_onQuery(func(d any) any {
			v, _ := d.(SkyDict[any]).Lookup("page")
			s, _ := v.(string)
			delivered = append(delivered, s)
			return "page=" + s
		})
	}
	app.update = func(msg, model any) any { return SkyTuple2{V0: msg, V1: cmdT{kind: "none"}} }
	app.view = func(model any) any { return velement("div", nil, []any{vtext("MODEL=" + AsString(model))}) }
	get := func(target, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-Sky-Nav", "1")
		}
		rr := httptest.NewRecorder()
		app.handleInitial(rr, req)
		return rr
	}
	rr := get("/?page=2&q=a%20b", "")
	if !strings.Contains(rr.Body.String(), "MODEL=page=2") {
		t.Fatalf("the first paint must show the query:\n%.400s", rr.Body.String())
	}
	cookie := rr.Header().Get("Set-Cookie")
	if i := strings.Index(cookie, ";"); i > 0 {
		cookie = cookie[:i]
	}
	get("/?page=2&q=a%20b", cookie)
	rr = get("/?page=3", cookie)
	if !strings.Contains(rr.Body.String(), "MODEL=page=3") {
		t.Fatalf("a changed query must reach update:\n%.400s", rr.Body.String())
	}
	get("/", cookie)
	if strings.Join(delivered, "|") != "2|3|" {
		t.Fatalf("deliveries = %q, want 2|3| (an unchanged query is not delivered again)", delivered)
	}
	// An app without the leaf: the query is only init's.
	delivered = nil
	app.subscriptions = func(model any) any { return Sub_none() }
	get("/?page=5", "")
	if len(delivered) != 0 {
		t.Fatal("no leaf, no delivery")
	}
}

// The browser half: the client reports its width at load (the page config
// names the widths), again only when a resize crosses one, and stops when an
// answer's X-Sky-Breakpoints header says the app has no leaf any more.
func TestLiveJS_ReportsTheViewportOnlyWhenABreakpointIsCrossed(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const winListeners = {};
const posts = [];
let nextBp = "600,900";
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  requestAnimationFrame: (fn) => { fn(); return 1; },
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", hash: "", reload() {} },
  navigator: {},
  innerWidth: 700,
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "tab-A", csrf: "tok", bp: [600, 900] }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) { this.url = url; this.readyState = 1; this.addEventListener = () => {}; this.close = () => {}; },
  fetch: (url, opts) => {
    if (String(url).endsWith("/_sky/event")) posts.push(JSON.parse(opts.body));
    return Promise.resolve({ ok: true, status: 204, headers: new Headers({ "X-Sky-Live": "1", "X-Sky-Breakpoints": nextBp }), json: async () => ({ seq: 1 }), text: async () => "" });
  },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = (ev, fn) => { (winListeners[ev] = winListeners[ev] || []).push(fn); };
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const resize = (w) => { sandbox.innerWidth = w; (winListeners["resize"] || []).forEach((fn) => fn({})); };
(async () => {
  const settle = async () => { for (let i = 0; i < 6; i++) await new Promise((r) => setImmediate(r)); };
  await settle();
  for (const w of [710, 750, 899]) { resize(w); await settle(); }
  resize(1200); await settle();
  resize(1300); await settle();
  nextBp = "";
  resize(500); await settle();
  resize(1000); await settle();
  const widths = posts.filter((p) => p.msg === "__skyViewport").map((p) => p.args[0]);
  process.stdout.write(JSON.stringify({ widths }));
})().catch((e) => { process.stdout.write("HARNESS-ERROR " + e.stack); process.exit(1); });
`
	dir := t.TempDir()
	jsPath := dir + "/client.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(liveClientJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harnessPath, jsPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	if got := string(out); !strings.Contains(got, `"widths":[700,1200,500]`) {
		t.Fatalf("the client must report at load and on each crossing only, and stop when the leaf is gone: %s", got)
	}
}
