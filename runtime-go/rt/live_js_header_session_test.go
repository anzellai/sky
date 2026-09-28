//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The client half of the header session transport (live_session_header.go),
// run for real: the embedded client JS executes in node against a minimal
// browser stub, with the page's boot config carrying a session token.
//
// Stream run (fetch + ReadableStream available):
//  1. the SSE stream is read with fetch, sends X-Sky-Session, parses events
//     split across chunks (hello, patch), and adopts a token the SSE response
//     hands over; an ended stream reports the EventSource-shaped error;
//  2. an event POST sends X-Sky-Session and adopts a new token from the
//     response;
//  3. an SSE rotate frame posts the ticket with the header and adopts the
//     token /_sky/rotate returns;
//  4. the unload flush uses a keepalive fetch that carries the header (a
//     beacon cannot).
//
// Ticket run (no streaming fetch): the client asks /_sky/sse-ticket with the
// header and opens an EventSource on ?tk=<ticket>; the stream's listeners
// reach that EventSource; an error closes it so the single-use ticket is
// never replayed.
//
// Skips when node is absent, like TestLiveJSSyntaxValid.
func TestLiveJS_HeaderSessionClient(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping the header-session client test")
	}
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const mode = process.argv[3];
const T0 = "00000000000000000000000000000000";
const T1 = "11111111111111111111111111111111";
const T2 = "22222222222222222222222222222222";
const T3 = "33333333333333333333333333333333";
const fetches = [];
const sources = [];
let reloads = 0;
let sseController = null;
function Headers(h) { this.h = h || {}; }
Headers.prototype.get = function (k) { return this.h[k] === undefined ? null : this.h[k]; };
const enc = new TextEncoder();
const routes = {
  "/_sky/sse": () => ({ ok: true, status: 200,
    headers: new Headers({ "Content-Type": "text/event-stream", "X-Sky-Session": T1 }),
    body: new ReadableStream({ start(c) { sseController = c; } }) }),
  "/_sky/event": () => ({ ok: true, status: 200,
    headers: new Headers({ "X-Sky-Live": "1", "Content-Type": "application/json", "X-Sky-Session": T2 }),
    json: async () => ({ seq: 1 }), text: async () => "" }),
  "/_sky/rotate": () => ({ ok: true, status: 200, headers: new Headers({ "X-Sky-Session": T3 }),
    json: async () => ({ sid: "NEWSID", token: T3 }) }),
  "/_sky/sse-ticket": () => ({ ok: true, status: 200, headers: new Headers({}),
    json: async () => ({ ticket: "K1" }) }),
};
const sandbox = {
  console: { warn() {}, log() {}, error() {} },
  setTimeout: (fn, ms) => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { pathname: "/", href: "http://x/", reload() { reloads++; } },
  navigator: { sendBeacon() { throw new Error("beacon used in header mode"); } },
  document: {
    readyState: "loading",
    getElementById: (id) => id === "sky-live-cfg" ? { textContent: JSON.stringify({ sid: "SID0", tab: "tab-A", tok: T0 }) } : null,
    addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    body: null, activeElement: null, visibilityState: "visible",
  },
  EventSource: function (url) {
    this.url = url; this.readyState = 0; this.closed = false; this.l = {};
    this.addEventListener = (ev, fn) => { (this.l[ev] = this.l[ev] || []).push(fn); };
    this.close = () => { this.closed = true; this.readyState = 2; };
    this.fire = (ev, data) => (this.l[ev] || []).forEach((fn) => fn({ type: ev, data }));
    sources.push(this);
  },
  fetch: (url, opts) => {
    fetches.push({ url: String(url), opts: opts || {} });
    const path = String(url).split("?")[0];
    const mk = routes[path];
    return Promise.resolve(mk ? mk() : { ok: true, status: 200, headers: new Headers({}), json: async () => ({}), text: async () => "" });
  },
  Blob: function () {},
  Uint8Array, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
  TextDecoder, AbortController,
};
if (mode === "stream") {
  sandbox.ReadableStream = ReadableStream;
  sandbox.Response = function () {};
  sandbox.Response.prototype.body = null;
}
sandbox.window = sandbox;
sandbox.window.addEventListener = () => {};
sandbox.window.crypto = { getRandomValues: (b) => { for (let i = 0; i < b.length; i++) b[i] = i; return b; } };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const g = (n) => vm.runInContext(n, sandbox);
const tick = async (n) => { for (let i = 0; i < (n || 6); i++) await new Promise((r) => setImmediate(r)); };
const hdr = (f) => f && f.opts && f.opts.headers ? f.opts.headers["X-Sky-Session"] : undefined;
(async () => {
  const out = { mode };
  g("__skyOpenSSE()");
  await tick();
  if (mode === "stream") {
    const sse = fetches.find((f) => f.url.indexOf("/_sky/sse?") >= 0);
    out.sseHeader = hdr(sse);
    out.usedEventSource = sources.length;
    // hello split across two chunks, then a patch frame.
    sseController.enqueue(enc.encode(": pad\n\nevent: hel"));
    sseController.enqueue(enc.encode("lo\ndata: {\"v\":1,\"sid\":\"SID0\",\"pe\":\"p1\"}\n\n"));
    await tick(12);
    out.helloOk = g("__skyHelloOk");
    out.tokAfterSse = g("__skyTok");
    sseController.enqueue(enc.encode("event: rotate\ndata: {\"ticket\":\"TK\"}\n\n"));
    await tick(12);
    const rot = fetches.find((f) => f.url.endsWith("/_sky/rotate"));
    out.rotateHeader = hdr(rot);
    out.rotateBody = rot ? JSON.parse(rot.opts.body) : null;
    await tick(12);
    out.tokAfterRotate = g("__skyTok");
    await g("__skyPostEventNow")({ sessionId: "NEWSID", seq: 2, msg: "", args: [], handlerId: "h", tab: "tab-A" }).catch(() => {});
    const ev = fetches.filter((f) => f.url.endsWith("/_sky/event")).pop();
    out.eventHeader = hdr(ev);
    out.tokAfterEvent = g("__skyTok");
    // The stream ends: the wrapper reports a permanent failure.
    let errState = null;
    g("__skySSE").addEventListener("error", function () { errState = this.readyState; });
    sseController.close();
    await tick(12);
    out.errorOnEnd = errState;
    // Unload flush: a keepalive fetch with the header.
    g("__skyInputsSnapshot = function () { return {x: 1}; }");
    g("__skyFlushPendingBeacon()");
    const fl = fetches.filter((f) => f.url.endsWith("/_sky/event")).pop();
    out.flushKeepalive = !!(fl && fl.opts.keepalive);
    out.flushHeader = hdr(fl);
  } else {
    const tk = fetches.find((f) => f.url.endsWith("/_sky/sse-ticket"));
    out.ticketHeader = hdr(tk);
    out.ticketBody = tk ? JSON.parse(tk.opts.body) : null;
    out.esUrlHasTicket = sources.length === 1 && sources[0].url.indexOf("&tk=K1") >= 0;
    const es = sources[0];
    es.fire("open");
    es.fire("hello", JSON.stringify({ v: 1, sid: "SID0", pe: "p1" }));
    out.helloOk = g("__skyHelloOk");
    out.wrapperOpen = g("__skySSE.readyState");
    es.fire("error");
    out.esClosedOnError = es.closed;
  }
  out.reloads = reloads;
  process.stdout.write(JSON.stringify(out));
})().catch((e) => { process.stdout.write("HARNESS-ERROR " + e.stack); process.exit(1); });
`
	dir := t.TempDir()
	jsPath := dir + "/client.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(liveClientJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(mode string, wants []string) {
		out, err := exec.Command(node, harnessPath, jsPath, mode).CombinedOutput()
		if err != nil {
			t.Fatalf("node harness (%s) failed: %v\n%s", mode, err, out)
		}
		got := string(out)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("header-session client (%s): missing %s in %s", mode, want, got)
			}
		}
	}
	run("stream", []string{
		`"sseHeader":"00000000000000000000000000000000"`,
		`"usedEventSource":0`,
		`"helloOk":true`,
		`"tokAfterSse":"11111111111111111111111111111111"`,
		`"rotateHeader":"11111111111111111111111111111111"`,
		`"rotateBody":{"tab":"tab-A","ticket":"TK"}`,
		`"tokAfterRotate":"33333333333333333333333333333333"`,
		`"eventHeader":"33333333333333333333333333333333"`,
		`"tokAfterEvent":"22222222222222222222222222222222"`,
		`"errorOnEnd":2`,
		`"flushKeepalive":true`,
		`"flushHeader":"22222222222222222222222222222222"`,
		`"reloads":0`,
	})
	run("ticket", []string{
		`"ticketHeader":"00000000000000000000000000000000"`,
		`"ticketBody":{"tab":"tab-A"}`,
		`"esUrlHasTicket":true`,
		`"helloOk":true`,
		`"wrapperOpen":1`,
		`"esClosedOnError":true`,
		`"reloads":0`,
	})
}
