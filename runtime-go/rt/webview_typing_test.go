//go:build cgo && darwin

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Typing in a Sky.Webview window while the update loop is behind (v0.29.0).
//
// The shim sends every keystroke to the Go loop through a Bind callback and
// the loop answers each Msg with a patch from the model after THAT Msg. When
// the loop is slower than the typing (a slow update or view, a busy
// machine), a patch for keystroke n reaches a box that already holds n+1
// characters. Writing it rewound the box and clamped the caret, and the next
// key landed in the wrong place: the Sky.Spa hold bug by another road. The
// shim now numbers each input dispatch per control and the loop acks the
// number with the patches of that Msg; a value write to a control with an
// unacked dispatch is dropped, as Sky.Live's client does.
const webviewTypingHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
function El(tag, id) {
  this.tagName = tag.toUpperCase(); this.attrs = {}; this.kids = []; this.parent = null;
  this.nodeType = 1; this.value = ""; this.selectionStart = 0; this.selectionEnd = 0; this.scrollTop = 0;
  this.listeners = {};
  if (id) this.attrs["sky-id"] = id;
}
El.prototype.append = function (c) { c.parent = this; this.kids.push(c); return this; };
El.prototype.getAttribute = function (k) { return k in this.attrs ? this.attrs[k] : null; };
El.prototype.hasAttribute = function (k) { return k in this.attrs; };
El.prototype.setAttribute = function (k, v) { this.attrs[k] = String(v); };
El.prototype.removeAttribute = function (k) { delete this.attrs[k]; };
El.prototype.contains = function (o) { for (let n = o; n; n = n.parent) if (n === this) return true; return false; };
El.prototype.querySelectorAll = function () { return []; };
El.prototype.addEventListener = function (t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); };
El.prototype.removeEventListener = function () {};
El.prototype.setSelectionRange = function (s, e) { this.selectionStart = s; this.selectionEnd = e; };
Object.defineProperty(El.prototype, "attributes", { get() { return Object.keys(this.attrs).map((name) => ({ name, value: this.attrs[name] })); } });
Object.defineProperty(El.prototype, "textContent", { get() { return ""; }, set(v) {} });
const root = new El("div", "r");
const box = new El("input", "r.0#input");
box.attrs["sky-input"] = "SetQuery";
root.append(box);
const all = () => { const out = []; const walk = (n) => { out.push(n); n.kids.forEach(walk); }; walk(root); return out; };
const document = {
  readyState: "complete", activeElement: box, body: root, documentElement: root,
  getElementById: () => null,
  querySelectorAll(q) { return q === "*" ? all() : []; },
  querySelector(q) {
    const m = q.match(/^\[sky-id="(.*)"\]$/);
    return m ? all().find((n) => n.attrs["sky-id"] === m[1]) || null : null;
  },
  addEventListener() {},
};
const sent = [];
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  location: { pathname: "/", href: "http://x/", reload() {} },
  navigator: {}, document,
  __skyDispatch: (hid, args) => { sent.push(args[0]); return Promise.resolve(); },
  __skyDispatchInput: (hid, args, sid, seq) => { sent.push(args[0]); return Promise.resolve(); },
  __skyLog: () => {}, __skyResync: () => {},
  JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
if (typeof sandbox.__skyBindAll === "function") sandbox.__skyBindAll();
const type = (ch) => {
  box.value = box.value.slice(0, box.selectionStart) + ch + box.value.slice(box.selectionStart);
  box.selectionStart = box.selectionEnd = box.selectionStart + ch.length;
  (box.listeners.input || []).forEach((fn) => fn({ type: "input", currentTarget: box, target: box }));
};
const out = {};
// The loop is behind: three keys go out before any answer.
type("c"); type("l"); type("i");
out.sent = sent.join(",");
// The answer to "c" (seq 1): a patch with the model after "c".
sandbox.__skyApplyPatches([{ id: "r.0#input", attrs: { value: "c" } }], { "r.0#input": 1 });
out.afterFirst = box.value + "@" + box.selectionStart;
sandbox.__skyApplyPatches([{ id: "r.0#input", attrs: { value: "cl" } }], { "r.0#input": 2 });
out.afterSecond = box.value + "@" + box.selectionStart;
sandbox.__skyApplyPatches([{ id: "r.0#input", attrs: { value: "cli" } }], { "r.0#input": 3 });
out.afterThird = box.value + "@" + box.selectionStart;
// Every keystroke is acked: a programmatic value (a clear) applies.
sandbox.__skyApplyPatches([{ id: "r.0#input", attrs: { value: "" } }]);
out.cleared = box.value + "@" + box.selectionStart;
process.stdout.write(JSON.stringify(out));
`

func TestWebviewJS_TypingAheadOfTheLoopIsNotRewound(t *testing.T) {
	node := requireNode(t)
	dir := t.TempDir()
	jsPath, harness := dir+"/client.js", dir+"/harness.js"
	if err := os.WriteFile(jsPath, []byte(webviewSharedJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(webviewTypingHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harness, jsPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)
	for _, w := range []string{
		`"sent":"c,cl,cli"`,
		`"afterFirst":"cli@3"`,
		`"afterSecond":"cli@3"`,
		`"afterThird":"cli@3"`,
		`"cleared":"@0"`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("webview typing: missing %s in %s", w, got)
		}
	}
}
