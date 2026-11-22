//go:build !js

package rt

// The Sky.Live client half of the <html> attributes (Live.withDocument,
// live_document.go). A frame's "doc" operation is the whole new set as a list
// of {name, value}. The client sets each one on document.documentElement and
// removes the ones it set before that the new set does not have. It never
// touches an attribute it did not set, so the runtime's own data-sky-*
// markers stay. The values are data: they are set with setAttribute, never
// parsed as HTML or run as script. A name outside the allowed set is refused.
const liveDocClientJS = `
var __skyDocNames = null;
function __skyDocProblem(name) {
  try { if (window.console && console.error) console.error("[sky.live] " + name); } catch (_) {}
}
function __skyDocNameOk(n) {
  if (n === "lang" || n === "dir" || n === "class" || n === "translate") return true;
  if (!/^data-[a-z0-9][a-z0-9._-]*$/.test(n)) return false;
  return n !== "data-sky" && n.indexOf("data-sky-") !== 0;
}
function __skyApplyDoc(doc) {
  if (doc === undefined || doc === null) return;
  if (!Array.isArray(doc)) { __skyDocProblem("DocPayloadRejected"); return; }
  var el = document.documentElement, next = {}, own = Object.prototype.hasOwnProperty;
  for (var i = 0; i < doc.length; i++) {
    var a = doc[i];
    if (!a || typeof a.name !== "string" || typeof a.value !== "string" || !__skyDocNameOk(a.name)) {
      __skyDocProblem("DocPayloadRejected");
      continue;
    }
    next[a.name] = a.value;
  }
  var prev = __skyDocNames || {};
  for (var p in prev) {
    if (own.call(prev, p) && !own.call(next, p)) el.removeAttribute(p);
  }
  for (var n in next) {
    if (own.call(next, n) && el.getAttribute(n) !== next[n]) el.setAttribute(n, next[n]);
  }
  __skyDocNames = next;
}
function __skyDocBodyReply(text) {
  var marker = "<!--sky-doc:", at = text.lastIndexOf(marker);
  if (at < 0 || text.slice(-3) !== "-->") return { body: text, doc: null };
  var body = text.slice(0, at), doc = null;
  try {
    var bytes = atob(text.slice(at + marker.length, -3));
    var data = new Uint8Array(bytes.length);
    for (var i = 0; i < bytes.length; i++) data[i] = bytes.charCodeAt(i);
    doc = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(data));
    if (!Array.isArray(doc)) throw Error("invalid doc list");
  } catch (_) { __skyDocProblem("DocPayloadRejected"); doc = null; }
  return { body: body, doc: doc };
}
function __skyFetchedPageDoc(text) {
  var block = text.match(/<script type="application\/json" id="sky-live-cfg">([\s\S]*?)<\/script>/);
  if (!block) return null;
  try { var d = (JSON.parse(block[1]) || {}).doc; return d === undefined ? null : d; }
  catch (_) { __skyDocProblem("DocPayloadRejected"); return null; }
}
`
