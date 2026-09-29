//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The browser half of Std.Nav on Sky.Live (live_client_asset.go
// __skyNavApply and the SSE "nav" listener), in node against the Sky.Live
// client with a stub location, History API and fetch:
//
//  1. pushUrl to another path fetches the page like a sky-nav link (the
//     X-Sky-Nav header), then pushes the URL and patches;
//  2. replaceUrl to another path fetches the page and replaces the entry
//     (no new history entry);
//  3. replaceUrl "#" clears the fragment: no fetch, no new entry, and the
//     change is reported to Sub.onFragment;
//  4. pushUrl "#sec" moves only the fragment: no fetch, one new entry,
//     reported to Sub.onFragment;
//  5. a URL off this site is refused: no fetch, no history change, an error
//     in the console;
//  6. a page fetch the server refuses falls back to a real navigation;
//  7. the SSE "nav" frame waits for the events this tab already sent: it
//     runs after the event POST chain.
func TestNavJS_LiveClientAppliesNavigation(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
let cur = new URL("http://x.test/a?q=1");
const pushes = [], replaces = [], fetches = [], errors = [], assigned = [], frags = [];
const location = {
  get href() { return cur.href; }, get origin() { return cur.origin; },
  get pathname() { return cur.pathname; }, get search() { return cur.search; },
  get hash() { return cur.hash; },
  reload() {}, assign(u) { assigned.push(["assign", u]); }, replace(u) { assigned.push(["replace", u]); },
};
const history = {
  state: null,
  pushState(s, _t, u) { pushes.push(u); cur = new URL(u, cur.href); },
  replaceState(s, _t, u) { replaces.push(u); cur = new URL(u, cur.href); },
};
let fetchStatus = 200;
const sse = {};
const sandbox = {
  console: { warn() {}, log() {}, error(m) { errors.push(String(m)); } },
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location, history, URL,
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "T", csrf: "c" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function () { this.addEventListener = (ev, fn) => { sse[ev] = fn; }; this.close = () => {}; },
  fetch: (u, opts) => {
    fetches.push([String(u), opts && opts.headers ? opts.headers["X-Sky-Nav"] || "" : ""]);
    return Promise.resolve({ ok: fetchStatus === 200, status: fetchStatus, headers: { get: () => null },
      text: async () => "<p>page</p>", json: async () => ({}) });
  },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => b };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
sandbox.__navFrag = (h) => frags.push(h);
g("__skySendFragment = function () { __navFrag(String(window.location.hash || '')); }");
const fails = []; let ran = 0;
function T(name, got, want) { ran++; const a = JSON.stringify(got), b = JSON.stringify(want); if (a !== b) fails.push(name + ": got " + a + ", want " + b); }
const reset = () => { for (const a of [pushes, replaces, fetches, errors, assigned, frags]) a.length = 0; };
const tick = () => new Promise((r) => setImmediate(r));
(async () => {
  // 1
  await g("__skyNavApply")("/b?x=1", false);
  T("pushUrl to another path fetches the page like a sky-nav link", fetches, [["/b?x=1", "1"]]);
  T("then pushes the URL", [pushes, replaces, location.pathname + location.search], [["/b?x=1"], [], "/b?x=1"]);
  // 2
  reset();
  await g("__skyNavApply")("/c", true);
  T("replaceUrl to another path fetches the page and replaces the entry", [fetches, pushes, replaces], [[["/c", "1"]], [], ["/c"]]);
  // 3
  history.pushState(null, "", "/c#frag"); reset();
  await g("__skyNavApply")("#", true);
  T("replaceUrl \"#\" clears the fragment without a fetch or an entry", [fetches, pushes, replaces, location.href], [[], [], ["/c"], "http://x.test/c"]);
  T("the cleared fragment is reported", frags, [""]);
  // 4
  reset();
  await g("__skyNavApply")("#sec", false);
  T("pushUrl #sec moves only the fragment", [fetches, pushes, location.hash, frags], [[], ["/c#sec"], "#sec", ["#sec"]]);
  // 5
  reset();
  await g("__skyNavApply")("https://evil.test/x", false);
  await g("__skyNavApply")("//evil.test/x", false);
  T("a URL off this site is refused", [fetches, pushes, replaces, location.href], [[], [], [], "http://x.test/c#sec"]);
  T("with an error each", errors.filter((e) => e.indexOf("NavRejectedUrl") >= 0).length, 2);
  // 6
  reset(); fetchStatus = 404;
  await g("__skyNavApply")("/gone", false);
  T("a refused page fetch falls back to a real navigation", [assigned, pushes], [[["assign", "/gone"]], []]);
  fetchStatus = 200;
  // 7
  reset();
  let release;
  g("__skyPostChain = new Promise(function (r) { __navRelease = r; })".replace("__navRelease", "globalThis.__navRelease"));
  release = sandbox.__navRelease;
  g("__skyInit()");
  g("__skyOpenSSE()");
  sse["nav"]({ data: JSON.stringify({ url: "/d", replace: false }) });
  await tick();
  T("the nav frame waits for the in-flight event POSTs", fetches, []);
  release(); await tick(); await tick(); await tick();
  T("then routes", [fetches, pushes], [[["/d", "1"]], ["/d"]]);
  process.stdout.write(JSON.stringify({ ran, fails }));
})().catch((e) => { process.stdout.write(JSON.stringify({ ran, fails: fails.concat(["threw: " + e.stack]) })); });
`
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/client.js", []byte(liveClientJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/harness.js", []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, dir+"/harness.js", dir+"/client.js").CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, `"fails":[]`) || strings.Contains(got, `"ran":0`) {
		t.Fatalf("Std.Nav client contract: %s", got)
	}
}
