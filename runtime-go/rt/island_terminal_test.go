//go:build !js

package rt

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The built-in "sky-terminal" widget (island_terminal.go), run for real in
// node against a minimal stub of the DOM, like TestIslandJS_ClientRuntime.
// The harness asserts inside node and prints {"ran": N, "fails": [...]}, so a
// failure names its case. Skips when node is absent, like the island tests.

const terminalHarnessJS = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const mode = process.argv[3];
const fails = [];
let ran = 0;
function T(name, got, want) {
  ran++;
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) fails.push(name + ": got " + g + ", want " + w);
}
class Ev { constructor(type, init) { this.type = type; Object.assign(this, init || {}); } }
class CustomEvent extends Ev { constructor(type, init) { super(type); this.detail = init ? init.detail : null; } }
class El {
  constructor(tag, attrs) {
    this.nodeType = 1; this.tagName = tag.toUpperCase(); this._a = Object.assign({}, attrs || {});
    this.childNodes = []; this.parentNode = null; this.isConnected = false; this.events = [];
    this.style = {}; this._text = ""; this.handlers = {}; this.clientWidth = 0; this.clientHeight = 0;
  }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k) ? this._a[k] : null; }
  setAttribute(k, v) { this._a[k] = String(v); }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k); }
  removeAttribute(k) { delete this._a[k]; }
  get attributes() { return Object.keys(this._a).map((n) => ({ name: n, value: this._a[n] })); }
  get firstChild() { return this.childNodes[0] || null; }
  set textContent(v) { this.childNodes = []; this._text = String(v); }
  get textContent() { return this._text + this.childNodes.map((c) => c.textContent).join(""); }
  appendChild(c) { if (c.parentNode) c.parentNode.removeChild(c); c.parentNode = this; this.childNodes.push(c); return c; }
  removeChild(c) { this.childNodes.splice(this.childNodes.indexOf(c), 1); c.parentNode = null; return c; }
  querySelectorAll(sel) {
    const m = /^\[([^\]]+)\]$/.exec(sel); const out = [];
    const walk = (n) => { for (const c of n.childNodes) { if (c.nodeType === 1) { if (m && c.hasAttribute(m[1])) out.push(c); walk(c); } } };
    walk(this); return out;
  }
  contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; }
  dispatchEvent(ev) { this.events.push(ev); return true; }
  addEventListener(t, fn) { (this.handlers[t] = this.handlers[t] || []).push(fn); }
  removeEventListener(t, fn) { const l = this.handlers[t] || []; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); }
  fire(t, ev) { for (const fn of (this.handlers[t] || []).slice()) fn(ev); }
  focus() { this.focused = true; }
  getBoundingClientRect() { return this._text === "MMMMMMMMMM" ? { width: 80, height: 16 } : { width: 0, height: 0 }; }
}
let timers = [];
function runTimers() {
  for (let guard = 0; timers.length && guard < 100; guard++) {
    const t = timers; timers = [];
    for (const x of t) if (!x.dead) x.fn();
  }
}
const doc = new El("html");
doc.isConnected = true;
doc.readyState = "complete";
doc.createElement = (t) => new El(t);
doc.body = null;
doc.activeElement = null;
const winHandlers = {};
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn) => { const t = { fn, dead: false }; timers.push(t); return t; },
  clearTimeout: (t) => { if (t) t.dead = true; },
  document: doc,
  CustomEvent, Event: Ev, TextDecoder, Uint8Array, JSON, Math, Object, Array, String, Number, parseInt,
  atob: (s) => Buffer.from(s, "base64").toString("binary"),
  addEventListener: (t, fn) => { winHandlers[t] = fn; },
  removeEventListener: () => {},
};
sandbox.window = sandbox;
const island = new El("div", { "data-sky-island": "sky-terminal", "data-sky-island-id": "t1", "data-sky-props": "{\"label\":\"Shell\"}" });
island.isConnected = true;
island.clientWidth = 320; island.clientHeight = 160;
doc.appendChild(island);
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
// The page's client marks itself ready once its listeners are bound; until
// then the widget's sends from mount() are held (island_client.go).
vm.runInContext("Sky.__islandHostReady()", sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const b64 = (bytes) => Buffer.from(bytes).toString("base64");

if (mode === "vt") {
  const V = g("Sky.__vt");
  const VT = V.VT;
  let v;
  // cursor moves
  v = new VT(10, 5); v.write("\x1b[3;4HX");
  T("CUP writes at row 3 col 4", v.text()[2], "   X");
  T("CUP leaves the cursor after X", [v.cursor.x, v.cursor.y], [4, 2]);
  v.write("\x1b[99;99H"); T("CUP clamps", [v.cursor.x, v.cursor.y], [9, 4]);
  v.write("\x1b[100A"); T("CUU clamps at the top", v.cursor.y, 0);
  v.write("\x1b[100D"); T("CUB clamps at the left", v.cursor.x, 0);
  v.write("\x1b[3B\x1b[2C"); T("CUD and CUF move", [v.cursor.x, v.cursor.y], [2, 3]);
  v.write("\x1b[100B\x1b[100C"); T("CUD and CUF clamp", [v.cursor.x, v.cursor.y], [9, 4]);
  v.write("\x1b[A\x1b[D"); T("CUU / CUB default to 1", [v.cursor.x, v.cursor.y], [8, 3]);
  v.write("\x1b[5G"); T("CHA", v.cursor.x, 4);
  v.write("\x1b[2d"); T("VPA", v.cursor.y, 1);
  v.write("\x1b[H"); T("CUP defaults to home", [v.cursor.x, v.cursor.y], [0, 0]);
  v.write("\x1b[2;2H\x1b7\x1b[5;5H\x1b8"); T("ESC 7 / ESC 8 save and restore", [v.cursor.x, v.cursor.y], [1, 1]);
  v.write("\x1b[3;3H\x1b[s\x1b[1;1H\x1b[u"); T("CSI s / u save and restore", [v.cursor.x, v.cursor.y], [2, 2]);
  // colours and attributes
  v = new VT(20, 2);
  v.write("\x1b[31;42mA\x1b[0mB\x1b[91;104mC\x1b[38;5;196mD\x1b[38;5;244mE\x1b[38;5;21mF\x1b[48;2;1;2;3mG\x1b[39;49mH");
  const c = v.lines[0];
  T("SGR 31 / 42", [c[0].fg, c[0].bg], ["#cd3131", "#0dbc79"]);
  T("SGR 0 resets colours", [c[1].fg, c[1].bg], [null, null]);
  T("SGR 91 / 104 bright", [c[2].fg, c[2].bg], ["#f14c4c", "#3b8eea"]);
  T("SGR 38;5;196", c[3].fg, "#ff0000");
  T("SGR 38;5;244 grey ramp", c[4].fg, "#808080");
  T("SGR 38;5;21 colour cube", c[5].fg, "#0000ff");
  T("SGR 48;2;r;g;b truecolour", c[6].bg, "#010203");
  T("SGR 39 / 49 reset fg and bg", [c[7].fg, c[7].bg], [null, null]);
  v = new VT(10, 1);
  v.write("\x1b[1;4;7mX\x1b[22;24;27mY\x1b[1mZ\x1b[mW");
  const d = v.lines[0];
  T("SGR 1;4;7 on", [d[0].bold, d[0].underline, d[0].inverse], [true, true, true]);
  T("SGR 22;24;27 off", [d[1].bold, d[1].underline, d[1].inverse], [false, false, false]);
  T("SGR with no params resets", [d[2].bold, d[3].bold], [true, false]);
  // clear
  const fill = () => { const x = new VT(5, 3); x.write("abcde\r\nfghij\r\nklmno"); return x; };
  v = fill(); T("deferred wrap: exactly cols chars then CR LF adds no empty line", v.text(), ["abcde", "fghij", "klmno"]);
  v = fill(); v.write("\x1b[2;3H\x1b[0J"); T("J 0", v.text(), ["abcde", "fg", ""]);
  v = fill(); v.write("\x1b[2;3H\x1b[1J"); T("J 1", v.text(), ["", "   ij", "klmno"]);
  v = fill(); v.write("\x1b[2J"); T("J 2", v.text(), ["", "", ""]);
  v = fill(); v.write("\x1b[2;3H\x1b[K"); T("K 0", v.text(), ["abcde", "fg", "klmno"]);
  v = fill(); v.write("\x1b[2;3H\x1b[1K"); T("K 1", v.text(), ["abcde", "   ij", "klmno"]);
  v = fill(); v.write("\x1b[2;3H\x1b[2K"); T("K 2", v.text(), ["abcde", "", "klmno"]);
  // wrap
  v = new VT(5, 3); v.write("abcdefg"); T("auto-wrap at the right margin", v.text(), ["abcde", "fg", ""]);
  v = new VT(5, 3); v.write("abcde\r\nX"); T("deferred wrap is cleared by CR", v.text(), ["abcde", "X", ""]);
  v = new VT(5, 3); v.write("abcde"); T("the wrap is deferred", [v.cursor.x, v.cursor.y, v.wrapPending], [4, 0, true]);
  v = new VT(5, 3); v.write("\x1b[?7labcdefg"); T("?7l disables wrap", v.text(), ["abcdg", "", ""]);
  // editing
  v = new VT(5, 1); v.write("abcde\x1b[1;2H\x1b[2P"); T("DCH", v.text()[0], "ade");
  v = new VT(5, 1); v.write("abc\x1b[1;1H\x1b[2@"); T("ICH", v.text()[0], "  abc");
  v = new VT(5, 1); v.write("abcde\x1b[1;2H\x1b[2X"); T("ECH", v.text()[0], "a  de");
  v = new VT(10, 1); v.write("ab\bX\tY"); T("BS and TAB", v.text()[0], "aX      Y");
  v = new VT(5, 3); v.write("a\r\nb\r\nc\x1b[2;1H\x1b[L"); T("IL", v.text(), ["a", "", "b"]);
  v = new VT(5, 3); v.write("a\r\nb\r\nc\x1b[1;1H\x1b[M"); T("DL", v.text(), ["b", "c", ""]);
  // scrolling
  v = new VT(5, 2); v.write("1\r\n2\r\n3\r\n4");
  T("LF at the bottom scrolls", v.text(), ["3", "4"]);
  T("scrolled lines move to the scrollback", v.scrollbackText(), ["1", "2"]);
  v = new VT(5, 4); v.write("a\r\nb\r\nc\r\nd\x1b[2;3r");
  T("DECSTBM homes the cursor", [v.cursor.x, v.cursor.y], [0, 0]);
  v.write("\x1b[3;1H\n");
  T("LF at the region bottom scrolls only the region", v.text(), ["a", "c", "", "d"]);
  T("a region scroll keeps the scrollback", v.scrollbackText(), []);
  v = new VT(5, 3); v.write("a\r\nb\r\nc\x1b[S"); T("SU", v.text(), ["b", "c", ""]);
  v = new VT(5, 3); v.write("a\r\nb\r\nc\x1b[T"); T("SD", v.text(), ["", "a", "b"]);
  v = new VT(5, 3); v.write("a\x1bMb"); T("RI at the top scrolls down", v.text(), [" b", "a", ""]);
  // alternate screen
  v = new VT(5, 2); v.write("main\x1b[?1049h");
  T("?1049h shows a clear screen", v.text(), ["", ""]);
  v.write("\x1b[Halt"); T("writes go to the alternate screen", v.text(), ["alt", ""]);
  v.write("\x1b[?1049l");
  T("?1049l restores the main screen", v.text(), ["main", ""]);
  T("?1049l restores the cursor", [v.cursor.x, v.cursor.y], [4, 0]);
  v = new VT(5, 2); v.write("x\x1b[?47hy\x1b[?47l"); T("?47 round trip", v.text(), ["x", ""]);
  v = new VT(5, 2); v.write("\x1b[?25l"); T("?25l hides the cursor", v.cursor.visible, false);
  v.write("\x1b[?25h"); T("?25h shows the cursor", v.cursor.visible, true);
  // split sequences, OSC, reset
  v = new VT(10, 2); v.write("\x1b[3"); v.write("1mR"); T("a CSI split across writes", [v.text()[0], v.lines[0][0].fg], ["R", "#cd3131"]);
  v = new VT(10, 2); v.write("\x1b"); v.write("[2;2HZ"); T("ESC split from its CSI", v.text(), ["", " Z"]);
  v = new VT(10, 1); v.write("\x1b]0;title\x07ok\x1b]2;x\x1b\\!"); T("OSC is consumed", v.text()[0], "ok!");
  v = new VT(10, 1); v.write("\x1b(Bq\x1b[>cz"); T("charset and private-other sequences are consumed", v.text()[0], "qz");
  v = new VT(10, 1); v.write("abc\x1bc"); T("ESC c clears the screen", [v.text()[0], v.cursor.x], ["", 0]);
  // resize
  v = new VT(5, 3); v.write("a\r\nb\r\nc"); v.resize(3, 2);
  T("shrinking the rows keeps the cursor row", v.text(), ["b", "c"]);
  T("the dropped row goes to the scrollback", v.scrollbackText(), ["a"]);
  v.resize(6, 4); T("growing pads", [v.text(), v.lines[0].length], [["b", "c", "", ""], 6]);
  // UTF-8
  for (const manual of [false, true]) {
    const dec = V.utf8(manual), tag = manual ? " (manual decoder)" : " (TextDecoder)";
    const bytes = Buffer.from("é日😀", "utf8");
    let s = "";
    for (const part of [[0, 1], [1, 3], [3, 6], [6, 8], [8, bytes.length]]) s += dec(new Uint8Array(bytes.subarray(part[0], part[1])));
    T("UTF-8 split across chunks" + tag, s, "é日😀");
  }
  T("b64ToBytes", Array.from(V.b64ToBytes("aGk=")), [104, 105]);
  // keys
  const K = V.keyToSeq;
  T("Enter", K({ key: "Enter" }), "\r");
  T("Backspace", K({ key: "Backspace" }), "\x7f");
  T("Tab / Shift-Tab", [K({ key: "Tab" }), K({ key: "Tab", shiftKey: true })], ["\t", "\x1b[Z"]);
  T("arrows", [K({ key: "ArrowUp" }), K({ key: "ArrowDown" }), K({ key: "ArrowRight" }), K({ key: "ArrowLeft" })], ["\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D"]);
  T("Home End Delete PageUp F1", [K({ key: "Home" }), K({ key: "End" }), K({ key: "Delete" }), K({ key: "PageUp" }), K({ key: "F1" })], ["\x1b[H", "\x1b[F", "\x1b[3~", "\x1b[5~", "\x1bOP"]);
  T("Ctrl+C", K({ key: "c", ctrlKey: true }), "\x03");
  T("Ctrl+Shift+C", K({ key: "C", ctrlKey: true, shiftKey: true }), "\x03");
  T("Ctrl+D", K({ key: "d", ctrlKey: true }), "\x04");
  T("Ctrl+[ and Ctrl+Space", [K({ key: "[", ctrlKey: true }), K({ key: " ", ctrlKey: true })], ["\x1b", "\x00"]);
  T("Alt+x", K({ key: "x", altKey: true }), "\x1bx");
  T("printable", [K({ key: "a" }), K({ key: "A", shiftKey: true }), K({ key: "é" })], ["a", "A", "é"]);
  T("modifier-only keys", [K({ key: "Shift", shiftKey: true }), K({ key: "Control", ctrlKey: true }), K({ key: "Alt" }), K({ key: "Meta" })], [null, null, null, null]);
  T("meta combos", K({ key: "c", metaKey: true }), null);
}

if (mode === "widget") {
  const inst = () => island.__skyIsland && island.__skyIsland.inst;
  T("the widget mounts", !!inst(), true);
  T("mount marks the element ready", island.getAttribute("data-term-ready"), "1");
  T("mount sets role and label", [island.getAttribute("role"), island.getAttribute("aria-label"), island.tabIndex], ["application", "Shell", 0]);
  T("mount measures the cell size", [island.getAttribute("data-term-cols"), island.getAttribute("data-term-rows")], ["40", "10"]);
  const evs = () => island.events.map((e) => [e.type, e.detail]);
  T("mount sends resize then ready", evs(), [["skyisland-resize", { cols: 40, rows: 10 }], ["skyisland-ready", {}]]);
  island.events.length = 0;
  let prevented = 0;
  for (const k of ["l", "s", "Enter", "Shift"]) island.fire("keydown", { key: k, preventDefault() { prevented++; } });
  T("no send before the batch timer", island.events.length, 0);
  runTimers();
  T("keydowns batch into ONE input send", evs(), [["skyisland-input", { data: "ls\r" }]]);
  T("only mapped keys are prevented", prevented, 3);
  island.events.length = 0;
  island.fire("paste", { clipboardData: { getData: () => "a\nb" }, preventDefault() {} });
  runTimers();
  T("paste sends CR line ends", evs(), [["skyisland-input", { data: "a\rb" }]]);
  const I = g("Sky.__islands");
  const rowText = (y) => { const box = island.childNodes[0]; return box.childNodes[y].textContent.replace(/ +$/, ""); };
  const hello = Buffer.from("hello\r\nworld");
  I.command("t1", "output", { data: b64(hello), from: 0, next: hello.length });
  runTimers();
  T("output reaches the screen", [inst().vt.text()[0], inst().vt.text()[1]], ["hello", "world"]);
  T("output is painted into the rows", [rowText(0), rowText(1)], ["hello", "world"]);
  T("the offset advances", inst().next, 12);
  const u = Buffer.from("é日😀", "utf8");
  let at = 12;
  for (const part of [[0, 1], [1, 3], [3, 7], [7, u.length]]) {
    const b = u.subarray(part[0], part[1]);
    I.command("t1", "output", { data: b64(b), from: at, next: at + b.length });
    at += b.length;
  }
  runTimers();
  T("UTF-8 split across output commands", inst().vt.text()[1], "worldé日😀");
  I.command("t1", "output", { data: b64(hello), from: 0, next: hello.length });
  T("a repeated chunk is ignored", inst().vt.text()[1], "worldé日😀");
  const over = Buffer.concat([u.subarray(u.length - 2), Buffer.from("XY")]);
  I.command("t1", "output", { data: b64(over), from: at - 2, next: at + 2 });
  runTimers();
  T("an overlapping chunk writes only the new bytes", [inst().vt.text()[1], inst().next], ["worldé日😀XY", at + 2]);
  I.command("t1", "exit", { code: 0, signal: null });
  T("exit with a code", inst().vt.text()[2], "[process exited with code 0]");
  I.command("t1", "exit", { code: null, signal: 9 });
  T("exit by a signal", inst().vt.text()[4], "[process terminated by signal 9]");
  I.command("t1", "reset", {});
  runTimers();
  T("reset clears the screen and the offset", [inst().vt.text().join(""), inst().next, rowText(0)], ["", 0, ""]);
  // a resize without ResizeObserver comes from the window resize event
  island.events.length = 0;
  island.clientWidth = 480;
  winHandlers.resize();
  runTimers();
  T("a size change sends resize", evs(), [["skyisland-resize", { cols: 60, rows: 10 }]]);
  T("a size change updates the attributes", island.getAttribute("data-term-cols"), "60");
  island.events.length = 0;
  winHandlers.resize();
  runTimers();
  T("an unchanged size sends nothing", island.events.length, 0);
  island.setAttribute("data-sky-props", "{\"label\":\"Build\"}");
  I.update(island);
  T("update changes the label", island.getAttribute("aria-label"), "Build");
  island.isConnected = false;
  I.sweep();
  island.fire("keydown", { key: "x", preventDefault() {} });
  runTimers();
  T("destroy removes the listeners", island.events.length, 0);
}
process.stdout.write(JSON.stringify({ ran, fails }));
`

func runTerminalHarness(t *testing.T, mode string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping the terminal widget test")
	}
	dir := t.TempDir()
	jsPath := dir + "/client.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(islandClientJS+terminalWidgetJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(terminalHarnessJS), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harnessPath, jsPath, mode).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	var res struct {
		Ran   int      `json:"ran"`
		Fails []string `json:"fails"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("harness output is not JSON: %v\n%s", err, out)
	}
	if res.Ran == 0 {
		t.Fatalf("the %s harness ran no assertions", mode)
	}
	for _, f := range res.Fails {
		t.Error(f)
	}
}

// The VT core: cursor moves, colours, clears, wrap, editing, scrolling, the
// alternate screen, split sequences, resize, streaming UTF-8 and key mapping.
func TestTerminalJS_VTCore(t *testing.T) { runTerminalHarness(t, "vt") }

// The island: mount sends resize then ready, keys batch into one input,
// output commands dedupe by offset and decode split UTF-8, exit and reset,
// a resize, update and destroy.
func TestTerminalJS_Widget(t *testing.T) { runTerminalHarness(t, "widget") }

// The Rust build reads terminalWidgetJS as a raw literal, and both clients
// load it: it must hold no backquote and parse.
func TestTerminalJS_NoBackquoteAndSyntax(t *testing.T) {
	if strings.Contains(terminalWidgetJS, "`") {
		t.Fatal("terminalWidgetJS must hold no backquote (the Rust build reads it as a raw literal)")
	}
	if !strings.HasPrefix(terminalWidgetJS, "// Sky terminal widget (runtime-go/rt/island_terminal.go)") {
		t.Fatal("terminalWidgetJS must open with its header line")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping the syntax check")
	}
	f := t.TempDir() + "/terminal.js"
	if err := os.WriteFile(f, []byte(islandClientJS+terminalWidgetJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("terminalWidgetJS failed node --check:\n%s", out)
	}
}
