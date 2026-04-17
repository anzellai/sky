package rt

// Audit P0-5: codegen replaced raw `panic("sky: internal — codegen
// reached unreachable case arm (compiler bug)")` with rt.Unreachable
// so if exhaustiveness-vs-codegen ever drift, the failure becomes a
// catchable panic (converted to Err at the Task boundary) rather than
// an uncatchable process kill. This spec locks in:
//   1. Unreachable panics (not silent returns).
//   2. The panic message identifies the call site so logs can
//      pinpoint the originating case block.
//   3. Direct invocation can be recovered — i.e. it's a regular
//      panic, not runtime.Goexit or a signal.

import (
	"strings"
	"testing"
)

func TestUnreachable_Panics(t *testing.T) {
	panicked, msg := didPanic(func() { _ = Unreachable("case/test-subject") })
	if !panicked {
		t.Fatal("rt.Unreachable did not panic — defence-in-depth hole")
	}
	if !strings.Contains(msg, "sky.Unreachable") {
		t.Fatalf("Unreachable panic missing sky.Unreachable tag: %q", msg)
	}
	if !strings.Contains(msg, "case/test-subject") {
		t.Fatalf("Unreachable panic missing site identifier: %q", msg)
	}
}

func TestUnreachable_IsRecoverable(t *testing.T) {
	// The whole point of the P0-5 rewrite: the panic MUST be
	// catchable by a deferred recover in the normal way, so
	// rt's Server_listen / SkyFfiRecover / Live_app panic-catchers
	// can convert it into a clean Err. A runtime.Goexit or a
	// signal-level crash would bypass recover().
	recovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()
		_ = Unreachable("test-recover")
	}()
	if !recovered {
		t.Fatal("Unreachable panic was not caught by deferred recover")
	}
}
