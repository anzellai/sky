// Headless verification of the REAL Sky-emitted Sky.Spa app's ASYNC Cmd.perform
// / Http path with a STUBBED fetch. Clicking "Load" returns
// `Cmd.perform (Http.get "/api") GotResp`; on the client Http.get calls
// globalThis.fetch and returns a Promise the interpreter resolves via
// .then/.catch, dispatching GotResp(Ok resp) on success and GotResp(Err _) on
// rejection. We drive BOTH: a resolving stub (assert decoded body + status) and
// a rejecting stub (assert the Err branch fires — no silent drop). ZERO server.
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
const txt = id => { const p = byId(id); return p ? p.textContent : '<none>'; };
// The async chain (fetch → resp.text() → then → dispatch) is several microtask
// turns; drain generously.
const settle = async () => { for (let i = 0; i < 10; i++) await new Promise(r => setImmediate(r)); };

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);
  await settle();

  const steps = [];
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);

  check('initial render (state=init)', txt('state') === 'init', `state=${txt('state')}`);

  // ── success ──────────────────────────────────────────────────────────
  let fetchCalledWith = null;
  globalThis.fetch = (url, opts) => {
    fetchCalledWith = { url, method: opts && opts.method };
    return Promise.resolve({ status: 200, text: () => Promise.resolve('hello-body') });
  };
  byId('load').click();
  await settle();
  check('success: fetch was called with the url', fetchCalledWith && fetchCalledWith.url === '/api', `url=${fetchCalledWith && fetchCalledWith.url}`);
  check('success: Ok branch dispatched (state=ok)', txt('state') === 'ok', `state=${txt('state')}`);
  check('success: status decoded (200)', txt('status') === '200', `status=${txt('status')}`);
  check('success: body decoded (hello-body)', txt('body') === 'hello-body', `body=${txt('body')}`);

  // ── failure ──────────────────────────────────────────────────────────
  globalThis.fetch = () => Promise.reject(new Error('network down'));
  byId('load').click();
  await settle();
  check('failure: Err branch dispatched (state=err), not dropped', txt('state') === 'err', `state=${txt('state')}`);
  check('failure: status set to -1 by error branch', txt('status') === '-1', `status=${txt('status')}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa async Cmd.perform + Http.get (fetch, success + failure) verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
