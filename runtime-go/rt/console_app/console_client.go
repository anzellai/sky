package console_app

// client_js.go — bundled client-side script for the inline Sky
// Console (v0.16.1 PR 8-D). Lives in its own file so the JS body
// doesn't churn mount.go on every tweak.
//
// The script wires the rendered DOM to the rt-side update loop:
//
//   1. Opens an EventSource on /_sky/console/_sse. SSE-delivered
//      patches / patch frames apply directly to elements via
//      sky-id (`event: patches`) or full-body replacement
//      (`event: patch`). Wire shape mirrors live.go's
//      encodePatchesEventFromSnapshot so the format stays single-
//      source-of-truth.
//
//   2. Captures gesture events (click / input / submit / change /
//      keydown / focus / blur) on the [data-sky-console] subtree
//      and POSTs {msg, args} envelopes to /_sky/console/_event.
//      The rt-side console_loop.go drains the envelope channel and
//      runs hooks.Update → diff → broadcast.
//
//   3. Surfaces a debug counter on `window.__skyConsole` so the
//      verification script can assert "frame received post-click"
//      from the rendered page.
//
// Intentionally narrower than live.go's client JS:
//
//   - No retry / queue / hello-watchdog. The console is admin-only;
//     a wedged connection just needs a refresh. EventSource's
//     native reconnect handles transient drops.
//   - No input-authority protocol. The console doesn't ship
//     password fields or other dirty-input cases.
//   - No file / image upload drivers — out of scope.
//   - Plain string concatenation; the wire shape's args are raw
//     JSON values that rt's decoder accepts as-is.

// consoleClientJS returns the bundled client-side script body. It
// runs as the body's last child so consoleRoot exists by the time
// the wrapping IIFE evaluates.
func consoleClientJS() string {
	return consoleClientJSBody
}

// consoleClientJSBody is the literal script content. Held as a
// package-level string constant so the function call above
// allocates nothing on each request. Pages embed this verbatim
// inside `<script>…</script>`.
const consoleClientJSBody = `
(function() {
  var consoleRoot = document.getElementById('sky-console-root');
  if (!consoleRoot) return;
  window.__skyConsole = window.__skyConsole || { framesReceived: 0, lastSeq: 0, connected: false };

  function esc(s) {
    return String(s).replace(/"/g, '\\"');
  }

  // ringActiveSelection / restoreSelection mirror live.go's cursor-
  // preservation pattern for INPUT / TEXTAREA elements. Without this,
  // applyFullBody / applyPatches replacing innerHTML would reset the
  // user's cursor to the end of the value mid-edit.
  function ringActiveSelection(el) {
    if (!el) return null;
    var tag = el.tagName;
    if (tag !== "INPUT" && tag !== "TEXTAREA") return null;
    try {
      return { el: el, start: el.selectionStart, end: el.selectionEnd, scrollTop: el.scrollTop };
    } catch (_) { return null; }
  }

  function restoreSelection(snap) {
    if (!snap || !snap.el) return;
    try {
      var len = (snap.el.value || "").length;
      var s = Math.min(snap.start || 0, len);
      var e = Math.min(snap.end == null ? s : snap.end, len);
      snap.el.setSelectionRange(s, e);
      if (snap.scrollTop) snap.el.scrollTop = snap.scrollTop;
    } catch (_) {}
  }

  // applyPatches — mirror of __skyApplyPatches from live.go, scoped
  // to elements inside #sky-console-root so the host's own Sky.Live
  // can't be cross-mutated. Supports the same wire shape:
  // {id, text?, html?, attrs?, remove?}.
  function applyPatches(patches) {
    if (!patches || !patches.length) return;
    var snap = ringActiveSelection(document.activeElement);
    for (var i = 0; i < patches.length; i++) {
      var p = patches[i];
      var el = consoleRoot.querySelector('[sky-id="' + esc(p.id) + '"]');
      if (!el) continue;
      if (p.text !== undefined && p.text !== null) {
        el.textContent = p.text;
      }
      if (p.html !== undefined && p.html !== null) {
        el.innerHTML = p.html;
      }
      if (p.attrs) {
        var keys = Object.keys(p.attrs);
        for (var j = 0; j < keys.length; j++) {
          var k = keys[j], v = p.attrs[k];
          if (v === "" || v === null || v === undefined) {
            el.removeAttribute(k);
          } else {
            el.setAttribute(k, v);
            if (k === "value" && ("value" in el)) el.value = v;
            if (k === "checked") el.checked = v !== "" && v !== "false";
            if (k === "disabled") el.disabled = v !== "" && v !== "false";
          }
        }
      }
      if (p.remove) el.remove();
    }
    restoreSelection(snap);
  }

  // applyFullBody — fallback for first frame / full-replace SSE
  // event. Replaces the sky-console root's children atomically.
  function applyFullBody(body) {
    if (typeof body !== "string") return;
    var snap = ringActiveSelection(document.activeElement);
    consoleRoot.innerHTML = body;
    restoreSelection(snap);
  }

  function openSSE() {
    var es = new EventSource('/_sky/console/_sse');
    es.addEventListener('hello', function(ev) {
      window.__skyConsole.connected = true;
      window.__skyConsole.framesReceived++;
      try { var d = JSON.parse(ev.data); window.__skyConsole.sessionId = d.sid; } catch (_) {}
    });
    es.addEventListener('patches', function(ev) {
      window.__skyConsole.framesReceived++;
      try {
        var data = JSON.parse(ev.data);
        if (typeof data.seq === "number") window.__skyConsole.lastSeq = data.seq;
        if (data.patches) applyPatches(data.patches);
      } catch (e) {
        if (window.console && console.warn) console.warn('[sky.console] bad patches frame', e);
      }
    });
    es.addEventListener('patch', function(ev) {
      window.__skyConsole.framesReceived++;
      try {
        var data = JSON.parse(ev.data);
        if (typeof data.seq === "number") window.__skyConsole.lastSeq = data.seq;
        if (typeof data.body === "string") applyFullBody(data.body);
      } catch (e) {
        if (window.console && console.warn) console.warn('[sky.console] bad patch frame', e);
      }
    });
    es.addEventListener('heartbeat', function() {
      window.__skyConsole.connected = true;
    });
    es.onerror = function() {
      window.__skyConsole.connected = false;
      // Auto-reconnect: EventSource native reconnect is enough for
      // the admin-only console traffic.
    };
    return es;
  }

  // sendEvent — POST {msg, args} envelope to /_sky/console/_event.
  // Non-blocking; the SSE channel delivers the resulting patches
  // out-of-band so we don't need to await the response body.
  function sendEvent(msg, args) {
    var body = JSON.stringify({ msg: msg, args: args || [] });
    try {
      fetch('/_sky/console/_event', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', 'X-Sky-Console': '1' },
        body: body
      });
    } catch (_) {}
  }

  // Capture click / input / submit / change / keydown gestures on
  // [data-sky-console] subtree. Looks for the standard Sky.Live
  // sky-<event> attribute the renderer emits for each onClick /
  // onInput / etc. bound by Std.Ui's view helpers.
  var events = ["click", "input", "change", "submit", "keydown", "focus", "blur"];
  for (var i = 0; i < events.length; i++) {
    (function(ev) {
      consoleRoot.addEventListener(ev, function(e) {
        var t = e.target;
        if (!t || !t.closest) return;
        var el = t.closest('[sky-' + ev + ']');
        if (!el || !consoleRoot.contains(el)) return;
        var msg = el.getAttribute('sky-' + ev);
        if (!msg) return;
        if (ev === "submit") e.preventDefault();
        var args = [];
        if (ev === "input" || ev === "change") {
          if (t.type === "checkbox" || t.type === "radio") args.push(t.checked);
          else if (t.type === "number" || t.type === "range") args.push(t.valueAsNumber || 0);
          else args.push(t.value == null ? "" : String(t.value));
        } else if (ev === "submit") {
          var form = {};
          if (t.elements) {
            for (var j = 0; j < t.elements.length; j++) {
              var f = t.elements[j];
              if (!f.name || f.disabled) continue;
              if (f.type === "checkbox" || f.type === "radio") {
                if (f.checked) form[f.name] = f.value;
              } else if (f.type !== "submit" && f.type !== "button" && f.type !== "file") {
                form[f.name] = f.value;
              }
            }
          }
          args.push(form);
        } else if (ev === "keydown") {
          args.push(e.key || "");
        }
        sendEvent(msg, args);
      });
    })(events[i]);
  }

  openSSE();
})();
`
