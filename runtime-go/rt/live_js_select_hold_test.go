package rt

// live_js_select_hold_test.go — the open-<select> defence of the browser
// appliers of server-diffed patches must hold patches, never drop them.
//
// A real app reported the Sky.Spa form of this bug (spa_select_defer_test.go).
// The Sky.Live client and the Sky.Webview shim had the same rule: while a
// SELECT is the active element, a patch on it, inside it or on an ancestor of
// it was skipped (`continue`), and so was an SSE full body. The server had
// already moved its tree on, so the next diff found no change in that region
// and the patch was lost until a reload. And the select was defended against
// its own pick, although input and change fire after the picker closes. The
// webview shim also dropped a patch whose target was missing, with no resync.
//
// The harness runs the real client JS on a small fake DOM in node and calls
// __skyApplyPatches directly.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const selectHoldHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const live = process.argv[3] === "live";
// ---- a fake DOM: elements with sky-ids, text, attributes ----
const mutations = [];
function El(tag, id) {
  this.tagName = tag.toUpperCase(); this.attrs = {}; this.kids = []; this.parent = null; this._text = "";
  this.nodeType = 1;
  if (id) this.attrs["sky-id"] = id;
}
El.prototype.append = function (c) { c.parent = this; this.kids.push(c); return this; };
El.prototype.getAttribute = function (k) { return k in this.attrs ? this.attrs[k] : null; };
El.prototype.hasAttribute = function (k) { return k in this.attrs; };
El.prototype.setAttribute = function (k, v) { this.attrs[k] = String(v); mutations.push(this.attrs["sky-id"]); };
El.prototype.removeAttribute = function (k) { delete this.attrs[k]; mutations.push(this.attrs["sky-id"]); };
El.prototype.contains = function (o) { for (let n = o; n; n = n.parent) if (n === this) return true; return false; };
El.prototype.querySelectorAll = function () { return []; };
Object.defineProperty(El.prototype, "attributes", { get() { return Object.keys(this.attrs).map((name) => ({ name })); } });
Object.defineProperty(El.prototype, "textContent", {
  get() { return this._text + this.kids.map((k) => k.textContent).join(""); },
  set(v) { this.kids = []; this._text = String(v); mutations.push(this.attrs["sky-id"]); },
});
const root = new El("div", "r");
const wrap = new El("div", "r.0#div");
const sel = new El("select", "r.0#div.0#select");
const opt = new El("option", "r.0#div.0#select.0#option"); opt._text = "all";
sel.append(opt);
const list = new El("p", "r.0#div.1#p"); list._text = "rows";
const other = new El("section", "r.1#section"); other._text = "x";
wrap.append(sel).append(list);
root.append(wrap).append(other);
const all = () => { const out = []; const walk = (n) => { out.push(n); n.kids.forEach(walk); }; walk(root); return out; };
const docListeners = {};
const timers = [];
const document = {
  readyState: "complete", activeElement: null, visibilityState: "visible", body: null,
  getElementById: () => null, querySelectorAll: () => [],
  querySelector(q) {
    const m = q.match(/^\[sky-id="(.*)"\]$/);
    return m ? all().find((n) => n.attrs["sky-id"] === m[1]) || null : null;
  },
  addEventListener(t, fn) { (docListeners[t] = docListeners[t] || []).push(fn); },
};
const fire = (type, target) => (docListeners[type] || []).forEach((fn) => fn({ type, target }));
const runTimers = () => { while (timers.length) timers.shift()(); };
const warns = [];
let resynced = 0;
const bodies = [];
const sandbox = {
  console: { warn(...a) { warns.push(a.join(" ")); }, log() {}, error() {} },
  setTimeout: (fn) => { timers.push(fn); return timers.length; }, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() {} },
  navigator: {}, document,
  EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
  fetch: () => new Promise(() => {}),
  __skyResync: () => { resynced++; },
  __skyLog: (lvl, m) => { if (lvl === "warn") warns.push(m); },
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.scrollTo = () => {};
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
if (live) {
  // The Live applier's tail works on a real document; these parts are not
  // under test here. A missing target resyncs through __skyForceReopenSSE.
  sandbox.__resync = () => { resynced++; };
  sandbox.__body = (b) => { bodies.push(b); };
  g("__skyBindEvents = function () {}; __skyRunPaths = function () {}; __skyDidNavigate = function () { return false; }; __skyContainsFocusedInput = function () { return false; }; __skyForceReopenSSE = function () { __resync(); }; __skyPatch = function (b) { __body(b); }");
}
const apply = (ps) => sandbox.__skyApplyPatches(ps);
const out = {};

// 1. A tick while the select is focused and NOT picked (the picker may be
//    open): the select and its ancestors are untouched, an unrelated region
//    still updates.
document.activeElement = sel;
fire("focusin", sel);
apply([{ id: "r.0#div", attrs: { class: "t1" } }, { id: "r.0#div.1#p", text: "tick 1" }, { id: "r.1#section", text: "y" }]);
out.tickTouchedRegion = mutations.some((m) => m && m.startsWith("r.0#div"));
out.tickOther = other.textContent;
out.tickListHeld = list.textContent;
// 2. A second frame for the held region queues behind the first.
apply([{ id: "r.0#div", attrs: { class: "t2" } }]);
out.heldBeforeBlur = wrap.attrs["class"] || "";
// 3. Blur: the held patches apply, in order.
document.activeElement = null;
fire("focusout", sel);
runTimers();
out.afterBlurClass = wrap.attrs["class"] || "";
out.afterBlurList = list.textContent;

// 4. The report's case: focus the select, pick (input + change fire), and
//    the patch on the common parent applies at once.
document.activeElement = sel;
fire("focusin", sel);
fire("input", sel); fire("change", sel);
apply([{ id: "r.0#div", attrs: { class: "picked-none" } }, { id: "r.0#div.1#p", text: "Nothing matches." }]);
out.pickClass = wrap.attrs["class"] || "";
out.pickList = list.textContent;
// 5. Pick back.
fire("input", sel); fire("change", sel);
apply([{ id: "r.0#div", attrs: { class: "picked-all" } }, { id: "r.0#div.1#p", text: "rows" }]);
out.pickBackClass = wrap.attrs["class"] || "";
// 6. Pressing the select again may reopen it: defended again.
fire("pointerdown", sel);
apply([{ id: "r.0#div", attrs: { class: "tick-after-reopen" } }]);
out.reopenHeld = wrap.attrs["class"] || "";
if (live) {
  // 7. An SSE full body while the select is open is held too, and the
  //    patches after it queue behind it.
  if (typeof sandbox.__skyPatchOrHold === "function") sandbox.__skyPatchOrHold("<p>body</p>");
  apply([{ id: "r.1#section", text: "after-body" }]);
  out.bodyHeld = bodies.length === 0 && other.textContent === "y";
}
document.activeElement = null;
fire("focusout", sel);
runTimers();
out.reopenApplied = live ? (bodies.join() === "<p>body</p>" && other.textContent === "after-body") : wrap.attrs["class"] === "tick-after-reopen";
// 8. A patch whose target is missing resyncs instead of being dropped.
out.resyncedBefore = resynced;
apply([{ id: "r.9#gone", text: "z" }]);
out.resyncedAfter = resynced;
out.warned = warns.some((w) => w.includes("patches deferred while a <select> is open"));
process.stdout.write(JSON.stringify(out));
`

// runSelectHoldHarness runs selectHoldHarness on js and checks the result.
func runSelectHoldHarness(t *testing.T, js, mode string) {
	t.Helper()
	node := requireNode(t)
	dir := t.TempDir()
	jsPath := dir + "/client.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(selectHoldHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harnessPath, jsPath, mode).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)
	want := []string{
		`"tickTouchedRegion":false`,
		`"tickOther":"y"`,
		`"tickListHeld":"rows"`,
		`"heldBeforeBlur":""`,
		`"afterBlurClass":"t2"`,
		`"afterBlurList":"tick 1"`,
		`"pickClass":"picked-none"`,
		`"pickList":"Nothing matches."`,
		`"pickBackClass":"picked-all"`,
		`"reopenHeld":"picked-all"`,
		`"reopenApplied":true`,
		`"resyncedBefore":0`,
		`"resyncedAfter":1`,
		`"warned":true`,
	}
	if mode == "live" {
		want = append(want, `"bodyHeld":true`)
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s open-select defence: missing %s in %s", mode, w, got)
		}
	}
}

func TestLiveJS_OpenSelectHoldsPatches(t *testing.T) {
	runSelectHoldHarness(t, liveClientJS, "live")
}
