// Headless verification of the REAL Sky-emitted Sky.Spa app's subscription
// path with a FAKE timer. `subscriptions model = Sub.every 1000 Tick` (while
// ticking); the client driver must register a browser setInterval, dispatch
// Tick each interval (incrementing the model + re-rendering via the P2 diff),
// and — when Stop flips `ticking` off — reconcile the subscription away so the
// interval is cleared and no further ticks fire. ZERO server.
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

// ── fake timers ──────────────────────────────────────────────────────────
// Installed before go.run() so the wasm driver's js.Global().Call("setInterval")
// / clearInterval hit these. tickAll() fires each registered interval once,
// snapshotting first so a tick that clears a timer (Stop) is safe mid-iteration.
let timerSeq = 1;
const timers = new Map();
globalThis.setInterval = (fn, ms) => { const id = timerSeq++; timers.set(id, { fn, ms }); return id; };
globalThis.clearInterval = (id) => { timers.delete(id); };
const timerCount = () => timers.size;
const anyIntervalMs = () => { for (const t of timers.values()) return t.ms; return null; };
function tickAll() { for (const { fn } of [...timers.values()]) fn(); }

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
const nText = () => { const p = byId('n'); return p ? p.textContent : '<none>'; };
const flush = () => new Promise(r => setImmediate(r));

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);
  await flush();

  const steps = [];
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);

  check('initial render is 0', nText() === '0', `got ${nText()}`);
  check('subscription registered ONE interval at startup', timerCount() === 1, `count=${timerCount()}`);
  check('interval is 1000ms (Sub.every 1000)', anyIntervalMs() === 1000, `ms=${anyIntervalMs()}`);

  tickAll(); await flush();
  check('after tick 1: model=1, view re-rendered', nText() === '1', `got ${nText()}`);
  tickAll(); await flush();
  tickAll(); await flush();
  check('after tick 3: model=3', nText() === '3', `got ${nText()}`);
  check('still exactly ONE interval (unchanged sub left running)', timerCount() === 1, `count=${timerCount()}`);

  byId('stop').click(); await flush();
  check('after Stop: subscription reconciled away (interval cleared)', timerCount() === 0, `count=${timerCount()}`);

  tickAll(); await flush(); // no timers → nothing dispatched
  check('removed subscription stops firing (model still 3)', nText() === '3', `got ${nText()}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa Sub.every timer subscription (start/tick/stop) verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
