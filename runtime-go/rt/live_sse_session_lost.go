//go:build !js

// live_sse_session_lost.go — the SSE endpoint's answer when it will not stream.
//
// A Sky.Live page holds a session id in a cookie; the session itself lives in
// the server's session store. Three things break the pair while the page stays
// open:
//
//   - the process restarts and the store is `memory` (every redeploy);
//   - the request lands on a replica that never saw the session (several
//     upstream slots, no shared store, no sticky routing);
//   - the session expires (TTL) or is evicted.
//
// An EventSource cannot read the status or the body of a non-200 answer. The
// endpoint used to answer 404 "session not found", the browser saw only an
// `error`, and the client had to guess from a side probe (a POST to
// /_sky/event) whether to reload. When that probe answered anything else
// (a gate's 401, a proxy page) the client reconnected for ever.
//
// Now the client that understands it says so (`sl=1` on the SSE URL), and the
// endpoint answers 200 text/event-stream with ONE classified event and a clean
// end of stream:
//
//	event: session-lost
//	data: {"reason":"unknown-session"}
//
// The client reloads once (guarded against a reload loop, see
// __skyRecoverLostSession in live_client_asset.go), which mints a fresh
// session. A request without `sl=1` is a page loaded before this change (for
// example a tab opened against the previous process of a redeploy): it keeps
// the old 404 / 400 answer, which its old client already handles.
//
// The same event carries `auth-required` when an in-process sub-app's gate (the
// console's login gate) rejects the SSE request: the reload then shows the
// login form, instead of the client retrying a 401 it cannot read.
package rt

import (
	"fmt"
	"net/http"
)

// Reasons carried by the session-lost event and the server log line.
const (
	sseLostNoCookie       = "no-session-cookie"
	sseLostUnknownSession = "unknown-session"
	sseLostEvicted        = "session-evicted"
	sseLostAuthRequired   = "auth-required"
)

// sseClientUnderstandsSessionLost reports whether the SSE request came from a
// client that handles the `session-lost` event (it sends `sl=1`).
func sseClientUnderstandsSessionLost(r *http.Request) bool {
	return r != nil && r.URL.Query().Get("sl") == "1"
}

// writeSSESessionLost answers an SSE request that has no session this process
// will stream. It logs the condition at info with its reason, then answers in
// the form the requesting client understands (see the file comment).
func (app *liveApp) writeSSESessionLost(w http.ResponseWriter, r *http.Request, reason string) {
	logSSESessionLost(r, reason)
	if sseClientUnderstandsSessionLost(r) {
		writeSSESessionLostStream(w, reason)
		return
	}
	// Legacy answer for a client that predates the event.
	w.Header().Set("X-Sky-Live", "1")
	if reason == sseLostNoCookie {
		http.Error(w, "no session", http.StatusBadRequest)
		return
	}
	w.Header().Set("X-Sky-Status", "session-lost")
	http.Error(w, "session not found", http.StatusNotFound)
}

// writeSSESessionLostStream writes the whole classified answer: SSE headers,
// the event, and a clean end of stream (the handler returns after this).
func writeSSESessionLostStream(w http.ResponseWriter, reason string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Sky-Live", "1")
	h.Set("X-Sky-Status", "session-lost")
	h.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	writeSSESessionLostFrame(w, reason)
}

// writeSSESessionLostFrame writes the event on an already-open stream.
func writeSSESessionLostFrame(w http.ResponseWriter, reason string) {
	_, _ = fmt.Fprintf(w, "event: session-lost\ndata: {\"reason\":%q}\n\n", reason)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// logSSESessionLost records why a page lost its live channel. Info, not warn:
// after a redeploy with a memory store every open tab produces one line, and
// that is expected. The session id is not logged (it is a bearer credential).
func logSSESessionLost(r *http.Request, reason string) {
	path := ""
	if r != nil {
		path = r.URL.Path
	}
	logStructured("info", "live.sse.session_lost",
		"reason", reason,
		"path", path,
		"recoverable", sseClientUnderstandsSessionLost(r))
}

// gateProbeWriter runs an auth gate without letting its failure page reach an
// EventSource. It keeps its own header map so a denied gate's headers do not
// leak into the SSE answer; on success the headers the gate set (a refreshed
// cookie, for example) are copied to the real writer.
type gateProbeWriter struct {
	h http.Header
}

func (g *gateProbeWriter) Header() http.Header         { return g.h }
func (g *gateProbeWriter) WriteHeader(int)             {}
func (g *gateProbeWriter) Write(b []byte) (int, error) { return len(b), nil }

// gateSSE wraps an in-process sub-app's SSE handler with its auth gate. A
// request from a client that understands the session-lost event gets
// `auth-required` when the gate denies it; any other request gets the gate's
// own answer, as before.
func gateSSE(gate func(http.ResponseWriter, *http.Request) bool, h http.HandlerFunc) http.HandlerFunc {
	if gate == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !sseClientUnderstandsSessionLost(r) {
			if !gate(w, r) {
				return
			}
			h(w, r)
			return
		}
		probe := &gateProbeWriter{h: http.Header{}}
		if !gate(probe, r) {
			logSSESessionLost(r, sseLostAuthRequired)
			writeSSESessionLostStream(w, sseLostAuthRequired)
			return
		}
		for k, vs := range probe.h {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		h(w, r)
	}
}
