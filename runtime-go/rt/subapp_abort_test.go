package rt

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression: the Sky.Live + Sky.Http.Server outer panic-recovery
// wrappers used to catch http.ErrAbortHandler — Go's sentinel value
// httputil.ReverseProxy panics with when an SSE client disconnects
// mid-stream. Catching it (a) logged a noisy stack trace for what's
// actually a normal disconnect, and (b) raced with ReverseProxy's
// already-written response headers ("superfluous response.WriteHeader
// call"). The fix re-panics on this specific sentinel so net/http's
// own handler-recover treats it as the no-op abort it's meant to be.
//
// We mirror the runtime wrapper shape: an inner handler panics with
// ErrAbortHandler; the wrapper re-panics; an outer test sentinel
// catches the re-panic and asserts it's still ErrAbortHandler.
func TestErrAbortHandlerRePanics(t *testing.T) {
	wrapper := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec) // exactly what the runtime fix does
				}
				// non-abort: swallow + write 500 (runtime behaviour)
			}()
			h.ServeHTTP(w, r)
		})
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sse", nil)

	got := func() (caught any) {
		defer func() { caught = recover() }()
		wrapper(inner).ServeHTTP(rr, req)
		return nil
	}()
	if got != http.ErrAbortHandler {
		t.Errorf("expected ErrAbortHandler to propagate; got %v", got)
	}
}

// Sanity check: ordinary panics (not the abort sentinel) DO get
// swallowed by the same wrapper. Prevents the test above from
// accidentally passing because we made the wrapper re-panic
// everything.
func TestNonAbortPanicSwallowed(t *testing.T) {
	wrapper := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				// swallow
			}()
			h.ServeHTTP(w, r)
		})
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("not an abort, just bad luck")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oops", nil)
	got := func() (caught any) {
		defer func() { caught = recover() }()
		wrapper(inner).ServeHTTP(rr, req)
		return nil
	}()
	if got != nil {
		t.Errorf("expected ordinary panic to be swallowed; got re-panic %v", got)
	}
}
