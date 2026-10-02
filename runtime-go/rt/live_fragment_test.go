//go:build !js

package rt

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func fragmentTestApp(subs func(any) any) *liveApp {
	return &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
		init: func(req any) any {
			return SkyTuple2{V0: "", V1: cmdT{kind: "none"}}
		},
		update: func(msg, model any) any {
			return SkyTuple2{V0: msg, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("FRAG=" + model.(string))})
		},
		subscriptions: subs,
		msgTags:       map[string]int{},
	}
}

func postFragment(t *testing.T, app *liveApp, sid, frag string) *httptest.ResponseRecorder {
	t.Helper()
	app.store.Set(sid, &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		model:     "",
		handlers:  map[string]any{},
	})
	body := strings.NewReader(`{"sessionId":"` + sid + `","msg":"__skyFragment","args":["` + frag + `"]}`)
	req := httptest.NewRequest("POST", "/_sky/event", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "sky_sid="+sid)
	rr := httptest.NewRecorder()
	app.handleEvent(rr, req)
	return rr
}

// Sub.onFragment on Sky.Live: the fragment the browser reports reaches
// `update` through the app's own `Sub.onFragment toMsg` leaf, and the reply
// renders the new model.
func TestLiveFragmentEventReachesTheOnFragmentSub(t *testing.T) {
	app := fragmentTestApp(func(model any) any {
		return Sub_batch([]any{Sub_every(60000, "Tick"), Sub_onFragment(func(f any) any { return "got:" + f.(string) })})
	})
	rr := postFragment(t, app, "sid-frag", "section-2")
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	sess, _ := app.store.Get("sid-frag")
	if got := sess.model; got != "got:section-2" {
		t.Fatalf("model = %v, want got:section-2", got)
	}
	if !strings.Contains(rr.Body.String(), "FRAG=got:section-2") {
		t.Fatalf("the reply must render the new model: %s", rr.Body.String())
	}
}

// Without a Sub.onFragment leaf the report is a no-op: 204 No Content (the
// client patches nothing), model unchanged. It used to answer 200 with an
// empty body, which the client patched in: a page opened at `/#x` went blank.
func TestLiveFragmentEventWithoutASubIsANoOp(t *testing.T) {
	app := fragmentTestApp(func(model any) any { return Sub_none() })
	rr := postFragment(t, app, "sid-nofrag", "x")
	if rr.Code != 204 || rr.Body.Len() != 0 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	sess, _ := app.store.Get("sid-nofrag")
	if sess.model != "" {
		t.Fatalf("model changed to %v without a Sub.onFragment", sess.model)
	}
}

// The browser half: the embedded client posts `__skyFragment` at load when
// the page has a fragment, and again on every hashchange.
func TestLiveJS_ReportsTheFragmentAtLoadAndOnHashchange(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const winListeners = {};
const posts = [];
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/#intro", hash: "#intro", reload() {} },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "tab-A", csrf: "tok" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) { this.url = url; this.readyState = 1; this.addEventListener = () => {}; this.close = () => {}; },
  fetch: (url, opts) => {
    if (String(url).endsWith("/_sky/event")) posts.push(JSON.parse(opts.body));
    return Promise.resolve({ ok: true, status: 200, headers: new Headers({ "X-Sky-Live": "1", "Content-Type": "application/json" }), json: async () => ({ seq: 1 }), text: async () => "" });
  },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = (ev, fn) => { (winListeners[ev] = winListeners[ev] || []).push(fn); };
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
(async () => {
  const settle = async () => { for (let i = 0; i < 6; i++) await new Promise((r) => setImmediate(r)); };
  await settle();
  sandbox.location.hash = "#part-2";
  (winListeners["hashchange"] || []).forEach((fn) => fn({}));
  await settle();
  sandbox.location.hash = "";
  (winListeners["hashchange"] || []).forEach((fn) => fn({}));
  await settle();
  const frags = posts.filter((p) => p.msg === "__skyFragment").map((p) => p.args[0]);
  process.stdout.write(JSON.stringify({ frags, handlerIds: posts.filter((p) => p.msg === "__skyFragment").map((p) => p.handlerId) }));
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
	got := string(out)
	if !strings.Contains(got, `"frags":["intro","part-2",""]`) || !strings.Contains(got, `"handlerIds":["","",""]`) {
		t.Fatalf("the client must report the fragment at load and on each hashchange: %s", got)
	}
}

// The client half of the no-op: a 204 answer to /_sky/event is never patched
// into the page (an empty patch would blank it).
func TestLiveJS_A204EventAnswerIsNotPatched(t *testing.T) {
	js := liveClientJS
	i := strings.Index(js, "if (r.status === 204)")
	j := strings.Index(js, "return r.text().then(function(t) {\n      __skyLoaderEnd();")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("__skySend must return on a 204 before the text patch path (204 at %d, patch at %d)", i, j)
	}
}
