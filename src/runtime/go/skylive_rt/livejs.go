package skylive_rt

// LiveJS is the client-side JavaScript for Sky.Live, served at /_sky/live.js.
// It handles event binding, dispatching events to the server via POST,
// applying DOM patches, client-side navigation, SSE, and polling fallback.
const LiveJS = `(function() {
  'use strict';
  var root = document.querySelector('[sky-root]');
  if (!root) return;
  var sid = root.getAttribute('sky-root');
  var cfg = { inputMode: 'debounce', pollInterval: 0 };

  // Load server config (input mode, poll interval)
  fetch('/_sky/config').then(function(r) { return r.json(); }).then(function(c) {
    cfg = c;
    if (cfg.inputMode === 'blur') rebindInputs();
    if (cfg.pollInterval > 0) startPolling();
  }).catch(function() {});

  // ── Event Binding ────────────────────────────────────
  function bind() {
    // Click
    root.querySelectorAll('[sky-click]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('click', function() {
        send(el.getAttribute('sky-click'), jsonArgs(el));
      });
    });
    // Double click
    root.querySelectorAll('[sky-dblclick]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('dblclick', function() {
        send(el.getAttribute('sky-dblclick'), jsonArgs(el));
      });
    });
    // Input — mode depends on config
    root.querySelectorAll('[sky-input]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      if (cfg.inputMode === 'blur') {
        // Blur mode: only send on blur/enter, keep input client-side
        el.addEventListener('blur', function(e) {
          send(el.getAttribute('sky-input'), [e.target.value]);
        });
        el.addEventListener('keydown', function(e) {
          if (e.key === 'Enter' && el.tagName !== 'TEXTAREA') {
            send(el.getAttribute('sky-input'), [e.target.value]);
          }
        });
      } else {
        // Debounce mode: send after 150ms pause in typing
        var timer;
        el.addEventListener('input', function(e) {
          clearTimeout(timer);
          timer = setTimeout(function() {
            send(el.getAttribute('sky-input'), [e.target.value]);
          }, 150);
        });
      }
    });
    // Change
    root.querySelectorAll('[sky-change]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('change', function(e) {
        send(el.getAttribute('sky-change'), [e.target.value]);
      });
    });
    // Submit
    root.querySelectorAll('[sky-submit]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('submit', function(e) {
        e.preventDefault();
        var data = {};
        new FormData(e.target).forEach(function(v, k) { data[k] = v; });
        send(el.getAttribute('sky-submit'), [data]);
      });
    });
    // Focus
    root.querySelectorAll('[sky-focus]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('focus', function() {
        send(el.getAttribute('sky-focus'), []);
      });
    });
    // Blur (explicit sky-blur, separate from input blur mode)
    root.querySelectorAll('[sky-blur]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('blur', function() {
        send(el.getAttribute('sky-blur'), []);
      });
    });
    // Image input: reads file, resizes, compresses, sends base64 data URL
    root.querySelectorAll('[sky-image]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('change', function(e) {
        var f = e.target.files[0];
        if (!f) return;
        var maxW = parseInt(el.getAttribute('sky-file-width') || '1200');
        var maxH = parseInt(el.getAttribute('sky-file-height') || '1200');
        _skyResizeImage(f, maxW, maxH, function(result) {
          send(el.getAttribute('sky-image'), [result]);
        });
      });
    });
    // Generic file input: reads file as base64, sends data URL (no compression)
    root.querySelectorAll('[sky-file]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('change', function(e) {
        var f = e.target.files[0];
        if (!f) return;
        var r = new FileReader();
        r.onload = function(ev) {
          send(el.getAttribute('sky-file'), [ev.target.result]);
        };
        r.readAsDataURL(f);
      });
    });
  }

  function _skyResizeImage(file, maxW, maxH, cb) {
    var img = new Image();
    var url = URL.createObjectURL(file);
    img.onload = function() {
      URL.revokeObjectURL(url);
      var w = img.width, h = img.height;
      if (w > maxW) { h = Math.round(h * maxW / w); w = maxW; }
      if (h > maxH) { w = Math.round(w * maxH / h); h = maxH; }
      var canvas = document.createElement('canvas');
      canvas.width = w; canvas.height = h;
      canvas.getContext('2d').drawImage(img, 0, 0, w, h);
      cb(canvas.toDataURL('image/jpeg', 0.85));
    };
    img.src = url;
  }

  function rebindInputs() {
    // Force re-bind inputs after config loads with blur mode
    root.querySelectorAll('[sky-input]').forEach(function(el) {
      el._skyBound = false;
    });
    bind();
  }

  function jsonArgs(el) {
    var raw = el.getAttribute('sky-args');
    return raw ? JSON.parse(raw) : [];
  }

  // ── Event Dispatch ───────────────────────────────────
  var pending = false;
  var queue = [];

  function send(msg, args) {
    if (!sid) return;
    if (pending) {
      queue.push([msg, args]);
      return;
    }
    pending = true;
    fetch('/_sky/event', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ msg: msg, args: args || [], sid: sid })
    })
    .then(function(res) {
      if (res.status === 410) { location.reload(); return null; }
      if (!res.ok) return null;
      return res.json();
    })
    .then(function(data) {
      pending = false;
      if (data) {
        applyPatches(data.patches || []);
        if (data.url) history.pushState({}, '', data.url);
        if (data.title) document.title = data.title;
      }
      // Process queued events
      if (queue.length > 0) {
        var next = queue.shift();
        send(next[0], next[1]);
      }
    })
    .catch(function() { pending = false; });
  }

  // ── DOM Patching ─────────────────────────────────────
  function applyPatches(patches) {
    for (var i = 0; i < patches.length; i++) {
      var p = patches[i];
      var el = root.querySelector('[sky-id="' + p.id + '"]');
      if (!el) continue;
      if (p.text !== undefined && p.text !== null) el.textContent = p.text;
      if (p.html !== undefined && p.html !== null) el.innerHTML = p.html;
      if (p.attrs) {
        var keys = Object.keys(p.attrs);
        for (var j = 0; j < keys.length; j++) {
          var k = keys[j];
          if (p.attrs[k] === null) el.removeAttribute(k);
          else {
            el.setAttribute(k, p.attrs[k]);
            // Sync DOM properties that don't reflect from attributes
            if (k === 'value' && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT')) {
              el.value = p.attrs[k];
            } else if (k === 'checked' && el.tagName === 'INPUT') {
              el.checked = p.attrs[k] !== 'false' && p.attrs[k] !== '';
            }
          }
        }
      }
      if (p.remove) el.remove();
      if (p.append) el.insertAdjacentHTML('beforeend', p.append);
    }
    // Re-bind events for any new nodes
    bind();
  }

  // ── Client-Side Navigation ───────────────────────────
  document.addEventListener('click', function(e) {
    var link = e.target.closest ? e.target.closest('[sky-nav]') : null;
    if (!link) return;
    // Allow ctrl/cmd+click to open in new tab
    if (e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
    var href = link.getAttribute('href');
    if (href === location.pathname) return;
    history.pushState({}, '', href);
    navigateTo(href);
  });

  function navigateTo(path) {
    fetch('/_sky/resolve?path=' + encodeURIComponent(path))
    .then(function(res) { return res.json(); })
    .then(function(data) {
      if (data.msg) send(data.msg, data.args || []);
    });
  }

  window.addEventListener('popstate', function() {
    navigateTo(location.pathname);
  });

  // ── SSE (Server Push) ────────────────────────────────
  var sseActive = false;
  function connectSSE() {
    if (!window.EventSource) return;
    var es = new EventSource('/_sky/stream?sid=' + sid);
    es.onopen = function() { sseActive = true; };
    es.onmessage = function(e) {
      try {
        var data = JSON.parse(e.data);
        applyPatches(data.patches || []);
        if (data.url) history.pushState({}, '', data.url);
        if (data.title) document.title = data.title;
      } catch(err) {}
    };
    es.onerror = function() {
      sseActive = false;
      es.close();
      // If polling is configured, don't reconnect SSE — polling takes over
      if (cfg.pollInterval > 0) return;
      setTimeout(connectSSE, 3000);
    };
  }

  // ── Polling Fallback ─────────────────────────────────
  function startPolling() {
    setInterval(function() {
      if (sseActive) return; // SSE is working, skip polling
      fetch('/_sky/poll?sid=' + sid)
      .then(function(res) {
        if (res.status === 410) { location.reload(); return null; }
        if (!res.ok) return null;
        return res.json();
      })
      .then(function(data) {
        if (data) {
          applyPatches(data.patches || []);
          if (data.url) history.pushState({}, '', data.url);
          if (data.title) document.title = data.title;
        }
      })
      .catch(function() {});
    }, cfg.pollInterval || 5000);
  }

  // ── Public API ──────────────────────────────────────
  // Expose send() so custom client-side JS (e.g. Firebase Auth)
  // can dispatch Sky.Live events programmatically.
  window.__sky_send = function(msg, args) { send(msg, args || []); };

  // ── Init ─────────────────────────────────────────────
  bind();
  connectSSE();
})();`
