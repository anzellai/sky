package rt

import (
	"encoding/gob"
	"testing"
)

type namedGobStruct struct{ X int }

// L10a — a gob.Register that panics must be reported as FAILED so the caller
// does NOT cache it as registered. Pre-fix, the dedup flag was set BEFORE the
// panic-prone register, so a failed registration was remembered as done → every
// later encodeSession naming that type failed → the session silently dropped to
// memory-only. tryGobRegisterVal must recover the panic and return false.
func TestTryGobRegisterValRecoversAndReportsFailure(t *testing.T) {
	// Success path: a named struct registers and returns true, no panic.
	if !tryGobRegisterVal(namedGobStruct{}) {
		t.Fatal("registering a named struct should succeed (true)")
	}
	// Failure path: gob.Register(nil) panics; tryGobRegisterVal must RECOVER it
	// (not propagate) and return false so the caller won't cache a non-registration.
	got := tryGobRegisterVal(nil)
	if got {
		t.Fatal("registering nil should return false after recovering the panic")
	}
}

// Sanity: confirm gob.Register(nil) does panic (so the test above is meaningful).
func TestGobRegisterNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Skip("gob.Register(nil) did not panic on this Go version; failure-path assertion is version-dependent")
		}
	}()
	gob.Register(nil)
}
