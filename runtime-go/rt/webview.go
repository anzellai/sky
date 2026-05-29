// Sky.Webview — desktop UI backend (v0.1 MVP).
//
// Webview.app drives a native system webview (WKWebView on macOS,
// WebView2 on Windows, WebKitGTK on Linux) via the `webview_go`
// library. Same TEA shape as Sky.Live + Sky.Tui:
//
//	main =
//	    Webview.app
//	        { init = init
//	        , update = update
//	        , view = view
//	        , subscriptions = subscriptions
//	        , window = { title = "Sky Stopwatch", size = ( 800, 600 ) }
//	        }
//	        |> Task.run
//
// What's reused from Sky.Live:
//
//   - HtmlToVNode + assignSkyIDs + renderVNode + diffTrees
//   - The XSS-hardening JS helpers (focus-preserving DOM replacer,
//     __skyReviveScripts for late-injected <script> tags). The
//     concrete JS is sourced from webviewSharedJS below, which is
//     shape-compatible with Sky.Live's patches but doesn't include
//     the SSE / POST / CSRF / session-store wire (webview has no
//     wire — the bridge is `Bind` + `Eval` in-process).
//
// Threading model:
//
//   - `webview_go.Run()` blocks the calling OS thread for the
//     lifetime of the window (Cocoa main-thread requirement on
//     macOS — runtime.LockOSThread is invoked under the hood).
//   - All `SetHtml` / `Eval` calls from background goroutines MUST
//     route through `w.Dispatch(func() { ... })` so the webview's
//     own message pump applies them on the main thread.
//   - User Msgs flow into a bounded `msgCh chan any` and the update
//     loop runs on a dedicated goroutine (NOT the main thread —
//     `Run` owns that). When update produces a new VNode tree the
//     loop diffs against the previous, JSON-encodes the patches,
//     and asks the webview to apply them via Dispatch + Eval.
//
// v0.1 explicitly OUT of scope (deferred to v0.2 / v0.3):
//
//   - alwaysOnTop, transparent, decorated window flags
//   - Tray icons, global hotkeys, native file dialogs
//   - Std.Voice intents
//   - Windows / Linux platform validation (compiles + links, but
//     only smoke-tested on macOS for this MVP)
//
// Build constraints:
//
//   - cgo required (webview_go is a cgo binding into system WebKit /
//     WebView2 / WebKitGTK).
//   - v0.1 ships macOS only. Linux + Windows fall through to
//     webview_stub.go so the symbol is present at link time and
//     callers get a clear `Err Error` remediation rather than a
//     missing-pkg-config build failure. v0.2 will widen the tag once
//     Linux (webkit2gtk-4.0 / 4.1) + Windows (WebView2 SDK) smoke
//     validation lands.

//go:build cgo && darwin

package rt

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	webview "github.com/webview/webview_go"
)

// Webview_app is the Task-shaped entry point. Calling it returns a
// thunk; Task.run forces it and the loop blocks until the user closes
// the window OR a fatal init error occurs.
func Webview_app(cfg any) any {
	return func() any {
		return webviewAppRun(cfg)
	}
}

// webviewMsgChCap is the bounded msgCh capacity. Matches Sky.Tui's
// `make(chan any, 32)` — large enough to absorb a tight tick burst,
// small enough to surface a stuck update loop quickly.
const webviewMsgChCap = 32

// webviewMsgDropped counts msgCh-full incidents over the program's
// lifetime. Surfaced via the debug log so a user can spot a stuck
// update loop or a runaway publisher. NOT a metric (no observability
// federation in webview v0.1) — just a counter incremented under
// `webviewState.mu`.
type webviewState struct {
	mu          sync.Mutex
	msgDropped  uint64
	currentTree *VNode         // last committed render; nil on first paint
	handlers    map[string]any // <sky-id>.<event> → Msg ctor (rebuilt per render)
}

func (s *webviewState) bumpDropped() {
	s.mu.Lock()
	s.msgDropped++
	s.mu.Unlock()
}

func (s *webviewState) swapTree(newTree *VNode, newHandlers map[string]any) *VNode {
	s.mu.Lock()
	prev := s.currentTree
	s.currentTree = newTree
	s.handlers = newHandlers
	s.mu.Unlock()
	return prev
}

// lookupHandler maps the wire payload to the actual Sky Msg ctor.
// The shim sends `(handlerId, args)` so the server can recover the
// typed Msg through the renderer-built handlers map. Returns nil
// if the id is unknown — happens when a patch from a stale render
// is replayed during the brief window between view recomputation
// and patch application.
func (s *webviewState) lookupHandler(hid string) any {
	s.mu.Lock()
	h := s.handlers
	s.mu.Unlock()
	if h == nil {
		return nil
	}
	return h[hid]
}

// webviewAppRun implements the v0.1 MVP loop. Returns Ok(()) when the
// user closes the window cleanly; Err(...) on init failure.
func webviewAppRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	subsFn := Field(cfg, "Subscriptions")
	windowCfg := Field(cfg, "Window")
	if initFn == nil || updateFn == nil || viewFn == nil || windowCfg == nil {
		return Err[any, any](ErrInvalidInput(
			"Webview.app: cfg must define init / update / view / window"))
	}

	title := webviewFieldString(windowCfg, "Title", "Sky.Webview")
	width, height := webviewWindowSize(windowCfg)

	// `webview.New(true)` enables right-click → "Inspect Element"
	// on platforms that support it (macOS WKWebView ships DevTools
	// when the host binary is signed for dev). Toggling via
	// SKY_WEBVIEW_DEBUG keeps prod builds tighter; default on.
	debug := os.Getenv("SKY_WEBVIEW_DEBUG") != "0" &&
		os.Getenv("SKY_WEBVIEW_DEBUG") != "false"

	w := webview.New(debug)
	if w == nil {
		msg := "Webview.app: webview.New returned nil — system webview backend unavailable " +
			"(macOS: WKWebView ships with the OS; Windows: install Edge WebView2 runtime; " +
			"Linux: install libwebkit2gtk-4.0-37 or webkit2gtk4.0)"
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}
	defer w.Destroy()

	w.SetTitle(title)
	w.SetSize(width, height, webview.HintNone)

	state := &webviewState{}

	// msgCh: bounded TEA pipe. Bridge code (Bind callbacks) and
	// background sub goroutines push Msgs here; the update goroutine
	// drains. doneCh fires when webview.Run() returns.
	msgCh := make(chan any, webviewMsgChCap)
	doneCh := make(chan struct{})

	// `__skyDispatch` is the JS → Go bridge. The shim calls it with
	// (handlerId, args) on every typed DOM event. handlerId looks
	// up the actual Msg ctor (which renderVNode populated into the
	// handlers map at last render); applyMsgArgs decodes args.
	if err := w.Bind("__skyDispatch", func(handlerId string, args []any) error {
		ctor := state.lookupHandler(handlerId)
		if ctor == nil {
			// Stale handler from a previous render — silently drop;
			// next event will dispatch against the fresh handlers map.
			return nil
		}
		rawArgs := make([]json.RawMessage, len(args))
		for i, a := range args {
			b, err := json.Marshal(a)
			if err != nil {
				return err
			}
			rawArgs[i] = json.RawMessage(b)
		}
		msg := applyMsgArgs(ctor, rawArgs, "")
		if msg == nil {
			return nil
		}
		select {
		case msgCh <- msg:
		default:
			state.bumpDropped()
			fmt.Fprintf(os.Stderr,
				"[sky.webview] msgCh full (%d), dropped msg for handler %q (dropped total: %d)\n",
				webviewMsgChCap, handlerId, state.msgDropped+1)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[sky.webview] Bind __skyDispatch failed: %v\n", err)
		return Err[any, any](ErrIo("Webview.app: bind __skyDispatch: " + err.Error()))
	}

	// Console-bridge: forward `console.log(...)` from the webview's
	// JS context to the host stderr. Useful for debugging the patch
	// shim in dev; silently no-op'd in prod by suppressing the JS
	// caller behind a guard if needed.
	if err := w.Bind("__skyLog", func(level string, message string) error {
		fmt.Fprintf(os.Stderr, "[sky.webview/%s] %s\n", level, message)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[sky.webview] Bind __skyLog failed: %v\n", err)
	}

	// Install the patch-applier shim BEFORE the first render so the
	// initial SetHtml's onload handlers can call `__skyApplyPatches`
	// during late hydration.
	w.Init(webviewSharedJS)

	// Initial state: call init () to get (model, cmd).
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

	// First render: VNode tree → HTML body → SetHtml. renderVNode
	// populates the handlers map with every (sky-id, event) → Msg
	// ctor binding so __skyDispatch can recover the typed ctor
	// from the wire's handlerId.
	htmlAny := SkyCall(viewFn, model)
	tree := webviewBuildTree(htmlAny)
	handlers := map[string]any{}
	body := renderVNode(*tree, handlers)
	state.swapTree(tree, handlers)
	w.SetHtml(webviewPageWrap(body))

	// Subscription manager. Same shape as Sky.Tui / Sky.Cli — pushes
	// Sub.every ticks into msgCh.
	subMgr := newSubManager(msgCh)
	subMgr.update(subsFn, model)

	// Update loop on a background goroutine — `webview.Run()` owns
	// the calling OS thread until the window closes. The loop pulls
	// Msgs, runs update, diffs the new tree against the last one,
	// and Dispatches the resulting patch payload back to the main
	// thread for Eval. safeGo wraps panic recovery.
	safeGo("Webview.app update loop", func() {
		for {
			select {
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				newModel := cliApplyUpdate(updateFn, msg, model, msgCh)
				model = newModel
				subMgr.update(subsFn, model)

				// Compute new tree + diff. Render once to populate
				// the new handlers map; the rendered HTML is only
				// used when we have to fall back to a full SetHtml
				// (first-render edge).
				newHTMLAny := SkyCall(viewFn, model)
				newTree := webviewBuildTree(newHTMLAny)
				newHandlers := map[string]any{}
				newBody := renderVNode(*newTree, newHandlers)
				prev := state.swapTree(newTree, newHandlers)

				// First-render edge: prev guaranteed non-nil here
				// because the initial render swapped it in BEFORE
				// Run started. Belt-and-braces guard kept anyway.
				if prev == nil {
					html := webviewPageWrap(newBody)
					w.Dispatch(func() { w.SetHtml(html) })
					continue
				}

				patches := diffTrees(prev, newTree, nil)
				if len(patches) == 0 {
					continue
				}
				payload, err := json.Marshal(patches)
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"[sky.webview] patch marshal failed: %v\n", err)
					continue
				}
				js := fmt.Sprintf("__skyApplyPatches(%s);", string(payload))
				w.Dispatch(func() { w.Eval(js) })
			case <-doneCh:
				return
			}
		}
	})

	// Block until the window closes. After Run returns we tear down
	// the sub goroutines + signal the update loop to exit.
	w.Run()
	subMgr.stopAll()
	close(doneCh)

	return Ok[any, any](struct{}{})
}

// webviewBuildTree wraps HtmlToVNode + assignSkyIDs in one shot. The
// returned tree's children carry the same sky-id structural-path
// scheme as Sky.Live's, so the patch shim can address them by
// `[sky-id="…"]` selectors without divergence.
func webviewBuildTree(node any) *VNode {
	vn := HtmlToVNode(node)
	assignSkyIDs(&vn, "0")
	return &vn
}

// webviewPageWrap builds the document shell around the user's body.
// Mirror of live.go's page wrap, minus dev-banner / CSRF meta / SSE
// reconnect machinery. The CSS reset is reused verbatim so a single
// view function paints identically under Sky.Live and Sky.Webview.
//
// No <script> tag for the shim — it's already injected via
// `w.Init(webviewSharedJS)` which runs on every navigation BEFORE
// the page's own scripts. webviewSharedJS has an IIFE re-entry
// guard so double-injection (during hot reload) is a no-op.
func webviewPageWrap(body string) string {
	return fmt.Sprintf(
		`<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>%s</style></head><body><div id="sky-root">%s</div></body></html>`,
		liveBaseCSS, body)
}

// webviewFieldString reads a string-typed field from a record, falling
// back to the supplied default when absent or non-string.
func webviewFieldString(rec any, name, def string) string {
	v := Field(rec, name)
	if v == nil {
		return def
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

// webviewWindowSize extracts (width, height) from cfg.window.size.
// The Sky-side type is `( Int, Int )`. Defaults to 1024×720.
func webviewWindowSize(windowCfg any) (int, int) {
	const (
		defaultW = 1024
		defaultH = 720
	)
	size := Field(windowCfg, "Size")
	if size == nil {
		return defaultW, defaultH
	}
	w := AsInt(tupleFirst(size))
	h := AsInt(tupleSecond(size))
	if w <= 0 {
		w = defaultW
	}
	if h <= 0 {
		h = defaultH
	}
	return w, h
}

// webviewSharedJS is the JS shim injected into every Sky.Webview
// page. It re-implements the patch-applier subset of Sky.Live's
// inline JS using the same patch shape (`[{id, text, html, attrs,
// remove}]`) so the same diffTrees output drives both. The wire
// half — POST / SSE / CSRF — is replaced by the in-process
// `__skyDispatch` Bind callback.
//
// Hardening parity with Sky.Live:
//
//   - `__skyReviveScripts` re-injects late <script> tags so JS
//     bundles bootstrap when their host element first appears via
//     a patch (not the initial SSR).
//   - Focus / cursor / scroll preservation on input + textarea
//     value updates so a server-driven re-render doesn't yank
//     the user's caret.
//   - Open <select> defence: skip any patch that would mutate an
//     ancestor of the focused select (closing the dropdown
//     mid-pick).
//
// Differences vs Sky.Live:
//
//   - No POST / SSE / __skySend — the dispatch path is `window.
//     __skyDispatch(msgName, args)` directly into the bound Go
//     callback.
//   - No reconnect banner / queue / heartbeat — the bridge is
//     in-process; there's no network failure mode.
//   - No CSRF — same reason.
//   - No URL-from-Page / sky-nav — desktop apps don't have an
//     address bar.
//
// The two surfaces converge in v0.2 via the planned shared-JS
// extraction (a single `liveJSShared` const used by both shims).
const webviewSharedJS = `
(function () {
  if (window.__skyApplyPatches) { return; } // hot-reload safety
  function logTo(level) {
    return function (msg) {
      try { window.__skyLog && window.__skyLog(level, String(msg)); }
      catch (_) {}
    };
  }
  var __skyWarn = logTo("warn");

  function __skyEscapeHTML(s) {
    var d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function __skyContainsFocusedInput(el) {
    var a = document.activeElement;
    if (!a || a === document.body) return false;
    var tag = a.tagName;
    if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
    return el === a || el.contains(a);
  }

  // __skyReviveScripts: browsers DO NOT execute <script> tags
  // inserted via innerHTML / outerHTML / SetHtml. After every patch
  // that may have introduced new <script>s, walk the patched
  // subtree, clone each into a fresh <script> element, and replace
  // the inert original. This is the same logic used by Sky.Live so
  // late-hydration JS bundles (e.g. analytics, editors) bootstrap.
  function __skyReviveScripts(root) {
    if (!root || !root.querySelectorAll) return;
    var scripts = root.querySelectorAll("script");
    for (var i = 0; i < scripts.length; i++) {
      var old = scripts[i];
      if (old.__skyRevived) continue;
      var fresh = document.createElement("script");
      for (var j = 0; j < old.attributes.length; j++) {
        var a = old.attributes[j];
        fresh.setAttribute(a.name, a.value);
      }
      fresh.text = old.text;
      fresh.__skyRevived = true;
      old.parentNode.replaceChild(fresh, old);
    }
  }

  // __skyReplaceHTMLPreservingFocus: when we have to replace a
  // container's innerHTML, snapshot the currently focused INPUT /
  // TEXTAREA / SELECT before the swap and restore it after. Same
  // pattern as Sky.Live's preservation logic — copied verbatim
  // (modulo SSE-specific dirty-input bookkeeping) so the password-
  // manager re-prompt class of bug stays closed under webview.
  function __skyReplaceHTMLPreservingFocus(container, newHTML) {
    var active = document.activeElement;
    var saved = null;
    if (active && (active.tagName === "INPUT" || active.tagName === "TEXTAREA"
                   || active.tagName === "SELECT")
        && container.contains(active)) {
      saved = {
        tag: active.tagName,
        name: active.getAttribute("name"),
        skyId: active.getAttribute("sky-id"),
        selStart: null, selEnd: null, scrollTop: 0
      };
      try { saved.selStart = active.selectionStart; saved.selEnd = active.selectionEnd; }
      catch (_) {}
      saved.scrollTop = active.scrollTop;
    }
    container.innerHTML = newHTML;
    if (saved && saved.skyId) {
      var sel = '[sky-id="' + saved.skyId.replace(/"/g, '\\"') + '"]';
      var fresh = container.querySelector(sel);
      if (fresh && fresh.tagName === saved.tag) {
        try { fresh.focus(); } catch (_) {}
        if (saved.selStart !== null && typeof fresh.setSelectionRange === "function") {
          var len = (fresh.value || "").length;
          try {
            fresh.setSelectionRange(
              Math.min(saved.selStart, len),
              Math.min(saved.selEnd === null ? saved.selStart : saved.selEnd, len)
            );
          } catch (_) {}
        }
        if (saved.scrollTop) fresh.scrollTop = saved.scrollTop;
      }
    }
  }

  // __skyApplyPatches: identical wire shape to Sky.Live's. Walks
  // patches in order, applies via querySelector('[sky-id=…]'),
  // skips any whose target is inside the open <select> ancestor
  // chain. Revives scripts at the end so newly-mounted JS bundles
  // bootstrap.
  window.__skyApplyPatches = function (patches) {
    if (!patches || patches.length === 0) return;
    var openSel = (document.activeElement && document.activeElement.tagName === "SELECT")
        ? document.activeElement : null;
    for (var i = 0; i < patches.length; i++) {
      var p = patches[i];
      var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
      if (!el) continue;
      if (openSel && (el === openSel || el.contains(openSel) || openSel.contains(el))) {
        continue;
      }
      if (p.text !== undefined && p.text !== null) {
        if (__skyContainsFocusedInput(el)) {
          __skyReplaceHTMLPreservingFocus(el, __skyEscapeHTML(p.text));
        } else {
          el.textContent = p.text;
        }
      }
      if (p.html !== undefined && p.html !== null) {
        __skyReplaceHTMLPreservingFocus(el, p.html);
      }
      if (p.attrs) {
        var keys = Object.keys(p.attrs);
        var isInputLike = el.tagName === "INPUT" || el.tagName === "TEXTAREA";
        var hadFocus = isInputLike && el === document.activeElement;
        var savedSelStart = null, savedSelEnd = null, savedScrollTop = 0;
        if (hadFocus) {
          try { savedSelStart = el.selectionStart; savedSelEnd = el.selectionEnd; }
          catch (_) {}
          savedScrollTop = el.scrollTop;
        }
        var valueChanged = false;
        for (var j = 0; j < keys.length; j++) {
          var k = keys[j], v = p.attrs[k];
          if (v === "") { el.removeAttribute(k); }
          else {
            el.setAttribute(k, v);
            if (k === "value" && ("value" in el)) {
              el.value = v;
              valueChanged = true;
            }
            if (k === "checked") el.checked = v !== "" && v !== "false";
            if (k === "selected") el.selected = v !== "" && v !== "false";
            if (k === "disabled") el.disabled = v !== "" && v !== "false";
          }
        }
        if (hadFocus && valueChanged && savedSelStart !== null &&
            typeof el.setSelectionRange === "function") {
          var newLen = (el.value || "").length;
          var s = Math.min(savedSelStart, newLen);
          var e = Math.min(savedSelEnd === null ? s : savedSelEnd, newLen);
          try { el.setSelectionRange(s, e); } catch (_) {}
          if (savedScrollTop) el.scrollTop = savedScrollTop;
        }
      }
      if (p.remove) el.remove();
    }
    __skyBindEvents(document);
    var skyRoot = document.getElementById("sky-root");
    if (skyRoot) __skyReviveScripts(skyRoot);
  };

  // __skyBindEvents: walks the DOM for sky-<event> attributes and
  // wires native listeners. Each binding dispatches through
  // window.__skyDispatch(msgName, args), the Go-side Bind callback.
  // Re-run after every patch because new sky-* attrs may have
  // appeared.
  var __skyBoundSentinel = "__skySkyBound";
  function __skyBindEvents(root) {
    root = root || document;
    var events = ["click", "input", "change", "submit", "focus", "blur",
                  "keydown", "keyup", "keypress", "mouseover", "mouseout"];
    for (var ei = 0; ei < events.length; ei++) {
      var ev = events[ei];
      var sel = "[sky-" + ev + "]";
      var nodes = root.querySelectorAll(sel);
      for (var ni = 0; ni < nodes.length; ni++) {
        var n = nodes[ni];
        var key = __skyBoundSentinel + "_" + ev;
        if (n[key]) continue;
        n[key] = true;
        n.addEventListener(ev, makeHandler(ev));
      }
    }
  }

  function makeHandler(ev) {
    return function (e) {
      var t = e.currentTarget;
      // data-sky-hid carries the handler id (sky-id.event); the
      // Go-side handlers map looks up the actual Msg ctor from
      // this. sky-<event> still holds the display name for
      // debugging — but it's NOT the dispatch key.
      var hid = t.getAttribute("data-sky-hid");
      if (!hid) return;
      var args = [];
      switch (ev) {
        case "submit":
          e.preventDefault();
          var fd = new FormData(t);
          var obj = {};
          fd.forEach(function (v, k) { obj[k] = v; });
          args = [obj];
          break;
        case "input":
        case "change":
          if (t.type === "checkbox" || t.type === "radio") {
            args = [t.checked];
          } else if (t.type === "number" || t.type === "range") {
            args = [parseFloat(t.value)];
          } else {
            args = [t.value];
          }
          break;
        case "keydown":
        case "keyup":
        case "keypress":
          args = [e.key];
          break;
      }
      try { window.__skyDispatch(hid, args); }
      catch (err) { __skyWarn("__skyDispatch failed: " + err); }
    };
  }

  // Initial bind after the document is ready. SetHtml's body lands
  // synchronously so DOMContentLoaded has already fired by the time
  // Init's script runs — we bind immediately. The patch path
  // re-binds inside __skyApplyPatches.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { __skyBindEvents(document); });
  } else {
    __skyBindEvents(document);
  }
})();
`
