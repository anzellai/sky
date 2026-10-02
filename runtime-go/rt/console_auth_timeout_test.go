package rt

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The Sky Console's call into `App.withConsoleAuth` is bounded.
//
// Regression: invokeConsoleAuthCallback forced the check's Task with no
// time limit. A check stuck on a slow database query stalled every console
// request, and an open console stream whose periodic re-check hung kept
// streaming live telemetry. Now a check that has not answered within
// consoleAuthCallbackTimeout denies (fail closed), and its late answer is
// discarded.

// blockingConsoleCallback admits while `admin` is true, and while `block`
// is true its Task waits until the test ends (a stuck query).
type blockingConsoleCallback struct {
	admin   atomic.Bool
	block   atomic.Bool
	release chan struct{}
	once    sync.Once
}

func newBlockingConsoleCallback(t *testing.T) *blockingConsoleCallback {
	cb := &blockingConsoleCallback{release: make(chan struct{})}
	t.Cleanup(func() { cb.once.Do(func() { close(cb.release) }) })
	return cb
}

func (b *blockingConsoleCallback) fn() any {
	return func(_ any) any {
		return func() any {
			if b.block.Load() {
				<-b.release
			}
			if b.admin.Load() {
				return Ok[any, any](Just[any](consoleTestIdentity{Subject: "admin-1", Email: "a@example.test", Claims: map[string]string{}}))
			}
			return Ok[any, any](Nothing[any]())
		}
	}
}

func setupBlockingConsoleAppMode(t *testing.T, cb *blockingConsoleCallback, bound time.Duration) {
	t.Helper()
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	prev := consoleAuthCallbackTimeout
	consoleAuthCallbackTimeout = bound
	SetConsoleAuthCallback(cb.fn())
	t.Cleanup(func() {
		SetConsoleAuthCallback(nil)
		ResetConsoleAuthStateForTesting()
		consoleAuthCallbackTimeout = prev
	})
}

// consoleGetWithin runs consoleGet on its own goroutine and fails the test
// when it has not answered within d (the unbounded check never answers).
func consoleGetWithin(t *testing.T, d time.Duration, cookies ...*http.Cookie) (bool, int) {
	t.Helper()
	type res struct {
		ok     bool
		status int
	}
	done := make(chan res, 1)
	go func() {
		r := httptestConsoleGet(cookies...)
		done <- res{r.ok, r.status}
	}()
	select {
	case r := <-done:
		return r.ok, r.status
	case <-time.After(d):
		t.Fatalf("a console request with a stuck consoleAuth check did not answer within %v", d)
		return false, 0
	}
}

// A console request whose check hangs is refused within the bound.
func TestConsoleAuthTimeout_StuckCheckIsRefusedWithinTheBound(t *testing.T) {
	cb := newBlockingConsoleCallback(t)
	cb.admin.Store(true)
	cb.block.Store(true)
	setupBlockingConsoleAppMode(t, cb, 100*time.Millisecond)

	start := time.Now()
	ok, status := consoleGetWithin(t, 3*time.Second)
	if ok || status != http.StatusForbidden {
		t.Fatalf("a check that never answers: allowed=%v status=%d, want refused with 403", ok, status)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("refusal took %v, want about the 100ms bound", el)
	}
	// The late answer (an admit) must not be applied: once the check
	// returns, a fresh request without a cookie still runs the check.
	cb.once.Do(func() { close(cb.release) })
	time.Sleep(50 * time.Millisecond)
	cb.block.Store(false)
	cb.admin.Store(false)
	if ok, status := consoleGetWithin(t, 3*time.Second); ok || status != http.StatusForbidden {
		t.Fatalf("after a timed-out admit: allowed=%v status=%d, want 403 (the late answer must be discarded)", ok, status)
	}
}

// A fast check still admits under the bound.
func TestConsoleAuthTimeout_FastCheckStillAdmits(t *testing.T) {
	cb := newBlockingConsoleCallback(t)
	cb.admin.Store(true)
	setupBlockingConsoleAppMode(t, cb, 2*time.Second)
	if ok, status := consoleGetWithin(t, 3*time.Second); !ok {
		t.Fatalf("a check that admits at once was refused (status %d)", status)
	}
}

// An open console stream ends when its periodic re-check hangs.
func TestConsoleAuthTimeout_StuckRecheckEndsTheOpenStream(t *testing.T) {
	clock := withConsoleTestClock(t)
	withStreamRegateEvery(t, 20*time.Millisecond)
	cb := newBlockingConsoleCallback(t)
	cb.admin.Store(true)
	setupBlockingConsoleAppMode(t, cb, 100*time.Millisecond)
	srv := newConsoleStreamServer(t)

	ok, _, issued := consoleGet(t)
	if !ok || issued == "" || issued == "-" {
		t.Fatalf("admin: allowed=%v cookie=%q", ok, issued)
	}
	s := openConsoleStream(t, srv, consoleCookie(issued))
	if !s.waitData(2 * time.Second) {
		t.Fatalf("an admitted console stream sent no live data")
	}
	cb.block.Store(true) // the app's check now hangs on a stuck query
	clock.Advance(consoleAppRecheckInterval + time.Second)
	ended, auth := s.waitEnd(3 * time.Second)
	if !ended {
		t.Fatalf("the open console stream kept streaming while its re-check hung")
	}
	if !auth {
		t.Fatalf("the stream ended without the session-lost auth-required event")
	}
}

func httptestConsoleGet(cookies ...*http.Cookie) struct {
	ok     bool
	status int
} {
	r, _ := http.NewRequest("GET", "http://example.test/_sky/console/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	ok := evaluateConsoleAuth(w, r)
	return struct {
		ok     bool
		status int
	}{ok, w.Code}
}
