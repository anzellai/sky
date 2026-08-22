// Headless verification of the REAL Sky-emitted Sky.Spa app's Cmd.perform path.
// Clicking "Fetch" makes update return `Cmd.perform (Time.now ()) GotTime`; the
// client effect interpreter must run the (synchronous) Time.now task and
// dispatch `GotTime (Ok millis)`, which the model records and the view shows.
// ZERO server. Proves perform → toMsg(result) → dispatch → re-render.
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

const byId = id => walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null;
const timeText = () => { const p = byId('time'); return p ? p.textContent : '<none>'; };
const flush = () => new Promise(r => setImmediate(r));

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);
  await flush();

  const steps = [];
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);

  check('initial render is 0', timeText() === '0', `got ${timeText()}`);

  byId('fetch').click();
  await flush();

  const after = timeText();
  const n = Number(after);
  // Time.now under wasm returns real epoch-millis; a synchronous perform must
  // have dispatched GotTime(Ok millis) and re-rendered by now.
  check('after Fetch: model updated from Cmd.perform (Time.now) result',
    Number.isFinite(n) && n > 1000000000000, `time=${after}`);
  check('after Fetch: not the error branch (-1)', after !== '-1', `time=${after}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa Cmd.perform (sync Time.now task) verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
