//go:build !js

// stream_deadline.go — a long-lived response lifts the server's per-request
// deadlines.
//
// Sky.Http.Server (Server.listen, rt_server.go) builds its http.Server with
// ReadTimeout and WriteTimeout (30 s each by default, SKY_HTTP_*_TIMEOUT). Those
// are right for an ordinary request/response: a slow or stalled client cannot
// hold a connection for ever. They are wrong for a response that is MEANT to
// stay open. net/http arms both deadlines on the connection when it reads the
// request, and nothing ever moved them, so every stream served by a
// Server.listen host was cut at 30 s:
//
//   - the embedded console's Sky.Live SSE (/_sky/console/_sky/sse), which a
//     Sky.Http.Server app or a Sky.Spa backend mounts in-process;
//   - a Sky.Http.Server.Stream response (text/event-stream and any other
//     streamed body), including the Sky.Spa push topic (Spa_streamTopic);
//   - a WebSocket, whose hijacked connection keeps the deadlines it had.
//
// The cut is not a clean end of stream. The write deadline fails the next
// write, net/http cannot send the terminating chunk, and it closes the
// connection. A reverse proxy then reports the upstream body as truncated
// (Caddy: "aborting with incomplete response … unexpected EOF"), resets the
// HTTP/2 stream (browser: net::ERR_HTTP2_PROTOCOL_ERROR on a 200), and the
// EventSource reconnects — about every 30 s plus the retry delay, for ever.
//
// Sky.Live's own server (live.go) sets neither timeout, so a Sky.Live app did
// not show the fault. The fix is at the stream, not at the server: each
// long-lived handler lifts both deadlines on ITS connection before it writes,
// through http.ResponseController, which reaches the connection through every
// middleware writer that implements Unwrap (statusCapture, spaRpcRecorder,
// spaNotFoundInterceptWriter). Ordinary requests keep their deadlines.
package rt

import (
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

// streamDeadlineWarned makes the "cannot lift the deadline" warning fire once
// per process. The condition is a wrapper that hides the connection, which is a
// property of the build, not of one request.
var streamDeadlineWarned atomic.Bool

// releaseStreamDeadlines clears the read and write deadlines of the connection
// that carries w, so a stream is not cut by the server's per-request timeouts.
// what names the stream kind for the warning. It returns false when the
// deadlines could not be cleared (a wrapper in the chain does not implement
// Unwrap); the stream still runs, and the warning names the cause.
func releaseStreamDeadlines(w http.ResponseWriter, what string) bool {
	rc := http.NewResponseController(w)
	errW := rc.SetWriteDeadline(time.Time{})
	errR := rc.SetReadDeadline(time.Time{})
	if errW == nil && errR == nil {
		return true
	}
	if errors.Is(errW, http.ErrNotSupported) || errors.Is(errR, http.ErrNotSupported) {
		if streamDeadlineWarned.CompareAndSwap(false, true) {
			logStructured("warn", "stream.deadline_not_released",
				"stream", what,
				"reason", "a ResponseWriter wrapper does not implement Unwrap; the server timeouts will cut this stream")
		}
	}
	return false
}
