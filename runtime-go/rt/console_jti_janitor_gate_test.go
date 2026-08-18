// console_jti_janitor_gate_test.go — the consumed-JTI janitor survives a
// panicking prune cycle.
//
//	console-jti-janitor-survives-a-panic   TestJTIJanitorSurvivesAPanic
//
// # The defect
//
// `startJTIJanitor` spawned a bare `for { time.Sleep(5*time.Minute);
// pruneConsumedJTI() }` with no recover. `pruneConsumedJTI` walks a sync.Map
// and deletes from it; a panic in there ended the janitor for the process
// lifetime and `consumedJTI` then grew without bound for as long as the
// process ran.
//
// Low severity — the map only grows with successful URL handshakes — and it is
// fixed anyway, with the same mechanism as the rest of the class, because the
// one site left carrying a defect is the one the next audit finds.
//
// This gate drives periodic.Every with the janitor's own Config shape rather
// than `startJTIJanitor` itself: that function is sync.Once-guarded and has no
// stop channel by design (the loop genuinely runs for the process lifetime),
// so calling it would leak a goroutine into every later test in the package
// and could only ever run once across the whole binary.
//
// Fixture isolation: nothing touches the filesystem or the global map.
package rt

import (
	"sync"
	"testing"
	"time"

	"sky-app/rt/periodic"
)

// TestJTIJanitorSurvivesAPanic — a panic in a prune cycle costs THAT CYCLE,
// not the janitor.
func TestJTIJanitorSurvivesAPanic(t *testing.T) {
	var mu sync.Mutex
	cycles := 0
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		// Stands in for the absent recover the shipped janitor had.
		defer func() {
			_ = recover()
			close(done)
		}()
		periodic.Every(periodic.Config{
			Name:     "console.jti-janitor",
			Interval: 5 * time.Millisecond,
			Stop:     stop,
			Report:   periodicReport,
			Work: func(time.Time) error {
				mu.Lock()
				cycles++
				n := cycles
				mu.Unlock()
				if n == 1 {
					panic("injected panic in the JTI prune walk")
				}
				pruneConsumedJTI()
				return nil
			},
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := cycles
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	got := cycles
	mu.Unlock()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the JTI janitor did not return after stop closed")
	}

	n := 0

	n++
	if got < 3 {
		t.Errorf("the JTI janitor ran %d cycle(s) after a panic in its first one, want >= 3.\n"+
			"The goroutine is dead for the process lifetime and consumedJTI grows without "+
			"bound for as long as the process runs.", got)
	}

	n++
	if _, ok := warnLogged("console.jti-janitor.cycle_panicked"); !ok {
		t.Error("no warn logged for the panicked JTI prune cycle")
	}

	reportAssertions(t, n)
}
