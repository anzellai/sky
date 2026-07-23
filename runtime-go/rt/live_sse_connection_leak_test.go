package rt

import (
	"strings"
	"testing"
)

// Regression: the SSE EventSource lifecycle must never orphan a live
// connection, and must release it on navigation. Sky.Live opens one SSE
// (EventSource) per page; a streaming EventSource holds one of the browser's
// ~6-per-host HTTP/1.1 connection slots. Two ways connections used to pile up
// until the pool exhausted and every request (navigation, clicks, images)
// froze:
//
//  1. __skyOpenSSE overwrote __skySSE with a fresh EventSource WITHOUT closing
//     the old one first — a reconnect race or a double-open leaked the old
//     stream.
//  2. Nothing closed the EventSource on navigation, so an app that navigates
//     via full-page loads (a fresh SSE per page) overlapped the closing stream
//     with the next page's new one under rapid clicking.
//
// Symptom: the whole tab freezes, the spinner never resolves, clicks are no-ops
// — the classic connection-pool-exhaustion signature.
func TestSseOpenIsIdempotentAndClosesOnUnload(t *testing.T) {
	js := liveJSWithCfgAndCsrfWithBase("test-sid", liveBannerConfig{}, "csrf-token", "")

	// __skyOpenSSE must close any existing connection BEFORE creating a new one.
	open := strings.Index(js, "function __skyOpenSSE()")
	if open < 0 {
		t.Fatal("__skyOpenSSE missing")
	}
	newES := strings.Index(js[open:], "new EventSource(")
	if newES < 0 {
		t.Fatal("__skyOpenSSE no longer creates an EventSource")
	}
	body := js[open : open+newES]
	if !strings.Contains(body, "__skySSE.close()") {
		t.Error("__skyOpenSSE creates a new EventSource without first closing the " +
			"existing __skySSE — a reconnect race leaks the old stream and exhausts " +
			"the browser's connection pool")
	}

	// A pagehide listener must close the SSE so navigation frees the slot.
	if !strings.Contains(js, `addEventListener("pagehide"`) {
		t.Error("no pagehide listener — the SSE connection is not released on navigation")
	}
	// The pagehide teardown must actually close + null the connection.
	ph := strings.LastIndex(js, `addEventListener("pagehide"`)
	tail := js[ph:]
	if !strings.Contains(tail[:min(len(tail), 400)], "__skySSE.close()") ||
		!strings.Contains(tail[:min(len(tail), 400)], "__skySSE = null") {
		t.Error("pagehide handler does not close + null __skySSE — the connection " +
			"lingers and rapid full-page navigation exhausts the pool")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
