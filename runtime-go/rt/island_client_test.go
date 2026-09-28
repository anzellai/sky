//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The widget-island runtime and the Sky.Live client's island paths, run for
// real in node against a minimal stub of the DOM (like
// TestLiveJS_SessionRotationClient):
//
//  1. __skyExtractArgs hands an island event's detail to the server as JSON
//     text, and any other CustomEvent's detail as it is (it used to send []).
//  2. The lifecycle: a registered widget mounts with its props, updates when
//     the props attribute changes, and is destroyed when it leaves the page;
//     a command sent before the island mounts waits and is delivered on
//     mount; send(type, data) dispatches the lower-cased CustomEvent.
//  3. pool / adopt keep the SAME element across an HTML swap, carrying the
//     fresh attributes; a changed identity is not adopted.
//  4. The SSE "island" frame reaches the widget's command handler.
//  5. A widget's send() before the page's client has bound its listeners
//     is held and dispatched when __skyInit runs (it used to reach no
//     listener and was lost).
//
// Skips when node is absent, like TestLiveJSSyntaxValid.
func TestIslandJS_ClientRuntime(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
class Ev { constructor(type) { this.type = type; } }
class CustomEvent extends Ev { constructor(type, init) { super(type); this.detail = init ? init.detail : null; } }
class El {
  constructor(tag, attrs) {
    this.nodeType = 1; this.tagName = tag.toUpperCase(); this._a = Object.assign({}, attrs || {});
    this.childNodes = []; this.parentNode = null; this.isConnected = false; this.events = [];
  }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k) ? this._a[k] : null; }
  setAttribute(k, v) { this._a[k] = String(v); }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k); }
  removeAttribute(k) { delete this._a[k]; }
  get attributes() { return Object.keys(this._a).map((n) => ({ name: n, value: this._a[n] })); }
  appendChild(c) { if (c.parentNode) c.parentNode.removeChild(c); c.parentNode = this; this.childNodes.push(c); return c; }
  removeChild(c) { this.childNodes.splice(this.childNodes.indexOf(c), 1); c.parentNode = null; return c; }
  replaceChild(n, o) {
    if (n.parentNode) n.parentNode.removeChild(n);
    const i = this.childNodes.indexOf(o); this.childNodes[i] = n; n.parentNode = this; o.parentNode = null; return o;
  }
  querySelectorAll(sel) {
    const m = /^\[([^\]]+)\]$/.exec(sel); const out = [];
    const walk = (n) => { for (const c of n.childNodes) { if (c.nodeType === 1) { if (m && c.hasAttribute(m[1])) out.push(c); walk(c); } } };
    walk(this); return out;
  }
  contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; }
  dispatchEvent(ev) { this.events.push(ev); return true; }
}
const sseListeners = {};
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() {} },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "S", tab: "T", csrf: "c" }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) { this.addEventListener = (ev, fn) => { sseListeners[ev] = fn; }; this.close = () => {}; },
  fetch: () => Promise.resolve({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) }),
  CustomEvent, Event: Ev,
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => b };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const out = {};
// 1. args
out.islandArgs = g("__skyExtractArgs")(new CustomEvent("skyisland-change", { detail: { text: "hi", n: 2 } }));
out.islandNullArgs = g("__skyExtractArgs")(new CustomEvent("skyisland-ping", {}));
out.customArgs = g("__skyExtractArgs")(new CustomEvent("picked", { detail: "red" }));
out.plainArgs = g("__skyExtractArgs")(new Ev("mouseover"));
// 2. lifecycle
const log = [];
const root = new El("div"); root.isConnected = true;
const island = new El("div", { "data-sky-island": "counter", "data-sky-island-id": "c1", "data-sky-props": "{\"start\":5}", "sky-id": "r.1" });
island.isConnected = true; root.appendChild(island);
const I = g("Sky.__islands");
I.command("c1", "reset", { to: 1 });
let sendFn = null;
g("Sky").island("counter", {
  mount(el, props, send) { this.n = props.start; sendFn = send; log.push("mount:" + props.start + ":" + (el === island) + ":" + (this.el === island)); },
  update(props) { log.push("update:" + props.start + ":" + this.n); },
  command(name, payload) { log.push("command:" + name + ":" + JSON.stringify(payload)); },
  destroy() { log.push("destroy:" + this.n); },
});
I.scan(root);
island.setAttribute("data-sky-props", "{\"start\":6}");
I.update(island);
I.update(island);
// A send before the page's client has bound its listeners is held (the
// widget mounted while the page was loading), then dispatched by __skyInit.
out.heldSend = sendFn("Early", { v: 0 });
out.heldBeforeInit = island.events.length;
g("__skyInit()");
out.flushedOnInit = island.events.length === 1 ? island.events[0].type : null;
island.events.length = 0;
out.sent = sendFn("TextChanged", { v: 1 });
out.sentEvent = { type: island.events[0].type, detail: island.events[0].detail, isCustom: island.events[0] instanceof CustomEvent };
out.badSend = sendFn("x", { self: null, get loop() { throw new Error("no"); } });
// 4. SSE command
g("__skyOpenSSE()");
sseListeners["island"]({ data: JSON.stringify({ id: "c1", name: "goto", payload: { line: 3 } }) });
// 3. adopt
const container = new El("div"); container.isConnected = true;
container.appendChild(island);
const pool = I.pool(container);
const frag = new El("div");
const fresh = new El("div", { "data-sky-island": "counter", "data-sky-island-id": "c1", "data-sky-props": "{\"start\":7}", "sky-id": "r.2" });
frag.appendChild(fresh);
const other = new El("div", { "data-sky-island": "counter", "data-sky-island-id": "c2", "data-sky-props": "{}" });
frag.appendChild(other);
island.setAttribute("sky-stale", "x"); island.setAttribute("tabindex", "0");
I.adopt(pool, frag);
out.adoptedSame = frag.childNodes[0] === island;
out.adoptedAttrs = { id: island.getAttribute("sky-id"), props: island.getAttribute("data-sky-props"), stale: island.getAttribute("sky-stale"), widgetAttr: island.getAttribute("tabindex") };
out.otherKept = frag.childNodes[1] === other;
// disconnect → destroy
island.isConnected = false;
I.sweep();
out.log = log;
process.stdout.write(JSON.stringify(out));
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
	outB, err := exec.Command(node, harnessPath, jsPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, outB)
	}
	got := string(outB)
	for _, want := range []string{
		`"islandArgs":["{\"text\":\"hi\",\"n\":2}"]`,
		`"islandNullArgs":["null"]`,
		`"customArgs":["red"]`,
		`"plainArgs":[]`,
		`"heldSend":true`,
		`"heldBeforeInit":0`,
		`"flushedOnInit":"skyisland-early"`,
		`"sent":true`,
		`"sentEvent":{"type":"skyisland-textchanged","detail":{"v":1},"isCustom":true}`,
		`"badSend":false`,
		`"adoptedSame":true`,
		`"adoptedAttrs":{"id":"r.2","props":"{\"start\":7}","stale":null,"widgetAttr":"0"}`,
		`"otherKept":true`,
		`"log":["mount:5:true:true","command:reset:{\"to\":1}","update:6:5","command:goto:{\"line\":3}","destroy:5"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s\n  in %s", want, got)
		}
	}
}

// The Sky.Spa boot loader carries the same island runtime, so a widget file
// registers the same way under --target web:app.
func TestIslandJS_SpaBootCarriesTheRuntime(t *testing.T) {
	if !strings.HasPrefix(SpaBootJS, islandClientJS) {
		t.Fatal("SpaBootJS must start with the island runtime")
	}
	if !strings.HasPrefix(liveClientJS, islandClientJS) {
		t.Fatal("liveClientJS must start with the island runtime")
	}
	if strings.Contains(islandClientJS, "`") {
		t.Fatal("islandClientJS must hold no backquote (the Rust build reads it as a raw literal)")
	}
	node := requireNode(t)
	f := t.TempDir() + "/boot.js"
	if err := os.WriteFile(f, []byte(SpaBootJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("SpaBootJS failed node --check:\n%s", out)
	}
}
