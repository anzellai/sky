// Headless verification of the EXPLICIT TYPED SERVER BOUNDARY (P4): the REAL
// Sky.Spa client wasm fetches the REAL stateless Sky.Http.Server backend
// (localhost:8942) with `Spa.getJson widgetCodec …` and decodes the response
// with the SHARED `widgetCodec` — the exact codec the backend encoded with.
//
// This is a genuine round-trip: no fetch stub. Node's global fetch carries the
// wasm's `globalThis.fetch` to the backend, whose JSON was produced by
// `Codec.toJson widgetCodec` from the same symlinked Shared.sky. If the two
// codecs disagreed on the wire shape, the decode would fail and state=failed.
//
// PREREQ: the backend must be running (../server/sky-out/app on :8942). The
// runner script starts it; run standalone with the server already up.
require('./wasm_exec.js');
const fs = require('fs');

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.attrs = {}; this.listeners = {}; this._text = null; this.parent = null; this._value = ''; }
  get tagName() { return this.tag.startsWith('#') ? this.tag : this.tag.toUpperCase(); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(e, f) { (this.listeners[e] = this.listeners[e] || []).push(f); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  set innerHTML(v) { if (v === '') { this.children.forEach(c => (c.parent = null)); this.children = []; } }
  get innerHTML() { return ''; }
  set textContent(v) { this._text = v; this.children.forEach(c => (c.parent = null)); this.children = []; }
  get textContent() { return this._text !== null ? this._text : this.children.map(c => c.textContent).join(''); }
  get value() { return this._value; } set value(v) { this._value = String(v); }
  get parentNode() { return this.parent; }
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
  addEventListener: () => {},
};

const byId = id => walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null;
const txt = id => { const p = byId(id); return p ? p.textContent : '<none>'; };
// A network round-trip is several macrotask turns; drain generously.
const settle = async () => { for (let i = 0; i < 40; i++) await new Promise(r => setTimeout(r, 5)); };

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);
  await settle();

  const steps = [];
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);

  check('initial state=idle', txt('state') === 'idle', `state=${txt('state')}`);

  byId('load').click();
  await settle();

  check('round-trip: state=loaded (real backend decoded)', txt('state') === 'loaded', `state=${txt('state')}`);
  check('shared codec: name=sprocket', txt('name') === 'sprocket', `name=${txt('name')}`);
  check('shared codec: qty=42 (Int decoded)', txt('qty') === '42', `qty=${txt('qty')}`);
  check('shared codec: active=true (Bool decoded)', txt('active') === 'true', `active=${txt('active')}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa explicit boundary: real Sky.Spa wasm client fetched the real stateless Sky.Http.Server backend and decoded its Widget with the SHARED codec (both sides, one Shared.sky).`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
