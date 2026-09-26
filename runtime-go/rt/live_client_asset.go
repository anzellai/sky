//go:build !js

package rt

// The Sky.Live browser client, as ONE same-origin static asset.
//
// Every page Sky serves must run under a strict Content-Security-Policy
// (script-src 'self' 'wasm-unsafe-eval', no hashes, no 'unsafe-inline'). The
// client used to be a fmt.Sprintf template inlined into every page with the
// session id and CSRF token spliced in as JS literals, so a proxy sending that
// policy blocked it and the page (the Sky Console included) was dead.
//
// Now the script is a constant, served at liveClientPath
// (/_sky/live.<sha256[:12]>.js, under the app's base path for a sub-app) with
// an immutable cache header, and the per-page values ride in a
// <script type="application/json" id="sky-live-cfg"> block (liveCfgBlock). The
// regression gates are csp_strict_test.go and scripts/csp-e2e.sh.

import (
	"encoding/json"
)

// liveBootCfg is the per-page data the client reads from #sky-live-cfg.
type liveBootCfg struct {
	Sid              string `json:"sid"`
	Base             string `json:"base"`
	View             string `json:"view"`
	Csrf             string `json:"csrf"`
	BannerEnabled    bool   `json:"bannerEnabled"`
	RetryBaseMs      int    `json:"retryBaseMs"`
	RetryMaxMs       int    `json:"retryMaxMs"`
	RetryMaxAttempts int    `json:"retryMaxAttempts"`
	EventQueueMax    int    `json:"eventQueueMax"`
	MsgReconnecting  string `json:"msgReconnecting"`
	MsgOffline       string `json:"msgOffline"`
	HelloTimeoutMs   int    `json:"helloTimeoutMs"`
	HeartbeatTtlMs   int    `json:"heartbeatTtlMs"`
}

func newLiveBootCfg(sid string, cfg liveBannerConfig, csrfToken, basePath, view string) liveBootCfg {
	return liveBootCfg{
		Sid: sid, Base: basePath, View: view, Csrf: csrfToken,
		BannerEnabled: cfg.Enabled, RetryBaseMs: cfg.BaseMs, RetryMaxMs: cfg.MaxMs,
		RetryMaxAttempts: cfg.MaxAttempts, EventQueueMax: cfg.QueueMax,
		MsgReconnecting: cfg.Reconnecting, MsgOffline: cfg.Offline,
		HelloTimeoutMs: cfg.HelloTimeoutMs, HeartbeatTtlMs: cfg.HeartbeatTtlMs,
	}
}

// liveCfgBlock renders the non-executable config block. json.Marshal escapes
// <, > and & as \u003c / \u003e / \u0026, so no value can close the element.
func liveCfgBlock(c liveBootCfg) string {
	b, err := json.Marshal(c)
	if err != nil {
		b = []byte("{}")
	}
	return `<script type="application/json" id="sky-live-cfg">` + string(b) + `</script>`
}

// livePageScripts is the tail of every Sky.Live page: the config block, then
// the external client. The config block MUST directly follow </div> of
// #sky-root: the client's __skyPatch strips a full-page sky-nav response with
// /<div id="sky-root">(…)<\/div><script type="application\/json" id="sky-live-cfg">/.
func livePageScripts(sid string, cfg liveBannerConfig, csrfToken, basePath, view string) string {
	return liveCfgBlock(newLiveBootCfg(sid, cfg, csrfToken, basePath, view)) +
		`<script src="` + basePath + liveClientPath + `"></script>`
}

// liveClientPath is the client's URL path relative to the app's base path.
var liveClientPath = "/_sky/live." + assetHash(liveClientJS) + ".js"

// liveClientJS is the whole Sky.Live browser client.
const liveClientJS = `// Sky.Live client (runtime-go/rt/live_client_asset.go). Served as the
// same-origin, content-hashed file /_sky/live.<hash>.js so a strict
// Content-Security-Policy (script-src 'self') runs it. Per-page values come
// from the non-executable <script type="application/json" id="sky-live-cfg">
// block the page carries just before this script.
var __skyCfg = (function () {
  try {
    var el = document.getElementById("sky-live-cfg");
    return el ? (JSON.parse(el.textContent || "{}") || {}) : {};
  } catch (e) {
    return {};
  }
})();
var __skySid = __skyCfg.sid || "";
var __skyBase = __skyCfg.base || "";
var __skyView = __skyCfg.view || "";
// __skyTabId — a per-PAGE id generated once on load (Phase 1 multi-tab
// fan-out). Sent on the SSE query (?tab=) so the server can address this
// connection, and on every event POST so the server excludes THIS tab
// from the dispatch broadcast (it already applied the patch on its HTTP
// response). Two tabs of one session get distinct ids; a reload mints a
// fresh one. Random base36 — URL-safe, no escaping needed.
var __skyTabId = (Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2));
// Tracks the last settled URL path so a patch can tell a real page navigation
// (scroll the new page to the top, like a normal browser navigation) from an
// in-place SSE/event update on the same page (leave the user's scroll alone).
var __skyLastPath = location.pathname;
var __skyCsrfToken = __skyCfg.csrf || "";
var __skyBannerEnabled = !!__skyCfg.bannerEnabled;
var __skyRetryBaseMs = __skyCfg.retryBaseMs;
var __skyRetryMaxMs = __skyCfg.retryMaxMs;
var __skyRetryMaxAttempts = __skyCfg.retryMaxAttempts;
var __skyEventQueueMax = __skyCfg.eventQueueMax;
var __skyMsgReconnecting = __skyCfg.msgReconnecting || "";
var __skyMsgOffline = __skyCfg.msgOffline || "";
var __skyHelloTimeoutMs = __skyCfg.helloTimeoutMs;
var __skyHeartbeatTtlMs = __skyCfg.heartbeatTtlMs;

// ── Input authority protocol state ───────────────────────────
// See docs/skylive/input-authority-protocol.md §Client state.
// Step 2 populates these counters + per-input table on every send
// and response; Step 3 activates the patch filter that reads them;
// Step 4 activates the stale-drop test against __skyLastAppliedSeq.
//
// Cycle 3 P47 (pub/sub global+local seq split — see
// docs/skylive/pubsub-design.md §3.2): __skyLastGlobalSeq is the
// app-wide broadcast counter. The server stamps it onto every
// broadcast-derived SSE frame (event:patches OR event:patch); the
// client dedupes against the largest value already applied so a
// replayed broadcast (e.g. SSE reconnect that re-delivers buffered
// frames) drops at the boundary without mutating state twice. Frames
// from per-session dispatch (the common case) carry globalSeq=0 OR
// omit the field; the guard treats 0 / missing as "no broadcast
// ordering constraint" and never blocks.
var __skyClientSeq = 0;       // monotonic, client-owned; bumped on every __skySend
var __skyLastAppliedSeq = 0;  // server-owned; largest local seq already applied
var __skyLastGlobalSeq = 0;   // server-owned; largest broadcast globalSeq already applied (P47)
var __skyInputs = {};         // sky-id → InputEntry (populated by __skyBindOne)

// ── View identity (runtime-go/rt/live_view_version.go) ────────────
// __skyView is the content id of the body the DOM shows. Every event
// carries it, so the server resolves the handler against THAT render and
// never against a newer one (a click on row b made before the reply to a
// click on row a arrived must delete b). Delta frames name the render
// they were diffed against (base): one that arrives before its base is
// HELD until the base lands, then applied in order — or, if the base
// never arrives, a resync replaces the DOM. Out-of-order frames are no
// longer dropped for good (the stuck "loading" state).
var __skyPendingFrames = [];
var __skyGapTimer = null;
var __skyGapWaitMs = 1500;
var __skyProcEpoch = null;   // server process epoch from the SSE hello
var __skyBaseBySeq = {};     // event seq -> {sid, value} of the focused field when it was sent
var __skyRebaseBase = null;  // the entry of the reply being applied (see __skyRebase)

function __skyInputEntry(sid) {
  var e = __skyInputs[sid];
  if (!e) {
    e = __skyInputs[sid] = {
      liveValue: "", lastSentSeq: 0, lastAckedSeq: 0,
      pendingDebounceId: null, pendingSend: null
    };
  }
  return e;
}

// __skyInputsSnapshot — dirty-input projection bundled into every
// outgoing event. Only entries whose user-typed value is newer than
// the server's latest ack are included, so the wire stays compact
// when the client and server agree.
function __skyInputsSnapshot() {
  var out = null;
  var ids = Object.keys(__skyInputs);
  for (var i = 0; i < ids.length; i++) {
    var e = __skyInputs[ids[i]];
    if (e.lastSentSeq <= e.lastAckedSeq) continue;
    if (!out) out = {};
    out[ids[i]] = {value: e.liveValue, seq: e.lastSentSeq};
  }
  return out;
}

// __skyIngestSeq — fold a response or SSE frame's {seq, ackInputs}
// into client state. seq advances __skyLastAppliedSeq monotonically;
// ackInputs retires per-input dirty flags so the next snapshot omits
// caught-up fields.
// __skyIsDirty — a typable form field (input / textarea / select)
// whose DOM state is authoritative over the server's view. The check
// is scoped to those tags ONLY: buttons, anchors, divs and other
// focused-but-non-typable elements have no keystrokes to preserve,
// so treating them as dirty would wrongly block patches that wipe
// their containing subtree (e.g. navigating from a "new game"
// screen into a board view, where the focused button legitimately
// disappears). Scope signals: focus, pending debounce keyed by
// data-sky-hid, or an unacked typed value at the input's sky-id.
function __skyIsDirty(el) {
  if (!el || el.nodeType !== 1) return false;
  var tag = el.tagName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
  // IME composition in progress: the value is the pre-edit, never the
  // user's final text.
  if (el.__skyComposing) return true;
  var sid = el.getAttribute && el.getAttribute("sky-id");
  if (sid) {
    // Keystrokes waiting for their debounce.
    if (__skyInputPending[__skyHid(el, "input")]) return true;
    // Keystrokes sent but not yet acked by the server.
    var e = __skyInputs[sid];
    if (e && e.lastSentSeq > e.lastAckedSeq) return true;
    // A tracked input the server has acked is NOT dirty, even while it
    // has focus: a programmatic model value (a clear, a normalisation)
    // applies once the user's own keystrokes are acknowledged.
    if (e) return false;
  }
  // An input without a tracked handler (no sky-input): the server never
  // hears its keystrokes, so the only evidence of typing is focus plus
  // an edit since it took focus.
  return el === document.activeElement && !!el.__skyTyped;
}

function __skyIngestSeq(seq, ackInputs, globalSeq) {
  if (typeof seq === "number" && seq > __skyLastAppliedSeq) {
    __skyLastAppliedSeq = seq;
  }
  // Cycle 3 P47: monotonic-applied semantics on the broadcast counter,
  // mirroring the local-seq path. Missing / zero / non-numeric globalSeq
  // is treated as "no broadcast ordering constraint" and ignored.
  if (typeof globalSeq === "number" && globalSeq > __skyLastGlobalSeq) {
    __skyLastGlobalSeq = globalSeq;
  }
  if (ackInputs) {
    var ids = Object.keys(ackInputs);
    for (var i = 0; i < ids.length; i++) {
      var e = __skyInputs[ids[i]];
      if (!e) continue;
      var n = ackInputs[ids[i]];
      if (n > e.lastAckedSeq) e.lastAckedSeq = n;
    }
  }
}

// __skyHandleResponse — gate DOM-mutating work behind the monotonic
// seq check (Step 4 / I2). An out-of-order or replayed frame with
// seq ≤ __skyLastAppliedSeq is dropped entirely: a newer frame has
// already landed with a later view, and applying the stale payload
// would regress the DOM. Legacy frames that omit seq (or report 0)
// always apply — pre-upgrade servers keep working.
//
// Cycle 3 P47 (pub/sub global+local seq split — see
// docs/skylive/pubsub-design.md §3.2): broadcast-derived frames also
// carry an OPTIONAL globalSeq. If supplied AND already applied (i.e.
// globalSeq > 0 && globalSeq <= __skyLastGlobalSeq) the frame is
// dropped — a replayed broadcast (e.g. an SSE reconnect re-delivering
// buffered frames) would otherwise mutate state twice. Both guards
// fire independently: a frame is dropped if EITHER counter has already
// passed it; the localSeq guard alone suffices for the legacy
// non-broadcast case (globalSeq omitted / 0 → broadcast guard always
// passes).
//
// View identity: "view" is the render the frame brings the DOM to;
// "delta" frames (patch lists) also name their "base" render and apply
// only on top of it — see __skyHoldFrame.
function __skyHandleResponse(seq, ackInputs, applyFn, globalSeq, view, base, delta) {
  if (typeof seq === "number" && seq > 0 && seq <= __skyLastAppliedSeq) {
    return; // stale — a newer local-seq frame already landed
  }
  if (typeof globalSeq === "number" && globalSeq > 0 && globalSeq <= __skyLastGlobalSeq) {
    return; // stale — a newer broadcast frame already landed
  }
  if (delta && base && __skyView && base !== __skyView) {
    // A delta computed against a render this DOM does not show yet (a
    // frame overtook an earlier one). Hold it; applying it now would
    // corrupt the DOM, dropping it would lose the change for good.
    __skyHoldFrame({seq: seq, ackInputs: ackInputs, apply: applyFn,
                    globalSeq: globalSeq, view: view, base: base});
    return;
  }
  __skyIngestSeq(seq, ackInputs, globalSeq);
  applyFn();
  if (view) __skyView = view;
  __skyReleaseHeld();
}

function __skyHoldFrame(f) {
  __skyPendingFrames.push(f);
  __skyPendingFrames.sort(function(a, b) { return (a.seq || 0) - (b.seq || 0); });
  if (__skyPendingFrames.length > 32) __skyPendingFrames.shift();
  if (__skyGapTimer === null) {
    __skyGapTimer = setTimeout(function() {
      __skyGapTimer = null;
      if (__skyPendingFrames.length > 0) __skyRequestResync();
    }, __skyGapWaitMs);
  }
}

// __skyReleaseHeld applies every held delta whose base the DOM now
// shows, in seq order, and discards held frames a newer frame superseded.
function __skyReleaseHeld() {
  var progressed = true;
  while (progressed && __skyPendingFrames.length > 0) {
    progressed = false;
    for (var i = 0; i < __skyPendingFrames.length; i++) {
      var f = __skyPendingFrames[i];
      if (typeof f.seq === "number" && f.seq > 0 && f.seq <= __skyLastAppliedSeq) {
        __skyPendingFrames.splice(i, 1);
        i--;
        continue;
      }
      if (!f.base || f.base === __skyView) {
        __skyPendingFrames.splice(i, 1);
        __skyIngestSeq(f.seq, f.ackInputs, f.globalSeq);
        f.apply();
        if (f.view) __skyView = f.view;
        progressed = true;
        break;
      }
    }
  }
  if (__skyPendingFrames.length === 0 && __skyGapTimer !== null) {
    clearTimeout(__skyGapTimer);
    __skyGapTimer = null;
  }
}

// __skyRequestResync asks for the current view as a full body. Used when
// a held delta's base never arrived. No Msg is dispatched.
function __skyRequestResync() {
  __skyClientSeq++;
  __skyPostEvent({sessionId: __skySid, seq: __skyClientSeq, msg: "__skyResync",
                  args: [], handlerId: "", tab: __skyTabId});
}

// __skyRebase: the reply to an event sets a new value for the focused
// field, but the user has typed on since the event left (a chat composer:
// type, Enter, keep typing). The field is dirty, so the value would be
// dropped and the old text kept: "first" + "second" became "firstsecond"
// although update cleared the draft on Enter. When the field still starts
// with its value at send time and only has text appended, the appended text
// is the user's edit ON TOP of the server's value: apply the server's value,
// keep the appended text after it, and send the result. Any other edit
// (inside the old text) keeps the DOM, as before.
function __skyRebase(el, serverValue) {
  var b = __skyRebaseBase;
  if (!b || el.__skyComposing || el.getAttribute("sky-id") !== b.sid) return false;
  var cur = String(el.value == null ? "" : el.value);
  var sv = String(serverValue == null ? "" : serverValue);
  if (sv === b.value || cur.length <= b.value.length || cur.lastIndexOf(b.value, 0) !== 0) return false;
  el.value = sv + cur.slice(b.value.length);
  var end = el.value.length;
  try { el.setSelectionRange(end, end); } catch (_) {}
  if (el.hasAttribute("sky-input")) {
    __skyDispatchInput(el, el.getAttribute("sky-input"), __skyHid(el, "input"), [el.value]);
  }
  return true;
}

// __skyShowError: a small runtime banner for a classified update panic,
// shown whether or not the app's model has a Notification field.
var __skyErrorEl = null;
var __skyErrorTimer = null;
function __skyShowError(ref) {
  if (!document.body) return;
  if (!__skyErrorEl) {
    __skyErrorEl = document.createElement("div");
    __skyErrorEl.id = "__sky-error";
    __skyErrorEl.setAttribute("role", "alert");
    __skyErrorEl.style.cssText = [
      "position:fixed", "left:50%", "top:16px", "transform:translateX(-50%)",
      "padding:8px 16px", "border-radius:6px",
      "font:13px/1.4 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif",
      "color:#fff", "background:#b91c1c", "box-shadow:0 2px 8px rgba(0,0,0,0.25)",
      "z-index:2147483647", "pointer-events:none"
    ].join(";");
    document.body.appendChild(__skyErrorEl);
  }
  __skyErrorEl.textContent = "Something went wrong" + (ref ? " (ref " + ref + ")" : "") +
      ". Your last action was not applied.";
  __skyErrorEl.style.display = "block";
  clearTimeout(__skyErrorTimer);
  __skyErrorTimer = setTimeout(function() {
    if (__skyErrorEl) __skyErrorEl.style.display = "none";
  }, 8000);
}

// ── Focus preservation via node identity ────────────────────
// Sky.Live renders subtrees via innerHTML replacement (both on JSON
// patches that carry p.html and on full-HTML navigations). Plain
// innerHTML DESTROYS the focused input element — even though JS is
// single-threaded, the browser's internal input-method editor (IME),
// autofill popover, undo stack, composition state, pointer-cursor
// blink, password manager affordances, and native caret are all
// tied to the live DOM NODE. Destroying it and recreating a clone
// with the same .value loses every one of those.
//
// The correct fix is to preserve node identity through the swap:
// before the replacement, locate the focused INPUT / TEXTAREA /
// SELECT, find its placeholder in the new HTML (by sky-id → name),
// then SPLICE the live node into the new tree in place of the
// placeholder. Server-side attrs (class, type, placeholder, ...)
// get copied onto the live node, EXCEPT value/checked/selected —
// those stay under user authority.
//
// The live node never gets "destroyed" — it only moves between
// parents. .value, .selectionStart, IME state, composition buffer,
// autofill state all survive. Keystrokes in flight land on the
// same node regardless of where the browser has currently attached
// it in the DOM tree.
//
// Re-focus at the end because replaceChild on a focused element
// temporarily blurs it (focus isn't a property of the node, it's
// a property of the document). Selection is lost and must be
// restored too.

// __skyPlaceholderUncontrolled — true when the server-rendered
// element has no authority attribute set (no value/checked/selected,
// no textarea content, no option[selected]). For these the user-
// owned client state is canonical; we splice the live node across
// the swap so the user's typing isn't blanked. See
// docs/skylive/input-authority-protocol.md §I6 (full-body
// preservation).
function __skyPlaceholderUncontrolled(placeholder) {
  if (!placeholder) return false;
  if (placeholder.hasAttribute("value")) return false;
  if (placeholder.hasAttribute("checked")) return false;
  if (placeholder.hasAttribute("selected")) return false;
  var tag = placeholder.tagName;
  if (tag === "TEXTAREA") {
    return (placeholder.textContent || "").length === 0;
  }
  if (tag === "SELECT") {
    return placeholder.querySelectorAll("option[selected]").length === 0;
  }
  // type=file: browsers refuse programmatic value assignment, the
  // user's selection is the only truth — always treat as uncontrolled.
  if (tag === "INPUT" && placeholder.getAttribute("type") === "file") return true;
  return true;
}

// __skyFindPlaceholder — locate a live input's slot in the new tree.
// Prefer sky-id (structurally stable + uniquely keyed). Fall back to
// tag+name only when the live element has no sky-id AND the new tree
// has exactly one match — preventing wrong-input collisions when
// names recur (e.g. multiple address forms with name="line1").
function __skyFindPlaceholder(tmp, live) {
  var sid = live.getAttribute && live.getAttribute("sky-id");
  if (sid) {
    var bySid = tmp.querySelector('[sky-id="' + sid.replace(/"/g, '\\"') + '"]');
    if (bySid) return bySid;
  }
  var name = live.getAttribute && live.getAttribute("name");
  if (!name) return null;
  var tag = live.tagName.toLowerCase();
  var matches = tmp.querySelectorAll(tag + '[name="' + name.replace(/"/g, '\\"') + '"]');
  if (matches.length === 1) return matches[0];
  return null;
}

// __skyReplaceHTMLPreservingFocus — the authoritative swap.
// Drop-in for plain innerHTML assignment that keeps:
//   1. The currently-focused input (.value, IME state, composition
//      buffer, selection range, scroll position).
//   2. EVERY uncontrolled input/textarea/select in the subtree
//      (anything the server didn't render an authority attribute for).
//      Without this, an unfocused password field gets recreated by the
//      innerHTML swap and the user's typed secret is blanked — see
//      Bug 2 in docs/skylive/architecture.md §Input preservation.
// Used by both __skyPatch (full body) and __skyApplyPatches (p.html
// and large p.text patches).
function __skyReplaceHTMLPreservingFocus(container, newHTML) {
  var focused = document.activeElement;
  var focusedInside = focused && focused !== document.body &&
      container.contains(focused) &&
      (focused.tagName === "INPUT" ||
       focused.tagName === "TEXTAREA" ||
       focused.tagName === "SELECT");

  // Parse the new HTML into a detached element so we can splice
  // preserved live nodes into it before committing.
  //
  // Namespace correctness: when the container element is in a foreign-
  // content namespace (SVG or MathML), parsing the new HTML via a
  // plain document.createElement("div") + .innerHTML = ... uses the
  // HTML insertion mode, so element names like <g>, <rect>, <text>
  // (which the diff emits as direct children when it replaces the
  // children of an <svg> element) end up in the XHTML namespace
  // rather than SVG. The elements appear in the DOM but the browser
  // doesn't lay them out as SVG primitives — the canvas silently goes
  // blank after a shape add/remove with no JS error to point at.
  //
  // Range.createContextualFragment parses HTML using the namespace
  // context of the range's container, preserving SVG/MathML element
  // namespaces correctly. The downstream code accepts either an
  // Element or a DocumentFragment via the same .firstChild /
  // .querySelectorAll / .parentNode.replaceChild surface, so no
  // other changes are needed.
  //
  // Repro before this fix: any Sky.Live view that emits an HTML
  // patch at a sky-id pointing at an <svg> element (the diff does
  // this whenever the SVG's children-count changes, or a child
  // tag/kind mismatches between renders) leaves the SVG with HTML-
  // namespaced children. Drawing tools, charts, and apps that swap
  // inline-SVG icon <path> children are the common victims.
  var tmp;
  if (container.namespaceURI && container.namespaceURI !== "http://www.w3.org/1999/xhtml") {
    var range = document.createRange();
    range.selectNodeContents(container);
    tmp = range.createContextualFragment(newHTML);
  } else {
    tmp = document.createElement("div");
    tmp.innerHTML = newHTML;
  }

  // Snapshot focused-state BEFORE any DOM mutation. Selection read
  // throws on some input types, so catch.
  var selStart = null, selEnd = null, scrollTop = 0;
  if (focusedInside) {
    try {
      selStart = focused.selectionStart;
      selEnd   = focused.selectionEnd;
    } catch (_) {}
    scrollTop = focused.scrollTop;
  }

  // Walk the LIVE container's inputs/textareas/selects and decide
  // which ones to splice. The focused element is ALWAYS spliced
  // (active typing wins). Other elements are spliced only when the
  // server-side placeholder is uncontrolled (no value/checked/
  // selected) — i.e. user state is canonical.
  var preservedFocus = null;
  var liveNodes = container.querySelectorAll("input, textarea, select");
  for (var i = 0; i < liveNodes.length; i++) {
    var live = liveNodes[i];
    var placeholder = __skyFindPlaceholder(tmp, live);
    if (!placeholder) continue; // server unmounted: honour the server
    var isFocused = (live === focused);
    if (!isFocused && !__skyPlaceholderUncontrolled(placeholder)) {
      // Controlled field with a server-supplied value — let the
      // server win. Default innerHTML swap will recreate it from
      // placeholder.
      continue;
    }
    // Mirror placeholder attrs (class, type, placeholder, disabled,
    // aria-*, …) onto the live node — except the three authority
    // attrs the user drives. The user's .value / .checked /
    // .selected DOM property survives untouched.
    __skyCopyAttrsExceptAuthority(placeholder, live);
    // F9: the focused input keeps its node, but when the user's
    // keystrokes are all acknowledged its value follows the model like
    // any other patch (a clear / normalisation must show).
    if (isFocused) {
      var pv = null;
      if (live.tagName === "TEXTAREA") {
        if (!__skyPlaceholderUncontrolled(placeholder)) pv = placeholder.value;
      } else if (live.tagName === "INPUT" && placeholder.hasAttribute("value") &&
                 live.type !== "checkbox" && live.type !== "radio" && live.type !== "file") {
        pv = placeholder.getAttribute("value");
      }
      if (!__skyIsDirty(live)) {
        if (pv !== null && live.value !== pv) {
          live.value = pv;
          __skyNoteServerValue(live, pv);
        }
      } else if (pv !== null) {
        __skyRebase(live, pv);
      }
    }
    // Splice: replace the placeholder in tmp with the live node.
    // After this, the live node lives in tmp at the placeholder's
    // slot; the container still references it too (until the swap
    // below). DOM trees are tolerant of this — the upcoming
    // removeChild + appendChild commit moves it cleanly.
    placeholder.parentNode.replaceChild(live, placeholder);
    if (isFocused) preservedFocus = live;
  }

  // Splice IFRAMES across the swap (#568 second loop).  Without
  // this, an HTML-replace patch that touches the iframe's parent
  // subtree (sibling sky-id reorder, structural reorganisation)
  // destroys the live iframe via removeChild and creates a fresh
  // one from the placeholder markup.  The fresh iframe's src
  // triggers a navigation regardless of whether the value matches
  // the live one — every reload re-fires the embedded console's
  // handshake form, opens a new SSE, and pegs the tenant Cloud
  // Run instance.
  //
  // Same splice pattern as inputs.  The iframe's src is treated
  // as USER STATE AUTHORITY (the live document, internal SSE,
  // navigation history, scroll position are owned by the iframe).
  // Placeholder contributes non-authority attrs only — class,
  // style, sandbox, referrerpolicy — via the existing helper
  // (whose authority filter covers value/checked/selected; src
  // isn't in that list, so we strip it from the placeholder
  // before mirroring to avoid the same setAttribute-triggered
  // navigation the patch path's guard prevents).
  var liveFrames = container.querySelectorAll("iframe");
  for (var fi = 0; fi < liveFrames.length; fi++) {
    var liveFr = liveFrames[fi];
    var phFr = __skyFindPlaceholder(tmp, liveFr);
    if (!phFr) continue;
    // SRC-EQUALITY GATE.  sky-id is purely structural (tag +
    // position + form-name) and does NOT encode src.  So two
    // renders that emit <iframe src=A> then <iframe src=B> at
    // the same structural position share a sky-id, and naive
    // splicing would freeze the iframe at src=A forever — every
    // legitimate URL change would silently no-op.  Only splice
    // (preserve the live iframe) when the SERVER's intended src
    // matches the live src.  When they differ, fall through to
    // the default innerHTML path so the live iframe gets
    // destroyed and a fresh one navigates to the new URL.
    var liveSrc = liveFr.getAttribute("src") || "";
    var phSrc = phFr.getAttribute("src") || "";
    if (liveSrc !== phSrc) continue;
    // Strip src from placeholder so __skyCopyAttrsExceptAuthority
    // doesn't write it onto the live iframe (which would navigate
    // it even when the strings already match — assigning src
    // unconditionally re-fetches in some browsers).
    if (phFr.hasAttribute("src")) phFr.removeAttribute("src");
    __skyCopyAttrsExceptAuthority(phFr, liveFr);
    phFr.parentNode.replaceChild(liveFr, phFr);
  }

  // Commit: throw away container's current children (those we didn't
  // splice are stale; spliced ones already moved into tmp), then
  // attach tmp's children. Done.
  while (container.firstChild) container.removeChild(container.firstChild);
  while (tmp.firstChild) container.appendChild(tmp.firstChild);

  // Focus restoration on the SAME node — so .value, IME state,
  // composition buffer survive untouched. removeChild + appendChild
  // drop focus, so we re-set it now.
  if (preservedFocus) {
    try { preservedFocus.focus({preventScroll: true}); } catch (_) {
      try { preservedFocus.focus(); } catch (_) {}
    }
    if (typeof preservedFocus.setSelectionRange === "function" &&
        selStart !== null && selEnd !== null) {
      try { preservedFocus.setSelectionRange(selStart, selEnd); } catch (_) {}
    }
    if (scrollTop) preservedFocus.scrollTop = scrollTop;
  }
}

// __skyCopyAttrsExceptAuthority — mirror attrs from src onto dst,
// skipping the three the user drives directly. Removes attrs on
// dst that aren't in src (same "skip" rule). Used when splicing a
// live focused input into a server-rendered placeholder.
function __skyCopyAttrsExceptAuthority(src, dst) {
  if (!src || !dst || !src.attributes || !dst.attributes) return;
  var isAuthority = function(n) {
    return n === "value" || n === "checked" || n === "selected";
  };
  // Drop attrs that aren't present in src.
  var toRemove = [];
  for (var i = 0; i < dst.attributes.length; i++) {
    var n = dst.attributes[i].name;
    if (isAuthority(n)) continue;
    if (!src.hasAttribute(n)) toRemove.push(n);
  }
  for (var r = 0; r < toRemove.length; r++) dst.removeAttribute(toRemove[r]);
  // Add / update attrs from src.
  for (var j = 0; j < src.attributes.length; j++) {
    var a = src.attributes[j];
    if (isAuthority(a.name)) continue;
    if (dst.getAttribute(a.name) !== a.value) dst.setAttribute(a.name, a.value);
  }
}

// __skyDidNavigate: true when the URL pathname changed since the last settled
// patch (a genuine page navigation), updating the tracker either way. A
// navigation should land the new page at the top; an in-place update (SSE tick,
// same-page event, filter change that keeps the path) must not move the scroll.
function __skyDidNavigate() {
  var cur = location.pathname;
  var moved = (cur !== __skyLastPath);
  __skyLastPath = cur;
  return moved;
}

// __skyPatch: full-body replacement for sky-nav clicks, popstate,
// and the server's full-HTML fallback path. Routes through the
// node-preservation splicer so keystrokes never land on a destroyed
// DOM node.
function __skyPatch(t) {
  var root = document.getElementById("sky-root");
  if (!root) return;
  // Strip the full-document envelope when present (sky-nav fetches
  // return <!doctype><html>...</html>). The regex captures exactly
  // the rendered body, same as before.
  var m = t.match(/<div id="sky-root">([\s\S]*?)<\/div><script type="application\/json" id="sky-live-cfg">/);
  if (m) t = m[1];
  // The URL is already updated before __skyPatch runs (sky-nav pushState /
  // popstate). So a page navigation scrolls the new page to the top; a
  // same-page full-body patch preserves the user's scroll so nothing jumps.
  var scrollX = window.scrollX, scrollY = window.scrollY;
  __skyReplaceHTMLPreservingFocus(root, t);
  if (__skyDidNavigate()) window.scrollTo(0, 0);
  else window.scrollTo(scrollX, scrollY);
  __skyBindEvents(document);
  // Full-body patch (sky-nav click / popstate / mount): the URL is already
  // correct, so reconcile without minting a history entry (push=false).
  __skyRunPaths(root, false);
  __skyReviveScripts(root);
}

// __skyReviveScripts: browsers DO NOT execute <script> tags inserted
// via innerHTML (or any HTML-string assignment). When Sky.Live
// swaps the body via __skyReplaceHTMLPreservingFocus (sky-nav, full-
// body patches) or applies an attribute/HTML patch via
// __skyApplyPatches, any <script src=...> or inline <script>
// element in the new content is added to the DOM but never
// executed. This breaks any app-level JS bundle injected via the
// Sky-side Ui.html (Html.node "script" [...]) pattern (notably
// sky-editor's Editor.scriptTag).
//
// The fix: walk the new subtree for <script> elements, replace
// each with a freshly-created one carrying a STRICT ALLOWLIST of
// attributes. Freshly-created script nodes execute on insertion.
//
// Security (Cycle 3 audit gap C9 / cycle 2 plan P31):
//   - Attribute copy is filtered through __skyScriptAttrAllowlist.
//     Event-handler attrs (onerror, onload, onclick, …) are NEVER
//     re-emitted — the original unfiltered loop allowed an attacker
//     who controlled WYSIWYG content rendered back into Ui.html to
//     ship <script onerror=alert(1)> and watch the handler fire on
//     the next patch.
//   - Inline script bodies (textContent) are DROPPED unless the
//     element also carries a src= attribute (a same-origin opt-in:
//     Sky-bundled scripts like sky-editor's Editor.scriptTag set
//     src=; user-supplied inline bodies are silently rejected with
//     a console.warn so the misuse is visible during dev).
//   - Rejected scripts STILL get the data-sky-script-revived
//     marker so a subsequent revival pass doesn't reprocess them
//     (i.e. silent-drop is idempotent — no infinite warning storm).
//
// Idempotency: each revived <script> gets a data-sky-script-revived
// attribute; subsequent calls skip it. This prevents the bundle
// from re-loading on every patch (which would re-run any
// DOMContentLoaded handlers and re-fire setInterval-driven
// bootstraps multiple times).
//
// Safety: only matches <script> nodes inside root (the sky-root
// container). Top-level page <script> tags (in <head> or outside
// sky-root) are left alone — they ran on initial load and need
// no revival.
var __skyScriptAttrAllowlist = {
  "src": 1,
  "type": 1,
  "async": 1,
  "defer": 1,
  "integrity": 1,
  "crossorigin": 1,
  "nomodule": 1,
  "referrerpolicy": 1,
  "data-sky-script-revived": 1
};
function __skyReviveScripts(root) {
  if (!root) return;
  var scripts = root.querySelectorAll("script:not([data-sky-script-revived])");
  for (var i = 0; i < scripts.length; i++) {
    var old = scripts[i];
    // Mark the source element revived FIRST so a rejection branch
    // (no-src + inline body) doesn't re-trip on the next pass.
    try { old.setAttribute("data-sky-script-revived", "1"); } catch (_) {}
    var hasSrc = old.hasAttribute("src");
    var hasInline = !!(old.textContent && old.textContent.length > 0);
    // Reject inline-only scripts (no src) — same-origin opt-in via
    // src= is the contract. Console.warn so the misuse is visible
    // during dev; never throws (one bad node mustn't kill the loop).
    if (!hasSrc && hasInline) {
      try {
        if (typeof console !== "undefined" && console.warn) {
          console.warn("[sky.live] script revival rejected an inline <script> without src= (XSS hardening, gap C9). Bundle via src= for Sky-side scripts.");
        }
      } catch (_) {}
      continue;
    }
    var fresh = document.createElement("script");
    // Copy ONLY allowlisted attributes. Event-handler attrs (anything
    // starting with "on…") and any non-allowlisted attribute are
    // silently dropped — see __skyScriptAttrAllowlist.
    var droppedAttrs = null;
    for (var j = 0; j < old.attributes.length; j++) {
      var a = old.attributes[j];
      var n = a.name.toLowerCase();
      if (__skyScriptAttrAllowlist[n] === 1) {
        try { fresh.setAttribute(a.name, a.value); } catch (_) {}
      } else {
        // Capture for a single dev-time warn at the end (a single
        // <script onerror=…> shouldn't fire one warn per attr).
        if (!droppedAttrs) droppedAttrs = [];
        droppedAttrs.push(a.name);
      }
    }
    if (droppedAttrs) {
      try {
        if (typeof console !== "undefined" && console.warn) {
          console.warn("[sky.live] script revival dropped non-allowlisted attrs (XSS hardening, gap C9):", droppedAttrs.join(", "));
        }
      } catch (_) {}
    }
    // Inline body is now ONLY admitted when src= is also present.
    // This stays compatible with <script src=...>// optional inline
    // bootstrapping comment <\/script> patterns; the body is included
    // verbatim, the src= drives the actual execution.
    // (The escaped </ above prevents the literal closing-script tag
    // from terminating the inline JS wrapper at the HTML parser.)
    if (hasSrc && hasInline) {
      fresh.textContent = old.textContent;
    }
    fresh.setAttribute("data-sky-script-revived", "1");
    // Replacing the old node with the fresh one triggers script
    // execution (for src= it fetches + runs; for inline it runs
    // the body).
    old.parentNode.replaceChild(fresh, old);
  }
}

// ── Loading indicator ────────────────────────────────────────
// Call __skyLoaderStart() before network, __skyLoaderEnd() after. An element
// with id="sky-loader" gets the sky-loading class added/removed. Small
// 80ms delay so fast responses don't flash the indicator.
var __skyLoaderEl = null;
var __skyLoaderTimer = null;
function __skyLoaderStart() {
  __skyLoaderEl = __skyLoaderEl || document.getElementById("sky-loader");
  if (!__skyLoaderEl) return;
  clearTimeout(__skyLoaderTimer);
  __skyLoaderTimer = setTimeout(function() {
    __skyLoaderEl.classList.add("sky-loading");
  }, 80);
}
function __skyLoaderEnd() {
  clearTimeout(__skyLoaderTimer);
  if (__skyLoaderEl) __skyLoaderEl.classList.remove("sky-loading");
}

// ── Debounce ─────────────────────────────────────────────────
var __skyInputTimers = {};
var __skyInputPending = {};
function __skyDebouncedSend(msgName, args, hid, delay) {
  var key = hid || msgName;
  clearTimeout(__skyInputTimers[key]);
  // The view is captured NOW: the handler id belongs to the render the
  // user typed into, whatever renders land before the debounce fires.
  __skyInputPending[key] = { msgName: msgName, args: args, hid: hid, view: __skyView };
  __skyInputTimers[key] = setTimeout(function() {
    var p = __skyInputPending[key];
    delete __skyInputPending[key];
    if (!p) return;
    __skySend(p.msgName, p.args, p.hid, { noLoader: true, view: p.view, fromFlush: true });
  }, delay);
}
// Flush pending debounced input on blur (tab away / click elsewhere).
// Without this, typing fast then tabbing loses the last keystrokes
// because the debounce hasn't fired yet.
document.addEventListener("focusout", function(ev) {
  var t = ev.target;
  if (!t || !t.getAttribute) return;
  t.__skyTyped = false;
  var key = __skyHid(t, "input");
  if (__skyInputPending[key]) {
    clearTimeout(__skyInputTimers[key]);
    var p = __skyInputPending[key];
    delete __skyInputPending[key];
    __skySend(p.msgName, p.args, p.hid, { noLoader: true, view: p.view, fromFlush: true });
  }
}, true);
// Any user edit, handled or not: an untracked focused input counts as
// dirty once the user has typed into it (see __skyIsDirty).
document.addEventListener("input", function(ev) {
  var t = ev.target;
  if (!t || !ev.isTrusted) return;
  // A toggle is not typing, and a file input has no text to protect.
  if (t.type === "checkbox" || t.type === "radio" || t.type === "file") return;
  t.__skyTyped = true;
}, true);

// ── IME composition (UF-11) ──────────────────────────────────────
// While a composition is in progress the field holds the pre-edit
// ("k", "かな"), not the user's text. No input Msg is sent and no server
// value is written into the field until compositionend; then the
// committed text is sent once.
document.addEventListener("compositionstart", function(ev) {
  if (ev.target) ev.target.__skyComposing = true;
}, true);
document.addEventListener("compositionend", function(ev) {
  var t = ev.target;
  if (!t || !t.getAttribute) return;
  t.__skyComposing = false;
  if (t.hasAttribute("sky-input")) {
    __skyDispatchInput(t, t.getAttribute("sky-input"), __skyHid(t, "input"),
                       [t.value == null ? "" : String(t.value)]);
  }
}, true);

// ── I3: flush on unmount ─────────────────────────────────────
// Any pending debounce that hasn't fired by the time the user
// navigates or closes the tab would normally be discarded — the
// setTimeout is torn down with the page. These handlers flush
// synchronously so the final keystroke always reaches the server.
// See docs/skylive/input-authority-protocol.md §I3.

// __skyCollectPendingBatch — snapshot every pending-debounce entry
// into a batch array, bumping __skyClientSeq per entry so each gets
// its own order in the batch processed server-side. Clears the
// pending map as a side effect so the regular debounce callback
// can't double-fire after a beacon.
function __skyCollectPendingBatch() {
  var keys = Object.keys(__skyInputPending);
  if (keys.length === 0) return null;
  var batch = [];
  for (var i = 0; i < keys.length; i++) {
    var k = keys[i];
    clearTimeout(__skyInputTimers[k]);
    var p = __skyInputPending[k];
    delete __skyInputPending[k];
    __skyClientSeq++;
    batch.push({
      seq: __skyClientSeq,
      msg: p.msgName || "",
      args: p.args || [],
      handlerId: p.hid || "",
      view: p.view || ""
    });
  }
  return batch;
}

// __skyFlushPendingBeacon — POST pending debounces via sendBeacon so
// the request survives page unload. Single beacon carries the whole
// batch + the latest inputState snapshot so the server ingests the
// final DOM values before dispatching. Silent no-op when there's
// nothing pending or the browser lacks sendBeacon support.
function __skyFlushPendingBeacon() {
  if (!navigator || typeof navigator.sendBeacon !== "function") return;
  var batch = __skyCollectPendingBatch();
  var snapshot = __skyInputsSnapshot();
  if (!batch && !snapshot) return;
  var body = { sessionId: __skySid };
  if (batch)    body.batch = batch;
  if (snapshot) body.inputState = snapshot;
  // sendBeacon takes (url, data) only — there is NO headers argument, so
  // this request cannot carry X-Sky-Csrf and the CSRF middleware would
  // reject it (dropping the user's final debounced keystrokes on tab
  // close). The token rides in the body instead; the server compares it to
  // the __sky_csrf cookie exactly as it does the header, so this is the
  // same double-submit bind, not an exemption. Keep the Blob type
  // application/json: form/text encodings are CORS-safelisted and would
  // let a cross-origin beacon through without a preflight.
  if (__skyCsrfToken) body.csrf = __skyCsrfToken;
  try {
    var blob = new Blob([JSON.stringify(body)], {type: "application/json"});
    navigator.sendBeacon(__skyBase + "/_sky/event", blob);
  } catch (_) {}
}

// __skyFlushPendingSync — synchronous variant for same-page
// transitions where sendBeacon is overkill. Calls __skySend for
// each pending entry; the fetch requests are fire-and-forget and
// the browser keeps them alive across same-origin navigation.
function __skyFlushPendingSync() {
  var batch = __skyCollectPendingBatch();
  if (!batch) return;
  for (var i = 0; i < batch.length; i++) {
    var b = batch[i];
    __skySend(b.msg, b.args, b.handlerId, {noLoader: true, view: b.view, fromFlush: true});
  }
}

// Capture-phase click listener inside sky-root: before a link click
// leaves the current page, drain any pending debounce so the final
// typed value reaches the server in the same origin as the
// outgoing navigation. Beacon path handles cross-page; sync path
// handles SPA-style internal routing.
document.addEventListener("click", function(ev) {
  var a = ev.target && ev.target.closest && ev.target.closest("a[href]");
  if (!a) return;
  var root = document.getElementById("sky-root");
  if (!root || !root.contains(a)) return;
  var href = a.getAttribute("href") || "";
  // External or cross-origin → beacon (browser will tear down the
  // page, fetch would be cancelled). Same-origin navigation inside
  // SPA-style routing → sync flush (fetch survives).
  var isExternal = /^(https?:)?\/\//.test(href) && a.host !== location.host;
  if (isExternal || href === "") {
    __skyFlushPendingBeacon();
  } else {
    __skyFlushPendingSync();
  }
}, true);

// Tab close / navigate away: sendBeacon is the only path that
// survives the teardown. Listen on both events because iOS Safari
// + bfcache fire pagehide instead of beforeunload.
window.addEventListener("beforeunload", __skyFlushPendingBeacon);
window.addEventListener("pagehide", __skyFlushPendingBeacon);

// Release the SSE connection the instant we navigate away. A streaming
// EventSource can linger in the browser's connection pool while a full-page
// navigation tears the old document down; without an explicit close, an app
// that navigates via plain links (a fresh SSE per page) overlaps the closing
// stream with the next page's new one. Rapid clicking piles them up until the
// browser's ~6-connections-per-host HTTP/1.1 limit is hit, at which point every
// request (navigation, clicks, images) queues forever and the tab appears
// frozen. Closing here frees the slot before the next page opens its own.
window.addEventListener("pagehide", function() {
  try { if (__skySSE) __skySSE.close(); } catch (_) {}
  __skySSE = null;
  if (__skySseReopenTimer !== null) { clearTimeout(__skySseReopenTimer); __skySseReopenTimer = null; }
});
// bfcache restore (Back/Forward): the SSE was closed on pagehide, so reopen it.
window.addEventListener("pageshow", function(e) {
  if (e.persisted && __skySSE === null && __skySseReopenTimer === null) {
    __skySsePathNext = true;
    __skyOpenSSE();
  }
});

// ── Core send ────────────────────────────────────────────────
// Wire format (see docs/skylive/input-authority-protocol.md §Request):
//   {sessionId, seq, msg, args, handlerId, inputState?}
//   * seq is client-monotonic — server uses it to match responses to
//     the inputState snapshot that produced them.
//   * inputState carries the user's current DOM values for every
//     dirty input so the server's diff can align against reality
//     before emitting patches.
function __skySend(msgName, args, handlerId, opts) {
  opts = opts || {};
  // L6: a pending debounced input is an EARLIER user action than this
  // one. Send it first, so update sees the typed text before the Enter /
  // click that follows it (it used to see an empty draft).
  if (!opts.fromFlush) __skyFlushPendingSync();
  if (!opts.noLoader) __skyLoaderStart();
  __skyClientSeq++;
  var mySeq = __skyClientSeq;
  // The focused text field's value as this event leaves: the base the
  // reply's value for it applies to (see __skyRebase).
  var fae = document.activeElement;
  if (fae && (fae.tagName === "INPUT" || fae.tagName === "TEXTAREA") &&
      fae.type !== "checkbox" && fae.type !== "radio" && fae.type !== "file" &&
      fae.getAttribute("sky-id")) {
    __skyBaseBySeq[mySeq] = { sid: fae.getAttribute("sky-id"), value: String(fae.value == null ? "" : fae.value) };
    // A reply that never applies (dropped as stale) leaves its entry;
    // integer keys enumerate ascending, so drop the oldest past a bound.
    var bks = Object.keys(__skyBaseBySeq);
    if (bks.length > 64) delete __skyBaseBySeq[bks[0]];
  }
  // Stamp every currently-dirty input with this seq. The server's
  // ack (for a future response) will clear them back to parity.
  var dirtyIds = Object.keys(__skyInputs);
  for (var di = 0; di < dirtyIds.length; di++) {
    var de = __skyInputs[dirtyIds[di]];
    if (de.liveValue !== "" || de.pendingDebounceId !== null) {
      de.lastSentSeq = mySeq;
    }
  }
  var snapshot = __skyInputsSnapshot();
  var body = {
    sessionId: __skySid,
    seq: mySeq,
    msg: msgName || "",
    args: args || [],
    handlerId: handlerId || "",
    tab: __skyTabId,
    // The render the user acted on (L2): handlerId resolves against it.
    view: (opts.view !== undefined && opts.view !== null) ? opts.view : __skyView
  };
  if (snapshot) body.inputState = snapshot;
  __skyPostEvent(body);
}

// ── POST retry queue ─────────────────────────────────────────
// Wire-protocol POSTs are cheap (small JSON, idempotent on the
// server's seq-ordered state machine), so a transient network blip
// shouldn't lose the click. Failures push the body onto __skyEventQueue;
// retries fire on exponential backoff (500ms, 1s, 2s, … cap 16s);
// the SSE 'open' handler drains the queue eagerly when the server
// comes back. Cap at 50 entries — beyond that the user has been
// offline so long that replay isn't useful, drop oldest with a
// console warn so the page doesn't accumulate megabytes of state.
var __skyEventQueue = [];
var __skyRetryTimer = null;
var __skyRetryAttempts = 0;
// __skyRetryBaseMs / __skyRetryMaxMs / __skyRetryMaxAttempts /
// __skyEventQueueMax are templated at the top of this script from
// the SKY_LIVE_RETRY_* / SKY_LIVE_QUEUE_MAX env vars (see
// loadLiveBannerConfig).
// Event POSTs are SERIALISED: each waits for the previous reply. Two
// clicks in flight at once could reach the server in either order (and
// a flushed debounce could land after the Enter that followed it). While
// events wait in the retry queue, a new event queues behind them for the
// same reason.
var __skyPostChain = Promise.resolve();
var __skyDraining = false;
function __skyPostEvent(body) {
  if (__skyEventQueue.length > 0 && !__skyDraining) {
    __skyQueueInsert(body);
    return;
  }
  var fromQueue = __skyDraining;
  var run = function() {
    // An EARLIER event failed while this one waited in the chain: it must
    // not overtake that event. Queue it in order; the drain sends it.
    if (!fromQueue && __skyEventQueue.length > 0) {
      __skyQueueInsert(body);
      return;
    }
    return __skyPostEventNow(body);
  };
  __skyPostChain = __skyPostChain.then(run, run);
}
// __skyQueueInsert keeps the retry queue in send (seq) order. A replayed
// event that fails again goes back in FRONT of the later events it was
// ahead of; appending it let a later click overtake it (two queued deletes
// replayed as b, a).
function __skyQueueInsert(body) {
  var i = __skyEventQueue.length;
  while (i > 0 && typeof body.seq === "number" && typeof __skyEventQueue[i - 1].seq === "number" &&
         __skyEventQueue[i - 1].seq > body.seq) {
    i--;
  }
  __skyEventQueue.splice(i, 0, body);
}
function __skyPostEventNow(body) {
  // Phase 1.2 — attach the per-session CSRF token. The server-side
  // middleware (runtime-go/rt/csrf_middleware.go) rejects POSTs
  // without a matching X-Sky-Csrf / __sky_csrf cookie pair. Empty
  // token means CSRF is disabled at the runtime level (sky.toml
  // [security] csrf = false) — header omitted, middleware skipped.
  var headers = {"Content-Type":"application/json"};
  if (__skyCsrfToken) headers["X-Sky-Csrf"] = __skyCsrfToken;
  return fetch(__skyBase + "/_sky/event", {
    method: "POST",
    headers: headers,
    body: JSON.stringify(body),
    credentials: "same-origin"
  }).then(function(r){
    if (!r.ok && r.status >= 500) {
      // Server is up but rejecting (502/503/504 from a deploying LB,
      // or 500 from a panic that survived the recover guard). Treat
      // as transient — same retry path as a network failure.
      throw new Error("server " + r.status);
    }
    // Server-authored desync classification — the universal resync invariant.
    // A real Sky.Live desync response carries X-Sky-Status, so the client never
    // has to sniff a body string or misread the response as a proxy wedge (the
    // old bug: "handler not found" was misclassified → retried the dead handler
    // → stranded until a manual refresh):
    //   session-lost → the session cookie is unknown; only a full reload
    //                  (fresh init + new session) can recover.
    //   desync       → session valid, but this action can't dispatch against
    //                  the current view (stale DOM after a deploy / SSE drop).
    //                  The server already re-rendered the CURRENT view into
    //                  this response body — apply it to refresh the DOM +
    //                  data-sky-hid so the NEXT click matches. This action is
    //                  dropped (its captured payload is unrecoverable), which
    //                  beats stranding the whole client.
    var skyStatus = r.headers.get("X-Sky-Status");
    if (skyStatus === "session-lost") {
      __skyOnPostSuccess();            // server reachable → clear backoff/banner
      __skyRecoverLostSession("unknown-session");
      return;
    }
    if (skyStatus === "desync") {
      __skyOnPostSuccess();            // clears the reconnecting/offline banner
      if (window.console && console.warn) {
        console.warn("[sky.live] view desync — refreshing to the current server view; this action was dropped");
      }
      // Backstop a pathological never-converging view: after a few consecutive
      // desyncs, escalate to a full reload instead of looping.
      __skyConsecutiveResync = (__skyConsecutiveResync || 0) + 1;
      if (__skyConsecutiveResync > 5) {
        __skyConsecutiveResync = 0;
        if (!__skyProbedReload) { __skyProbedReload = true; window.location.reload(); }
        return;
      }
      return r.text().then(function(t) {
        var seqStr = r.headers.get("X-Sky-Seq");
        var seq = seqStr ? parseInt(seqStr, 10) : 0;
        var ackRaw = r.headers.get("X-Sky-Ack-Inputs");
        var ack = null;
        if (ackRaw) { try { ack = JSON.parse(ackRaw); } catch(_) {} }
        __skyHandleResponse(seq, ack, function() { __skyPatch(t); },
                            undefined, r.headers.get("X-Sky-View"), "", false);
      });
    }
    __skyConsecutiveResync = 0; // a normal response — reset the desync backstop
    // Reverse-proxy wedge detection: a real Sky.Live response always
    // carries X-Sky-Live: 1. Without it, we're looking at a proxy-
    // rewritten response (e.g. some edges turn upstream 502 into 200
    // OK with an HTML error page). Applying that as a "patch" would
    // replace the user's DOM with the proxy's error page, so we refuse
    // it and route through the failure path instead.
    //
    // For JSON content-type we keep a backwards-compat shim during
    // rolling deploys: a pre-marker server still returns valid JSON
    // with seq + patches, structurally indistinguishable from the
    // marked form, so accept it. HTML / text responses without the
    // marker are always rejected — those are the proxy-wedge shape.
    var skyMark = r.headers.get("X-Sky-Live");
    var ct = r.headers.get("Content-Type") || "";
    var isJson = ct.indexOf("application/json") >= 0;
    if (skyMark !== "1" && !isJson) {
      throw new Error("non-sky response " + r.status);
    }
    if (isJson) {
      return r.json().then(function(data) {
        // Even JSON is rejected if it lacks the protocol shape (no
        // seq field): some proxies (Cloudflare access denied, fly.io
        // edge errors) return JSON error envelopes with 200 OK.
        if (skyMark !== "1" && (!data || typeof data.seq === "undefined")) {
          throw new Error("non-sky json response");
        }
        __skyLoaderEnd();
        __skyOnPostSuccess();
        if (!data) return;
        __skyHandleResponse(data.seq, data.ackInputs, function() {
          __skyRebaseBase = __skyBaseBySeq[body.seq] || null;
          try {
            if (data.patches) __skyApplyPatches(data.patches);
          } finally {
            __skyRebaseBase = null;
            delete __skyBaseBySeq[body.seq];
          }
        }, data.globalSeq, data.view, data.base, true);
        if (data.error) __skyShowError(data.error);
      });
    }
    return r.text().then(function(t) {
      __skyLoaderEnd();
      __skyOnPostSuccess();
      var seqStr = r.headers.get("X-Sky-Seq");
      var seq = seqStr ? parseInt(seqStr, 10) : 0;
      var ackRaw = r.headers.get("X-Sky-Ack-Inputs");
      var ack = null;
      if (ackRaw) { try { ack = JSON.parse(ackRaw); } catch(_) {} }
      __skyHandleResponse(seq, ack, function() {
        __skyRebaseBase = __skyBaseBySeq[body.seq] || null;
        try {
          __skyPatch(t);
        } finally {
          __skyRebaseBase = null;
          delete __skyBaseBySeq[body.seq];
        }
      }, undefined, r.headers.get("X-Sky-View"), "", false);
    });
  }).catch(function() {
    __skyLoaderEnd();
    __skyOnPostFailure(body);
  });
}
function __skyOnPostSuccess() {
  // A successful POST proves the server reachable — clear any
  // backoff state and drain queued events behind this one. If the
  // SSE was the trigger that drained the queue, this is a no-op.
  __skyRetryAttempts = 0;
  if (__skyRetryTimer !== null) {
    clearTimeout(__skyRetryTimer);
    __skyRetryTimer = null;
  }
  if (__skyStatus !== "connected") {
    __skySetStatus("connected", "");
  }
  // SSE recovery: if the watchdog tore down the EventSource (offline
  // terminal state), a successful POST proves the network is back, so
  // reopen the stream too — otherwise subscriptions and Cmd.perform
  // results would silently not arrive even though clicks work. Cancel
  // any pending reopen-with-backoff and bring it forward.
  if (__skySSE === null) {
    if (__skySseReopenTimer !== null) {
      clearTimeout(__skySseReopenTimer);
      __skySseReopenTimer = null;
    }
    __skyOpenSSE();
  }
  __skyDrainQueue();
}
function __skyOnPostFailure(body) {
  // FIFO drop when the queue is at the cap — bail on the oldest
  // pending event rather than the new one, so the user's most
  // recent intent is preserved.
  if (__skyEventQueue.length >= __skyEventQueueMax) {
    var dropped = __skyEventQueue.shift();
    if (window.console && console.warn) {
      console.warn("[sky.live] event queue at cap; dropped oldest", dropped);
    }
  }
  __skyQueueInsert(body);
  __skyShowReconnecting();
  __skyScheduleRetry();
}
function __skyShowReconnecting() {
  if (__skyStatus === "offline") return;
  if (__skyStatus === "connected") {
    __skySetStatus("reconnecting", __skyMsgReconnecting);
  }
}
function __skyScheduleRetry() {
  if (__skyRetryTimer !== null) return;  // already pending
  if (__skyRetryAttempts >= __skyRetryMaxAttempts) {
    __skySetStatus("offline", __skyMsgOffline);
    return;
  }
  __skyRetryAttempts++;
  // 500, 1000, 2000, 4000, 8000, 16000, 16000, … (capped)
  var delay = Math.min(__skyRetryBaseMs * Math.pow(2, __skyRetryAttempts - 1), __skyRetryMaxMs);
  __skyRetryTimer = setTimeout(function() {
    __skyRetryTimer = null;
    __skyDrainQueue();
  }, delay);
}
function __skyDrainQueue() {
  if (__skyEventQueue.length === 0) return;
  // Send the head of the queue. If it succeeds, __skyOnPostSuccess
  // recurses into __skyDrainQueue to send the next one. If it
  // fails, the body re-enters the queue and the retry loop kicks
  // back in. Order is preserved (FIFO) — the server's seq matching
  // tolerates late deliveries via __skyHandleResponse.
  var head = __skyEventQueue.shift();
  __skyDraining = true;
  try { __skyPostEvent(head); } finally { __skyDraining = false; }
}

// Apply a list of sky-id addressed patches with input authority (I1):
// value/checked/selected attrs on dirty inputs are dropped so the
// user's DOM wins; innerHTML patches route through
// __skyReplaceHTMLPreservingFocus which splices the live focused
// input (same DOM node, same .value, same IME/composition state)
// through the new HTML so it's never destroyed. Per-attr and
// textContent updates are fine as-is — they don't regenerate nodes.
function __skyApplyPatches(patches) {
  if (!patches || patches.length === 0) return;
  // Open <select> defence: native dropdowns close on ANY DOM mutation
  // inside the open select OR any ancestor that would re-mount it.
  // There's no JS API for "is the dropdown open", so use focus as the
  // conservative proxy: if a SELECT is the active element, treat its
  // subtree (and ancestors that would re-mount it) as off-limits for
  // this patch cycle. The next user interaction (option click, blur)
  // triggers a fresh response and reconciliation. Sibling subtrees
  // and unrelated parts of the DOM apply normally — the dropdown is
  // unaffected. See Bug 3 in docs/skylive/architecture.md.
  var openSel = (document.activeElement && document.activeElement.tagName === "SELECT")
      ? document.activeElement : null;
  // Track patches whose target sky-id isn't in the DOM. Old code
  // silently dropped these (continue), which produced the
  // "stuck at loading" UX class (issue triaged 2026-07-17,
  // sky-diagram): server emits a text-patch at r.3#div.5#p for a
  // conditionally-rendered element that arrived via a prior HTML
  // patch. If the client applies patches in a race with the
  // preceding HTML swap — or if the two views' child-count
  // shape drifted from the server's expectation — the sky-id
  // never materialised, and the completion Msg silently vanished.
  //
  // Distinguishing "legit skip" (patch[0] replaced patch[1]'s
  // target within the same batch — safe) from "real desync"
  // (server thinks element exists, client's DOM says otherwise —
  // NOT safe) can't be done inside the loop. But at end-of-batch
  // we know: any patch that missed is either race-safe (server
  // will send another update soon) or a genuine desync that
  // needs recovery. Rather than pick one, take the "soft-resync"
  // path: force-reopen SSE, which re-fetches the full body under
  // the existing session cookie. Cheap (bounded by SKY_LIVE_
  // RETRY_MAX_ATTEMPTS) + idempotent — if the server agrees the
  // client is fine, next full body confirms it; if state really
  // drifted, the full body corrects it. Users never see a UI that
  // silently ignores a completion.
  var missedTarget = 0;
  for (var i = 0; i < patches.length; i++) {
    var p = patches[i];
    var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
    if (!el) {
      missedTarget++;
      if (window.console && console.warn) {
        console.warn("[sky.live] patch target not found:", p.id,
            "(op=" + (p.text !== undefined ? "text" : p.html !== undefined ? "html" :
              p.kids !== undefined ? "kids" : p.replace !== undefined ? "replace" : "attrs") + ")");
      }
      continue;
    }
    if (openSel && (el === openSel || el.contains(openSel) || openSel.contains(el))) {
      // Skip: any mutation here would close the dropdown mid-pick.
      continue;
    }
    if (p.text !== undefined && p.text !== null) {
      // textContent on a container that contains the focused input
      // would also wipe the input (replaces all children with one
      // text node). Guard the same way as innerHTML.
      if (__skyContainsFocusedInput(el)) {
        __skyReplaceHTMLPreservingFocus(el, __skyEscapeHTML(p.text));
      } else {
        el.textContent = p.text;
      }
    }
    if (p.replace !== undefined && p.replace !== null) {
      // The root changed tag, or a form became another form: replace the
      // element itself.
      __skyReplaceElement(el, p.replace);
      continue;
    }
    if (p.html !== undefined && p.html !== null) {
      __skyReplaceHTMLPreservingFocus(el, p.html);
    }
    if (p.kids) {
      missedTarget += __skyApplyKids(el, p.kids);
    }
    if (p.attrs) {
      var dirty = __skyIsDirty(el);
      var keys = Object.keys(p.attrs);
      // Cursor preservation: when applying a "value" attr to a
      // focused INPUT or TEXTAREA, snapshot the selection range
      // BEFORE setting .value (which otherwise resets the cursor
      // to the end of the new string). Common case: user clicked
      // into a textarea, paused so their dirty flag cleared, and
      // the server pushes a fresh value via SSE. Without this,
      // the cursor jumps to the end mid-edit. Clamping handles
      // shorter new values (selectionStart > newLen -> newLen).
      var isInputLike = el.tagName === "INPUT" || el.tagName === "TEXTAREA";
      var hadFocus = isInputLike && el === document.activeElement;
      var savedSelStart = null, savedSelEnd = null, savedScrollTop = 0;
      if (hadFocus) {
        try {
          savedSelStart = el.selectionStart;
          savedSelEnd = el.selectionEnd;
        } catch (_) {}
        savedScrollTop = el.scrollTop;
      }
      var valueChanged = false;
      for (var j = 0; j < keys.length; j++) {
        var k = keys[j], v = p.attrs[k];
        // Authority filter: the user is currently editing this
        // field, so the server's proposed value/checked/selected
        // would stomp in-flight keystrokes. Drop them and let the
        // next event round-trip settle the state.
        if (dirty && (k === "value" || k === "checked" || k === "selected")) {
          if (k === "value") __skyRebase(el, v);
          continue;
        }
        if (v === "") {
          el.removeAttribute(k);
          // F2/F3: value / checked / selected / disabled are DOM
          // PROPERTIES once the user has touched the control; removing the
          // attribute does not reset them. A model reset to "" left the
          // old text on screen, and a radio group showed every option the
          // user had ever picked as checked.
          if (k === "value" && ("value" in el) && el.tagName !== "SELECT") {
            if (el.value !== "") { el.value = ""; valueChanged = true; }
            __skyNoteServerValue(el, "");
          }
          if (k === "checked") el.checked = false;
          if (k === "selected") el.selected = false;
          if (k === "disabled") el.disabled = false;
        }
        else {
          // Idempotent setAttribute (#568): some elements re-fetch or
          // re-navigate on ANY assignment to certain attributes, even
          // when the new value is identical to the existing one. The
          // poster child is the iframe src attribute — calling
          // setAttribute with the same value causes the browser to
          // re-navigate the iframe, dropping any SSE / cookie /
          // scroll state inside. Same class: img src refetches,
          // link href rebuilds the stylesheet, script src re-executes
          // (browsers vary). Skipping the no-op write costs one
          // getAttribute compare per attr and rules out a whole bug
          // class. Mirrors the guard in __skyCopyAttrsExceptAuthority.
          if (el.getAttribute(k) !== v) {
            el.setAttribute(k, v);
          }
          // Sync DOM properties that don't reflect from attrs.
          if (k === "value" && ("value" in el)) {
            el.value = v;
            valueChanged = true;
            __skyNoteServerValue(el, v);
          }
          if (k === "checked") el.checked = v !== "" && v !== "false";
          if (k === "selected") el.selected = v !== "" && v !== "false";
          if (k === "disabled") el.disabled = v !== "" && v !== "false";
        }
      }
      // Restore selection on focused input/textarea after a value
      // update. Clamp to the new value length so a shorter server
      // value does not throw RangeError. Scroll restore matters
      // mostly for multi-line textarea where the user may have
      // scrolled below the visible area.
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
  // Any patch whose target sky-id wasn't in the DOM signals a
  // client/server view-shape drift. Force-reopen the SSE stream so
  // the server ships a fresh full body; state converges. Skipping
  // the resync leaves the user staring at a stale UI (e.g. a
  // stuck loading indicator when the completion patch went nowhere).
  // Bounded by SKY_LIVE_RETRY_MAX_ATTEMPTS so a repeated drift
  // eventually surfaces as the offline banner instead of a busy loop.
  if (missedTarget > 0) {
    __skyForceReopenSSE();
  }
  // Any new sky-* attribute in the patched DOM needs a listener.
  __skyBindEvents(document);
  // After SSE-driven patches the URL also needs reconciling — without
  // this, programmatic Navigate Msgs would only update the in-memory
  // model and leave the address bar pointing at the previous page.
  // push=true: a programmatic Navigate is a real navigation and gets a
  // Back-able history entry.
  __skyRunPaths(document, true);
  // If those patches were a programmatic navigation to a different page (the
  // pathname just changed above), scroll the new page to the top — matching
  // sky-nav clicks. Ordinary same-page SSE/event updates (and filter changes
  // that keep the path) leave the scroll where the user had it.
  if (__skyDidNavigate()) window.scrollTo(0, 0);
  // Any <script> in newly-patched HTML wouldn't execute via innerHTML
  // — revive them so JS bundles (e.g. sky-editor) bootstrap correctly
  // when their host element first appears via a patch (not the initial
  // SSR).  See __skyReviveScripts above for the full rationale.
  var skyRootForPatches = document.getElementById("sky-root");
  if (skyRootForPatches) __skyReviveScripts(skyRootForPatches);
}

// ── Children reconcile (Patch.kids) ─────────────────────────
// The diff (runtime-go/rt/live_core.go diffChildren / KidOp) lists the
// target's NEW children in order: {keep: id[, id: newId]} keeps the
// existing child with that sky-id as the SAME node — a focused input
// inside it keeps focus, caret, IME state and its typed value — and
// {html: markup} is a new node. Every other child is removed. A kept
// child whose id changed is renamed (its subtree's sky-id / data-sky-hid
// move from the old prefix to the new one), so the DOM never holds an id
// the server no longer renders. Returns how many kept children were
// missing (a desync the caller resyncs).
function __skyParseInto(container, html) {
  if (container.namespaceURI && container.namespaceURI !== "http://www.w3.org/1999/xhtml") {
    var range = document.createRange();
    range.selectNodeContents(container);
    return range.createContextualFragment(html);
  }
  var t = document.createElement("template");
  t.innerHTML = html;
  return t.content;
}

function __skyApplyKids(el, kids) {
  var byId = {}, kept = {}, renames = [], missed = 0, c, i, k;
  for (c = el.firstElementChild; c; c = c.nextElementSibling) {
    var sid = c.getAttribute("sky-id");
    if (sid) byId[sid] = c;
  }
  var focused = document.activeElement;
  var focusInside = !!(focused && focused !== el && el.contains(focused));
  var selS = null, selE = null;
  if (focusInside) {
    try { selS = focused.selectionStart; selE = focused.selectionEnd; } catch (_) {}
  }
  for (i = 0; i < kids.length; i++) {
    k = kids[i];
    if (!k.keep) continue;
    if (byId[k.keep] && !kept[k.keep]) {
      kept[k.keep] = byId[k.keep];
      if (k.id && k.id !== k.keep) renames.push([byId[k.keep], k.keep, k.id]);
    } else {
      missed++;
    }
  }
  for (c = el.firstChild; c; ) {
    var next = c.nextSibling;
    if (!(c.nodeType === 1 && kept[c.getAttribute("sky-id")] === c)) el.removeChild(c);
    c = next;
  }
  __skyRenameKept(renames);
  var cursor = el.firstChild;
  for (i = 0; i < kids.length; i++) {
    k = kids[i];
    var nodes;
    if (k.keep && kept[k.keep]) nodes = [kept[k.keep]];
    else if (k.html !== undefined && k.html !== null) nodes = Array.prototype.slice.call(__skyParseInto(el, k.html).childNodes);
    else continue;
    for (var j = 0; j < nodes.length; j++) {
      if (nodes[j] === cursor) { cursor = cursor.nextSibling; continue; }
      el.insertBefore(nodes[j], cursor);
    }
  }
  // Moving a kept node (a reorder) blurs it; restore focus and caret.
  if (focusInside && focused.isConnected && document.activeElement !== focused) {
    try { focused.focus({preventScroll: true}); } catch (_) { try { focused.focus(); } catch (_) {} }
    if (selS !== null && typeof focused.setSelectionRange === "function") {
      try { focused.setSelectionRange(selS, selE === null ? selS : selE); } catch (_) {}
    }
  }
  return missed;
}

// __skyRenameKept — move each renamed kept subtree's ids (and the
// client's per-input state) from the old prefix to the new one. All ids
// are read before any is written, so a shift (a->b while b->c) is safe.
function __skyRenameKept(renames) {
  var moves = [], r, i;
  for (r = 0; r < renames.length; r++) {
    var root = renames[r][0], from = renames[r][1], to = renames[r][2];
    var all = [root].concat(Array.prototype.slice.call(root.querySelectorAll("[sky-id]")));
    for (i = 0; i < all.length; i++) {
      var e = all[i], sid = e.getAttribute("sky-id");
      if (!sid || (sid !== from && sid.indexOf(from + ".") !== 0)) continue;
      var m = {el: e, to: to + sid.slice(from.length), hid: null, entry: __skyInputs[sid]};
      var hid = e.getAttribute("data-sky-hid");
      if (hid && hid.indexOf(from + ".") === 0) m.hid = to + hid.slice(from.length);
      if (m.entry) delete __skyInputs[sid];
      moves.push(m);
    }
  }
  for (i = 0; i < moves.length; i++) {
    var mv = moves[i];
    mv.el.setAttribute("sky-id", mv.to);
    if (mv.hid) mv.el.setAttribute("data-sky-hid", mv.hid);
    if (mv.entry) __skyInputs[mv.to] = mv.entry;
  }
}

// __skyReplaceElement — Patch.replace: the element itself is replaced
// (only the root, when it changes tag). An innerHTML write nested the new
// root inside the old one.
function __skyReplaceElement(el, html) {
  var parent = el.parentNode;
  if (!parent) return;
  parent.replaceChild(__skyParseInto(parent, html), el);
}

// __skyNoteServerValue records a value the SERVER wrote into a tracked
// input, so the next inputState snapshot reports what the field shows
// rather than the user's last typed value (the server's I5 alignment
// compares against it).
function __skyNoteServerValue(el, v) {
  var sid = el.getAttribute && el.getAttribute("sky-id");
  if (sid && __skyInputs[sid]) __skyInputs[sid].liveValue = v;
}

function __skyContainsFocusedInput(el) {
  var a = document.activeElement;
  if (!a || a === document.body) return false;
  var tag = a.tagName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
  return el === a || el.contains(a);
}

function __skyEscapeHTML(s) {
  var d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}

// ── TEA event binding ────────────────────────────────────────
// Walks the DOM for sky-<event> attributes and binds a native listener
// that extracts args and dispatches through the TEA update cycle.
// Re-run after every DOM patch because new sky-* attrs may have appeared.
// F12: every event the view declares is bound — the set is read from the
// DOM (each sky-<event> attribute), not from a fixed list, so
// contextmenu / scroll / reset / select / load / error / custom events
// dispatch like click does.
var __skyNonEventAttrs = {"sky-id": 1, "sky-nav": 1, "sky-key": 1, "sky-enter": 1};
function __skyBindEvents(root) {
  root = root || document;
  var all = root.querySelectorAll ? root.querySelectorAll("*") : [];
  for (var i = 0; i < all.length; i++) {
    var el = all[i];
    var attrs = el.attributes;
    for (var j = 0; j < attrs.length; j++) {
      var n = attrs[j].name;
      if (n.length > 4 && n.lastIndexOf("sky-", 0) === 0 && !__skyNonEventAttrs[n]) {
        __skyBindOneEl(el, n.slice(4));
      }
    }
  }
  __skyBindEnter(root);
}

// __skyHid is the handler id of el's handler for evName: the key the
// server registered at render time (<sky-id>.<event>). Derived per event:
// an element with several handlers (onChange + onEnter, onClick +
// onMouseOver) resolves each event to its OWN handler.
function __skyHid(el, evName) {
  return ((el.getAttribute && el.getAttribute("sky-id")) || "") + "." + evName;
}

// Synthetic "enter" event (Ui.onEnter): the DOM has no "enter" event, so bind
// a keydown listener on [sky-enter] and fire the bound Msg only on a plain
// Enter (no Shift), calling preventDefault so a <textarea> does not also insert
// a newline for the sending keystroke. Shift-Enter is left untouched, so it
// inserts a newline as normal. The Msg carries no args (the client already
// filtered to Enter), so it dispatches as a bare Msg.
function __skyBindEnter(root) {
  var nodes = root.querySelectorAll("[sky-enter]");
  for (var i = 0; i < nodes.length; i++) {
    var el = nodes[i];
    if (el["__sky_enter"]) continue;
    el["__sky_enter"] = true;
    el.addEventListener("keydown", function(ev) {
      if (ev.key !== "Enter" || ev.shiftKey || ev.isComposing) return;
      var target = ev.currentTarget;
      if (!target.hasAttribute("sky-enter")) return;
      ev.preventDefault();
      __skySend(target.getAttribute("sky-enter"), [], __skyHid(target, "enter"));
    });
  }
}

// __skyRunPaths: the CSP-safe way for a render to
// ask for "update the address bar after a render." Looks
// for [data-sky-path] elements and pushes / replaces history if the
// value differs from location. It evaluates no string; the only
// DOM APIs touched are getAttribute and history.pushState /
// replaceState. Works under strict CSP (no 'unsafe-eval') and has no
// XSS surface (the value is a URL path, never executed).
//
// The element is intentionally NOT removed after running — Sky.Live's
// patches identify elements by sky-id and look them up via
// querySelector; removing the data-sky-path element would orphan its
// sky-id, and the next attribute patch (when the path changes) would
// silently skip. The path-check makes the call idempotent, so leaving
// the element in place is cheap — at most one comparison per patch.
// The push arg is the caller's history intent:
//   - false (full-body patch: sky-nav click / popstate / initial mount):
//     the correct URL is ALREADY in the address bar (the click handler pushed
//     it, popstate is browser-driven, mount is the loaded URL). Reconcile with
//     replaceState only - a full-body patch must never mint a second entry,
//     otherwise Back needs two presses per page.
//   - true (SSE-driven patch: a programmatic Navigate Msg): the address bar
//     still shows the previous page, so this IS a new navigation and gets a
//     real, Back-able history entry via pushState.
// Making the intent explicit removes the old reliance on pushState/patch
// ordering: the full-body path can no longer double-push under any interleaving.
function __skyRunPaths(root, push) {
  var els = (root || document).querySelectorAll("[data-sky-path]");
  for (var i = 0; i < els.length; i++) {
    var p = els[i].getAttribute("data-sky-path");
    if (!p) continue;
    if (location.pathname !== p) {
      try { history[push ? "pushState" : "replaceState"]({}, "", p); } catch (_) {}
    } else if (location.search) {
      try { history.replaceState({}, "", p); } catch (_) {}
    }
  }
  // v0.16.18 #558-PR4 — sibling that manages the query string.
  // The value is the raw query (no leading '?'); empty value means
  // "no params, strip any existing query string". Always
  // replaceState (never push) — filter changes shouldn't grow the
  // back-button history. The path is preserved, so this composes
  // with data-sky-path: paths push, queries replace.
  var qels = (root || document).querySelectorAll("[data-sky-query]");
  for (var j = 0; j < qels.length; j++) {
    var q = qels[j].getAttribute("data-sky-query") || "";
    var current = (location.search || "").replace(/^\?/, "");
    if (q === current) continue;
    var target = location.pathname + (q ? "?" + q : "");
    try { history.replaceState({}, "", target); } catch (_) {}
  }
}

function __skyBindOne(root, eventName) {
  var nodes = root.querySelectorAll("[sky-" + eventName + "]");
  for (var i = 0; i < nodes.length; i++) __skyBindOneEl(nodes[i], eventName);
}

function __skyBindOneEl(el, eventName) {
  if (el["__sky_" + eventName]) return;
  el["__sky_" + eventName] = true;
  el.addEventListener(eventName, function(ev) {
    var target = ev.currentTarget;
    // A patch can remove the handler after the listener was attached.
    if (!target.hasAttribute("sky-" + ev.type)) return;
    var msgName = target.getAttribute("sky-" + ev.type);
    var hid = __skyHid(target, ev.type);
    // Some events want preventDefault (submit, form-link navigation);
    // click doesn't (we only intercept when the attribute is set).
    if (ev.type === "submit") ev.preventDefault();
    var args = __skyExtractArgs(ev);
    if (ev.type === "input") {
      // UF-11: no Msg for an IME pre-edit; compositionend sends the text.
      if (ev.isComposing || target.__skyComposing) return;
      __skyDispatchInput(target, msgName, hid, args);
      return;
    }
    __skySend(msgName, args, hid);
  });
}

// __skyDispatchInput: record the live value against the input's sky-id
// (so the snapshot bundled with the next send reflects the DOM, and the
// patch filter recognises the input as dirty) and debounce the send.
function __skyDispatchInput(target, msgName, hid, args) {
  var sid = target.getAttribute("sky-id");
  if (sid) {
    var e = __skyInputEntry(sid);
    e.liveValue = args && args.length > 0 ? String(args[0]) : "";
  }
  __skyDebouncedSend(msgName, args, hid, 150);
}

// Extract the args array for a DOM event following the legacy Sky.Live
// convention:
//   * click / focus / blur / mouse*    → []         (just the msg)
//   * input / change                   → [value]    (typed input value)
//   * submit                           → [formData] (plain object of [name]=value)
//   * keydown / keyup / keypress       → [key]      (event.key string)
function __skyExtractArgs(ev) {
  var t = ev.target;
  switch (ev.type) {
    case "input":
    case "change":
      if (!t) return [""];
      if (t.type === "checkbox" || t.type === "radio") return [t.checked];
      // UF-8: number / range send the field's TEXT. valueAsNumber is NaN
      // for a cleared or partial ("-") number field and used to be sent
      // as 0, so the model read 0 while the box was empty.
      return [t.value == null ? "" : String(t.value)];
    case "submit":
      // Form-data assembly. Two non-obvious rules:
      //
      // 1. SUBMITTER FILTER. <button type="submit"> and
      //    <input type="submit"> entries appear in form.elements.
      //    Spec: only the SUBMITTER (the button that actually
      //    triggered the submit) contributes its name/value to
      //    the payload — peer submit buttons MUST NOT. Editors
      //    routinely use multiple submit buttons sharing one
      //    name="action" (Save / Format / Check); the naive
      //    "iterate everything" loop lets later buttons clobber
      //    earlier ones, so the LAST button name=action wins
      //    regardless of which the user clicked. Honour
      //    ev.submitter (modern browsers; falls back to
      //    document.activeElement for old Safari).
      //
      // 2. Disabled fields are excluded by the spec — skip them
      //    too so a disabled-but-submittable field doesn't leak
      //    a stale value.
      var data = {};
      var submitter = ev.submitter ||
          (document.activeElement && t && t.contains(document.activeElement)
              ? document.activeElement : null);
      if (t && t.elements) {
        for (var i = 0; i < t.elements.length; i++) {
          var el = t.elements[i];
          if (!el.name || el.disabled) continue;
          if (el.type === "submit" || el.type === "button" ||
              el.type === "image" || el.type === "reset") {
            // Only the submitter button contributes its name/value.
            if (el === submitter) data[el.name] = el.value;
            continue;
          }
          if (el.type === "checkbox" || el.type === "radio") {
            if (el.checked) data[el.name] = el.value;
          } else if (el.type === "file") {
            // File handling via sky-file / sky-image drivers (below).
          } else {
            data[el.name] = el.value;
          }
        }
      }
      return [data];
    case "keydown":
    case "keyup":
    case "keypress":
      return [ev.key || ""];
    default:
      return [];
  }
}

// ── File / Image drivers ─────────────────────────────────────
// onFile / onImage register via data-sky-ev-sky-file / -sky-image
// attributes. The client reads the chosen file, optionally resizes
// (for images), and sends a base64 data URL as the event value.
document.addEventListener("change", function(ev) {
  var el = ev.target;
  if (!el || el.tagName !== "INPUT" || el.type !== "file") return;
  // UF-3: the attribute VALUE is the Msg's display name, which is "_"
  // (formerly "") for a function handler (Ui.onFile GotFile); test for
  // presence and dispatch by handler id like every other event.
  var hasFile  = el.hasAttribute("data-sky-ev-sky-file");
  var hasImage = el.hasAttribute("data-sky-ev-sky-image");
  if (!hasFile && !hasImage) return;
  var f = el.files && el.files[0];
  if (!f) return;
  // Client-side size guard via fileMaxSize. Saves the round-trip when
  // the user picks a 100MB file: drop with a console.warn rather than
  // streaming the bytes server-side just to reject them. Server-side
  // validation should still happen — this is a UX nicety, not a
  // security boundary.
  var maxSize = parseInt(el.getAttribute("data-sky-ev-sky-file-max-size") || "0");
  if (maxSize > 0 && f.size > maxSize) {
    if (window.console && console.warn) {
      console.warn(
        "[sky.live] file " + f.name + " (" + f.size +
        " bytes) exceeds fileMaxSize " + maxSize + "; dispatch dropped"
      );
    }
    el.value = "";  // clear the input so the user can pick another
    return;
  }
  if (hasFile) {
    var r = new FileReader();
    // __skySend's args param is List a on the wire (server expects
    // []json.RawMessage); a bare string would unmarshal-fail. Wrap
    // the data URL in a single-element array — the Sky-side Msg
    // constructor declared as 'String -> Msg' reads args[0].
    r.onload = function(e) {
      __skySend(el.getAttribute("data-sky-ev-sky-file"), [e.target.result], __skyHid(el, "sky-file"));
    };
    r.readAsDataURL(f);
  }
  if (hasImage) {
    var maxW = parseInt(el.getAttribute("data-sky-ev-sky-file-max-width")  || "1200");
    var maxH = parseInt(el.getAttribute("data-sky-ev-sky-file-max-height") || "1200");
    __skyResizeImage(f, maxW, maxH, function(dataUrl) {
      // Same wire-format reason as the onFile branch — wrap in array.
      __skySend(el.getAttribute("data-sky-ev-sky-image"), [dataUrl], __skyHid(el, "sky-image"));
    });
  }
});

function __skyResizeImage(file, maxW, maxH, cb) {
  var img = new Image();
  var url = URL.createObjectURL(file);
  img.onload = function() {
    URL.revokeObjectURL(url);
    var w = img.width, h = img.height;
    if (w > maxW) { h = Math.round(h * maxW / w); w = maxW; }
    if (h > maxH) { w = Math.round(w * maxH / h); h = maxH; }
    var canvas = document.createElement("canvas");
    canvas.width = w; canvas.height = h;
    canvas.getContext("2d").drawImage(img, 0, 0, w, h);
    cb(canvas.toDataURL("image/jpeg", 0.85));
  };
  img.src = url;
}

// Expose programmatic dispatch for custom JS integrations (e.g. Firebase
// auth callbacks that need to send a Msg after the SDK resolves).
window.__sky_send = function(id, value, opts) { __skySend(id, value, opts); };
// sky-nav: intercept clicks on <a sky-nav ...> links so navigation is a
// client-side fetch + innerHTML swap instead of a full page reload.
// Falls back to normal navigation on modifier keys (cmd/ctrl/shift/alt),
// middle-click, and non-GET targets.
document.addEventListener("click", function(ev) {
  if (ev.defaultPrevented) return;
  if (ev.button !== 0) return;
  if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
  var el = ev.target;
  while (el && el.tagName !== "A") el = el.parentElement;
  if (!el) return;
  if (!el.hasAttribute("sky-nav")) return;
  var href = el.getAttribute("href");
  if (!href || href.charAt(0) === "#") return;
  // External links are left to the browser.
  try {
    var u = new URL(href, window.location.href);
    if (u.origin !== window.location.origin) return;
  } catch (e) { return; }
  ev.preventDefault();
  fetch(href, { headers: { "X-Sky-Nav": "1", "X-Sky-Tab": __skyTabId }, credentials: "same-origin" })
    .then(function(r) {
      // r.ok check is load-bearing. Without it, a 404 body like
      // "session not found" (server lost our session_id store
      // entry — TTL expiry, store-restart, store-config change,
      // cross-deploy cookie collision) would be passed verbatim
      // to __skyPatch and become the whole page body.
      // Non-OK → full-page reload, which triggers the runtime's
      // initial-page handler: creates a fresh session_id and
      // re-runs the app's init. Apps gate on session presence
      // (Maybe Session in Model) so the reload lands cleanly on
      // whatever surface their init is configured to render.
      if (!r.ok) { window.location.href = href; return; }
      return r.text().then(function(t) {
        // Push the URL BEFORE patching. __skyPatch runs the data-sky-path
        // sync handler, which pushes a new entry whenever location.pathname
        // doesn't already match. If we patched first (stale pathname), it
        // would push href, and the pushState below would push it AGAIN —
        // two history entries per sky-nav click, so Back needs two presses
        // to move one page. Setting the URL first makes the data-sky-path
        // handler see a matching pathname and replaceState (a no-op) instead.
        window.history.pushState({}, "", href);
        __skyPatch(t);
        var nv = r.headers.get("X-Sky-View");
        if (nv) __skyView = nv;
      });
    })
    .catch(function() { window.location.href = href; });
});
window.addEventListener("popstate", function() {
  fetch(window.location.href, { headers: { "X-Sky-Nav": "1", "X-Sky-Tab": __skyTabId }, credentials: "same-origin" })
    .then(function(r) {
      // Same r.ok gate as the sky-nav click path. Without it,
      // Back/Forward to a URL after the server lost our session
      // renders the 404 body as the whole page.
      if (!r.ok) { window.location.href = window.location.href; return; }
      return r.text().then(function(t) {
        __skyPatch(t);
        var nv = r.headers.get("X-Sky-View");
        if (nv) __skyView = nv;
      });
    })
    .catch(function() { /* Back/Forward fetch failed; leave URL alone. */ });
});
// ── Status banner (connection state) ─────────────────────────
// Single bottom-pinned element rendered by the runtime (NOT by the
// user's view) showing connection health. State machine:
//   "connected"     → invisible
//   "reconnecting"  → amber bar, "Reconnecting…" + attempt counter
//   "offline"       → red bar, "Connection lost — refresh to retry"
// State transitions land in commits 2 + 3; this commit just wires
// the DOM + setter so the rest of the JS can flip states without
// touching the HTML directly. Hidden via display:none until a real
// reconnect attempt fires (no flicker on initial page load).
var __skyStatus = "connected";          // current state
var __skyStatusEl = null;               // banner root, set on DOMContentLoaded
var __skyStatusMsgEl = null;            // text node child
var __skyStatusGraceTimer = null;       // 500ms anti-flicker timer
function __skySetStatus(state, msg) {
  __skyStatus = state;
  if (!__skyStatusEl) return;           // banner not yet injected
  // Strip the previous state class, add the current one.
  var classes = __skyStatusEl.className.split(" ").filter(function(c) {
    return c.indexOf("sky-status--") !== 0;
  });
  classes.push("sky-status--" + state);
  __skyStatusEl.className = classes.join(" ");
  if (__skyStatusMsgEl && msg !== undefined) {
    __skyStatusMsgEl.textContent = msg;
  }
}
function __skyInjectStatusBanner() {
  if (__skyStatusEl) return;            // idempotent
  if (!__skyBannerEnabled) return;      // SKY_LIVE_BANNER=off
  var el = document.createElement("div");
  el.id = "__sky-status";
  el.className = "sky-status sky-status--connected";
  el.setAttribute("role", "status");
  el.setAttribute("aria-live", "polite");
  // Inline styles — no global stylesheet leak. Max z-index puts the
  // banner above any user fixed-position element. Fixed position
  // bottom-center; transitions for fade in/out feel less jarring.
  el.style.cssText = [
    "position:fixed",
    "left:50%",
    "bottom:16px",
    "transform:translateX(-50%)",
    "padding:8px 16px",
    "border-radius:6px",
    "font:13px/1.4 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif",
    "color:#fff",
    "box-shadow:0 2px 8px rgba(0,0,0,0.25)",
    "z-index:2147483647",
    "pointer-events:none",            // never intercept clicks
    "transition:opacity 200ms",
    "opacity:1"
  ].join(";");
  // State-specific styles applied via inline style overrides on
  // each setStatus call would be cleaner, but overriding via class
  // on a <style> tag keeps the inline cssText readable. Append a
  // tiny <style> with the variant rules.
  var style = document.createElement("style");
  style.textContent = "" +
    "#__sky-status.sky-status--connected{display:none}" +
    "#__sky-status.sky-status--reconnecting{background:#b45309}" +
    "#__sky-status.sky-status--offline{background:#b91c1c}" +
    "#__sky-status.sky-status--lost{background:#b91c1c}";
  document.head.appendChild(style);
  var msgEl = document.createElement("span");
  msgEl.className = "sky-status__msg";
  el.appendChild(msgEl);
  document.body.appendChild(el);
  __skyStatusEl = el;
  __skyStatusMsgEl = msgEl;
  // Replay current state in case it changed before DOM was ready.
  __skySetStatus(__skyStatus, "");
}

// ── Server-Sent Events ───────────────────────────────────────
// Frame envelope since v0.9.3+: {seq, body, ackInputs?}. Falls back to
// treating e.data as a raw HTML body when JSON parsing fails, so a
// mixed-version rollout doesn't break the open-SSE connection.
//
// Reverse-proxy hardening: the browser's EventSource has no
// application-level liveness check — if a misbehaving proxy holds the
// socket open with no body or rewrites an upstream 502 to 200 with a
// non-SSE HTML payload, EventSource will fire 'open' and never fire
// 'error', leaving the client silently wedged. The server now sends
// an immediate 'hello' event and a periodic 'heartbeat'; the client
// watchdog (below) treats absence of either as a wedge and force-
// reconnects with backoff. See docs/skylive/architecture.md
// §SSE wedge detection.
var __skySSE = null;
var __skyOpenAt = 0;          // ms timestamp of last EventSource.open
var __skyLastSseAt = 0;       // ms timestamp of any SSE event
var __skyHelloOk = false;     // server sent its handshake this connection
var __skyWatchdogTimer = null;
var __skySseReopenTimer = null;
var __skyForcedClose = false; // true while we're tearing down to reopen
// L3: "path" goes on the SSE URL only when THIS document was loaded
// (first open, bfcache restore) — the server applies it as a
// navigation. A reconnect after a network blip is not a navigation, so
// it must not re-route the session's shared page under the user's other
// tabs.
var __skySsePathNext = true;
function __skyOpenSSE() {
  __skyForcedClose = false;
  __skyHelloOk = false;
  __skyOpenAt = 0;
  // Idempotent: never orphan a live connection. Overwriting __skySSE with a
  // new EventSource WITHOUT closing the old one leaks the old stream — it
  // stays open in the browser, holding one of the ~6 per-host HTTP/1.1
  // connection slots. A few of those (reconnect races, or full-page-nav apps
  // that open a fresh SSE per page while the previous is still tearing down)
  // exhaust the pool and every subsequent request (navigation, clicks) hangs.
  // At most ONE EventSource exists at any time.
  try { if (__skySSE) __skySSE.close(); } catch (_) {}
  // Carry this tab's current URL so the server's reconnect-resync can
  // reconcile the session's page with the URL the browser is actually
  // showing (bfcache Back/Forward + full-reload nav reopen the SSE
  // WITHOUT a route-running GET; without the path the resync would push
  // the last-navigated page over the restored DOM). Re-read on every
  // (re)open so a bfcache restore sends the restored page's path.
  var withPath = __skySsePathNext;
  __skySsePathNext = false;
  // sl=1: this client handles the server's "session-lost" event, so the
  // server may answer a lost session with that event instead of a 404 an
  // EventSource cannot read (live_sse_session_lost.go).
  __skySSE = new EventSource(__skyBase + "/_sky/sse?tab=" + __skyTabId + "&sl=1" +
      (withPath ? "&path=" + encodeURIComponent(location.pathname) : ""));
  // The server has no session for this page (restart with a memory store,
  // another replica, expiry) or its gate refused the stream. Reconnecting
  // cannot help: recover by reloading, once, with an honest banner.
  __skySSE.addEventListener("session-lost", function(e) {
    var d = null;
    try { d = JSON.parse(e.data); } catch (_) {}
    __skyRecoverLostSession(d && d.reason ? d.reason : "session-lost");
  });
  __skySSE.addEventListener("hello", function(e) {
    // Handshake received — we know we hit a real Sky.Live v2 server,
    // not a proxy that intercepted with a generic 200. Anything
    // before hello is suspect, so the connected-state flip happens
    // HERE, not on EventSource.open. Remember that THIS page's
    // server speaks v2 so future watchdog cycles can tighten the
    // wedge-detection threshold to the fast 8s hello timeout.
    __skyServerSpeaksV2 = true;
    __skyHelloOk = true;
    __skyLastSseAt = Date.now();
    // L7: a new server process restarts its broadcast counter; reset the
    // broadcast guard when the process epoch changes, or every broadcast
    // of the new process would be dropped as already seen.
    var hp = null;
    try { hp = JSON.parse(e.data); } catch (_) {}
    if (hp && hp.pe) {
      if (__skyProcEpoch !== null && hp.pe !== __skyProcEpoch) __skyLastGlobalSeq = 0;
      __skyProcEpoch = hp.pe;
    }
    if (__skyStatusGraceTimer !== null) {
      clearTimeout(__skyStatusGraceTimer);
      __skyStatusGraceTimer = null;
    }
    if (__skyStatus !== "connected") {
      __skySetStatus("connected", "");
    }
    __skyRetryAttempts = 0;
    if (__skyRetryTimer !== null) {
      clearTimeout(__skyRetryTimer);
      __skyRetryTimer = null;
    }
    // The session works: a later loss gets a fresh reload budget.
    try { sessionStorage.removeItem(__skyLostKey); } catch (_) {}
    if (__skyEventQueue.length > 0) __skyDrainQueue();
  });
  __skySSE.addEventListener("heartbeat", function(e) {
    __skyLastSseAt = Date.now();
  });
  __skySSE.addEventListener("patch", function(e) {
    __skyLastSseAt = Date.now();
    // Old servers (pre-handshake) only ever send "patch" events.
    // A real patch is itself proof we're talking to a Sky.Live server,
    // not a proxy-rewritten 200-OK, so treat first-patch-without-hello
    // as an implicit handshake. This keeps a new client from trapping
    // itself when a rolling deploy puts it in front of an old server.
    if (!__skyHelloOk) {
      __skyHelloOk = true;
      if (__skyStatusGraceTimer !== null) {
        clearTimeout(__skyStatusGraceTimer);
        __skyStatusGraceTimer = null;
      }
      if (__skyStatus !== "connected") {
        __skySetStatus("connected", "");
      }
      __skyRetryAttempts = 0;
      if (__skyRetryTimer !== null) {
        clearTimeout(__skyRetryTimer);
        __skyRetryTimer = null;
      }
    }
    var frame;
    try { frame = JSON.parse(e.data); } catch (_) {
      // Legacy frame (pre-v0.9.3 server) — raw HTML, no seq to gate on.
      // Open-<select> defence (Bug 3): same-cycle as the patches path.
      // SSE-pushed full-body re-renders during an open dropdown would
      // collapse it; skip the body, the next user interaction triggers
      // reconciliation. Active user paths (sky-nav, popstate, POST
      // text fallback) are NOT defended — those are user-initiated and
      // dropping them would be worse UX than the dropdown collapsing.
      if (document.activeElement && document.activeElement.tagName === "SELECT") return;
      return __skyPatch(e.data.replace(/\\n/g, "\n"));
    }
    if (frame && typeof frame === "object") {
      __skyHandleResponse(frame.seq, frame.ackInputs, function() {
        if (document.activeElement && document.activeElement.tagName === "SELECT") return;
        if (frame.body) __skyPatch(frame.body.replace(/\\n/g, "\n"));
      }, frame.globalSeq, frame.view, "", false);
    }
  });
  // Cycle 3 P50b / Gap C11 — structural-patches SSE event.
  //
  // The producer (Cycle 3 P50a) now ships event:patches for any
  // render whose diff against the previous tree fits in a small
  // patch list (the typical 1-3 attribute/text node change at
  // ~200-1000 B, vs the ~14 KB full body). The legacy event:patch
  // handler above stays for first-renders, reconnect-resync,
  // full-replace fallbacks, and any pre-P50a server.
  //
  // Shape parity with the HTTP /_sky/event reply: frame is
  // {seq, ackInputs, patches} — identical to writeEventJSON's
  // envelope, so __skyApplyPatches consumes both routes without
  // divergence. seq-gating via __skyHandleResponse means out-of-
  // order frames (a stale patches frame arriving after a fresher
  // patch frame, e.g. across a brief network blip) are dropped at
  // the same monotonic guard the HTTP path uses.
  //
  // No open-<select> defence at this outer level — __skyApplyPatches
  // already has its own per-patch focus-restore + open-select skip
  // (live.go:4386+); applying it twice would surface as a no-op
  // either way, but the inner check is the canonical defence.
  // Focus / input-authority / dirty-input filtering all flow through
  // the same code path as the HTTP-side patches application, so
  // in-flight typing is preserved without server-side clientState
  // alignment (the SSE producer passes nil clientState to diffTrees;
  // the client's __skyIsDirty filter takes over).
  __skySSE.addEventListener("patches", function(e) {
    __skyLastSseAt = Date.now();
    // Same implicit-handshake defence as the legacy patch listener:
    // a real patches frame proves we're talking to a Sky.Live server,
    // so unstick the hello check even if the dedicated 'hello' event
    // got eaten by a misbehaving proxy.
    if (!__skyHelloOk) {
      __skyHelloOk = true;
      if (__skyStatusGraceTimer !== null) {
        clearTimeout(__skyStatusGraceTimer);
        __skyStatusGraceTimer = null;
      }
      if (__skyStatus !== "connected") {
        __skySetStatus("connected", "");
      }
      __skyRetryAttempts = 0;
      if (__skyRetryTimer !== null) {
        clearTimeout(__skyRetryTimer);
        __skyRetryTimer = null;
      }
    }
    var frame;
    try { frame = JSON.parse(e.data); }
    catch (_) {
      // Producer guarantees JSON for event:patches; a non-JSON
      // payload is impossible from a P50a+ server. Drop silently
      // rather than running __skyPatch on garbage.
      return;
    }
    if (!frame || typeof frame !== "object" || !frame.patches) return;
    __skyHandleResponse(frame.seq, frame.ackInputs, function() {
      __skyApplyPatches(frame.patches);
    }, frame.globalSeq, frame.view, frame.base, true);
  });
  // L12: a classified update panic in any dispatch path of this session.
  __skySSE.addEventListener("skyerror", function(e) {
    __skyLastSseAt = Date.now();
    var d = null;
    try { d = JSON.parse(e.data); } catch (_) {}
    __skyShowError(d && d.ref ? d.ref : "");
  });
  __skySSE.addEventListener("open", function() {
    // EventSource fired open — but we don't trust this alone, since a
    // proxy can rewrite a non-SSE 200 OK into something that fires
    // open without ever delivering a frame. Wait for 'hello' to flip
    // to connected. Just record the open timestamp so the watchdog
    // can measure "how long have we been open without a hello".
    __skyOpenAt = Date.now();
    __skyLastSseAt = Date.now();
  });
  __skySSE.addEventListener("error", function() {
    // Suppress the banner when we triggered the close ourselves
    // (force-reopen path) — those errors are an artefact of our own
    // teardown, not a real outage signal.
    if (__skyForcedClose) return;
    // CLOSED (2) means the browser failed the connection permanently.
    // Per the EventSource spec, this happens for any non-200 HTTP
    // response (Caddy/Nginx 502 when upstream is down, 504 timeout,
    // 503 service unavailable) AND for the wrong Content-Type. The
    // browser will NOT retry on its own — we have to drive the
    // reconnect ourselves. Without this branch the whole reconnect
    // story collapses behind a reverse proxy that returns proper
    // 5xx codes during outages.
    if (__skySSE && __skySSE.readyState === 2) {
      __skyForceReopenSSE();
      return;
    }
    // CONNECTING (0): browser is auto-retrying (network blip, no HTTP
    // response received yet). Show the banner only if the situation
    // persists past the grace window — a quick error+reopen burst
    // shouldn't paint chrome.
    if (__skyStatus !== "connected") return;
    if (__skyStatusGraceTimer !== null) return;
    __skyStatusGraceTimer = setTimeout(function() {
      __skyStatusGraceTimer = null;
      if (__skySSE && __skySSE.readyState === 1 && __skyHelloOk) return;
      __skySetStatus("reconnecting", __skyMsgReconnecting);
    }, 500);
  });
}

// __skyForceReopenSSE — close the current EventSource and queue a
// fresh open with backoff. Each call bumps the retry counter; once
// it exceeds __skyRetryMaxAttempts the banner flips to "offline" but
// reconnect attempts CONTINUE in the background at the max delay so
// a healed proxy is picked up automatically (otherwise the user is
// permanently stuck unless they click something or refresh, which is
// surprising on push-driven UIs like dashboards or chat). Backoff
// matches the POST retry schedule so the user doesn't see two
// independent timers.
function __skyForceReopenSSE() {
  __skyForcedClose = true;
  try { if (__skySSE) __skySSE.close(); } catch (_) {}
  __skySSE = null;
  if (__skyStatus === "connected") {
    __skySetStatus("reconnecting", __skyMsgReconnecting);
  }
  __skyRetryAttempts++;
  // Session-loss probe: when the SSE is wedged (typically a server
  // restart with the memory store, or a sky.toml [live] store change
  // wiping the persistent session), no amount of reopen retries can
  // recover the lost session — the only path forward is a full page
  // reload, which fires handleInitial and creates a fresh session.
  // We probe with a fake POST: a 404 + X-Sky-Live: 1 + body
  // containing "session not found" is the unambiguous signal that the
  // server is up but doesn't know our cookie. Anything else (network
  // error, 5xx, healthy 200) keeps the normal retry path engaged so
  // we don't reload on a transient blip — full reload destroys
  // uncontrolled-input state that v0.11.7's preservation rules can't
  // bring back.
  __skyProbeSessionLost();
  if (__skyRetryAttempts >= __skyRetryMaxAttempts && __skyStatus !== "offline") {
    __skySetStatus("offline", __skyMsgOffline);
  }
  if (__skySseReopenTimer !== null) {
    clearTimeout(__skySseReopenTimer);
  }
  var delay = Math.min(__skyRetryBaseMs * Math.pow(2, __skyRetryAttempts - 1), __skyRetryMaxMs);
  __skySseReopenTimer = setTimeout(function() {
    __skySseReopenTimer = null;
    __skyOpenSSE();
  }, delay);
}

// __skyProbeSessionLost — fire-and-forget POST whose only purpose is
// to read the server's reaction to our existing sky_sid cookie. If
// the server is up AND has lost our session (memory-store restart,
// store-kind change, session TTL expiry), we get a 404 with the
// X-Sky-Live marker and a "session not found" body. That's the cue
// to hard-reload — every reopen attempt would otherwise loop on the
// same 404 forever.
//
// Must NOT trigger any user-visible side effects on the server. We
// send a Msg name that no real app registers and supply no
// handlerId, so handleEvent's code path goes:
//   session not found → 404 (the case we're probing for)
//   session found, handler not found → 404 with a different body
//   (we explicitly check the body string to avoid false positives).
var __skyProbedReload = false;  // one-shot guard so we don't trigger
                                // multiple reloads from a burst of
                                // failed reopen attempts.
var __skyConsecutiveResync = 0; // consecutive X-Sky-Status:desync soft-resyncs;
                                // reset on any normal response. Backstops a
                                // pathological never-converging view by
                                // escalating to a full reload after a few.
function __skyProbeSessionLost() {
  if (__skyProbedReload) return;
  var headers = {"Content-Type": "application/json"};
  if (__skyCsrfToken) headers["X-Sky-Csrf"] = __skyCsrfToken;
  fetch(__skyBase + "/_sky/event", {
    method: "POST",
    headers: headers,
    body: JSON.stringify({sessionId: __skySid, msg: "__skySessionPing", args: []}),
    credentials: "same-origin"
  }).then(function(r) {
    if (r.status !== 404) return;
    if (r.headers.get("X-Sky-Live") !== "1") return;
    return r.text().then(function(body) {
      // Specifically "session not found" — distinguishes from
      // "handler not found" (which means the session is fine, just
      // our probe Msg name doesn't exist; that's expected and
      // doesn't warrant a reload).
      if (body.indexOf("session not found") < 0) return;
      __skyRecoverLostSession("unknown-session");
    });
  }).catch(function() {
    // Network error / server down. Keep retrying via normal path.
  });
}

// __skyRecoverLostSession — the ONE recovery path for a page whose server
// session is gone (the SSE "session-lost" event, a POST answered
// X-Sky-Status: session-lost, the probe above). Reconnecting cannot bring a
// lost session back, so the page stops its live channel and reloads, which
// mints a fresh session (or, for "auth-required", shows the login form).
//
// Reload-loop guard: the reload times of this page's base path are kept in
// sessionStorage. A third loss inside 60 s means the reload does not restore a
// session (for example requests spread over replicas that share no session
// store). The page then stops and says so, instead of reloading for ever. A
// working session (the SSE hello) clears the record.
var __skyLostKey = "__sky_lost_reloads:" + (__skyBase || "/");
function __skyRecoverLostSession(reason) {
  if (__skyProbedReload) return;
  __skyProbedReload = true;
  __skyForcedClose = true;
  try { if (__skySSE) __skySSE.close(); } catch (_) {}
  __skySSE = null;
  if (__skySseReopenTimer !== null) { clearTimeout(__skySseReopenTimer); __skySseReopenTimer = null; }
  if (__skyWatchdogTimer !== null) { clearInterval(__skyWatchdogTimer); __skyWatchdogTimer = null; }
  var now = Date.now();
  var recent = [];
  try {
    recent = JSON.parse(sessionStorage.getItem(__skyLostKey) || "[]").filter(function(t) {
      return typeof t === "number" && now - t < 60000;
    });
  } catch (_) { recent = []; }
  if (recent.length >= 2) {
    if (window.console && console.warn) {
      console.warn("[sky.live] session lost (" + reason + ") again after reloading; not reloading again");
    }
    __skySetStatus("lost", "The server no longer knows this page's session, and reloading did not restore it. Reload the page to try again.");
    return;
  }
  recent.push(now);
  try { sessionStorage.setItem(__skyLostKey, JSON.stringify(recent)); } catch (_) {}
  if (window.console && console.warn) {
    console.warn("[sky.live] session lost (" + reason + "); reloading the page to start a new session");
  }
  __skySetStatus("lost", reason === "auth-required" ? "Signed out. Reloading…" : "Session ended. Reloading…");
  window.location.reload();
}

// __skyWatchdog — runs every 5s. Two wedge detectors layered:
//   1. Connection has been quiet for longer than __skyHeartbeatTtlMs
//      (35s default). Catches every wedge shape — a proxy holding
//      the socket open with no body, an upstream 502 rewritten to
//      200 + HTML, mid-stream TCP stalls. The 35s threshold is
//      tuned to be just over 2× the server's 15s heartbeat; if the
//      server is new we miss at most one heartbeat before reacting.
//   2. Faster handshake check: once this PAGE has confirmed the
//      server speaks the v2 protocol (any session received a hello),
//      tighten the threshold to __skyHelloTimeoutMs (8s) on every
//      subsequent connection. Pre-v2 servers stay on the slower
//      heartbeat-ttl path so a rolling deploy doesn't wedge new
//      clients hitting old pods. The page-scoped flag survives SSE
//      teardowns + reopens within the same tab.
// Both paths increment the retry counter via __skyForceReopenSSE,
// so a wedge that persists reaches "offline" instead of looping
// forever — but reopen attempts continue at the max delay so a
// healed proxy reconnects automatically without a refresh.
var __skyServerSpeaksV2 = false;
function __skyWatchdog() {
  // If we have no live EventSource AND no reopen scheduled, the
  // 'error' handler must have missed (rare race) or some path tore
  // it down without re-arming. Drive the reopen here so the page
  // never gets permanently disconnected.
  if (!__skySSE && __skySseReopenTimer === null) {
    __skyForceReopenSSE();
    return;
  }
  if (!__skySSE) return;
  // CLOSED (2): browser failed the connection (non-200, wrong CT)
  // and won't retry. The 'error' handler should have caught this,
  // but cover the case where it didn't fire (e.g. error during
  // initial handshake before listeners attached, or a browser
  // implementation quirk). Single source of truth — both paths end
  // in __skyForceReopenSSE.
  if (__skySSE.readyState === 2) {
    if (!__skyForcedClose) {
      __skyForceReopenSSE();
    }
    return;
  }
  if (__skySSE.readyState !== 1) return;  // CONNECTING (0): browser is retrying, leave it
  var now = Date.now();
  // Effective threshold:
  //   - Brand-new SSE on a v2-confirmed server → fast hello timeout
  //     (8s) since we expect a hello promptly.
  //   - Otherwise → conservative heartbeat ttl (35s) so old servers
  //     and idle dashboards don't false-positive.
  var quietMs = now - __skyLastSseAt;
  var threshold = __skyHeartbeatTtlMs;
  if (__skyServerSpeaksV2 && !__skyHelloOk) {
    threshold = __skyHelloTimeoutMs;
  }
  if (quietMs > threshold) {
    if (window.console && console.warn) {
      console.warn("[sky.live] SSE quiet for " + quietMs +
        "ms (threshold " + threshold + "ms) — reopening");
    }
    __skyForceReopenSSE();
  }
}

// Kick off the SSE connection + watchdog. Watchdog interval is short
// enough (5s) that a wedge is detected within 5s + helloTimeout / ttl
// of the actual fault, and long enough to not be a measurable CPU cost.
__skyOpenSSE();
__skyWatchdogTimer = setInterval(__skyWatchdog, 5000);

// On tab visibility change, re-evaluate immediately — when a tab
// resumes from background the OS may have torn down the underlying
// TCP, but EventSource sometimes lags in detecting it. Eager check
// avoids the user staring at a stale UI for the full watchdog cycle.
document.addEventListener("visibilitychange", function() {
  if (document.visibilityState === "visible") {
    __skyWatchdog();
  }
});

// ── Init ─────────────────────────────────────────────────────
// Bind initial DOM event listeners + inject the status banner once
// the HTML is parsed. Banner needs document.body to exist, so it
// goes through the same gate as event binding.
function __skyInit() {
  __skyBindEvents(document);
  __skyInjectStatusBanner();
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", __skyInit);
} else {
  __skyInit();
}
`
