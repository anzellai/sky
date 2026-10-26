package rt

// live_js_lost_session_leaving_test.go — a session-lost reload must not
// abort a navigation the page has already started.
//
// The Sky Console's "Sign out" is a plain link to /_sky/console/_logout. The
// handler revokes the console cookie, and the open console stream then ends
// at once with the session-lost `auth-required` event. The client answered
// that with location.reload() while the browser was still following the
// logout's 303 to the login form, so the reload aborted the navigation
// (release run 40362387176: console-controls-e2e "sign-out: ends the console
// session — page.waitForNavigation: net::ERR_ABORTED"). A page that is
// already leaving (beforeunload fired) has its next document on the way; the
// reload waits, and runs only if the page is still here later.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLiveJS_LostSessionReloadWaitsForANavigationInFlight(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
let reloads = 0;
const winListeners = {};
const timers = [];
let clock = 1000000;
class FakeDate extends Date {}
FakeDate.now = () => clock;
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimeout() {},
  setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() { reloads++; } },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "T", csrf: "c" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
  fetch: () => Promise.resolve({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({}), text: async () => "" }),
  Uint8Array, JSON, Math, Date: FakeDate, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = (t, fn) => { (winListeners[t] = winListeners[t] || []).push(fn); };
sandbox.window.scrollTo = () => {};
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const settle = () => new Promise((r) => setImmediate(r));
(async () => {
  const out = {};
  // The user clicked a link: the page is leaving.
  (winListeners["beforeunload"] || []).forEach((fn) => fn({}));
  g("__skySetStatus = function () {}");
  g("__skyRecoverLostSession")("auth-required");
  await settle(); await settle();
  out.reloadsWhileLeaving = reloads;
  // The navigation never replaced the page (a download, a 204): the reload
  // still happens once the page is plainly not leaving.
  for (let i = 0; i < 20 && reloads === 0; i++) {
    const due = timers.splice(0);
    due.forEach((t) => { clock += t.ms || 0; t.fn(); });
    await settle(); await settle();
  }
  out.reloadsLater = reloads;
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
	for _, want := range []string{`"reloadsWhileLeaving":0`, `"reloadsLater":1`} {
		if !strings.Contains(got, want) {
			t.Errorf("lost-session reload during a navigation: missing %s in %s", want, got)
		}
	}
}
