package rt

// terminalWidgetJS is the built-in "sky-terminal" widget island: a small
// VT100 / xterm subset renderer that Std.Ui.Terminal binds to a PTY process.
// It rides in the same same-origin client files as the island runtime (it
// must come right after islandClientJS, which defines window.Sky.island), so a
// strict Content-Security-Policy (script-src 'self') runs it and no widget
// file has to be loaded. The Rust build reads this literal as a raw string, so
// it must hold no backquote. It sets styles through the CSSOM and text through
// textContent only (no innerHTML, no style attribute, no eval).
//
// The protocol (island name "sky-terminal"):
//
//	props          null, or {"label": String} (the aria-label; default
//	               "Terminal")
//
//	widget -> app  (Std.Ui.onIslandEvent)
//	  "resize"     {"cols": Int, "rows": Int}: the measured size, sent on
//	               mount and whenever it changes (debounced 50 ms)
//	  "ready"      {}: sent once after the first "resize" on every mount; the
//	               widget holds nothing, so the app replays the scrollback
//	               ("reset", then "output" from offset 0)
//	  "input"      {"data": String}: keystrokes and pastes, batched for up to
//	               10 ms into one event
//
//	app -> widget  (Cmd.toIsland id name payload)
//	  "reset"      {}: clear the screen, the scrollback and the offset
//	  "output"     {"data": base64 String, "from": Int, "next": Int,
//	               "dropped": Bool}: the bytes of the process output that
//	               start at offset "from"; "next" is the offset after them.
//	               Bytes the widget already has (below its own offset) are
//	               skipped, so a repeated or overlapping chunk writes each
//	               byte once. Bytes are decoded as streaming UTF-8, so a
//	               character split across two chunks decodes correctly. A
//	               chunk that starts past the widget's offset without
//	               "dropped" (the ring overwrote them) means commands were
//	               lost on the way: the widget sends "ready" again, once until
//	               the next "reset", to get a repaint.
//	  "exit"       {"code": Int|null, "signal": Int|null}: prints
//	               "[process exited with code N]" or
//	               "[process terminated by signal N]"
//
//	attributes the widget sets on the island element: tabindex 0, role
//	"application", aria-label, data-term-ready "1" (after mount),
//	data-term-cols and data-term-rows (after every resize). They are not in
//	the server's sky-* / data-sky-* namespace, so an HTML swap that adopts the
//	element keeps them.
//
// The cursor is drawn as an inverse cell whenever it is visible (focus does
// not change it). A wide character (CJK, emoji) takes one cell. The VT core
// is exposed for tests as window.Sky.__vt (VT, b64ToBytes, keyToSeq, utf8).
const terminalWidgetJS = `// Sky terminal widget (runtime-go/rt/island_terminal.go): the "sky-terminal" island.
(function () {
  "use strict";
  var w = typeof window !== "undefined" ? window : this;
  var Sky = w.Sky = w.Sky || {};
  if (!Sky.island || Sky.__terminal) return;
  Sky.__terminal = true;
  var SCROLLBACK = 1000;
  var DEF_FG = "#d4d4d4", DEF_BG = "#1e1e1e";
  var BASIC = ["#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
    "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"];
  function hex2(n) {
    n = Math.max(0, Math.min(255, n | 0));
    return (n < 16 ? "0" : "") + n.toString(16);
  }
  function rgb(r, g, b) { return "#" + hex2(r) + hex2(g) + hex2(b); }
  function palette(n) {
    if (typeof n !== "number" || isNaN(n) || n < 0 || n > 255) return undefined;
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
  function clamp(v, lo, hi) { return v < lo ? lo : (v > hi ? hi : v); }
  function newAttr() {
    return { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, inverse: false };
  }
  function copyAttr(a) {
    return { fg: a.fg, bg: a.bg, bold: a.bold, dim: a.dim, italic: a.italic, underline: a.underline, inverse: a.inverse };
  }

  // ── The VT core: no DOM ──────────────────────────────────────────
  // Parser states: 0 ground, 1 after ESC, 2 CSI, 3 OSC / DCS string,
  // 4 ESC inside a string, 5 skip one character (charset designation).
  function VT(cols, rows) {
    this.cols = clamp((cols | 0) || 80, 1, 1000);
    this.rows = clamp((rows | 0) || 24, 1, 1000);
    this.scrollback = [];
    this.reset();
  }
  VT.prototype.blank = function () {
    return { ch: " ", fg: null, bg: this.attr ? this.attr.bg : null, bold: false, dim: false,
      italic: false, underline: false, inverse: false };
  };
  VT.prototype.blankLine = function () {
    var l = [];
    for (var i = 0; i < this.cols; i++) l.push(this.blank());
    return l;
  };
  VT.prototype.reset = function (keepScrollback) {
    if (!keepScrollback) this.scrollback = [];
    this.attr = newAttr();
    this.lines = [];
    for (var y = 0; y < this.rows; y++) this.lines.push(this.blankLine());
    this.cursor = { x: 0, y: 0, visible: true };
    this.wrapPending = false;
    this.autowrap = true;
    this.top = 0;
    this.bottom = this.rows - 1;
    this.saved = null;
    this.alt = null;
    this.state = 0;
    this.params = "";
    this.priv = "";
  };
  VT.prototype.write = function (s) {
    s = String(s);
    for (var i = 0; i < s.length; i++) {
      var c = s.charAt(i), code = s.charCodeAt(i);
      if (code >= 0xD800 && code <= 0xDBFF && i + 1 < s.length) {
        var lo = s.charCodeAt(i + 1);
        if (lo >= 0xDC00 && lo <= 0xDFFF) { c = s.substr(i, 2); i++; }
      }
      this.feed(c, code);
    }
  };
  VT.prototype.feed = function (c, code) {
    switch (this.state) {
      case 1: this.esc(c); return;
      case 2:
        if (code === 0x1b) { this.state = 1; return; }
        if (code === 0x18 || code === 0x1a) { this.state = 0; return; }
        if (code < 0x20) { this.control(code); return; }
        if (code >= 0x40 && code <= 0x7e) { this.state = 0; this.csi(c); return; }
        if (c === "?" || c === ">" || c === "=" || c === "<") { this.priv += c; return; }
        this.params += c;
        return;
      case 3:
        if (code === 7) { this.state = 0; return; }
        if (code === 0x1b) { this.state = 4; return; }
        return;
      case 4:
        this.state = 0;
        if (c !== "\\") { this.state = 1; this.esc(c); }
        return;
      case 5: this.state = 0; return;
    }
    if (code === 0x1b) { this.state = 1; return; }
    if (code < 0x20 || code === 0x7f) { this.control(code); return; }
    this.print(c);
  };
  VT.prototype.esc = function (c) {
    this.state = 0;
    switch (c) {
      case "[": this.state = 2; this.params = ""; this.priv = ""; return;
      case "]": case "P": case "X": case "^": case "_": this.state = 3; return;
      case "7": this.save(); return;
      case "8": this.restore(); return;
      case "c": this.reset(true); return;
      case "D": this.index(); return;
      case "M": this.reverseIndex(); return;
      case "E": this.cursor.x = 0; this.index(); return;
      case "(": case ")": case "*": case "+": case "-": case ".": case "/":
      case "#": case "%": case " ":
        this.state = 5; return;
    }
  };
  VT.prototype.control = function (code) {
    var cur = this.cursor;
    switch (code) {
      case 8: if (cur.x > 0) cur.x--; this.wrapPending = false; return;
      case 9: cur.x = Math.min(this.cols - 1, (Math.floor(cur.x / 8) + 1) * 8); this.wrapPending = false; return;
      case 10: case 11: case 12: this.index(); return;
      case 13: cur.x = 0; this.wrapPending = false; return;
    }
  };
  VT.prototype.index = function () {
    this.wrapPending = false;
    if (this.cursor.y === this.bottom) this.scrollUp(1);
    else if (this.cursor.y < this.rows - 1) this.cursor.y++;
  };
  VT.prototype.reverseIndex = function () {
    this.wrapPending = false;
    if (this.cursor.y === this.top) this.scrollDown(1);
    else if (this.cursor.y > 0) this.cursor.y--;
  };
  VT.prototype.scrollUp = function (n) {
    n = clamp(n, 1, this.bottom - this.top + 1);
    for (var k = 0; k < n; k++) {
      var gone = this.lines.splice(this.top, 1)[0];
      if (this.top === 0 && !this.alt) {
        this.scrollback.push(gone);
        if (this.scrollback.length > SCROLLBACK) this.scrollback.shift();
      }
      this.lines.splice(this.bottom, 0, this.blankLine());
    }
  };
  VT.prototype.scrollDown = function (n) {
    n = clamp(n, 1, this.bottom - this.top + 1);
    for (var k = 0; k < n; k++) {
      this.lines.splice(this.bottom, 1);
      this.lines.splice(this.top, 0, this.blankLine());
    }
  };
  VT.prototype.print = function (c) {
    var cur = this.cursor, a = this.attr;
    if (this.wrapPending) {
      this.wrapPending = false;
      if (this.autowrap) { cur.x = 0; this.index(); }
    }
    this.lines[cur.y][cur.x] = { ch: c, fg: a.fg, bg: a.bg, bold: a.bold, dim: a.dim, italic: a.italic,
      underline: a.underline, inverse: a.inverse };
    if (cur.x >= this.cols - 1) {
      if (this.autowrap) this.wrapPending = true;
    } else {
      cur.x++;
    }
  };
  VT.prototype.save = function () {
    this.saved = { x: this.cursor.x, y: this.cursor.y, attr: copyAttr(this.attr) };
  };
  VT.prototype.restore = function () {
    this.wrapPending = false;
    if (!this.saved) { this.cursor.x = 0; this.cursor.y = 0; return; }
    this.cursor.x = clamp(this.saved.x, 0, this.cols - 1);
    this.cursor.y = clamp(this.saved.y, 0, this.rows - 1);
    this.attr = copyAttr(this.saved.attr);
  };
  VT.prototype.erase = function (y, a, b) {
    var line = this.lines[y];
    for (var i = Math.max(0, a); i < b && i < this.cols; i++) line[i] = this.blank();
  };
  VT.prototype.csi = function (fin) {
    var raw = this.params.split(/[;:]/), n = [], i;
    for (i = 0; i < raw.length; i++) n.push(raw[i] === "" ? NaN : parseInt(raw[i], 10));
    var priv = this.priv;
    function num(k, d) { var v = n[k]; return (v === undefined || isNaN(v)) ? d : v; }
    function cnt(k) { var v = num(k, 1); return v < 1 ? 1 : v; }
    if (priv !== "" && priv !== "?") return;
    var cur = this.cursor, C = this.cols, R = this.rows, line, k;
    if (priv === "?") {
      if (fin !== "h" && fin !== "l") return;
      var on = fin === "h";
      for (i = 0; i < n.length; i++) {
        switch (n[i]) {
          case 25: cur.visible = on; break;
          case 7: this.autowrap = on; if (!on) this.wrapPending = false; break;
          case 47: case 1047: case 1049:
            if (on) this.enterAlt(); else this.leaveAlt();
            break;
        }
      }
      return;
    }
    if (fin.charCodeAt(0) === 96) fin = "G"; // HPA is CHA
    if (fin === "m") { this.sgr(n); return; }
    this.wrapPending = false;
    switch (fin) {
      case "A": cur.y = Math.max(0, cur.y - cnt(0)); return;
      case "B": cur.y = Math.min(R - 1, cur.y + cnt(0)); return;
      case "C": cur.x = Math.min(C - 1, cur.x + cnt(0)); return;
      case "D": cur.x = Math.max(0, cur.x - cnt(0)); return;
      case "E": cur.y = Math.min(R - 1, cur.y + cnt(0)); cur.x = 0; return;
      case "F": cur.y = Math.max(0, cur.y - cnt(0)); cur.x = 0; return;
      case "G": cur.x = clamp(cnt(0) - 1, 0, C - 1); return;
      case "d": cur.y = clamp(cnt(0) - 1, 0, R - 1); return;
      case "H": case "f":
        cur.y = clamp(cnt(0) - 1, 0, R - 1);
        cur.x = clamp(cnt(1) - 1, 0, C - 1);
        return;
      case "J":
        switch (num(0, 0)) {
          case 0:
            this.erase(cur.y, cur.x, C);
            for (k = cur.y + 1; k < R; k++) this.lines[k] = this.blankLine();
            return;
          case 1:
            for (k = 0; k < cur.y; k++) this.lines[k] = this.blankLine();
            this.erase(cur.y, 0, cur.x + 1);
            return;
          case 2:
            for (k = 0; k < R; k++) this.lines[k] = this.blankLine();
            return;
          case 3:
            this.scrollback = [];
            return;
        }
        return;
      case "K":
        switch (num(0, 0)) {
          case 0: this.erase(cur.y, cur.x, C); return;
          case 1: this.erase(cur.y, 0, cur.x + 1); return;
          case 2: this.erase(cur.y, 0, C); return;
        }
        return;
      case "L":
        if (cur.y < this.top || cur.y > this.bottom) return;
        for (k = 0; k < Math.min(cnt(0), this.bottom - cur.y + 1); k++) {
          this.lines.splice(this.bottom, 1);
          this.lines.splice(cur.y, 0, this.blankLine());
        }
        cur.x = 0;
        return;
      case "M":
        if (cur.y < this.top || cur.y > this.bottom) return;
        for (k = 0; k < Math.min(cnt(0), this.bottom - cur.y + 1); k++) {
          this.lines.splice(cur.y, 1);
          this.lines.splice(this.bottom, 0, this.blankLine());
        }
        cur.x = 0;
        return;
      case "P":
        line = this.lines[cur.y];
        line.splice(cur.x, Math.min(cnt(0), C - cur.x));
        while (line.length < C) line.push(this.blank());
        return;
      case "@":
        line = this.lines[cur.y];
        for (k = 0; k < Math.min(cnt(0), C - cur.x); k++) line.splice(cur.x, 0, this.blank());
        line.length = C;
        return;
      case "X": this.erase(cur.y, cur.x, cur.x + cnt(0)); return;
      case "S": this.scrollUp(cnt(0)); return;
      case "T": this.scrollDown(cnt(0)); return;
      case "r":
        var t = num(0, 1) - 1, b = num(1, R) - 1;
        if (t < 0) t = 0;
        if (b >= R || b < 0) b = R - 1;
        if (t < b) { this.top = t; this.bottom = b; }
        cur.x = 0;
        cur.y = 0;
        return;
      case "s": this.save(); return;
      case "u": this.restore(); return;
    }
  };
  VT.prototype.sgr = function (n) {
    if (n.length === 0) n = [0];
    for (var i = 0; i < n.length; i++) {
      var v = isNaN(n[i]) ? 0 : n[i], a = this.attr;
      if (v === 0) { this.attr = newAttr(); continue; }
      if (v === 1) a.bold = true;
      else if (v === 2) a.dim = true;
      else if (v === 3) a.italic = true;
      else if (v === 4) a.underline = true;
      else if (v === 7) a.inverse = true;
      else if (v === 22) { a.bold = false; a.dim = false; }
      else if (v === 23) a.italic = false;
      else if (v === 24) a.underline = false;
      else if (v === 27) a.inverse = false;
      else if (v >= 30 && v <= 37) a.fg = BASIC[v - 30];
      else if (v === 39) a.fg = null;
      else if (v >= 40 && v <= 47) a.bg = BASIC[v - 40];
      else if (v === 49) a.bg = null;
      else if (v >= 90 && v <= 97) a.fg = BASIC[v - 90 + 8];
      else if (v >= 100 && v <= 107) a.bg = BASIC[v - 100 + 8];
      else if (v === 38 || v === 48) {
        var col, mode = n[i + 1];
        if (mode === 5) { col = palette(n[i + 2]); i += 2; }
        else if (mode === 2) { col = rgb(n[i + 2], n[i + 3], n[i + 4]); i += 4; }
        else { i = n.length; }
        if (col !== undefined) { if (v === 38) a.fg = col; else a.bg = col; }
      }
    }
  };
  VT.prototype.enterAlt = function () {
    if (this.alt) return;
    this.alt = { lines: this.lines, x: this.cursor.x, y: this.cursor.y, attr: copyAttr(this.attr) };
    this.lines = [];
    for (var y = 0; y < this.rows; y++) this.lines.push(this.blankLine());
    this.wrapPending = false;
  };
  VT.prototype.leaveAlt = function () {
    var s = this.alt;
    if (!s) return;
    this.alt = null;
    this.lines = s.lines;
    this.cursor.x = clamp(s.x, 0, this.cols - 1);
    this.cursor.y = clamp(s.y, 0, this.rows - 1);
    this.attr = s.attr;
    this.wrapPending = false;
  };
  VT.prototype.fitCols = function (lines) {
    for (var y = 0; y < lines.length; y++) {
      var l = lines[y];
      if (l.length > this.cols) l.length = this.cols;
      while (l.length < this.cols) l.push(this.blank());
    }
  };
  VT.prototype.resize = function (cols, rows) {
    cols = clamp((cols | 0) || this.cols, 1, 1000);
    rows = clamp((rows | 0) || this.rows, 1, 1000);
    this.cols = cols;
    var cur = this.cursor, lines = this.lines;
    this.fitCols(lines);
    if (rows < lines.length) {
      var drop = Math.max(0, Math.min(cur.y - (rows - 1), lines.length - rows));
      for (var k = 0; k < drop; k++) {
        var gone = lines.shift();
        if (!this.alt) {
          this.scrollback.push(gone);
          if (this.scrollback.length > SCROLLBACK) this.scrollback.shift();
        }
      }
      cur.y -= drop;
      lines.length = rows;
    }
    while (lines.length < rows) lines.push(this.blankLine());
    if (this.alt) {
      var al = this.alt.lines;
      this.fitCols(al);
      if (al.length > rows) al.length = rows;
      while (al.length < rows) al.push(this.blankLine());
    }
    this.rows = rows;
    this.top = 0;
    this.bottom = rows - 1;
    cur.x = clamp(cur.x, 0, cols - 1);
    cur.y = clamp(cur.y, 0, rows - 1);
    this.wrapPending = false;
  };
  function rowText(l) {
    var s = "";
    for (var i = 0; i < l.length; i++) s += l[i].ch;
    return s.replace(/ +$/, "");
  }
  VT.prototype.text = function () {
    var out = [];
    for (var y = 0; y < this.lines.length; y++) out.push(rowText(this.lines[y]));
    return out;
  };
  VT.prototype.scrollbackText = function () {
    var out = [];
    for (var y = 0; y < this.scrollback.length; y++) out.push(rowText(this.scrollback[y]));
    return out;
  };

  // ── Bytes: base64 and streaming UTF-8 ────────────────────────────
  var B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  function b64ToBytes(s) {
    s = String(s === null || s === undefined ? "" : s).replace(/\s+/g, "").replace(/-/g, "+").replace(/_/g, "/");
    var bin = null, i, u;
    if (typeof w.atob === "function") {
      try { bin = w.atob(s); } catch (_) { bin = null; }
    }
    if (bin !== null) {
      u = new Uint8Array(bin.length);
      for (i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i) & 255;
      return u;
    }
    var clean = s.replace(/[^A-Za-z0-9+\/]/g, ""), out = [], buf = 0, bits = 0;
    for (i = 0; i < clean.length; i++) {
      buf = (buf << 6) | B64.indexOf(clean.charAt(i));
      bits += 6;
      if (bits >= 8) {
        bits -= 8;
        out.push((buf >> bits) & 255);
        buf &= (1 << bits) - 1;
      }
    }
    return new Uint8Array(out);
  }
  function cpString(cp) {
    if (cp < 0x10000) return String.fromCharCode(cp);
    cp -= 0x10000;
    return String.fromCharCode(0xD800 + (cp >> 10), 0xDC00 + (cp & 0x3ff));
  }
  // utf8(forceManual) returns a streaming decoder: a function from bytes to
  // text that keeps an incomplete trailing sequence for the next call.
  function utf8(forceManual) {
    if (!forceManual && typeof w.TextDecoder === "function") {
      try {
        var td = new w.TextDecoder("utf-8");
        return function (bytes) { return td.decode(bytes, { stream: true }); };
      } catch (_) {}
    }
    var pend = [];
    return function (bytes) {
      var all = pend.concat(Array.prototype.slice.call(bytes)), out = "", i = 0;
      pend = [];
      while (i < all.length) {
        var b = all[i], need, cp, k, ok = true;
        if (b < 0x80) { out += String.fromCharCode(b); i++; continue; }
        if (b >= 0xC2 && b < 0xE0) { need = 1; cp = b & 0x1f; }
        else if (b >= 0xE0 && b < 0xF0) { need = 2; cp = b & 0x0f; }
        else if (b >= 0xF0 && b < 0xF5) { need = 3; cp = b & 0x07; }
        else { out += "�"; i++; continue; }
        for (k = 1; k <= need && i + k < all.length; k++) {
          var cb = all[i + k];
          if ((cb & 0xC0) !== 0x80) { ok = false; break; }
          cp = (cp << 6) | (cb & 0x3f);
        }
        if (!ok) { out += "�"; i++; continue; }
        if (i + need >= all.length) { pend = all.slice(i); break; }
        out += cpString(cp);
        i += need + 1;
      }
      return out;
    };
  }

  // ── Keys ─────────────────────────────────────────────────────────
  var NAMED = {
    Enter: "\r", Backspace: "\x7f", Tab: "\t", Escape: "\x1b",
    ArrowUp: "\x1b[A", ArrowDown: "\x1b[B", ArrowRight: "\x1b[C", ArrowLeft: "\x1b[D",
    Home: "\x1b[H", End: "\x1b[F", Delete: "\x1b[3~", Insert: "\x1b[2~",
    PageUp: "\x1b[5~", PageDown: "\x1b[6~",
    F1: "\x1bOP", F2: "\x1bOQ", F3: "\x1bOR", F4: "\x1bOS"
  };
  var MODIFIER = { Shift: 1, Control: 1, Alt: 1, AltGraph: 1, Meta: 1, OS: 1, CapsLock: 1,
    NumLock: 1, ScrollLock: 1, Fn: 1, Dead: 1, Unidentified: 1, Process: 1 };
  function single(k) {
    if (k.length === 1) return true;
    if (k.length !== 2) return false;
    var h = k.charCodeAt(0), l = k.charCodeAt(1);
    return h >= 0xD800 && h <= 0xDBFF && l >= 0xDC00 && l <= 0xDFFF;
  }
  function keyToSeq(ev) {
    if (!ev || typeof ev.key !== "string" || ev.key === "") return null;
    var k = ev.key, seq;
    if (ev.metaKey || MODIFIER[k]) return null;
    if (k === "Tab" && ev.shiftKey) seq = "\x1b[Z";
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

  Sky.__vt = { VT: VT, b64ToBytes: b64ToBytes, keyToSeq: keyToSeq, utf8: utf8 };

  // ── The island ───────────────────────────────────────────────────
  function labelOf(props) {
    return props && typeof props.label === "string" && props.label !== "" ? props.label : "Terminal";
  }
  // eff is a cell's effective style; inv is true for the cursor cell.
  function eff(c, inv) {
    var fg = c.fg || DEF_FG, bg = c.bg || DEF_BG;
    if (c.inverse !== inv) { var t = fg; fg = bg; bg = t; }
    return { fg: fg, bg: bg, b: c.bold, u: c.underline, i: c.italic, d: c.dim,
      key: fg + "|" + bg + "|" + (c.bold ? 1 : 0) + (c.underline ? 1 : 0) + (c.italic ? 1 : 0) + (c.dim ? 1 : 0) };
  }
  Sky.island("sky-terminal", {
    mount: function (el, props, send) {
      var self = this, d = w.document;
      self.el = el;
      self.send = send;
      self.dead = false;
      self.next = 0;
      self.dec = utf8(false);
      self.q = "";
      self.qt = null;
      self.rsz = null;
      self.raf = null;
      self.rowEls = [];
      self.sigs = [];
      el.tabIndex = 0;
      el.setAttribute("role", "application");
      el.setAttribute("aria-label", labelOf(props));
      var box = d.createElement("div"), s = box.style;
      s.fontFamily = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";
      s.whiteSpace = "pre";
      s.overflow = "hidden";
      s.lineHeight = "1.2";
      s.background = DEF_BG;
      s.color = DEF_FG;
      s.width = "100%";
      s.height = "100%";
      s.boxSizing = "border-box";
      el.appendChild(box);
      self.box = box;
      var sz = self.measure();
      self.size = sz;
      self.vt = new VT(sz[0], sz[1]);
      self.layoutRows();
      self.onKey = function (ev) { self.key(ev); };
      self.onPaste = function (ev) { self.paste(ev); };
      self.onDown = function () { try { el.focus(); } catch (_) {} };
      self.onFocus = function () { self.schedule(); };
      el.addEventListener("keydown", self.onKey);
      el.addEventListener("paste", self.onPaste);
      el.addEventListener("mousedown", self.onDown);
      el.addEventListener("focus", self.onFocus);
      el.addEventListener("blur", self.onFocus);
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
    measure: function () {
      var el = this.el, d = w.document, probe = d.createElement("span"), ps = probe.style, r = null;
      probe.textContent = "MMMMMMMMMM";
      ps.position = "absolute";
      ps.visibility = "hidden";
      ps.whiteSpace = "pre";
      this.box.appendChild(probe);
      try { r = probe.getBoundingClientRect ? probe.getBoundingClientRect() : null; } catch (_) { r = null; }
      this.box.removeChild(probe);
      var cw = r ? r.width / 10 : 0, chh = r ? r.height : 0;
      var W = el.clientWidth || 0, H = el.clientHeight || 0;
      if (!(cw > 0) || !(chh > 0) || !(W > 0) || !(H > 0)) return [80, 24];
      return [clamp(Math.floor(W / cw), 2, 500), clamp(Math.floor(H / chh), 2, 200)];
    },
    layoutRows: function () {
      var d = w.document, n = this.vt.rows;
      while (this.rowEls.length < n) {
        var row = d.createElement("div");
        row.style.whiteSpace = "pre";
        this.box.appendChild(row);
        this.rowEls.push(row);
      }
      while (this.rowEls.length > n) this.box.removeChild(this.rowEls.pop());
      this.sigs = [];
      this.el.setAttribute("data-term-cols", String(this.vt.cols));
      this.el.setAttribute("data-term-rows", String(this.vt.rows));
    },
    resized: function () {
      var self = this;
      if (self.dead) return;
      if (self.rsz !== null) clearTimeout(self.rsz);
      self.rsz = setTimeout(function () {
        self.rsz = null;
        if (self.dead) return;
        var sz = self.measure();
        if (sz[0] === self.size[0] && sz[1] === self.size[1]) return;
        self.size = sz;
        self.vt.resize(sz[0], sz[1]);
        self.layoutRows();
        self.send("resize", { cols: sz[0], rows: sz[1] });
        self.schedule();
      }, 50);
    },
    key: function (ev) {
      var seq = keyToSeq(ev);
      if (seq === null) return;
      if (ev.preventDefault) ev.preventDefault();
      this.queue(seq);
    },
    paste: function (ev) {
      var t = "";
      try { t = (ev.clipboardData || w.clipboardData).getData("text"); } catch (_) { t = ""; }
      if (!t) return;
      if (ev.preventDefault) ev.preventDefault();
      this.queue(String(t).replace(/\r?\n/g, "\r"));
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
    render: function () {
      var vt = this.vt, cur = vt.cursor;
      for (var y = 0; y < vt.rows && y < this.rowEls.length; y++) {
        var line = vt.lines[y], cx = (cur.visible && cur.y === y) ? Math.min(cur.x, vt.cols - 1) : -1;
        var sig = "", x;
        for (x = 0; x < line.length; x++) sig += eff(line[x], x === cx).key + "\u0001" + line[x].ch + "\u0002";
        if (sig === this.sigs[y]) continue;
        this.sigs[y] = sig;
        this.paintRow(this.rowEls[y], line, cx);
      }
    },
    paintRow: function (row, line, cx) {
      var d = w.document;
      while (row.firstChild) row.removeChild(row.firstChild);
      var run = null, text = "";
      function flushRun() {
        if (run === null) return;
        var span = d.createElement("span"), st = span.style;
        span.textContent = text;
        st.color = run.fg;
        st.backgroundColor = run.bg;
        if (run.b) st.fontWeight = "bold";
        if (run.u) st.textDecoration = "underline";
        if (run.i) st.fontStyle = "italic";
        if (run.d) st.opacity = "0.7";
        row.appendChild(span);
      }
      for (var x = 0; x < line.length; x++) {
        var e = eff(line[x], x === cx);
        if (run === null || e.key !== run.key) {
          flushRun();
          run = e;
          text = "";
        }
        text += line[x].ch;
      }
      flushRun();
    },
    command: function (name, payload) {
      payload = payload || {};
      if (name === "reset") {
        this.vt.reset();
        this.next = 0;
        this.dec = utf8(false);
        this.sigs = [];
        this.gapAsked = false;
        this.schedule();
        return;
      }
      if (name === "output") {
        var bytes = b64ToBytes(payload.data);
        var from = typeof payload.from === "number" ? payload.from : this.next;
        var next = typeof payload.next === "number" ? payload.next : from + bytes.length;
        if (from + bytes.length <= this.next) return;
        if (from > this.next && !payload.dropped && !this.gapAsked) {
          // Bytes are missing that the process's ring still holds (a command
          // lost with a dropped connection): ask for a repaint, once until
          // the reset arrives, and write what came meanwhile.
          this.gapAsked = true;
          this.send("ready", {});
        }
        if (from < this.next) bytes = bytes.subarray(this.next - from);
        var text = this.dec(bytes);
        if (text) this.vt.write(text);
        this.next = next;
        this.schedule();
        return;
      }
      if (name === "exit") {
        var how = (payload.signal !== null && payload.signal !== undefined)
          ? "terminated by signal " + payload.signal
          : "exited with code " + (payload.code === null || payload.code === undefined ? "?" : payload.code);
        this.vt.write("\r\n[process " + how + "]\r\n");
        this.schedule();
      }
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
      if (this.qt !== null) clearTimeout(this.qt);
      if (this.rsz !== null) clearTimeout(this.rsz);
      if (this.raf !== null) {
        if (this.rafKind === 1 && w.cancelAnimationFrame) w.cancelAnimationFrame(this.raf);
        else clearTimeout(this.raf);
      }
      this.qt = this.rsz = this.raf = null;
    }
  });
})();
`
