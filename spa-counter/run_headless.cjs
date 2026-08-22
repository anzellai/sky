// Headless verification of the REAL Sky-emitted Sky.Spa app running as wasm.
// Loads wasm_exec.js, shims a DOM (rich enough for the client-side diff renderer
// — querySelector by sky-id, getAttribute, activeElement), instantiates the
// emitted main.wasm, then drives the button handlers and asserts the Model
// transitions render correctly via the DIFF path (initial mount is a full build;
// each +1/-1/Reset applies a minimal text patch, not a full rebuild). ZERO server.
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.attrs = {}; this.listeners = {};
    this._text = null; this.parent = null; this._value = '';
    this.selectionStart = 0; this.selectionEnd = 0; this.scrollTop = 0;
    this.checked = false; this.disabled = false;
  }
  get tagName() { return this.tag.startsWith('#') ? this.tag : this.tag.toUpperCase(); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(e, f) { (this.listeners[e] = this.listeners[e] || []).push(f); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  set innerHTML(v) { if (v === '') { this.children.forEach(c => (c.parent = null)); this.children = []; } }
  get innerHTML() { return ''; }
  set textContent(v) { this._text = v; this.children.forEach(c => (c.parent = null)); this.children = []; }
  get textContent() { return this._text !== null ? this._text : this.children.map(c => c.textContent).join(''); }
  get value() { return this._value; }
  set value(v) { this._value = String(v); this.selectionStart = this.selectionEnd = this._value.length; }
  setSelectionRange(s, e) { this.selectionStart = s; this.selectionEnd = e; }
  focus() { globalThis.document.activeElement = this; }
  contains(node) { if (node === this) return true; return this.children.some(c => c.contains && c.contains(node)); }
  remove() { if (this.parent) { const i = this.parent.children.indexOf(this); if (i >= 0) this.parent.children.splice(i, 1); } }
  click() { (this.listeners['click'] || []).forEach(f => f.call(this, { target: this, currentTarget: this, type: 'click' })); }
}

const root = new El('#app');
function walk(node, pred, acc = []) { if (pred(node)) acc.push(node); node.children.forEach(c => walk(c, pred, acc)); return acc; }
function parseSkyId(sel) { const m = /^\[sky-id="([^"]*)"\]$/.exec(sel); return m ? m[1].replace(/\\"/g, '"') : null; }
globalThis.document = {
  activeElement: null,
  createElement: t => new El(t),
  createTextNode: t => { const e = new El('#text'); e._text = t; return e; },
  getElementById: id => (id === 'app' ? root : walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null),
  querySelector: sel => { const id = parseSkyId(sel); if (id === null) return null; return walk(root, n => n.getAttribute && n.getAttribute('sky-id') === id)[0] || null; },
  body: root,
};

function countText() { const p = walk(root, n => n.tag === 'p')[0]; return p ? p.textContent : '<none>'; }
function button(label) { return walk(root, n => n.tag === 'button' && n.textContent === label)[0]; }

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);                    // runs main(); initial render, then parks at select{}
  await new Promise(r => setImmediate(r));  // flush the Go scheduler

  const steps = [];
  const check = (label, want) => { const got = countText(); steps.push(`${got === want ? 'PASS' : 'FAIL'}  ${label}: expected ${want}, got ${got}`); };

  check('initial render', '0');
  button('+1').click(); await new Promise(r => setImmediate(r));
  button('+1').click(); await new Promise(r => setImmediate(r));
  button('+1').click(); await new Promise(r => setImmediate(r));
  check('after +1 x3 (pure client-local update, diff-applied)', '3');
  button('Reset').click(); await new Promise(r => setImmediate(r));
  check('after Reset', '0');
  button('-1').click(); await new Promise(r => setImmediate(r));
  check('after -1', '-1');

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — REAL Sky.Spa app: client TEA loop + client-side diff renderer verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
