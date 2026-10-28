//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLiveJS_ReopensAsSoonAsTheServerAnswers: a server that comes back during
// a long reconnect backoff is picked up within about a second, not when the
// backoff step ends.
//
// The regression: the reopen schedule doubles (base, 2x, 4x, … up to the
// cap), and fast failures (a proxy's 503 while its upstream restarts) reach
// the 8 and 16 s steps within seconds. A backend restart of a second or two
// could then leave the page offline for up to 16 s more; the release gate's
// console-live e2e (appauth-spa-caddy) saw the Sky Console come back after
// 21 s once (release run 38961621716). The client now asks the server once a
// second while a long step waits, and reopens when anything but a gateway
// error answers.
//
// The embedded client runs in node with hand-driven timers: the test fires
// the poll interval itself, so nothing depends on wall-clock time.
func TestLiveJS_ReopensAsSoonAsTheServerAnswers(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
let opened = 0;
const timeouts = new Map();
const intervals = new Map();
let nextId = 1;
let fetchStatus = 503;
const fetches = [];
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => { const id = nextId++; timeouts.set(id, { fn, ms }); return id; },
  clearTimeout: (id) => { timeouts.delete(id); },
  setInterval: (fn, ms) => { const id = nextId++; intervals.set(id, { fn, ms }); return id; },
  clearInterval: (id) => { intervals.delete(id); },
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() {} },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "tab-A", csrf: "tok", retryBaseMs: 500, retryMaxMs: 16000, retryMaxAttempts: 8 }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) {
    opened++;
    this.url = url; this.readyState = 1;
    this.addEventListener = () => {};
    this.close = () => {};
  },
  fetch: (url, opts) => {
    fetches.push(String(url));
    return Promise.resolve({ ok: fetchStatus < 400, status: fetchStatus, headers: new Headers({}), text: async () => "" });
  },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const settle = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r)); };
const reach = () => [...intervals.values()].filter((v) => v.ms === 1000);
const pollOnce = async () => { for (const v of reach()) v.fn(); await settle(); };
(async () => {
  const out = {};
  const base = opened; // the client opens its stream when it loads
  out.reachBefore = reach().length;
  // Four fast failures: the reopen now waits 4 s.
  for (let i = 0; i < 4; i++) g("__skyForceReopenSSE()");
  out.longestWait = Math.max(...[...timeouts.values()].map((t) => t.ms));
  out.polling = reach().length;
  // The proxy still has no upstream: no reopen.
  fetchStatus = 503;
  await pollOnce();
  out.openedWhileDown = opened - base;
  // The server answers (a 403, or a 404 for a lost session, is an answer).
  fetchStatus = 403;
  await pollOnce();
  out.openedWhenUp = opened - base;
  out.pollingAfter = reach().length;
  out.pinged = fetches.filter((u) => u.endsWith("/_sky/event")).length > 0;
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
		`"reachBefore":0`,
		`"longestWait":4000`,
		`"polling":1`,
		`"openedWhileDown":0`,
		`"openedWhenUp":1`,
		`"pollingAfter":0`,
		`"pinged":true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reconnect: missing %s in %s", want, got)
		}
	}
}
