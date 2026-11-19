package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// An html patch (the new children of one element) on a table, a thead, a
// tbody or a row must keep the row and cell tags.
//
// __skyReplaceHTMLPreservingFocus used to parse the new markup inside a
// detached <div>. A <div> parses in body context, where the HTML parser
// drops <thead>, <tbody>, <tr>, <td> and <th> tags that are not inside a
// <table>: a view whose table rows changed when a tab switched lost every
// row and cell (only the cell text stayed). The kids patch already parsed in
// a <template>, whose content keeps the tags; the html patch now does too.
//
// The harness models the one parser rule that matters: a <div> drops the
// table-internal tags (keeping their text), a <template> keeps them.
func TestLiveJS_HTMLPatchOnTableKeepsRowsAndCells(t *testing.T) {
	node := requireNode(t)
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const TABLE_TAGS = new Set(["thead", "tbody", "tfoot", "tr", "td", "th", "caption", "colgroup", "col"]);
class Node0 {
  constructor() { this.childNodes = []; this.parentNode = null; }
  get firstChild() { return this.childNodes[0] || null; }
  appendChild(c) { if (c.nodeType === 11) { while (c.firstChild) this.appendChild(c.firstChild); return c; }
    if (c.parentNode) c.parentNode.removeChild(c); c.parentNode = this; this.childNodes.push(c); return c; }
  removeChild(c) { this.childNodes.splice(this.childNodes.indexOf(c), 1); c.parentNode = null; return c; }
  replaceChild(n, o) { if (n.parentNode) n.parentNode.removeChild(n);
    const i = this.childNodes.indexOf(o); this.childNodes[i] = n; n.parentNode = this; o.parentNode = null; return o; }
  contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; }
  querySelectorAll(sel) {
    const alts = sel.split(",").map((s) => s.trim()).map((s) => {
      const m = /^([a-z]*)(?:\[([^\]=]+)(?:="([^"]*)")?\])?$/.exec(s);
      return m ? { tag: m[1], attr: m[2], val: m[3] } : null;
    });
    const out = [];
    const ok = (e) => alts.some((a) => a && (!a.tag || e.localName === a.tag) &&
      (!a.attr || (e.hasAttribute(a.attr) && (a.val === undefined || e.getAttribute(a.attr) === a.val))));
    const walk = (n) => { for (const c of n.childNodes) if (c.nodeType === 1) { if (ok(c)) out.push(c); walk(c); } };
    walk(this); return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
class Text0 extends Node0 { constructor(t) { super(); this.nodeType = 3; this.textContent = t; } }
class Frag extends Node0 { constructor() { super(); this.nodeType = 11; } }
function parse(html, keepTable) {
  const root = new Frag(); let cur = root;
  const re = /<\/?([a-zA-Z0-9]+)([^>]*)>|([^<]+)/g; let m;
  while ((m = re.exec(html))) {
    if (m[3] !== undefined) { cur.appendChild(new Text0(m[3])); continue; }
    const tag = m[1].toLowerCase();
    if (!keepTable && TABLE_TAGS.has(tag)) continue;
    if (m[0][1] === "/") { if (cur.parentNode) cur = cur.parentNode; continue; }
    const attrs = {}; const ar = /([a-zA-Z0-9-]+)="([^"]*)"/g; let a;
    while ((a = ar.exec(m[2]))) attrs[a[1]] = a[2];
    const e = new El(tag, attrs); cur.appendChild(e); cur = e;
  }
  return root;
}
class El extends Node0 {
  constructor(tag, attrs) { super(); this.nodeType = 1; this.localName = tag; this.tagName = tag.toUpperCase();
    this.namespaceURI = "http://www.w3.org/1999/xhtml"; this._a = Object.assign({}, attrs || {}); this.content = null; }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k) ? this._a[k] : null; }
  setAttribute(k, v) { this._a[k] = String(v); }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._a, k); }
  removeAttribute(k) { delete this._a[k]; }
  get attributes() { return Object.keys(this._a).map((n) => ({ name: n, value: this._a[n] })); }
  set innerHTML(h) {
    const f = parse(h, this.localName === "template");
    if (this.localName === "template") { this.content = f; return; }
    this.childNodes = []; while (f.firstChild) this.appendChild(f.firstChild);
  }
}
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() {} },
  navigator: {},
  document: {
    readyState: "loading",
    getElementById: () => null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
    createElement: (t) => new El(t),
    createElementNS: (ns, t) => new El(t),
    createDocumentFragment: () => new Frag(),
  },
  EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
  fetch: () => Promise.resolve({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) }),
  JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
};
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const swap = vm.runInContext("__skyReplaceHTMLPreservingFocus", sandbox);
const shape = (n) => n.nodeType === 3 ? n.textContent : n.localName + "(" + n.childNodes.map(shape).join(",") + ")";
const out = {};
for (const [tag, html] of [
  ["table", '<thead><tr><th>Name</th></tr></thead><tbody><tr sky-id="r1"><td>a</td></tr></tbody>'],
  ["tbody", '<tr sky-id="r1"><td role="cell">a</td></tr><tr sky-id="r2"><td>b</td></tr>'],
  ["tr", '<td>x</td><td>y</td>'],
]) {
  const el = new El(tag);
  el.appendChild(new El("tr"));
  swap(el, html);
  out[tag] = shape(el);
}
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
		`"table":"table(thead(tr(th(Name))),tbody(tr(td(a))))"`,
		`"tbody":"tbody(tr(td(a)),tr(td(b)))"`,
		`"tr":"tr(td(x),td(y))"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s\n  in %s", want, got)
		}
	}
}
