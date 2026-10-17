//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLiveJS_LostSessionReloadsOnlyWhenTheServerAnswers: a "session lost"
// reload waits until the server answers. The reload used to run at once, and
// when the frame came from a server on its way down (or the network dropped)
// the page landed on the browser's error page, where no script runs, and never
// recovered (the console-live e2e's spa-direct failure). Driven in node with
// hand-fired timers: the server is down for two asks, then answers.
func TestLiveJS_LostSessionReloadsOnlyWhenTheServerAnswers(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const timeouts = [];
let reloads = 0;
let up = false;
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => { timeouts.push({ fn, ms }); return timeouts.length; },
  clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() { reloads++; } },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "tab-A", csrf: "tok" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
  fetch: () => up
    ? Promise.resolve({ ok: false, status: 404, headers: new Headers({ "X-Sky-Live": "1" }), text: async () => "session not found" })
    : Promise.reject(new TypeError("Failed to fetch")),
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const settle = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r)); };
const fireRetry = async () => {
  const i = timeouts.findIndex((t) => t.ms === 1000);
  if (i < 0) return false;
  const t = timeouts.splice(i, 1)[0];
  t.fn();
  await settle();
  return true;
};
(async () => {
  const out = {};
  g('__skyRecoverLostSession("session-evicted")');
  await settle();
  out.whileDown = reloads;
  out.retried = await fireRetry();
  out.stillDown = reloads;
  up = true;
  out.retriedAgain = await fireRetry();
  out.whenUp = reloads;
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
		`"whileDown":0`,
		`"retried":true`,
		`"stillDown":0`,
		`"retriedAgain":true`,
		`"whenUp":1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lost-session reload: missing %s in %s", want, got)
		}
	}
}
