// Headless verification of the REAL Sky-emitted Sky.Spa CLIENT-SIDE ROUTING
// (P4), zero server. A DOM + History-API shim drives the actual wasm driver:
//   - initial URL "/" renders Home (deep-link resolution at mount);
//   - a pure client-side counter proves navigation does NOT reload (count
//     survives a route change);
//   - clicking an internal <a href="/about"> is intercepted → pushState +
//     in-app nav (no page load), location.pathname updates, onNavigate fires;
//   - popstate (Back) returns to Home with client state intact;
//   - an unknown route resolves to the notFound page;
//   - an EXTERNAL link is NOT intercepted (defaultPrevented stays false).
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

const LISTENERS = { click: [], popstate: [] };

class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.attrs = {}; this.listeners = {};
    this._text = null; this.parent = null; this._value = '';
  }
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
  // An <a>'s resolved origin/pathname, as a browser would compute them.
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
    (this.listeners['click'] || []).forEach(f => f.call(this, ev)); // own (button onClick)
    LISTENERS.click.forEach(f => f.call(globalThis.document, ev));  // bubble to document (router)
    return ev;
  }
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

const byId = id => walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null;
const txt = id => { const p = byId(id); return p ? p.textContent : '<none>'; };
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setImmediate(r)); };
const popTo = async (p) => { globalThis.location.pathname = p; LISTENERS.popstate.forEach(f => f()); await settle(); };

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);
  await settle();

  const steps = [];
  const check = (label, ok, detail) => steps.push(`${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? ': ' + detail : ''}`);

  // ── deep-link at mount ────────────────────────────────────────────────
  check('mount: initial URL "/" renders Home', txt('page') === 'home', `page=${txt('page')}`);
  const navsAtMount = Number(txt('navs'));
  check('mount: onNavigate fired once (navs=1)', navsAtMount === 1, `navs=${txt('navs')}`);

  // ── pure client-side state (no server) ────────────────────────────────
  byId('inc').click(); byId('inc').click(); await settle();
  check('client-side counter works (count=2)', txt('count') === '2', `count=${txt('count')}`);

  // ── intercepted internal nav (NO page reload) ─────────────────────────
  const evAbout = byId('to-about').click(); await settle();
  check('nav: internal link intercepted (defaultPrevented)', evAbout.defaultPrevented === true);
  check('nav: view updated to About WITHOUT reload', txt('page') === 'about', `page=${txt('page')}`);
  check('nav: no reload — client count preserved (2)', txt('count') === '2', `count=${txt('count')}`);
  check('nav: URL updated via pushState (/about)', globalThis.location.pathname === '/about', `pathname=${globalThis.location.pathname}`);
  check('nav: onNavigate fired again (navs=2)', Number(txt('navs')) === 2, `navs=${txt('navs')}`);

  // ── external link is NOT intercepted ──────────────────────────────────
  const evExt = byId('to-ext').click(); await settle();
  check('external link NOT intercepted (defaultPrevented=false)', evExt.defaultPrevented === false);
  check('external link did not change the in-app page', txt('page') === 'about', `page=${txt('page')}`);

  // ── Back/Forward (popstate) ───────────────────────────────────────────
  await popTo('/');
  check('back (popstate): page=Home', txt('page') === 'home', `page=${txt('page')}`);
  check('back: client count still preserved (2)', txt('count') === '2', `count=${txt('count')}`);

  // ── unknown route → notFound ──────────────────────────────────────────
  await popTo('/does-not-exist');
  check('unknown route → notFound page', txt('page') === 'notfound', `page=${txt('page')}`);

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa client-side routing (deep-link, intercepted nav, no-reload, popstate, notFound, external passthrough) verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
