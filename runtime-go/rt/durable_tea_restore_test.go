//go:build !js

package rt

import "testing"

// A stored snapshot that fails to restore must NOT be silently replaced:
// the run boots from init and no later update overwrites the snapshot
// (SA-8). Pre-fix, applyRestore turned the Err into init and the first
// update's persist overwrote the only copy of the old state.
func TestDurableBoot_RestoreFailureKeepsSnapshot(t *testing.T) {
	persisted := 0
	wiring := map[string]any{
		"Enabled": true,
		"RunId":   "default",
		"Restore": func(runId any) any {
			return func() any {
				return Err[any, any](ErrInvalidInput("snapshot does not decode"))
			}
		},
		"ApplyRestore": func(res any, initModel any) any {
			// The Sky default: anything but Ok (Just m) -> init.
			return initModel
		},
		"Persist": func(runId any, model any) any {
			return func() any {
				persisted++
				return Ok[any, any](struct{}{})
			}
		},
	}
	d := durableCtxOf(wiring)
	if d == nil {
		t.Fatal("durableCtxOf returned nil for an enabled wiring")
	}
	got := d.bootFixed("init-model")
	if got != "init-model" {
		t.Fatalf("boot = %v, want the init model", got)
	}
	d.persistFixed("model-after-update")
	if persisted != 0 {
		t.Fatalf("persist ran %d time(s) after a failed restore; the stored snapshot was overwritten", persisted)
	}
}

// A successful restore (or no snapshot) keeps persisting normally.
func TestDurableBoot_SuccessKeepsPersisting(t *testing.T) {
	persisted := 0
	wiring := map[string]any{
		"Enabled": true,
		"RunId":   "default",
		"Restore": func(runId any) any {
			return func() any { return Ok[any, any]("restored") }
		},
		"ApplyRestore": func(res any, initModel any) any { return "restored-model" },
		"Persist": func(runId any, model any) any {
			return func() any {
				persisted++
				return Ok[any, any](struct{}{})
			}
		},
	}
	d := durableCtxOf(wiring)
	if got := d.bootFixed("init"); got != "restored-model" {
		t.Fatalf("boot = %v", got)
	}
	d.persistFixed("m")
	if persisted != 1 {
		t.Fatalf("persist ran %d times, want 1", persisted)
	}
}
