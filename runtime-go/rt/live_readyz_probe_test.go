package rt

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// C1 regression — /_sky/readyz must reflect session-store health, not just
// "process up / not draining". Pre-fix, RegisterReadinessProbe had ZERO
// production callers, so readyz returned 200 even when the backing store was
// unreachable (or had silently fallen back to memory). That single gap hid the
// entire silent-store-failure class from orchestrators: every operator signal
// (process up, healthz, readyz) stayed green while sessions were ephemeral.
func TestReadyzReflectsStoreHealth(t *testing.T) {
	// Isolate from any probes other tests registered on the package globals.
	empty := []func() error{}
	readinessProbes.Store(&empty)
	readinessReady.Store(true)

	// The in-memory store is always healthy → readyz 200.
	mem := newMemoryStore(time.Minute)
	if err := mem.Ping(); err != nil {
		t.Fatalf("memoryStore.Ping() = %v, want nil (memory is always healthy)", err)
	}
	RegisterReadinessProbe("session-store", mem.Ping)
	if code := readyzStatusForTest(); code != 200 {
		t.Fatalf("healthy store: /_sky/readyz = %d, want 200", code)
	}

	// A down store (probe errors) → readyz 503 so the orchestrator stops
	// routing to the broken replica. This is the behaviour that was missing.
	RegisterReadinessProbe("session-store-down", func() error {
		return fmt.Errorf("dial tcp 127.0.0.1:5432: connect: connection refused")
	})
	if code := readyzStatusForTest(); code != 503 {
		t.Fatalf("down store: /_sky/readyz = %d, want 503 (readyz must NOT lie)", code)
	}
}

func readyzStatusForTest() int {
	rec := httptest.NewRecorder()
	HandleReadyz(rec, httptest.NewRequest("GET", "/_sky/readyz", nil))
	return rec.Code
}
