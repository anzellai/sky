package skylive_rt

// LiveJS is the client-side JavaScript for Sky.Live, served at /_sky/live.js.
// It handles event binding, dispatching events to the server via POST,
// applying DOM patches, and client-side navigation.
const LiveJS = `(function() {
  'use strict';
  var root = document.querySelector('[sky-root]');
  if (!root) return;
  var sid = root.getAttribute('sky-root');

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
    // Input (debounced 150ms)
    root.querySelectorAll('[sky-input]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      var timer;
      el.addEventListener('input', function(e) {
        clearTimeout(timer);
        timer = setTimeout(function() {
          send(el.getAttribute('sky-input'), [e.target.value]);
        }, 150);
      });
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
    // Blur
    root.querySelectorAll('[sky-blur]').forEach(function(el) {
      if (el._skyBound) return;
      el._skyBound = true;
      el.addEventListener('blur', function() {
        send(el.getAttribute('sky-blur'), []);
      });
    });
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
          else el.setAttribute(k, p.attrs[k]);
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
  function connectSSE() {
    if (!window.EventSource) return;
    var es = new EventSource('/_sky/stream?sid=' + sid);
    es.onmessage = function(e) {
      try {
        var data = JSON.parse(e.data);
        applyPatches(data.patches || []);
        if (data.url) history.pushState({}, '', data.url);
        if (data.title) document.title = data.title;
      } catch(err) {}
    };
    es.onerror = function() {
      es.close();
      setTimeout(connectSSE, 3000);
    };
  }

  // ── Init ─────────────────────────────────────────────
  bind();
  connectSSE();
})();`
