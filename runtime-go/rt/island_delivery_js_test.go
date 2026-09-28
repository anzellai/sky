//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The client half of the Cmd.toIsland delivery contract
// (island_client.go, live_island_delivery.go), in node against the Sky.Live
// client and a stub DOM. A lost command is never silent:
//
//  1. frames in order are delivered once; a repeated or older seq is not;
//  2. a frame that skips a seq resyncs the island at once: destroy, the
//     element emptied, mount again from the CURRENT props, the island event
//     "resync" {reason: "lost"}; then the frame is delivered;
//  3. an "islandsync" map above the last seq received resyncs the island
//     (a lost LAST command, found without waiting for the next one); frames
//     at or below the resync point are then ignored;
//  4. the hello baseline is adopted by a page with no record (no false
//     resync on a fresh page);
//  5. a new server epoch resyncs every island that had received commands;
//  6. the wait queue of an island that is not mounted overflows: the island
//     is resynced when it mounts;
//  7. a widget cannot send the reserved event "resync".
func TestIslandJS_DeliveryContract(t *testing.T) {
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
  get firstChild() { return this.childNodes[0] || null; }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k) ? this._a[k] : null; }
  setAttribute(k, v) { this._a[k] = String(v); }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k); }
  removeAttribute(k) { delete this._a[k]; }
  get attributes() { return Object.keys(this._a).map((n) => ({ name: n, value: this._a[n] })); }
  appendChild(c) { if (c.parentNode) c.parentNode.removeChild(c); c.parentNode = this; this.childNodes.push(c); return c; }
  removeChild(c) { this.childNodes.splice(this.childNodes.indexOf(c), 1); c.parentNode = null; return c; }
  querySelectorAll(sel) {
    const m = /^\[([^\]]+)\]$/.exec(sel); const out = [];
    const walk = (n) => { for (const c of n.childNodes) { if (c.nodeType === 1) { if (m && c.hasAttribute(m[1])) out.push(c); walk(c); } } };
    walk(this); return out;
  }
  contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; }
  dispatchEvent(ev) { this.events.push(ev); return true; }
}
const sse = {};
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
  EventSource: function () { this.addEventListener = (ev, fn) => { sse[ev] = fn; }; this.close = () => {}; },
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
const fails = []; let ran = 0;
function T(name, got, want) { ran++; const a = JSON.stringify(got), b = JSON.stringify(want); if (a !== b) fails.push(name + ": got " + a + ", want " + b); }
const root = new El("div"); root.isConnected = true;
const island = new El("div", { "data-sky-island": "ed", "data-sky-island-id": "e1", "data-sky-props": "{\"v\":1}" });
island.isConnected = true; root.appendChild(island);
const log = [];
let sendFn = null;
g("Sky").island("ed", {
  mount(el, props, send) { sendFn = send; el.appendChild(new El("span")); log.push("mount:" + props.v); },
  command(name, payload) { log.push("cmd:" + payload); },
  destroy() { log.push("destroy"); },
});
g("Sky.__islands").scan(root);
g("__skyInit()");
g("__skyOpenSSE()");
const cmd = (seq, p) => sse["island"]({ data: JSON.stringify({ id: "e1", name: "set", payload: p, seq: seq }) });
const sync = (e, s, r) => sse["islandsync"]({ data: JSON.stringify({ e: e, r: r || "heartbeat", s: s }) });
const resyncs = () => island.events.filter((e) => e.type === "skyisland-resync").map((e) => e.detail.reason);
// 4. hello baseline on a fresh page: adopt, no resync
sync("E1", { e1: 4 }, "hello");
T("a fresh page adopts the hello baseline", [log.slice(), resyncs()], [["mount:1"], []]);
// 1. in order
cmd(5, "a"); cmd(6, "b"); cmd(6, "b"); cmd(3, "old");
T("in-order frames are delivered once", log.slice(1), ["cmd:a", "cmd:b"]);
// 2. a skipped seq resyncs at once, from the CURRENT props
log.length = 0;
island.setAttribute("data-sky-props", "{\"v\":2}");
g("Sky.__islands").update(island);
cmd(9, "c");
T("a skipped seq remounts from the current props, then delivers", log.filter((l) => l !== "update"), ["destroy", "mount:2", "cmd:c"]);
T("the element is emptied before the remount", island.childNodes.length, 1);
T("the app is told: resync lost", resyncs(), ["lost"]);
// 3. a sync map above the last seq resyncs, and older frames are ignored
log.length = 0; island.events.length = 0;
sync("E1", { e1: 12 });
T("a sync map above the last seq resyncs", [log.slice(), resyncs()], [["destroy", "mount:2"], ["lost"]]);
cmd(11, "stale"); cmd(12, "stale2"); cmd(13, "d");
T("frames at or below the resync point are ignored", log.slice(2), ["cmd:d"]);
log.length = 0; island.events.length = 0;
sync("E1", { e1: 13 });
T("a sync map that agrees resyncs nothing", [log.slice(), resyncs()], [[], []]);
// 5. a new epoch resyncs islands that received commands
sync("E2", { e1: 0 }, "hello");
T("a new server epoch resyncs the island", [log.slice(), resyncs()], [["destroy", "mount:2"], ["restart"]]);
cmd(1, "e");
T("the new epoch's numbering starts again", log.slice(2), ["cmd:e"]);
// 6. the wait queue overflows for an island that is not mounted
const later = new El("div", { "data-sky-island": "ed", "data-sky-island-id": "e2", "data-sky-props": "{\"v\":7}" });
for (let i = 0; i < 300; i++) g("Sky.__islandCommand")("e2", "set", i);
log.length = 0;
later.isConnected = true; root.appendChild(later);
g("Sky.__islands").scan(root);
T("the overflowed island mounts, gets the kept commands, then resyncs", [log[0], log.length, later.events.filter((e) => e.type === "skyisland-resync").map((e) => e.detail.reason)], ["mount:7", 257, ["overflow"]]);
// 7. reserved
island.events.length = 0;
T("a widget cannot send resync", [sendFn("resync", {}), island.events.length], [false, 0]);
process.stdout.write(JSON.stringify({ ran, fails }));
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
		t.Fatalf("delivery contract: %s", got)
	}
}
