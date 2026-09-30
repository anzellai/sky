package rt

// terminalWidgetJS is the built-in "sky-terminal" widget island that
// Std.Ui.Terminal binds to a PTY process. It is a renderer only: the
// terminal is emulated on the server (term_screen.go), and the widget
// applies the screen-diff frames the server sends (term_frame.go has the
// op list) to its copy of the grid and draws it on a <canvas>.
//
// It rides in the same same-origin client files as the island runtime (it
// must come right after islandClientJS, which defines window.Sky.island), so a
// strict Content-Security-Policy (script-src 'self') runs it and no widget
// file has to be loaded. The Rust build reads this literal as a raw string, so
// it must hold no backquote. It sets styles through the CSSOM and text through
// textContent only (no innerHTML, no style attribute, no eval); the selection
// colour is a constructed stylesheet (adoptedStyleSheets), which no
// style-src policy blocks.
//
// Drawing. A frame marks the rows it changed; one draw pass per animation
// frame repaints only those rows, each as one clear, one fillRect per run of
// equal background, one fillText per run of equal colour and font (ASCII
// cells; a wide or non-ASCII character is drawn in its own cell), and the
// underline / strike rules. Frames that arrive between two paints cost no
// drawing of their own. Without a 2D canvas (an old browser, a test DOM)
// the text layer below is shown instead, without colours.
//
// Text layer. Over the canvas lies one transparent text row per screen
// row, in the same font and row height: it is what a screen reader reads,
// what a mouse selects and what copy (Cmd+C, or Ctrl+Shift+C with a
// selection) copies. A polite live region announces the rows a frame
// changed, at most once a second.
//
// Scrollback. The widget keeps the server's scrollback (1000 lines); the
// mouse wheel scrolls through it (not on the alternate screen), and a key
// press goes back to the bottom.
//
// The protocol (island name "sky-terminal"):
//
//	props          null, or {"label": String} (the aria-label; default
//	               "Terminal")
//
//	widget -> app  (Std.Ui.onIslandEvent)
//	  "resize"     {"cols": Int, "rows": Int}: the measured size, sent on
//	               mount and whenever it changes (debounced 50 ms)
//	  "ready"      {}: sent once after the first "resize" on every mount, and
//	               again when a frame does not apply on top of the last one
//	               (a frame was lost on the way): the app answers with a
//	               repaint (a frame with base -1)
//	  "input"      {"data": String}: keystrokes and pastes, batched for up to
//	               10 ms into one event (a paste in bracketed-paste mode is
//	               wrapped in ESC [200~ ... ESC [201~)
//
//	app -> widget  (Cmd.toIsland id name payload)
//	  "frame"      a screen-diff frame (term_frame.go)
//
//	attributes the widget sets on the island element: tabindex 0, role
//	"application", aria-label, data-term-ready "1" (after mount),
//	data-term-cols and data-term-rows (the measured size, after every
//	resize), data-term-renderer ("canvas" or "text"), data-term-title (the
//	OSC title) and data-term-bell (the bell count). They are not in the
//	server's sky-* / data-sky-* namespace, so an HTML swap that adopts the
//	element keeps them.
//
// The model, the line decoder and the key mapping are exposed for tests as
// window.Sky.__term.
const terminalWidgetJS = `// Sky terminal widget (runtime-go/rt/island_terminal.go): the "sky-terminal" island.
(function () {
  "use strict";
  var w = typeof window !== "undefined" ? window : this;
  var Sky = w.Sky = w.Sky || {};
  if (!Sky.island || Sky.__terminal) return;
  Sky.__terminal = true;
  var SCROLLBACK = 1000;
  var FONT = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";
  var DEF_FG = "#d4d4d4", DEF_BG = "#1e1e1e";
  var BASIC = ["#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
    "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"];
  function hex2(n) {
    n = Math.max(0, Math.min(255, n | 0));
    return (n < 16 ? "0" : "") + n.toString(16);
  }
  function rgb(r, g, b) { return "#" + hex2(r) + hex2(g) + hex2(b); }
  function palette(n) {
    n = n | 0;
    if (n < 16) return BASIC[n];
    if (n < 232) {
      n -= 16;
      var L = [0, 95, 135, 175, 215, 255];
      return rgb(L[Math.floor(n / 36)], L[Math.floor(n / 6) % 6], L[n % 6]);
    }
    var v = 8 + 10 * (n - 232);
    return rgb(v, v, v);
  }
  // colour: -1 the default, 0-255 the palette, 256 + 0xRRGGBB a true colour.
  function colour(v, dflt) {
    if (typeof v !== "number" || v < 0) return dflt;
    if (v < 256) return palette(v);
    v -= 256;
    return rgb((v >> 16) & 255, (v >> 8) & 255, v & 255);
  }
  function clamp(v, lo, hi) { return v < lo ? lo : (v > hi ? hi : v); }

  // ── The screen model: the frame ops, no DOM ──────────────────────
  var DEF = [-1, -1, 0];
  var BLANK = { t: " ", s: DEF, w: 1 };
  // decodeLine turns [style, text, ...] (from index i) into cells. A cell is
  // {t: text, s: [fg, bg, flags], w: 1 | 2 | 0 (the tail of a wide one)}.
  function decodeLine(styles, runs, i) {
    var out = [];
    for (i = i || 0; i + 1 < runs.length; i += 2) {
      var s = styles[runs[i]] || DEF, fl = s[2] | 0, text = String(runs[i + 1]);
      var wide = (fl & 64) !== 0, cw = wide ? 2 : 1;
      if (fl & 128) {
        out.push({ t: text, s: s, w: cw });
        if (wide) out.push({ t: "", s: s, w: 0 });
        continue;
      }
      for (var k = 0; k < text.length; k++) {
        var c = text.charAt(k), code = text.charCodeAt(k);
        if (code >= 0xD800 && code <= 0xDBFF && k + 1 < text.length) { c = text.substr(k, 2); k++; }
        out.push(c === " " && fl === 0 ? BLANK : { t: c, s: s, w: cw });
        if (wide) out.push({ t: "", s: s, w: 0 });
      }
    }
    return out;
  }
  function lineText(cells) {
    var s = "";
    for (var i = 0; i < cells.length; i++) if (cells[i].w !== 0) s += cells[i].t;
    return s.replace(/ +$/, "");
  }
  function Model() {
    this.cols = 0; this.rows = 0; this.grid = []; this.sb = []; this.seq = 0;
    this.cx = 0; this.cy = 0; this.cursorOn = true; this.title = ""; this.modes = 0; this.bells = 0;
    this.dirty = {}; this.all = true; this.pushed = 0;
  }
  Model.prototype.blankRow = function () {
    var r = [];
    for (var i = 0; i < this.cols; i++) r.push(BLANK);
    return r;
  };
  Model.prototype.mark = function (a, b) { for (var y = a; y <= b; y++) this.dirty[y] = true; };
  Model.prototype.push = function (l) { this.sb.push(l); this.pushed++; };
  // apply applies one frame; false when it does not apply on top of the
  // last one (a frame was lost): the caller asks for a repaint.
  Model.prototype.apply = function (f) {
    if (!f || typeof f.seq !== "number" || !f.ops) return true;
    if (f.base !== -1 && f.base !== this.seq) return false;
    this.seq = f.seq;
    var st = f.st || [], ops = f.ops, i, k, y;
    for (var o = 0; o < ops.length; o++) {
      var op = ops[o];
      switch (op[0]) {
        case "z":
          this.cols = clamp(op[1] | 0, 1, 1000);
          this.rows = clamp(op[2] | 0, 1, 1000);
          this.grid = [];
          for (y = 0; y < this.rows; y++) this.grid.push(this.blankRow());
          this.all = true;
          break;
        case "x":
          this.sb = [];
          this.all = true;
          break;
        case "p":
          for (i = 1; i < op.length; i++) this.push(decodeLine(st, op[i], 0));
          break;
        case "u": case "d":
          var top = op[1] | 0, bot = Math.min(op[2] | 0, this.rows - 1), n = op[3] | 0;
          if (top > bot) break;
          k = Math.min(n, bot - top + 1);
          if (op[0] === "u") {
            var p = op[4];
            if (p === 1) { for (i = 0; i < k; i++) this.push(this.grid[top + i]); }
            else if (p && p.length) { for (i = 0; i < p.length; i++) this.push(decodeLine(st, p[i], 0)); }
            this.grid.splice(top, k);
            for (i = 0; i < k; i++) this.grid.splice(bot - k + 1 + i, 0, this.blankRow());
          } else {
            this.grid.splice(bot - k + 1, k);
            for (i = 0; i < k; i++) this.grid.splice(top, 0, this.blankRow());
          }
          this.mark(top, bot);
          break;
        case "r":
          y = op[1] | 0;
          if (y < 0 || y >= this.rows) break;
          var x = op[2] | 0, cells = decodeLine(st, op, 3), row = this.grid[y].slice();
          for (i = x; i < this.cols; i++) row[i] = i - x < cells.length ? cells[i - x] : BLANK;
          this.grid[y] = row;
          this.dirty[y] = true;
          break;
        case "c":
          this.dirty[this.cy] = true;
          this.cx = op[1] | 0; this.cy = op[2] | 0; this.cursorOn = op[3] === 1;
          this.dirty[this.cy] = true;
          break;
        case "t": this.title = String(op[1]); break;
        case "m": this.modes = op[1] | 0; break;
        case "b": this.bells += op[1] | 0; break;
      }
    }
    if (this.sb.length > SCROLLBACK) this.sb.splice(0, this.sb.length - SCROLLBACK);
    return true;
  };
  Model.prototype.text = function () {
    var out = [];
    for (var y = 0; y < this.grid.length; y++) out.push(lineText(this.grid[y]));
    return out;
  };
  Model.prototype.scrollbackText = function () {
    var out = [];
    for (var y = 0; y < this.sb.length; y++) out.push(lineText(this.sb[y]));
    return out;
  };

  // ── Keys ─────────────────────────────────────────────────────────
  var NAMED = {
    Enter: "\r", Backspace: "\x7f", Tab: "\t", Escape: "\x1b",
    ArrowUp: "\x1b[A", ArrowDown: "\x1b[B", ArrowRight: "\x1b[C", ArrowLeft: "\x1b[D",
    Home: "\x1b[H", End: "\x1b[F", Delete: "\x1b[3~", Insert: "\x1b[2~",
    PageUp: "\x1b[5~", PageDown: "\x1b[6~",
    F1: "\x1bOP", F2: "\x1bOQ", F3: "\x1bOR", F4: "\x1bOS"
  };
  var APP = { ArrowUp: "\x1bOA", ArrowDown: "\x1bOB", ArrowRight: "\x1bOC", ArrowLeft: "\x1bOD", Home: "\x1bOH", End: "\x1bOF" };
  var MODIFIER = { Shift: 1, Control: 1, Alt: 1, AltGraph: 1, Meta: 1, OS: 1, CapsLock: 1,
    NumLock: 1, ScrollLock: 1, Fn: 1, Dead: 1, Unidentified: 1, Process: 1 };
  function single(k) {
    if (k.length === 1) return true;
    if (k.length !== 2) return false;
    var h = k.charCodeAt(0), l = k.charCodeAt(1);
    return h >= 0xD800 && h <= 0xDBFF && l >= 0xDC00 && l <= 0xDFFF;
  }
  // keyToSeq maps a keydown to the bytes a terminal sends; appCursor is the
  // cursor-keys mode (?1h) the program set.
  function keyToSeq(ev, appCursor) {
    if (!ev || typeof ev.key !== "string" || ev.key === "") return null;
    var k = ev.key, seq;
    if (ev.metaKey || MODIFIER[k]) return null;
    if (k === "Tab" && ev.shiftKey) seq = "\x1b[Z";
    else if (appCursor && !ev.ctrlKey && !ev.altKey && Object.prototype.hasOwnProperty.call(APP, k)) seq = APP[k];
    else if (Object.prototype.hasOwnProperty.call(NAMED, k)) seq = NAMED[k];
    else if (!single(k)) return null;
    else if (ev.ctrlKey) {
      var c = k.toLowerCase(), code = c.charCodeAt(0);
      if (code >= 97 && code <= 122) seq = String.fromCharCode(code - 96);
      else if (c === " " || c === "@" || c === "2") seq = "\x00";
      else if (c === "[" || c === "3") seq = "\x1b";
      else if (c === "\\" || c === "4") seq = "\x1c";
      else if (c === "]" || c === "5") seq = "\x1d";
      else if (c === "^" || c === "6") seq = "\x1e";
      else if (c === "_" || c === "-" || c === "7") seq = "\x1f";
      else if (c === "?" || c === "8") seq = "\x7f";
      else return null;
    } else seq = k;
    if (ev.altKey) seq = "\x1b" + seq;
    return seq;
  }

  Sky.__term = { Model: Model, decodeLine: decodeLine, lineText: lineText, keyToSeq: keyToSeq, colour: colour };

  // ── The island ───────────────────────────────────────────────────
  function labelOf(props) {
    return props && typeof props.label === "string" && props.label !== "" ? props.label : "Terminal";
  }
  var sheetDone = false;
  function selectionSheet() {
    if (sheetDone) return;
    sheetDone = true;
    try {
      var d = w.document, sh = new w.CSSStyleSheet();
      sh.replaceSync(".sky-term-text ::selection, .sky-term-text::selection { background: rgba(90, 140, 255, 0.45); color: transparent; }");
      d.adoptedStyleSheets = d.adoptedStyleSheets.concat([sh]);
    } catch (_) {}
  }
  function now() { return w.performance && w.performance.now ? w.performance.now() : Date.now(); }
  Sky.island("sky-terminal", {
    mount: function (el, props, send) {
      var self = this, d = w.document;
      self.el = el;
      self.send = send;
      self.dead = false;
      self.model = new Model();
      self.back = 0;
      self.gapAsked = false;
      self.q = "";
      self.qt = null;
      self.rsz = null;
      self.raf = null;
      self.texts = [];
      self.rowEls = [];
      self.lastSay = 0;
      self.say = null;
      self.bellT = null;
      self.passes = 0;
      el.tabIndex = 0;
      el.setAttribute("role", "application");
      el.setAttribute("aria-label", labelOf(props));
      var box = d.createElement("div"), s = box.style;
      s.position = "relative";
      s.fontFamily = FONT;
      s.overflow = "hidden";
      s.background = DEF_BG;
      s.color = DEF_FG;
      s.width = "100%";
      s.height = "100%";
      s.boxSizing = "border-box";
      el.appendChild(box);
      self.box = box;
      var cv = d.createElement("canvas"), ctx = null;
      try { ctx = cv.getContext ? cv.getContext("2d") : null; } catch (_) { ctx = null; }
      if (ctx) {
        cv.style.position = "absolute";
        cv.style.left = "0";
        cv.style.top = "0";
        cv.style.display = "block";
        box.appendChild(cv);
        self.canvas = cv;
        self.ctx = ctx;
      } else {
        self.canvas = null;
        self.ctx = null;
      }
      el.setAttribute("data-term-renderer", ctx ? "canvas" : "text");
      var tl = d.createElement("div"), ts = tl.style;
      tl.className = "sky-term-text";
      ts.position = "absolute";
      ts.left = "0";
      ts.top = "0";
      ts.width = "100%";
      ts.whiteSpace = "pre";
      ts.overflow = "hidden";
      ts.color = ctx ? "transparent" : DEF_FG;
      ts.userSelect = "text";
      ts.cursor = "text";
      box.appendChild(tl);
      self.textLayer = tl;
      var live = d.createElement("div"), ls = live.style;
      live.setAttribute("aria-live", "polite");
      live.setAttribute("aria-atomic", "false");
      ls.position = "absolute";
      ls.width = "1px";
      ls.height = "1px";
      ls.overflow = "hidden";
      ls.clip = "rect(0 0 0 0)";
      ls.whiteSpace = "pre";
      box.appendChild(live);
      self.live = live;
      if (ctx) selectionSheet();
      var sz = self.measure();
      self.size = sz;
      self.layout();
      self.onKey = function (ev) { self.key(ev); };
      self.onPaste = function (ev) { self.paste(ev); };
      self.onDown = function () { try { el.focus({ preventScroll: true }); } catch (_) { try { el.focus(); } catch (_) {} } };
      self.onFocus = function () { self.model.dirty[self.model.cy] = true; self.schedule(); };
      self.onWheel = function (ev) { self.wheel(ev); };
      el.addEventListener("keydown", self.onKey);
      el.addEventListener("paste", self.onPaste);
      el.addEventListener("mousedown", self.onDown);
      el.addEventListener("focus", self.onFocus);
      el.addEventListener("blur", self.onFocus);
      el.addEventListener("wheel", self.onWheel);
      self.onResize = function () { self.resized(); };
      if (typeof w.ResizeObserver === "function") {
        self.ro = new w.ResizeObserver(self.onResize);
        self.ro.observe(el);
      } else if (w.addEventListener) {
        self.ro = null;
        w.addEventListener("resize", self.onResize);
      }
      el.setAttribute("data-term-ready", "1");
      send("resize", { cols: sz[0], rows: sz[1] });
      send("ready", {});
      self.schedule();
    },
    // measure returns [cols, rows] for the box and sets the cell size.
    measure: function () {
      var el = this.el, d = w.document, probe = d.createElement("span"), ps = probe.style, r = null;
      probe.textContent = "MMMMMMMMMM";
      ps.position = "absolute";
      ps.visibility = "hidden";
      ps.whiteSpace = "pre";
      ps.lineHeight = "1.2";
      this.box.appendChild(probe);
      try { r = probe.getBoundingClientRect ? probe.getBoundingClientRect() : null; } catch (_) { r = null; }
      this.box.removeChild(probe);
      var cw = r ? r.width / 10 : 0, chh = r ? Math.round(r.height) : 0;
      var fs = 0;
      try { fs = parseFloat(w.getComputedStyle(this.box).fontSize) || 0; } catch (_) { fs = 0; }
      this.cw = cw > 0 ? cw : 8;
      this.ch = chh > 0 ? chh : 16;
      this.fontPx = fs > 0 ? fs : Math.round(this.ch / 1.2);
      this.W = el.clientWidth || 0;
      this.H = el.clientHeight || 0;
      if (!(cw > 0) || !(chh > 0) || !(this.W > 0) || !(this.H > 0)) return [80, 24];
      return [clamp(Math.floor(this.W / cw), 2, 500), clamp(Math.floor(this.H / chh), 2, 200)];
    },
    // layout sizes the canvas to the box (in device pixels) and the text rows
    // to the cell height, and repaints everything.
    layout: function () {
      var dpr = w.devicePixelRatio || 1, cv = this.canvas;
      if (cv) {
        cv.width = Math.max(1, Math.round(this.W * dpr));
        cv.height = Math.max(1, Math.round(this.H * dpr));
        cv.style.width = this.W + "px";
        cv.style.height = this.H + "px";
        this.dpr = dpr;
      }
      this.textLayer.style.lineHeight = this.ch + "px";
      this.el.setAttribute("data-term-cols", String(this.size[0]));
      this.el.setAttribute("data-term-rows", String(this.size[1]));
      this.model.all = true;
    },
    resized: function () {
      var self = this;
      if (self.dead) return;
      if (self.rsz !== null) clearTimeout(self.rsz);
      self.rsz = setTimeout(function () {
        self.rsz = null;
        if (self.dead) return;
        var W = self.W, H = self.H, sz = self.measure();
        if (sz[0] === self.size[0] && sz[1] === self.size[1] && W === self.W && H === self.H) return;
        var sent = sz[0] !== self.size[0] || sz[1] !== self.size[1];
        self.size = sz;
        self.layout();
        if (sent) self.send("resize", { cols: sz[0], rows: sz[1] });
        self.schedule();
      }, 50);
    },
    selected: function () {
      try {
        var sel = w.getSelection && w.getSelection();
        return !!(sel && !sel.isCollapsed && String(sel) !== "" && sel.anchorNode && this.textLayer.contains(sel.anchorNode));
      } catch (_) { return false; }
    },
    key: function (ev) {
      // Copy with a selection: Ctrl+Shift+C (Cmd+C never reaches keyToSeq).
      if (ev.ctrlKey && ev.shiftKey && (ev.key === "C" || ev.key === "c") && this.selected()) return;
      var seq = keyToSeq(ev, (this.model.modes & 1) !== 0);
      if (seq === null) return;
      if (ev.preventDefault) ev.preventDefault();
      if (this.back) { this.back = 0; this.model.all = true; this.schedule(); }
      this.queue(seq);
    },
    paste: function (ev) {
      var t = "";
      try { t = (ev.clipboardData || w.clipboardData).getData("text"); } catch (_) { t = ""; }
      if (!t) return;
      if (ev.preventDefault) ev.preventDefault();
      t = String(t).replace(/\r?\n/g, "\r");
      // Control characters other than tab and CR are removed (A-4): pasted
      // text containing ESC [201~ would end bracketed paste early and the
      // rest would run as typed input, CR as Enter.
      t = t.replace(/[\x00-\x08\x0b-\x0c\x0e-\x1f\x7f]/g, "");
      if (this.model.modes & 2) t = "\x1b[200~" + t + "\x1b[201~";
      this.queue(t);
    },
    wheel: function (ev) {
      var m = this.model;
      if ((m.modes & 4) || !m.sb.length) return;
      var lines = ev.deltaY < 0 ? -3 : (ev.deltaY > 0 ? 3 : 0);
      var b = clamp(this.back - lines, 0, m.sb.length);
      if (b === this.back) return;
      if (ev.preventDefault) ev.preventDefault();
      this.back = b;
      m.all = true;
      this.schedule();
    },
    queue: function (s) {
      var self = this;
      self.q += s;
      if (self.qt === null) {
        self.qt = setTimeout(function () { self.qt = null; self.flush(); }, 10);
      }
    },
    flush: function () {
      if (!this.q || this.dead) return;
      var q = this.q;
      this.q = "";
      this.send("input", { data: q });
    },
    schedule: function () {
      var self = this;
      if (self.raf !== null || self.dead) return;
      var f = function () { self.raf = null; if (!self.dead) self.render(); };
      if (typeof w.requestAnimationFrame === "function") {
        self.rafKind = 1;
        self.raf = w.requestAnimationFrame(f);
      } else {
        self.rafKind = 0;
        self.raf = setTimeout(f, 16);
      }
    },
    // lineAt is the line shown on screen row y (scrolled back by this.back).
    lineAt: function (y) {
      var m = this.model, i = m.sb.length - this.back + y;
      return i < m.sb.length ? m.sb[i] : (m.grid[i - m.sb.length] || null);
    },
    // render is the one draw pass of an animation frame: the dirty rows.
    render: function () {
      var m = this.model, rows = this.size[1], y;
      this.passes++;
      if (!m.cols) return;
      var n = Math.max(rows, m.rows);
      while (this.rowEls.length < n) {
        var r = w.document.createElement("div");
        r.style.height = this.ch + "px";
        r.style.overflow = "hidden";
        this.textLayer.appendChild(r);
        this.rowEls.push(r);
        this.texts.push(null);
      }
      while (this.rowEls.length > n) { this.textLayer.removeChild(this.rowEls.pop()); this.texts.pop(); }
      var all = m.all || this.back > 0, ctx = this.ctx, changed = [];
      if (ctx && all) {
        ctx.setTransform(this.dpr || 1, 0, 0, this.dpr || 1, 0, 0);
        ctx.fillStyle = DEF_BG;
        ctx.fillRect(0, 0, this.W, this.H);
      }
      var focused = w.document.activeElement === this.el;
      for (y = 0; y < n; y++) {
        if (!all && !m.dirty[y]) continue;
        var line = this.lineAt(y) || [];
        var cx = (this.back === 0 && m.cursorOn && m.cy === y) ? Math.min(m.cx, m.cols - 1) : -1;
        if (ctx) this.drawRow(ctx, y, line, cx, focused);
        var t = lineText(line);
        if (this.texts[y] !== t) {
          this.texts[y] = t;
          this.rowEls[y].textContent = t;
          if (t !== "") changed.push(t);
        }
      }
      m.dirty = {};
      m.all = false;
      if (changed.length) this.announce(changed);
    },
    font: function (fl) {
      return ((fl & 4) ? "italic " : "") + ((fl & 1) ? "bold " : "") + this.fontPx + "px " + FONT;
    },
    drawRow: function (ctx, y, line, cx, focused) {
      var cw = this.cw, ch = this.ch, y0 = y * ch, mid = y0 + ch / 2, i, c, x;
      ctx.setTransform(this.dpr || 1, 0, 0, this.dpr || 1, 0, 0);
      ctx.fillStyle = DEF_BG;
      ctx.fillRect(0, y0, this.W, ch);
      // Resolve each cell's colours once (inverse and the cursor swap them).
      var n = Math.min(line.length, this.model.cols), fgs = [], bgs = [];
      for (x = 0; x < n; x++) {
        c = line[x];
        var s = c.s, fg = colour(s[0], DEF_FG), bg = colour(s[1], DEF_BG);
        var inv = ((s[2] & 16) !== 0) !== (x === cx && focused);
        if (inv) { var t = fg; fg = bg; bg = t; }
        fgs.push(fg);
        bgs.push(bg);
      }
      // Backgrounds: one fillRect per run of equal colour.
      for (x = 0; x < n;) {
        var b = bgs[x], e = x + 1;
        while (e < n && bgs[e] === b) e++;
        if (b !== DEF_BG) { ctx.fillStyle = b; ctx.fillRect(x * cw, y0, (e - x) * cw, ch); }
        x = e;
      }
      ctx.textBaseline = "middle";
      // Text: one fillText per run of ASCII cells of equal colour and font.
      var run = "", rx = 0, rf = null, rfont = null, rdim = false;
      var self = this;
      function flush() {
        run = run.replace(/ +$/, "");
        if (run !== "") {
          ctx.globalAlpha = rdim ? 0.6 : 1;
          ctx.fillStyle = rf;
          ctx.fillText(run, rx * cw, mid);
        }
        run = "";
      }
      for (x = 0; x < n; x++) {
        c = line[x];
        if (c.w === 0) continue;
        var fl = c.s[2] | 0, f = self.font(fl), dim = (fl & 2) !== 0;
        if (f !== rfont) { flush(); rfont = f; ctx.font = f; }
        var ascii = c.w === 1 && c.t.length === 1 && c.t.charCodeAt(0) < 0x7f;
        if (!ascii) {
          flush();
          if (c.t !== " " && c.t !== "") {
            ctx.globalAlpha = dim ? 0.6 : 1;
            ctx.fillStyle = fgs[x];
            ctx.fillText(c.t, x * cw, mid);
          }
          continue;
        }
        if (run === "" || fgs[x] !== rf || dim !== rdim) { flush(); rx = x; rf = fgs[x]; rdim = dim; }
        run += c.t;
      }
      flush();
      ctx.globalAlpha = 1;
      // Underline and strike: one rule per run.
      for (x = 0; x < n;) {
        var dec = line[x].s[2] & 40, col = fgs[x], e2 = x + 1;
        while (e2 < n && (line[e2].s[2] & 40) === dec && fgs[e2] === col) e2++;
        if (dec) {
          ctx.fillStyle = col;
          if (dec & 8) ctx.fillRect(x * cw, y0 + ch - 2, (e2 - x) * cw, 1);
          if (dec & 32) ctx.fillRect(x * cw, mid, (e2 - x) * cw, 1);
        }
        x = e2;
      }
      if (cx >= 0 && !focused) {
        ctx.strokeStyle = DEF_FG;
        ctx.lineWidth = 1;
        ctx.strokeRect(cx * cw + 0.5, y0 + 0.5, cw - 1, ch - 1);
      }
    },
    announce: function (lines) {
      var self = this, t = now();
      self.pendingSay = lines.slice(-5).join("\n");
      if (self.say !== null) return;
      var wait = Math.max(0, 1000 - (t - self.lastSay));
      self.say = setTimeout(function () {
        self.say = null;
        self.lastSay = now();
        if (!self.dead) self.live.textContent = self.pendingSay;
      }, wait);
    },
    command: function (name, payload) {
      if (name !== "frame") return;
      var m = this.model, before = m.bells, title = m.title;
      m.pushed = 0;
      if (!m.apply(payload)) {
        if (!this.gapAsked) {
          // A frame was lost on the way: ask for a repaint, once until it comes.
          this.gapAsked = true;
          this.send("ready", {});
        }
        return;
      }
      if (payload && payload.base === -1) this.gapAsked = false;
      if (this.back > 0) this.back = Math.min(this.back + m.pushed, m.sb.length);
      if (m.title !== title) this.el.setAttribute("data-term-title", m.title);
      if (m.bells !== before) this.bell();
      this.schedule();
    },
    bell: function () {
      var self = this, s = self.box.style;
      self.el.setAttribute("data-term-bell", String(self.model.bells));
      s.outline = "2px solid " + DEF_FG;
      if (self.bellT !== null) clearTimeout(self.bellT);
      self.bellT = setTimeout(function () { self.bellT = null; s.outline = ""; }, 150);
    },
    update: function (props) {
      this.el.setAttribute("aria-label", labelOf(props));
    },
    destroy: function () {
      this.dead = true;
      var el = this.el;
      if (this.ro) { try { this.ro.disconnect(); } catch (_) {} }
      else if (w.removeEventListener) w.removeEventListener("resize", this.onResize);
      el.removeEventListener("keydown", this.onKey);
      el.removeEventListener("paste", this.onPaste);
      el.removeEventListener("mousedown", this.onDown);
      el.removeEventListener("focus", this.onFocus);
      el.removeEventListener("blur", this.onFocus);
      el.removeEventListener("wheel", this.onWheel);
      if (this.qt !== null) clearTimeout(this.qt);
      if (this.rsz !== null) clearTimeout(this.rsz);
      if (this.say !== null) clearTimeout(this.say);
      if (this.bellT !== null) clearTimeout(this.bellT);
      if (this.raf !== null) {
        if (this.rafKind === 1 && w.cancelAnimationFrame) w.cancelAnimationFrame(this.raf);
        else clearTimeout(this.raf);
      }
      this.qt = this.rsz = this.raf = this.say = this.bellT = null;
    }
  });
})();
`
