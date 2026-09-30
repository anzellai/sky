package rt

import (
	"testing"
	"time"
)

// recvOrFail receives from ch or fails the test after d. A bare `<-ch` in a
// test turns a failure into a hang until the go-test timeout, which names
// no test and no line (G-11).
func recvOrFail[T any](t *testing.T, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("nothing received within %v", d)
	}
	var zero T
	return zero
}
