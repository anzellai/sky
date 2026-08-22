// Headless acceptance test for the Sky.Spa CLIENT-SIDE DIFF renderer.
//
// Loads the REAL Sky-emitted wasm, shims a DOM rich enough to model focus,
// caret/selection, and querySelector-by-sky-id, then drives keystrokes and
// asserts the diff renderer:
//   1. updates the derived elements on every keystroke (diff applied, correct),
//   2. keeps the focused input FOCUSED across the re-render,
//   3. preserves the caret/selection position,
//   4. never clobbers the input's value with stale data,
//   5. produces a MINIMAL patch set — the input DOM node identity is stable
//      (not recreated) and no new elements are created during a keystroke,
// and, for a PROGRAMMATIC value change (the Uppercase button) that DOES reach
// the focused input as a value patch, that the value updates while the caret is
// preserved. Zero server.
require('./wasm_exec.js'); // defines globalThis.Go
const fs = require('fs');

let created = 0; // counts createElement calls — a full rebuild would spike this

class El {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this._text = null;
    this.parent = null;
    this._value = '';
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.scrollTop = 0;
    this.checked = false;
    this.disabled = false;
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
  // Browser semantics: assigning .value moves the caret to the end. The runtime
  // saves + restores the selection around a value write; this models what it is
  // defending against.
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
  createElement: t => { created++; return new El(t); },
  createTextNode: t => { created++; const e = new El('#text'); e._text = t; return e; },
  getElementById: id => (id === 'app' ? root : walk(root, n => n.getAttribute && n.getAttribute('id') === id)[0] || null),
  querySelector: sel => { const id = parseSkyId(sel); if (id === null) return null; return walk(root, n => n.getAttribute && n.getAttribute('sky-id') === id)[0] || null; },
  body: root,
};

// ── driving + assertions ────────────────────────────────────────────
const flush = () => new Promise(r => setImmediate(r));
const byId = id => walk(root, n => n.tag !== '#text' && n.getAttribute && n.getAttribute('id') === id)[0];
const steps = [];
const ok = (cond, label) => steps.push(`${cond ? 'PASS' : 'FAIL'}  ${label}`);

function focus(el) { globalThis.document.activeElement = el; }
function fireInput(el) { (el.listeners['input'] || []).forEach(f => f.call(el, { target: el, currentTarget: el, type: 'input' })); }
// Insert `ch` at the current caret (models a real keystroke: DOM value updates,
// caret advances, THEN the input event fires).
function typeChar(el, ch) {
  const pos = el.selectionStart;
  el._value = el._value.slice(0, pos) + ch + el._value.slice(el.selectionEnd);
  el.selectionStart = el.selectionEnd = pos + ch.length;
  fireInput(el);
}

const go = new globalThis.Go();
WebAssembly.instantiate(fs.readFileSync('./main.wasm'), go.importObject).then(async res => {
  go.run(res.instance);          // main(): initial full mount, then parks at select{}
  await flush();

  const input = byId('field');
  ok(!!input && input.tagName === 'INPUT', 'initial mount: input present');
  ok(byId('count').textContent === '0', 'initial count is 0');

  const inputRef = input;        // capture node identity for the stability check

  // ── type "hello" at the end ──
  focus(input);
  for (const ch of 'hello') { const before = created; typeChar(input, ch); await flush();
    ok(created === before, `keystroke '${ch}': no elements created (minimal patch, no rebuild)`);
  }
  ok(byId('count').textContent === '5', 'derived count updated to 5 (diff applied)');
  ok(byId('echo').textContent === 'You typed: hello', 'derived echo updated (diff applied)');
  ok(globalThis.document.activeElement === input, 'input retained focus across re-renders');
  ok(byId('field') === inputRef, 'input DOM node identity stable (not rebuilt)');
  ok(input.value === 'hello', 'input value not clobbered');
  ok(input.selectionStart === 5, 'caret at end after appending');

  // ── caret preservation: type in the MIDDLE ──
  input.selectionStart = input.selectionEnd = 2; // between 'e' and 'l'
  const beforeMid = created;
  typeChar(input, 'X');          // -> "heXllo", caret 3
  await flush();
  ok(created === beforeMid, 'mid-string keystroke: still no elements created');
  ok(input.value === 'heXllo', 'mid-string value correct + not clobbered');
  ok(input.selectionStart === 3 && input.selectionEnd === 3, 'caret preserved mid-string (not reset to end)');
  ok(byId('count').textContent === '6', 'derived count updated to 6');
  ok(globalThis.document.activeElement === input, 'focus retained after mid-string edit');
  ok(byId('field') === inputRef, 'input node identity still stable');

  // ── PROGRAMMATIC value patch to the focused input (Uppercase button) ──
  // Model != DOM, so a value patch DOES reach the focused input; it must apply
  // AND keep the caret.
  input.selectionStart = input.selectionEnd = 3; // caret inside the word
  byId('upper').click();
  await flush();
  ok(input.value === 'HEXLLO', 'programmatic value patch applied to focused input');
  ok(input.selectionStart === 3, 'caret preserved across programmatic value write');
  ok(globalThis.document.activeElement === input, 'focus retained across programmatic write');
  ok(byId('count').textContent === '6', 'count unchanged (length same)');
  ok(byId('field') === inputRef, 'input node identity stable across value patch');

  console.log(steps.join('\n'));
  const failed = steps.filter(s => s.startsWith('FAIL')).length;
  console.log(`\n${failed === 0 ? 'ALL PASS' : failed + ' FAILED'} — Sky.Spa client-side diff renderer: focus + caret + minimal-patch verified headlessly in wasm, no server.`);
  process.exit(failed === 0 ? 0 : 1);
}).catch(e => { console.error('wasm error:', e); process.exit(2); });
