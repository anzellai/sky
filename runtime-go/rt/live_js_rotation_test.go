//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The client half of session-id rotation (live_session_rotation.go), run for
// real: the embedded client JS executes in node against a minimal stub of
// the browser (EventSource, fetch, document, sessionStorage), and the test
// drives the three paths the server relies on.
//
//  1. An SSE `rotate` frame makes the tab POST {tab, ticket} to /_sky/rotate
//     and adopt the sid the server answers with.
//  2. An event POST answered with X-Sky-Sid makes the tab echo that sid.
//  3. An event POST answered X-Sky-Status: session-rotating goes to the
//     retry queue, never to the reload a lost session gets.
//
// Skips when node is absent, like TestLiveJSSyntaxValid.
func TestLiveJS_SessionRotationClient(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const listeners = {};
const fetches = [];
let reloads = 0;
let nextFetch = null;
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() { reloads++; } },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "OLD", tab: "tab-A", csrf: "tok" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) {
    this.url = url; this.readyState = 1;
    this.addEventListener = (ev, fn) => { listeners[ev] = fn; };
    this.close = () => {};
  },
  fetch: (url, opts) => {
    fetches.push({ url, opts });
    const r = nextFetch || { ok: true, status: 200, headers: new Headers({ "X-Sky-Live": "1", "Content-Type": "application/json" }), json: async () => ({ seq: 1 }) , text: async () => "" };
    nextFetch = null;
    return Promise.resolve(r);
  },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
(async () => {
  const out = {};
  out.tab = g("__skyTabId");
  g("__skyOpenSSE()");
  out.hasRotate = typeof listeners["rotate"] === "function";
  out.hasRotating = typeof listeners["rotating"] === "function";
  nextFetch = { ok: true, status: 200, headers: new Headers({}), json: async () => ({ sid: "NEW1" }) };
  listeners["rotate"]({ data: JSON.stringify({ ticket: "T1" }) });
  await new Promise((r) => setImmediate(r)); await new Promise((r) => setImmediate(r));
  const rot = fetches.find((f) => String(f.url).endsWith("/_sky/rotate"));
  out.rotateBody = rot ? JSON.parse(rot.opts.body) : null;
  out.rotateCsrf = rot ? rot.opts.headers["X-Sky-Csrf"] : null;
  out.sidAfterRotate = g("__skySid");

  nextFetch = { ok: true, status: 200, headers: new Headers({ "X-Sky-Live": "1", "Content-Type": "application/json", "X-Sky-Sid": "NEW2" }), json: async () => ({ seq: 2 }) };
  await g("__skyPostEventNow")({ sessionId: "NEW1", seq: 2, msg: "", args: [], handlerId: "h", tab: "tab-A" }).catch(() => {});
  out.sidAfterEvent = g("__skySid");

  nextFetch = { ok: false, status: 409, headers: new Headers({ "X-Sky-Live": "1", "X-Sky-Status": "session-rotating" }), text: async () => "session rotating" };
  let rotatingErr = null;
  await g("__skyPostEventNow")({ sessionId: "NEW2", seq: 3, msg: "", args: [], handlerId: "h", tab: "tab-A" }).catch((e) => { rotatingErr = String(e && e.message); });
  out.rotatingErr = rotatingErr;
  out.queuedForRetry = g("__skyEventQueue.map(function (b) { return b.seq; }).join(\",\")");
  out.reloadedOnRotating = g("__skyProbedReload");
  out.reloads = reloads;
  process.stdout.write(JSON.stringify(out));
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
	for _, want := range []string{
		`"tab":"tab-A"`,
		`"hasRotate":true`,
		`"hasRotating":true`,
		`"rotateBody":{"tab":"tab-A","ticket":"T1"}`,
		`"rotateCsrf":"tok"`,
		`"sidAfterRotate":"NEW1"`,
		`"sidAfterEvent":"NEW2"`,
		`"queuedForRetry":"3"`,
		`"reloadedOnRotating":false`,
		`"reloads":0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("client rotation behaviour: missing %s in %s", want, got)
		}
	}
}
