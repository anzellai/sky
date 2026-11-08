//go:build !js

package rt

// Scroll delivery uses the same accepted frame and actual-DOM barrier as its
// view. IDs and commands are data; neither is evaluated as JavaScript or CSS.
const liveScrollClientJS = `
function __skyScrollProblem(name) {
  try { if (window.console && console.error) console.error("[sky.live] " + name); } catch (_) {}
}
function __skyAfterDOMCommit(deliveries, view) {
  // A deferred render that is superseded never committed its complete DOM.
  // Do not later execute its command against a same-id node of another render.
  var held = typeof __skyHeld !== "undefined" ? __skyHeld : null;
  if (held) held.scrollDelivery = null;
  if (!deliveries || deliveries.length === 0) return;
  if (!Array.isArray(deliveries)) { __skyScrollProblem("ScrollPayloadRejected"); return; }
  var own = [];
  for (var i = 0; i < deliveries.length; i++) {
    var d = deliveries[i];
    if (!d || typeof d.tab !== "string" || !d.command || typeof d.command !== "object") {
      __skyScrollProblem("ScrollPayloadRejected"); continue;
    }
    if (d.tab === __skyTabId) own.push(d);
  }
  if (own.length === 0) return;
  if (typeof view !== "string" || !view) { __skyScrollProblem("ScrollViewMissing"); return; }
  if (view !== __skyView) return;
  if (held) {
    held.scrollDelivery = { deliveries: own, view: view, seq: __skyLastAppliedSeq };
    return;
  }
  for (var j = 0; j < own.length; j++) {
    try { __skyScrollApply(own[j].command); }
    catch (_) { __skyScrollProblem("ScrollFailure"); }
  }
}
function __skyFinishHeldScroll(delivery) {
  if (delivery && delivery.seq === __skyLastAppliedSeq && delivery.view === __skyView) {
    __skyAfterDOMCommit(delivery.deliveries, delivery.view);
  }
}
function __skyScrollBodyReply(text) {
  var marker = "<!--sky-scroll:", at = text.lastIndexOf(marker);
  if (at < 0 || text.slice(-3) !== "-->") return { body: text, scrolls: null };
  var body = text.slice(0, at), deliveries = null;
  try {
    var bytes = atob(text.slice(at + marker.length, -3));
    var data = new Uint8Array(bytes.length);
    for (var i = 0; i < bytes.length; i++) data[i] = bytes.charCodeAt(i);
    deliveries = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(data));
    if (!Array.isArray(deliveries)) throw Error("invalid scroll list");
  } catch (_) { __skyScrollProblem("ScrollPayloadRejected"); deliveries = null; }
  return { body: body, scrolls: deliveries };
}
function __skyFetchedPageScrolls(text) {
  var block = text.match(/<script type="application\/json" id="sky-live-cfg">([\s\S]*?)<\/script>/);
  if (!block) return null;
  try { return (JSON.parse(block[1]) || {}).scrolls || null; }
  catch (_) { __skyScrollProblem("ScrollPayloadRejected"); return null; }
}
// A fetched page is a full render, just like a full-body event response.
// Its seq must gate URL movement as well as DOM mutation and scroll effects.
function __skyApplyFetchedPage(response, text, move, finished) {
  var raw = response.headers.get("X-Sky-Seq");
  var seq = raw ? parseInt(raw, 10) : 0;
  var view = response.headers.get("X-Sky-View");
  __skyHandleResponse(seq, null, function() {
    if (move) move();
    __skyPatch(text);
    if (view) __skyView = view;
    if (finished) finished();
  }, undefined, view, "", false, __skyFetchedPageScrolls(text));
}
`
