// Headless verification of the REAL Sky-emitted Sky.Spa app running as wasm.
// Loads wasm_exec.js, shims a minimal DOM, instantiates the emitted main.wasm,
// then drives the event handlers and asserts the Model transitions render
// correctly — proving wasm instantiation, syscall/js interop, the runtime's
// Element->VNode->DOM renderer, and pure `update` + re-render per Msg, with
// ZERO server. This is the real emit path, not a hand-written spike.
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.attrs = {}; this.listeners = {}; this._text = null; }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(e, f) { (this.listeners[e] = this.listeners[e] || []).push(f); }
  appendChild(c) { this.children.push(c); return c; }
  set innerHTML(v) { if (v === '') this.children = []; }
  get innerHTML() { return ''; }
  set textContent(v) { this._text = v; this.children = []; }
  get textContent() { return this._text !== null ? this._text : this.children.map(c => c.textContent).join(''); }
  click() { (this.listeners['click'] || []).forEach(f => f.call(this, { target: this })); }
}
const root = new El('#app');
globalThis.document = {
  createElement: t => new El(t),
  createTextNode: t => { const e = new El('#text'); e._text = t; return e; },
  getElementById: () => root,
  body: root,
};

function findAll(node, pred, acc = []) { if (pred(node)) acc.push(node); node.children.forEach(c => findAll(c, pred, acc)); return acc; }
function countText() {
  const p = findAll(root, n => n.tag === 'p')[0];
  return p ? p.textContent : '<none>';
}
function button(label) {
  return findAll(root, n => n.tag === 'button' && n.textContent === label)[0];
}

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
  check('after +1 x3 (pure client-local update)', '3');
  button('Reset').click(); await new Promise(r => setImmediate(r));
  check('after Reset', '0');
  button('-1').click(); await new Promise(r => setImmediate(r));
  check('after -1', '-1');

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — REAL Sky.Spa app: client TEA loop + Element->DOM renderer verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
