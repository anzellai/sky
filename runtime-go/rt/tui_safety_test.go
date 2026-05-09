package rt

import (
	"sync"
	"testing"
)

// tuiTeardown must be idempotent — calling it twice from different
// goroutines (or once from the deferred cleanup and once from a
// signal handler that fired between teardown completion and process
// exit) shouldn't double-write ANSI codes.
func TestTuiTeardown_Idempotent(t *testing.T) {
	tuiInstallState(&tuiState{altScreen: true, cursorHidden: true})
	defer tuiUninstallState()

	tuiTeardown()
	if !tuiTornDown {
		t.Fatal("after first call, tuiTornDown should be true")
	}
	// Second call returns silently.
	tuiTeardown()
	if !tuiTornDown {
		t.Fatal("tuiTornDown stays true on subsequent calls")
	}
}

// tuiTeardown must work safely when no state was installed (the path
// hit when Cli.program runs and never modifies the terminal).
func TestTuiTeardown_NoState(t *testing.T) {
	tuiUninstallState() // ensure clean
	tuiTearMu.Lock()
	tuiTornDown = false
	tuiTearMu.Unlock()
	tuiTeardown() // should not panic
	if !tuiTornDown {
		t.Error("tuiTornDown should be set even when state was nil")
	}
}

// Concurrent teardown calls must not race on tuiTornDown. Run a
// stress test with many goroutines all calling teardown — the
// mutex-guarded check should serialise them and the side-effects
// (ANSI writes) only happen once.
func TestTuiTeardown_ConcurrentCalls(t *testing.T) {
	tuiInstallState(&tuiState{altScreen: true})
	defer tuiUninstallState()
	tuiTearMu.Lock()
	tuiTornDown = false
	tuiTearMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tuiTeardown()
		}()
	}
	wg.Wait()
	if !tuiTornDown {
		t.Error("after concurrent teardown, tuiTornDown must be true")
	}
}

// tuiInstallState resets tuiTornDown so a subsequent teardown actually
// runs. Important for sequential test cases that share the package.
func TestTuiInstallState_ResetsTornDownFlag(t *testing.T) {
	tuiTearMu.Lock()
	tuiTornDown = true
	tuiTearMu.Unlock()
	tuiInstallState(&tuiState{altScreen: true})
	defer tuiUninstallState()
	tuiTearMu.Lock()
	defer tuiTearMu.Unlock()
	if tuiTornDown {
		t.Error("tuiInstallState must reset tornDown so a fresh app run can teardown")
	}
}
