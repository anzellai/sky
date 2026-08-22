// Headless FULL-LOOP acceptance for the Sky.Spa Todos app: the REAL Sky-emitted
// wasm client driven against the REAL stateless Sky.Http.Server backend (SQLite
// store), proving the entire v1 stack end to end with ZERO stubbing of the wire.
//
// A DOM + History-API shim drives the actual wasm TEA driver; a thin fetch shim
// resolves the client's relative "/api/..." URLs against the running backend and
// COUNTS every network call, so the "pure UI = zero round-trip" claim is a
// measured assertion, not a hope. The runner ALSO talks to the backend directly
// (Node fetch) to prove durable persistence independently of the DOM.
//
// PREREQ: the backend must be running (../server/sky-out/app). TODOS_BASE gives
// its origin (default http://localhost:8951). The run_roundtrip.sh script builds
// everything, starts the backend on a clean DB, and invokes this file.
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

const BASE = process.env.TODOS_BASE || 'http://localhost:8951';
const WASM = fs.readFileSync('./main.wasm');

// ── fetch shim: resolve relative URLs to the backend + count calls ──────────
const realFetch = globalThis.fetch;
let fetchCount = 0;
globalThis.fetch = (url, opts) => {
  fetchCount++;
  const u = typeof url === 'string' && url.startsWith('/') ? BASE + url : url;
  return realFetch(u, opts);
};

// ── resettable DOM + History harness (a fresh one models a page reload) ─────
let root, LISTENERS;
class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.attrs = {}; this.listeners = {};
    this._text = null; this.parent = null; this._value = '';
    this.selectionStart = 0; this.selectionEnd = 0;
  }
  get tagName() { return this.tag.startsWith('#') ? this.tag : this.tag.toUpperCase(); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'value') this._value = String(v); }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(e, f) { (this.listeners[e] = this.listeners[e] || []).push(f); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  set innerHTML(v) { if (v === '') { this.children.forEach(c => (c.parent = null)); this.children = []; } }
  get innerHTML() { return ''; }
  set textContent(v) { this._text = v; this.children.forEach(c => (c.parent = null)); this.children = []; }
  get textContent() { return this._text !== null ? this._text : this.children.map(c => c.textContent).join(''); }
  get value() { return this._value; }
  set value(v) { this._value = String(v); this.selectionStart = this.selectionEnd = this._value.length; }
  get parentNode() { return this.parent; }
  get origin() {
    if (this.tagName !== 'A') return undefined;
    const h = this.getAttribute('href') || '';
    const m = /^https?:\/\/[^/]+/.exec(h);
    return m ? m[0] : 'http://localhost';
  }
  get pathname() {
    if (this.tagName !== 'A') return undefined;
    let h = this.getAttribute('href') || '/';
    h = h.replace(/^https?:\/\/[^/]+/, '');
    return (h.split('#')[0].split('?')[0]) || '/';
  }
  contains(node) { if (node === this) return true; return this.children.some(c => c.contains && c.contains(node)); }
  remove() { if (this.parent) { const i = this.parent.children.indexOf(this); if (i >= 0) this.parent.children.splice(i, 1); } }
  click(opts = {}) {
    const ev = {
      target: this, currentTarget: this, type: 'click',
      button: opts.button || 0,
      metaKey: !!opts.metaKey, ctrlKey: !!opts.ctrlKey, shiftKey: !!opts.shiftKey, altKey: !!opts.altKey,
      defaultPrevented: false, preventDefault() { this.defaultPrevented = true; },
    };
    (this.listeners['click'] || []).forEach(f => f.call(this, ev));
    LISTENERS.click.forEach(f => f.call(globalThis.document, ev));
    return ev;
  }
}
function walk(node, pred, acc = []) { if (pred(node)) acc.push(node); node.children.forEach(c => walk(c, pred, acc)); return acc; }
function parseSkyId(sel) { const m = /^\[sky-id="([^"]*)"\]$/.exec(sel); return m ? m[1].replace(/\\"/g, '"') : null; }

function makeDom() {
  root = new El('#app');
  LISTENERS = { click: [], popstate: [] };
  globalThis.document = {
    activeElement: null,
    createElement: t => new El(t),
    createTextNode: t => { const e = new El('#text'); e._text = t; return e; },
    getElementById: id => (id === 'app' ? root : walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null),
    querySelector: sel => { const id = parseSkyId(sel); if (id === null) return null; return walk(root, n => n.getAttribute && n.getAttribute('sky-id') === id)[0] || null; },
    body: root,
    addEventListener: (e, f) => { (LISTENERS[e] = LISTENERS[e] || []).push(f); },
  };
  globalThis.location = { origin: 'http://localhost', pathname: '/' };
  globalThis.history = {
    pushState: (s, t, url) => {
      let u = String(url).replace(/^https?:\/\/[^/]+/, '');
      globalThis.location.pathname = (u.split('#')[0].split('?')[0]) || '/';
    },
  };
  globalThis.addEventListener = (e, f) => { (LISTENERS[e] = LISTENERS[e] || []).push(f); };
}

const byId = id => walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null;
const txt = id => { const p = byId(id); return p ? p.textContent : '<none>'; };
const titleEls = () => walk(root, n => n.getAttribute && /^title-/.test(n.getAttribute('id') || ''));
const titles = () => titleEls().map(e => e.textContent);
const idOfTitle = (title) => { const e = titleEls().find(e => e.textContent === title); return e ? Number(e.getAttribute('id').replace('title-', '')) : null; };
const settle = async () => { for (let i = 0; i < 60; i++) await new Promise(r => setTimeout(r, 5)); };
const fireInput = (el, v) => { el.value = v; (el.listeners['input'] || []).forEach(f => f.call(el, { target: el, currentTarget: el, type: 'input' })); };
const popTo = async (p) => { globalThis.location.pathname = p; LISTENERS.popstate.forEach(f => f()); await settle(); };

// backend truth, read directly (independent of the DOM)
const backendTitles = async () => (await (await realFetch(BASE + '/api/todos')).json()).map(t => t.title);
const backendDoneOf = async (title) => { const rows = await (await realFetch(BASE + '/api/todos')).json(); const r = rows.find(t => t.title === title); return r ? r.done : null; };

async function runInstance() {
  makeDom();
  const go = new globalThis.Go();
  const res = await WebAssembly.instantiate(WASM, go.importObject);
  go.run(res.instance);
  await settle();
}

(async () => {
  const steps = [];
  globalThis.__steps = steps;
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);
  const uniq = 'buy-milk-' + Date.now();

  // ── 1. load-on-start: client fetches the list from the backend ──────────
  await runInstance();
  check('load-on-start: status=ready', txt('status') === 'ready', `status=${txt('status')}`);
  check('load-on-start: at least one network call happened', fetchCount >= 1, `fetchCount=${fetchCount}`);
  const startTotal = Number(txt('total'));

  // ── 2. ADD round-trips + persists ───────────────────────────────────────
  fireInput(byId('new-input'), uniq);
  const beforeAdd = fetchCount;
  byId('add-btn').click();
  await settle();
  check('add: POST fired (fetchCount grew)', fetchCount > beforeAdd, `+${fetchCount - beforeAdd}`);
  check('add: new todo rendered in the DOM', titles().includes(uniq), `titles=${JSON.stringify(titles())}`);
  check('add: total incremented', Number(txt('total')) === startTotal + 1, `total=${txt('total')}`);
  const be1 = await backendTitles();
  check('add: PERSISTED in backend store (curl truth)', be1.includes(uniq), `backend=${JSON.stringify(be1)}`);

  // ── 3. ZERO-ROUND-TRIP: pure UI (typing + filter nav) makes NO fetch ────
  const beforePureUi = fetchCount;
  fireInput(byId('new-input'), 'draft text not submitted');
  fireInput(byId('new-input'), 'more typing');
  const evActive = byId('f-active').click(); await settle();
  const evCompleted = byId('f-completed').click(); await settle();
  const evAll = byId('f-all').click(); await settle();
  check('zero-round-trip: typing + 3 filter navs made NO network call', fetchCount === beforePureUi, `delta=${fetchCount - beforePureUi}`);
  check('routing: filter nav intercepted (no reload)', evActive.defaultPrevented === true && evAll.defaultPrevented === true);

  // ── 4. ROUTING changes the view without a reload ────────────────────────
  await popTo('/active');
  check('routing: /active sets filter=active', txt('filter') === 'active', `filter=${txt('filter')}`);
  const activeCount = Number(txt('count'));
  await popTo('/completed');
  check('routing: /completed sets filter=completed', txt('filter') === 'completed', `filter=${txt('filter')}`);
  await popTo('/');
  check('routing: back to / sets filter=all', txt('filter') === 'all', `filter=${txt('filter')}`);
  check('routing: no reload — our new todo still cached client-side', titles().includes(uniq));

  // ── 5. TOGGLE round-trips + persists ────────────────────────────────────
  const tid = idOfTitle(uniq);
  check('toggle: found server-assigned id for our todo', tid !== null, `id=${tid}`);
  const beforeToggle = fetchCount;
  byId('toggle-' + tid).click();
  await settle();
  check('toggle: POST fired', fetchCount > beforeToggle);
  check('toggle: DOM shows done [x]', txt('toggle-' + tid) === '[x]', `toggle=${txt('toggle-' + tid)}`);
  check('toggle: PERSISTED done=true in backend', (await backendDoneOf(uniq)) === true);

  // ── 6. RENAME (edit-in-place: pure UI buffer, then a round-trip) ─────────
  const renamed = uniq + '-renamed';
  const beforeEditOpen = fetchCount;
  byId('title-' + tid).click();       // StartEdit — pure UI
  await settle();
  fireInput(byId('edit-input'), renamed); // typing in the edit buffer — pure UI
  check('rename: opening + typing the edit buffer made NO network call', fetchCount === beforeEditOpen, `delta=${fetchCount - beforeEditOpen}`);
  byId('edit-save').click();          // CommitEdit — round-trip
  await settle();
  check('rename: DOM shows the new title', titles().includes(renamed), `titles=${JSON.stringify(titles())}`);
  check('rename: PERSISTED in backend', (await backendTitles()).includes(renamed));

  // ── 7. DELETE round-trips + persists ────────────────────────────────────
  const beforeDelete = fetchCount;
  byId('del-' + tid).click();
  await settle();
  check('delete: POST fired', fetchCount > beforeDelete);
  check('delete: gone from the DOM', !titles().includes(renamed));
  check('delete: gone from backend', !(await backendTitles()).includes(renamed));

  // ── 8. RELOAD: a fresh client instance loads state from the backend ─────
  // Re-add one, then boot a brand-new wasm instance with a fresh DOM.
  const persist = 'persist-' + Date.now();
  fireInput(byId('new-input'), persist);
  byId('add-btn').click();
  await settle();
  fetchCount = 0;
  await runInstance();               // models the user reloading the page
  check('reload: fresh client loaded todos from backend', titles().includes(persist), `titles=${JSON.stringify(titles())}`);
  check('reload: the load came over the network', fetchCount >= 1, `fetchCount=${fetchCount}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa Todos full loop: real wasm client + real stateless Sky backend (SQLite). Durable add/toggle/rename/delete round-trip and persist; pure UI (typing, filter routing, edit buffer) is provably zero-network; a reloaded client rehydrates from the backend.`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error('runner error:', e); console.error('STEPS SO FAR:\n' + (globalThis.__steps || []).join('\n')); process.exit(2); });
