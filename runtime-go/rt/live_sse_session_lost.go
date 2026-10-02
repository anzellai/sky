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
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
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
//
// The gate does not stop at the open. An SSE stream lives as long as its tab,
// and the sub-app keeps pushing data down it (the console's `Sub.every` tick
// pushes metrics, logs and traces), so a gate that ran only once would let a
// tab opened before a sign-out keep receiving data after it. The open stream
// re-runs the same gate every subAppStreamRegateEvery, and ends at once when
// its console cookie id is revoked (`_logout`, a failed app re-check). When
// the gate denies, or the cookie is revoked, the stream ends with the session-lost `auth-required` event:
// the client stops, reloads, and the reload shows the gate's refusal.
func gateSSE(gate func(http.ResponseWriter, *http.Request) bool, h http.HandlerFunc) http.HandlerFunc {
	if gate == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		understands := sseClientUnderstandsSessionLost(r)
		if !understands {
			if !gate(w, r) {
				return
			}
		} else {
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
		}
		serveGatedStream(w, r, gate, h, understands)
	}
}

// subAppStreamRegateEvery is how often an open gated stream re-runs its gate.
// The gate itself decides what a re-run costs: under SKY_CONSOLE_AUTH=app it
// answers from its per-cookie record and calls the app's check only once the
// 60 s re-check window has passed, so an open tab ends at most this long
// after the window closes. A var so tests can run it fast.
var subAppStreamRegateEvery = time.Second

// serveGatedStream runs an admitted stream and ends it when its gate stops
// admitting the request.
func serveGatedStream(w http.ResponseWriter, r *http.Request, gate func(http.ResponseWriter, *http.Request) bool, h http.HandlerFunc, understands bool) {
	// Re-run the gate with the request the browser would send now: the
	// opening request plus any cookie the gate just issued (an app-mode
	// console mints its cookie on the first admitted request).
	recheck := requestWithIssuedCookies(r, w.Header())
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	wake, revoked, release := watchConsoleCookieRevocation(recheck)
	defer release()

	var lost atomic.Bool
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		t := time.NewTicker(subAppStreamRegateEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-wake:
			}
			if revoked() || !gate(&gateProbeWriter{h: http.Header{}}, recheck) {
				lost.Store(true)
				cancel()
				return
			}
		}
	}()

	h(w, r.WithContext(ctx))
	cancel()
	<-watcherDone
	if !lost.Load() {
		return
	}
	logSSESessionLost(r, sseLostAuthRequired)
	if understands && r.Context().Err() == nil {
		writeSSESessionLostFrame(w, sseLostAuthRequired)
	}
}

// requestWithIssuedCookies returns r with the cookies set in hdr (the answer
// about to go out) added to, or replacing, the ones r carries. A cleared
// cookie is dropped.
func requestWithIssuedCookies(r *http.Request, hdr http.Header) *http.Request {
	issued := (&http.Response{Header: http.Header{"Set-Cookie": hdr.Values("Set-Cookie")}}).Cookies()
	if len(issued) == 0 {
		return r
	}
	byName := map[string]*http.Cookie{}
	var order []string
	for _, c := range r.Cookies() {
		if _, seen := byName[c.Name]; !seen {
			order = append(order, c.Name)
		}
		byName[c.Name] = c
	}
	for _, c := range issued {
		if _, seen := byName[c.Name]; !seen {
			order = append(order, c.Name)
		}
		if c.MaxAge < 0 || c.Value == "" {
			byName[c.Name] = nil
			continue
		}
		byName[c.Name] = &http.Cookie{Name: c.Name, Value: c.Value}
	}
	out := r.Clone(r.Context())
	out.Header.Del("Cookie")
	for _, name := range order {
		if c := byName[name]; c != nil {
			out.AddCookie(c)
		}
	}
	return out
}
