// Headless verification of the Sky.Spa spike: run the Go->wasm TEA loop in Node
// with a minimal DOM shim, then drive the event handlers and assert the Model
// transitions render correctly. Proves: wasm instantiates, syscall/js interop
// works, the Element->DOM renderer builds the tree, and pure `update` +
// re-render round-trips on each dispatched Msg — all with ZERO server.
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
  click() { (this.listeners['click'] || []).forEach(f => f.call(this, {})); }
}
const root = new El('#root');
globalThis.document = {
  createElement: t => new El(t),
  createTextNode: t => { const e = new El('#text'); e._text = t; return e; },
  getElementById: () => root,
};

// walk helpers over the shim tree
function findAll(node, pred, acc = []) { if (pred(node)) acc.push(node); node.children.forEach(c => findAll(c, pred, acc)); return acc; }
function countText() {
  // the count is the <p> with tabular-nums styling
  const p = findAll(root, n => n.tag === 'p' && (n.attrs.style || '').includes('tabular-nums'))[0];
  return p ? p.textContent : '<none>';
}
function button(label) {
  return findAll(root, n => n.tag === 'button' && n.textContent === label)[0];
}

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);                 // runs main(); initial render happens, then parks at select{}
  await new Promise(r => setImmediate(r)); // flush the Go scheduler

  const steps = [];
  const check = (label, want) => { const got = countText(); steps.push(`${got === want ? 'PASS' : 'FAIL'}  ${label}: expected ${want}, got ${got}`); };

  check('initial render', '0');
  button('+1').click(); await new Promise(r => setImmediate(r));
  button('+1').click(); await new Promise(r => setImmediate(r));
  button('+1').click(); await new Promise(r => setImmediate(r));
  check('after +1 x3 (pure client-local update)', '3');
  button('Reset').click(); await new Promise(r => setImmediate(r));
  check('after Reset', '0');
  button('−1').click(); await new Promise(r => setImmediate(r)); // "−1"
  check('after -1', '-1');

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — client TEA loop + Element->DOM renderer verified headlessly, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
