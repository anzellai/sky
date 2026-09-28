//go:build !js

package rt

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The built-in "sky-terminal" widget (island_terminal.go), run for real in
// node against a minimal stub of the DOM, like TestIslandJS_ClientRuntime.
// The harness asserts inside node and prints {"ran": N, "fails": [...]}, so a
// failure names its case. node is required (requireNode).

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
// A 2D context that records its calls.
class Ctx {
  constructor() { this.calls = []; this.fillStyle = ""; this.font = ""; this.globalAlpha = 1; }
  setTransform() {}
  fillRect(x, y, w, h) { this.calls.push(["fillRect", x, y, w, h, this.fillStyle]); }
  fillText(t, x, y) { this.calls.push(["fillText", t, x, y, this.fillStyle, this.font]); }
  strokeRect(x, y, w, h) { this.calls.push(["strokeRect", x, y, w, h]); }
}
class El {
  constructor(tag, attrs) {
    this.nodeType = 1; this.tagName = tag.toUpperCase(); this._a = Object.assign({}, attrs || {});
    this.childNodes = []; this.parentNode = null; this.isConnected = false; this.events = [];
    this.style = {}; this._text = ""; this.handlers = {}; this.clientWidth = 0; this.clientHeight = 0;
    if (this.tagName === "CANVAS" && withCanvas) { this.ctx = new Ctx(); this.getContext = (k) => (k === "2d" ? this.ctx : null); }
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
  focus() { this.focused = true; doc.activeElement = this; }
  getBoundingClientRect() { return this._text === "MMMMMMMMMM" ? { width: 80, height: 16 } : { width: 0, height: 0 }; }
}
const withCanvas = mode === "canvas";
let timers = [];
function runTimers() {
  for (let guard = 0; timers.length && guard < 100; guard++) {
    const t = timers; timers = [];
    for (const x of t) if (!x.dead) x.fn();
  }
}
let frames = [];
function runFrame() { const f = frames; frames = []; for (const x of f) if (!x.dead) x.fn(0); }
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
  CustomEvent, Event: Ev, JSON, Math, Object, Array, String, Number, parseInt, parseFloat, Date,
  getComputedStyle: () => ({ fontSize: "13px" }),
  addEventListener: (t, fn) => { winHandlers[t] = fn; },
  removeEventListener: () => {},
};
if (withCanvas) {
  sandbox.requestAnimationFrame = (fn) => { const f = { fn, dead: false }; frames.push(f); return f; };
  sandbox.cancelAnimationFrame = (f) => { if (f) f.dead = true; };
}
sandbox.window = sandbox;
const island = new El("div", { "data-sky-island": "sky-terminal", "data-sky-island-id": "t1", "data-sky-props": "{\"label\":\"Shell\"}" });
island.isConnected = true;
island.clientWidth = 320; island.clientHeight = 160;
doc.appendChild(island);
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
vm.runInContext("Sky.__islandHostReady()", sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const TM = g("Sky.__term");

if (mode === "model") {
  const M = TM.Model;
  const st = [[-1, -1, 0], [1, -1, 1], [-1, -1, 64], [-1, -1, 128], [256 + 0x102030, 196, 8]];
  const cells = TM.decodeLine(st, [0, "ab", 2, "日本", 3, "é", 0, "😀x"], 0);
  T("decodeLine: plain, wide, cluster, surrogate pair", cells.map((c) => [c.t, c.w]), [["a", 1], ["b", 1], ["日", 2], ["", 0], ["本", 2], ["", 0], ["é", 1], ["😀", 1], ["x", 1]]);
  T("lineText skips wide tails", TM.lineText(cells), "ab日本é😀x");
  T("colour: default, palette, true colour", [TM.colour(-1, "D"), TM.colour(1, "D"), TM.colour(196, "D"), TM.colour(244, "D"), TM.colour(256 + 0x102030, "D")], ["D", "#cd3131", "#ff0000", "#808080", "#102030"]);
  let m = new M();
  T("a frame on nothing is a gap", m.apply({ seq: 5, base: 4, st: [], ops: [] }), false);
  T("a repaint always applies", m.apply({ seq: 1, base: -1, st: st, ops: [["z", 6, 3], ["x"], ["p", [0, "old"]], ["r", 0, 0, 0, "hello"], ["r", 1, 2, 1, "x"], ["c", 3, 1, 1], ["t", "T"], ["m", 3]] }), true);
  T("repaint: rows", m.text(), ["hello", "  x", ""]);
  T("repaint: scrollback", m.scrollbackText(), ["old"]);
  T("repaint: cursor title modes", [m.cx, m.cy, m.cursorOn, m.title, m.modes], [3, 1, true, "T", 3]);
  T("repaint: the style of a cell", m.grid[1][2].s, [1, -1, 1]);
  T("a frame on the last one applies", m.apply({ seq: 2, base: 1, st: st, ops: [["u", 0, 2, 1, 1]] }), true);
  T("u with p=1 pushes the widget's own top row", [m.text(), m.scrollbackText()], [["  x", "", ""], ["old", "hello"]]);
  m.apply({ seq: 3, base: 2, st: st, ops: [["u", 0, 2, 5, [[0, "l1"], [0, "l2"]]]] });
  T("u with lines pushes those lines, and n past the region clears it", [m.text(), m.scrollbackText()], [["", "", ""], ["old", "hello", "l1", "l2"]]);
  m.apply({ seq: 4, base: 3, st: st, ops: [["r", 0, 0, 0, "a"], ["r", 1, 0, 0, "b"], ["r", 2, 0, 0, "c"], ["d", 0, 2, 1], ["u", 1, 2, 1, 0]] });
  T("d scrolls down; u with p=0 pushes nothing", [m.text(), m.scrollbackText().length], [["", "b", ""], 4]);
  m.apply({ seq: 5, base: 4, st: st, ops: [["r", 1, 0, 0, "abcdef"], ["r", 1, 2, 0, "Z"]] });
  T("r from a column clears the rest of the row", m.text()[1], "abZ");
  m.apply({ seq: 6, base: 5, st: st, ops: [["b", 2], ["x"]] });
  T("b counts the bell; x clears the scrollback", [m.bells, m.sb.length], [2, 0]);
  T("a lost frame is a gap", m.apply({ seq: 8, base: 7, st: st, ops: [] }), false);
  T("a check frame on the last one applies", m.apply({ seq: 6, base: 6, st: [], ops: [] }), true);
  const lines = [];
  for (let i = 0; i < 1200; i++) lines.push([0, "n" + i]);
  m.apply({ seq: 7, base: 6, st: st, ops: [["p"].concat(lines)] });
  T("the scrollback keeps the last 1000 lines", [m.sb.length, TM.lineText(m.sb[0]), TM.lineText(m.sb[999])], [1000, "n200", "n1199"]);
  const K = TM.keyToSeq;
  T("Enter Backspace", [K({ key: "Enter" }), K({ key: "Backspace" })], ["\r", "\x7f"]);
  T("Tab / Shift-Tab", [K({ key: "Tab" }), K({ key: "Tab", shiftKey: true })], ["\t", "\x1b[Z"]);
  T("arrows", [K({ key: "ArrowUp" }), K({ key: "ArrowLeft" })], ["\x1b[A", "\x1b[D"]);
  T("arrows in cursor-keys mode", [K({ key: "ArrowUp" }, true), K({ key: "Home" }, true), K({ key: "ArrowUp", ctrlKey: true }, true)], ["\x1bOA", "\x1bOH", "\x1b[A"]);
  T("Home End Delete PageUp F1", [K({ key: "Home" }), K({ key: "End" }), K({ key: "Delete" }), K({ key: "PageUp" }), K({ key: "F1" })], ["\x1b[H", "\x1b[F", "\x1b[3~", "\x1b[5~", "\x1bOP"]);
  T("Ctrl+C Ctrl+D Ctrl+[ Ctrl+Space", [K({ key: "c", ctrlKey: true }), K({ key: "d", ctrlKey: true }), K({ key: "[", ctrlKey: true }), K({ key: " ", ctrlKey: true })], ["\x03", "\x04", "\x1b", "\x00"]);
  T("Alt+x", K({ key: "x", altKey: true }), "\x1bx");
  T("printable", [K({ key: "a" }), K({ key: "A", shiftKey: true }), K({ key: "é" })], ["a", "A", "é"]);
  T("modifier-only and Meta combos", [K({ key: "Shift", shiftKey: true }), K({ key: "Meta" }), K({ key: "c", metaKey: true })], [null, null, null]);
}

const inst = () => island.__skyIsland && island.__skyIsland.inst;
const evs = () => island.events.map((e) => [e.type, e.detail]);
const I = g("Sky.__islands");

if (mode === "widget") {
  T("the widget mounts", !!inst(), true);
  T("mount marks the element ready", island.getAttribute("data-term-ready"), "1");
  T("mount sets role and label", [island.getAttribute("role"), island.getAttribute("aria-label"), island.tabIndex], ["application", "Shell", 0]);
  T("without a 2D canvas the text layer renders", island.getAttribute("data-term-renderer"), "text");
  T("mount measures the cell size", [island.getAttribute("data-term-cols"), island.getAttribute("data-term-rows")], ["40", "10"]);
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
  const textRows = () => inst().textLayer.childNodes.map((r) => r.textContent);
  I.command("t1", "frame", { seq: 1, base: -1, st: [[-1, -1, 0]], ops: [["z", 40, 10], ["x"], ["r", 0, 0, 0, "hello"], ["r", 1, 0, 0, "world"], ["c", 5, 1, 1], ["t", ""], ["m", 2]] });
  runTimers();
  T("a repaint reaches the model", inst().model.text().slice(0, 2), ["hello", "world"]);
  T("the rows are painted into the text layer", textRows().slice(0, 3), ["hello", "world", ""]);
  island.events.length = 0;
  island.fire("paste", { clipboardData: { getData: () => "x" }, preventDefault() {} });
  runTimers();
  T("bracketed paste mode wraps a paste", evs(), [["skyisland-input", { data: "\x1b[200~x\x1b[201~" }]]);
  I.command("t1", "frame", { seq: 2, base: 1, st: [[-1, -1, 0]], ops: [["r", 1, 5, 0, "!"], ["t", "vim"], ["b", 1]] });
  runTimers();
  T("a diff frame updates one row", textRows().slice(0, 2), ["hello", "world!"]);
  T("the title and the bell reach attributes", [island.getAttribute("data-term-title"), island.getAttribute("data-term-bell")], ["vim", "1"]);
  island.events.length = 0;
  I.command("t1", "frame", { seq: 9, base: 8, st: [], ops: [] });
  I.command("t1", "frame", { seq: 10, base: 9, st: [], ops: [] });
  T("a gap asks for a repaint once", evs(), [["skyisland-ready", {}]]);
  T("the gap frame was not applied", inst().model.seq, 2);
  island.events.length = 0;
  I.command("t1", "frame", { seq: 3, base: -1, st: [[-1, -1, 0]], ops: [["z", 40, 10], ["x"], ["r", 0, 0, 0, "again"], ["c", 0, 1, 1]] });
  runTimers();
  T("the repaint replaces the screen", textRows().slice(0, 2), ["again", ""]);
  I.command("t1", "frame", { seq: 11, base: 10, st: [], ops: [] });
  T("after a repaint a new gap asks again", evs(), [["skyisland-ready", {}]]);
  island.events.length = 0;
  I.command("t1", "output", { data: "aGk=", from: 0, next: 2 });
  T("an unknown command is ignored", [island.events.length, inst().model.seq], [0, 3]);
  // a resize without ResizeObserver comes from the window resize event
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

if (mode === "canvas") {
  T("with a 2D canvas the canvas renders", island.getAttribute("data-term-renderer"), "canvas");
  const ctx = inst().canvas.ctx;
  const st = [[-1, -1, 0], [1, 4, 1], [-1, -1, 8]];
  let rows = [];
  for (let y = 0; y < 10; y++) rows.push(["r", y, 0, 0, "row" + y, 1, " red on blue ", 2, "under"]);
  I.command("t1", "frame", { seq: 1, base: -1, st: st, ops: [["z", 40, 10], ["x"]].concat(rows, [["c", 0, 9, 1]]) });
  for (let k = 2; k <= 6; k++) I.command("t1", "frame", { seq: k, base: k - 1, st: st, ops: [["r", 2, 0, 0, "frame" + k]] });
  T("frames between two paints draw nothing", ctx.calls.length, 0);
  const before = inst().passes;
  runFrame();
  T("six frames cost one draw pass", inst().passes - before, 1);
  T("the pass drew the latest row 2", ctx.calls.some((c) => c[0] === "fillText" && c[1] === "frame6"), true);
  T("a row of three runs is three fillText calls", ctx.calls.filter((c) => c[0] === "fillText" && c[3] === 5 * 16 + 8).map((c) => c[1]), ["row5", " red on blue", "under"]);
  T("its background run is one fillRect", ctx.calls.filter((c) => c[0] === "fillRect" && c[2] === 5 * 16 && c[5] === "#2472c8").length, 1);
  T("its underline is one rule", ctx.calls.filter((c) => c[0] === "fillRect" && c[2] === 5 * 16 + 14 && c[4] === 1).length, 1);
  ctx.calls.length = 0;
  I.command("t1", "frame", { seq: 7, base: 6, st: st, ops: [["r", 3, 4, 0, "X"]] });
  runFrame();
  const ys = new Set(ctx.calls.filter((c) => c[0] === "fillRect" || c[0] === "fillText").map((c) => c[0] === "fillRect" ? c[2] : c[3] - 8));
  T("a one-row frame redraws only that row", Array.from(ys), [3 * 16]);
  T("the redrawn row holds the new cell (an r op clears the rest of the row)", ctx.calls.filter((c) => c[0] === "fillText").map((c) => c[1]), ["row3X"]);
  ctx.calls.length = 0;
  runFrame();
  T("no change: no draw pass work", ctx.calls.length, 0);
  I.command("t1", "frame", { seq: 8, base: 7, st: st, ops: [["u", 0, 9, 1, 1], ["r", 9, 0, 0, "new"]] });
  runFrame();
  const drawnRows = new Set(ctx.calls.filter((c) => c[0] === "fillText").map((c) => (c[3] - 8) / 16));
  T("a scroll redraws the rows it moved", drawnRows.size, 10);
  T("the text layer follows the scroll", inst().textLayer.childNodes[9].textContent, "new");
}

if (mode === "roundtrip") {
  const data = JSON.parse(fs.readFileSync(process.argv[4], "utf8"));
  for (const c of data) {
    const m = new TM.Model();
    let ok = true;
    for (let i = 0; i < c.frames.length && ok; i++) {
      if (!m.apply(c.frames[i])) { fails.push(c.name + ": frame " + i + " did not apply"); ok = false; }
      const want = c.checks[i];
      if (ok && want) {
        ran++;
        const got = { text: m.text(), sb: m.scrollbackText(), cursor: [m.cx, m.cy, m.cursorOn], title: m.title, modes: m.modes };
        if (JSON.stringify(got) !== JSON.stringify(want)) {
          fails.push(c.name + " after frame " + i + ": got " + JSON.stringify(got).slice(0, 400) + " want " + JSON.stringify(want).slice(0, 400));
          ok = false;
        }
      }
    }
  }
}
process.stdout.write(JSON.stringify({ ran, fails }));
`

func runTerminalHarness(t *testing.T, mode string, extra ...string) {
	t.Helper()
	node := requireNode(t)
	dir := t.TempDir()
	jsPath := dir + "/client.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(islandClientJS+terminalWidgetJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(terminalHarnessJS), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{harnessPath, jsPath, mode}, extra...)
	out, err := exec.Command(node, args...).CombinedOutput()
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

// The widget's screen model: the line decoder (wide, cluster, surrogate
// pairs), every op, gap detection, the scrollback bound, colours and the
// key mapping (cursor-keys mode included).
func TestTerminalJS_Model(t *testing.T) { runTerminalHarness(t, "model") }

// The island without a canvas (the text-layer renderer): mount sends resize
// then ready, keys batch into one input, bracketed paste, frames reach the
// text layer, a gap asks for one repaint, title and bell, resize, update and
// destroy.
func TestTerminalJS_Widget(t *testing.T) { runTerminalHarness(t, "widget") }

// The canvas renderer against a recording 2D context: frames between two
// animation frames cost one draw pass, a row is one fillText per run and one
// fillRect per background run, a one-row frame redraws only that row, and
// an idle frame draws nothing.
func TestTerminalJS_CanvasDrawBatching(t *testing.T) { runTerminalHarness(t, "canvas") }

// The op stream across the language boundary: frames the Go screen makes
// (term_frame.go) applied by the widget's JS model give the Go screen's
// text, scrollback, cursor, title and modes after every frame.
func TestTerminalJS_FramesFromGoRoundTrip(t *testing.T) {
	type check struct {
		Text   []string `json:"text"`
		SB     []string `json:"sb"`
		Cursor []any    `json:"cursor"`
		Title  string   `json:"title"`
		Modes  int      `json:"modes"`
	}
	type tcase struct {
		Name   string           `json:"name"`
		Frames []map[string]any `json:"frames"`
		Checks []*check         `json:"checks"`
	}
	var cases []tcase
	for seed := int64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewSource(seed))
		in := randomTerminalBytes(rng, 30000)
		if seed == 1 {
			var b strings.Builder
			for i := 1; i <= 3000; i++ {
				fmt.Fprintf(&b, "%d\r\n", i)
			}
			in = []byte(b.String())
		}
		s := newVTScreen(20+rng.Intn(60), 4+rng.Intn(20))
		sh := &termShadow{}
		c := tcase{Name: fmt.Sprintf("seed %d", seed)}
		add := func(full bool) {
			f, ok := s.frame(sh, full)
			if !ok {
				return
			}
			sb := s.scrollbackText()
			if sb == nil {
				sb = []string{}
			}
			c.Frames = append(c.Frames, f)
			c.Checks = append(c.Checks, &check{Text: s.text(), SB: sb, Cursor: []any{s.cx, s.cy, s.cursorOn}, Title: s.title, Modes: s.modes()})
		}
		add(true)
		for off := 0; off < len(in); {
			n := min(1+rng.Intn(3000), len(in)-off)
			s.feed(in[off : off+n])
			off += n
			if seed%2 == 0 && rng.Intn(15) == 0 {
				s.resize(10+rng.Intn(60), 3+rng.Intn(20))
			}
			add(rng.Intn(40) == 0)
		}
		cases = append(cases, c)
	}
	b, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	p := t.TempDir() + "/frames.json"
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	runTerminalHarness(t, "roundtrip", p)
}

// The Rust build reads terminalWidgetJS as a raw literal, and both clients
// load it: it must hold no backquote and parse.
func TestTerminalJS_NoBackquoteAndSyntax(t *testing.T) {
	if strings.Contains(terminalWidgetJS, "`") {
		t.Fatal("terminalWidgetJS must hold no backquote (the Rust build reads it as a raw literal)")
	}
	if !strings.HasPrefix(terminalWidgetJS, "// Sky terminal widget (runtime-go/rt/island_terminal.go)") {
		t.Fatal("terminalWidgetJS must open with its header line")
	}
	node := requireNode(t)
	f := t.TempDir() + "/terminal.js"
	if err := os.WriteFile(f, []byte(islandClientJS+terminalWidgetJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("terminalWidgetJS failed node --check:\n%s", out)
	}
}
