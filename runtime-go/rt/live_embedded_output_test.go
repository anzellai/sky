//go:build !js

package rt

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Delay the shutdown tail after readiness until the test explicitly permits
// EOF. This makes the old Wait-before-drain ordering observable without an OS
// pipe-size assumption, a signal, or a long-running Live server.
type liveChildTailReader struct {
	stage   int
	reading chan struct{}
	release chan struct{}
}

func (r *liveChildTailReader) Read(p []byte) (int, error) {
	switch r.stage {
	case 0:
		r.stage++
		return copy(p, "LIVE_READY\n"), nil
	case 1:
		r.stage++
		close(r.reading)
		<-r.release
		return copy(p, "Sky.Live shutting down…\n"), nil
	default:
		return 0, io.EOF
	}
}
func TestLiveChildOutputWaitsForShutdownTailBeforeReap(t *testing.T) {
	r := &liveChildTailReader{reading: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(r.release) })
	out, ready := scanLiveChildOutput(r)
	if !<-ready {
		t.Fatal("fixture readiness missing")
	}
	<-r.reading
	called := make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- out.wait(func() error { close(called); return nil }) }()
	select {
	case <-called:
		t.Fatal("process wait ran before stdout reached EOF; shutdown tail can be lost")
	case <-time.After(100 * time.Millisecond):
	}
	release.Do(func() { close(r.release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "LIVE_READY\nSky.Live shutting down…\n" {
		t.Fatalf("incomplete child output: %q", got)
	}
	select {
	case <-called:
	default:
		t.Fatal("process was not reaped after stdout drained")
	}
}

type liveChildErrorReader struct{ err error }

func (r liveChildErrorReader) Read([]byte) (int, error) { return 0, r.err }
func TestLiveChildOutputReportsReadFailure(t *testing.T) {
	sentinel := errors.New("public fixture read failure")
	out, ready := scanLiveChildOutput(liveChildErrorReader{sentinel})
	if <-ready {
		t.Fatal("failed reader announced readiness")
	}
	if err := out.wait(func() error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("lost scanner failure: %v", err)
	}
}
func TestLiveChildOutputDrainsReadinessAndTail(t *testing.T) {
	const text = "before\nLIVE_READY\nSky.Live shutting down…\n"
	out, ready := scanLiveChildOutput(strings.NewReader(text))
	if !<-ready {
		t.Fatal("readiness missing")
	}
	<-out.done // This positive control starts only after its complete input drained.
	if err := out.wait(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if out.String() != text {
		t.Fatal("output changed")
	}
}
