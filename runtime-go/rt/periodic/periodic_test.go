// periodic_test.go — the properties every caller of this package relies on.
//
// These are unit tests of the mechanism. The per-SITE regression tests live
// next to the loops they protect (rt/periodic_loops_gate_test.go,
// rt/live_time_every_lock_test.go, hub/store_prune_gate_test.go,
// jobs/jobs_worker_gate_test.go), because "the analytics pruner survives a
// panic" is a claim about analytics_store.go, not about this file.
package periodic

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector is a Reporter that records what it was told.
type collector struct {
	mu   sync.Mutex
	reps []Report
}

func (c *collector) reporter() Reporter {
	return func(r Report) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.reps = append(c.reps, r)
	}
}

func (c *collector) snapshot() []Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Report, len(c.reps))
	copy(out, c.reps)
	return out
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reps)
}

// TestGuardConfinesAPanicToTheCycle — the defining property. A panic inside
// `work` does not escape Guard, so a caller's loop keeps running.
func TestGuardConfinesAPanicToTheCycle(t *testing.T) {
	c := &collector{}
	ran := 0
	for i := 0; i < 3; i++ {
		Guard("test.loop", c.reporter(), func() error {
			ran++
			panic("boom")
		})
	}
	if ran != 3 {
		t.Errorf("work ran %d times, want 3 — Guard let a panic escape and the caller's loop died", ran)
	}
	reps := c.snapshot()
	if len(reps) != 3 {
		t.Fatalf("got %d reports, want 3", len(reps))
	}
	for i, r := range reps {
		if r.Recovered == nil {
			t.Errorf("report %d has no Panic — a recover that discards what it caught is the defect", i)
		}
		if len(r.Stack) == 0 {
			t.Errorf("report %d has no Stack — the operator cannot find the panic site", i)
		}
		if r.Loop != "test.loop" {
			t.Errorf("report %d names loop %q, want %q", i, r.Loop, "test.loop")
		}
	}
}

// TestGuardReportsAReturnedError — the second half of the class. A cycle that
// fails without panicking is reported, never discarded.
func TestGuardReportsAReturnedError(t *testing.T) {
	c := &collector{}
	want := errors.New("exec failed")
	Guard("test.loop", c.reporter(), func() error { return want })
	reps := c.snapshot()
	if len(reps) != 1 {
		t.Fatalf("got %d reports, want 1", len(reps))
	}
	if !errors.Is(reps[0].Err, want) {
		t.Errorf("report carries err %v, want %v", reps[0].Err, want)
	}
	if reps[0].Recovered != nil {
		t.Errorf("report carries a Panic %v for a plain error return", reps[0].Recovered)
	}
}

// TestGuardIsSilentOnSuccess — a healthy cycle produces no report, so the
// reports an operator does see all mean something.
func TestGuardIsSilentOnSuccess(t *testing.T) {
	c := &collector{}
	Guard("test.loop", c.reporter(), func() error { return nil })
	if n := c.count(); n != 0 {
		t.Errorf("a successful cycle produced %d report(s), want 0", n)
	}
}

// TestGuardSurvivesAPanickingReporter — a bug in the log adapter must not
// reproduce the defect the Guard was added to close.
func TestGuardSurvivesAPanickingReporter(t *testing.T) {
	ran := 0
	bad := Reporter(func(Report) { panic("reporter is broken") })
	for i := 0; i < 2; i++ {
		Guard("test.loop", bad, func() error {
			ran++
			panic("boom")
		})
	}
	if ran != 2 {
		t.Errorf("work ran %d times, want 2 — a panicking Reporter escaped Guard", ran)
	}
}

// TestEveryKeepsTickingAfterAPanic — the property the seven shipped loops
// need. The FIRST cycle panicking is true under the broken and the fixed
// shape alike; only cycles 2 and 3 tell them apart.
func TestEveryKeepsTickingAfterAPanic(t *testing.T) {
	c := &collector{}
	var mu sync.Mutex
	cycles := 0
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		// Stands in for the recover the broken shape had at the goroutine's
		// top level: under the defect the panic arrives here and the test
		// reports a failed assertion rather than crashing the binary.
		defer func() {
			_ = recover()
			close(done)
		}()
		Every(Config{
			Name:     "test.every",
			Interval: 2 * time.Millisecond,
			Stop:     stop,
			Report:   c.reporter(),
			Work: func(time.Time) error {
				mu.Lock()
				cycles++
				n := cycles
				mu.Unlock()
				if n == 1 {
					panic("first cycle explodes")
				}
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
		t.Fatal("Every did not return after Stop closed")
	}

	if got < 3 {
		t.Errorf("Every ran %d cycle(s) after a panic in cycle 1, want >= 3 — "+
			"the loop is dead for the process lifetime", got)
	}
	if len(c.snapshot()) == 0 {
		t.Error("no report for the panicked cycle")
	}
}

// TestEveryExitsOnEitherStopChannel — Time.every depends on both.
func TestEveryExitsOnEitherStopChannel(t *testing.T) {
	for _, which := range []string{"Stop", "AlsoStop"} {
		t.Run(which, func(t *testing.T) {
			ch := make(chan struct{})
			cfg := Config{
				Name:     "test.stop",
				Interval: time.Millisecond,
				Report:   func(Report) {},
				Work:     func(time.Time) error { return nil },
			}
			if which == "Stop" {
				cfg.Stop = ch
			} else {
				cfg.AlsoStop = ch
			}
			done := make(chan struct{})
			go func() { Every(cfg); close(done) }()
			time.Sleep(5 * time.Millisecond)
			close(ch)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("Every ignored a closed %s channel", which)
			}
		})
	}
}

// TestEveryRefusesAnUnusableConfigLoudly — a loop that will never run says so.
// Returning quietly is the same silence the package exists to remove, moved
// from the first panic to startup.
func TestEveryRefusesAnUnusableConfigLoudly(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"zero interval", Config{Name: "x", Interval: 0, Work: func(time.Time) error { return nil }}, "not positive"},
		{"nil work", Config{Name: "x", Interval: time.Second}, "nil Work"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &collector{}
			tc.cfg.Report = c.reporter()
			done := make(chan struct{})
			go func() { Every(tc.cfg); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Every did not return on an unusable config")
			}
			reps := c.snapshot()
			if len(reps) != 1 {
				t.Fatalf("got %d reports, want exactly 1 naming the misconfiguration", len(reps))
			}
			if reps[0].Err == nil || !strings.Contains(reps[0].Err.Error(), tc.want) {
				t.Errorf("report %v does not name %q", reps[0].Err, tc.want)
			}
		})
	}
}
