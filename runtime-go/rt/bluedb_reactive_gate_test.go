package rt

// bluedb_reactive_gate_test.go — Phase-4c RG#2 capability boot-gate matrix. The gate decision is
// factored into the PURE reactiveCapabilityError so the boot verdict is directly assertable without
// os.Exit — the process-boot wrapper (assertReactiveCapabilityOrExit) is a thin os.Exit-on-error
// shell over this. Covers the deliverable's four required cases plus the boundaries.

import (
	"strings"
	"testing"
)

func TestReactiveCapabilityGate_Matrix(t *testing.T) {
	cases := []struct {
		name         string
		prod         bool
		scope        string
		usesReactive bool
		backend      string
		wantFatal    bool
	}{
		// (a) prod + reactive + embedded + NO assertion → FATAL (the core hazard).
		{"a_prod_embedded_no_assertion", true, "", true, "embedded", true},
		// (b) prod + reactive + embedded + assertion set → OK.
		{"b_prod_embedded_asserted", true, reactiveScopeSingleInstance, true, "embedded", false},
		// (c) dev + reactive + embedded + NO assertion → OK (warn+allow, dev ergonomics).
		{"c_dev_embedded_no_assertion", false, "", true, "embedded", false},
		// (d) prod + reactive + embedded DATA (postgres SESSIONS irrelevant) + assertion → OK
		//     (no false positive from the session store — the gate is about the data backend).
		{"d_prod_embedded_data_asserted", true, reactiveScopeSingleInstance, true, "embedded", false},

		// sqlite is the same local single-writer class as embedded.
		{"sqlite_prod_no_assertion", true, "", true, "sqlite", true},
		{"sqlite_prod_asserted", true, reactiveScopeSingleInstance, true, "sqlite", false},

		// postgres is a SHARED backend — NOT gated by the single-instance assertion here.
		{"postgres_prod_no_assertion", true, "", true, "postgres", false},

		// no reactivity → nothing to gate, even in prod on a local backend.
		{"no_reactive_prod_embedded", true, "", false, "embedded", false},

		// unknown/empty backend → not classified as local-single-writer → not fataled (avoids
		// false-fataling a backend we can't resolve; SQL live-delivery is the post-v1 arm).
		{"unknown_backend_prod", true, "", true, "", false},

		// assertion tolerant of case + whitespace.
		{"assertion_case_space", true, "  Single-Instance  ", true, "embedded", false},

		// a WRONG assertion value does NOT satisfy the gate.
		{"assertion_wrong_value", true, "multi", true, "embedded", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := reactiveCapabilityError(c.prod, c.scope, c.usesReactive, c.backend)
			if c.wantFatal && err == nil {
				t.Fatalf("want FATAL (non-nil error), got nil")
			}
			if !c.wantFatal && err != nil {
				t.Fatalf("want OK (nil error), got: %v", err)
			}
			if c.wantFatal {
				// The message must be actionable: name the backend + the exact assertion to set.
				msg := err.Error()
				if !strings.Contains(msg, reactiveScopeEnv) || !strings.Contains(msg, reactiveScopeSingleInstance) {
					t.Fatalf("fatal message not actionable (missing env/assertion): %q", msg)
				}
				if !strings.Contains(msg, c.backend) {
					t.Fatalf("fatal message should name the backend %q: %q", c.backend, msg)
				}
			}
		})
	}
}

// TestReactiveSQLArmGated is the Phase-4 silent-stale close (root-caused by the Phase-4 Judge). A
// reactive binding on a SQL / non-embedded Conn — connStoreId yields -1 for a SqlConn, which does
// NOT resolve to an embedded backend — CANNOT be wired for live delivery today: reactiveLoop paints
// the list once via the initial fill and then blocks on <-done forever with NO live updates.
//
// BEFORE the fix, reactiveDataBackendKind returned "" for such a binding, so
// reactiveCapabilityError("") returned nil in BOTH prod and dev (empty is neither the local
// single-writer class nor a fatal kind), AND the dev-warn branch (gated on isLocalSingleWriterBackend
// == true) never fired: a fully SILENT stale, in dev and prod alike.
//
// AFTER the fix the classifier reports reactiveBackendSQLUnsupported and the gate fires LOUD: fatal
// in prod (regardless of the single-instance scope assertion — no assertion makes SQL live-delivery
// work), warn-and-allow in dev (paint-once still renders; the developer is told it will NOT
// live-update). This test is the discovery artifact: it FAILS against the pre-fix gate (want FATAL,
// got nil) and PASSES once the gate classifies + refuses.
func TestReactiveSQLArmGated(t *testing.T) {
	// prod → FATAL, for every scope assertion (none of them can make SQL live-delivery work).
	for _, scope := range []string{"", reactiveScopeSingleInstance, "multi", "  Single-Instance  "} {
		err := reactiveCapabilityError(true, scope, true, reactiveBackendSQLUnsupported)
		if err == nil {
			t.Fatalf("prod sql-unsupported (scope=%q): want FATAL, got nil (SILENT STALE)", scope)
		}
		// The message must be actionable: say it's a SQL/non-embedded Conn, that it's unsupported,
		// and point at the embedded Conn remedy.
		msg := err.Error()
		for _, want := range []string{"SQL", "not supported", "embedded"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("fatal message not actionable, missing %q: %q", want, msg)
			}
		}
	}
	// dev → nil error (warn + allow at the call site). NOT a silent stale: the boot path emits a
	// one-shot warn for this backend kind (asserted structurally by the fix; the pure decision here
	// only proves dev does not FATAL).
	if err := reactiveCapabilityError(false, "", true, reactiveBackendSQLUnsupported); err != nil {
		t.Fatalf("dev sql-unsupported: want nil (warn+allow), got: %v", err)
	}
	// no reactivity → nothing to gate, even on the sql-unsupported backend.
	if err := reactiveCapabilityError(true, "", false, reactiveBackendSQLUnsupported); err != nil {
		t.Fatalf("no reactivity + sql-unsupported: want nil, got: %v", err)
	}
}

// TestReactiveSQLArmStoreDoesNotResolve documents WHY a SQL-arm binding classifies as
// sql-unsupported: connStoreId yields -1 for a SqlConn (Std/Persist.sky), and -1 does NOT resolve to
// an embedded backend in the runtime registry, so reactiveDataBackendKind cannot classify it as
// "embedded" and must route it to the sql-unsupported (loud) arm. (An inert failed-open KvConn(-1)
// shares this fate — it likewise never resolves — and equally deserves a loud, not silent, signal.)
func TestReactiveSQLArmStoreDoesNotResolve(t *testing.T) {
	if _, ok := embeddedBackend(int64(-1)); ok {
		t.Fatalf("store -1 resolved to an embedded backend; SQL-arm classification would be wrong")
	}
}

// TestReactiveScopeAsserted covers the assertion parser in isolation.
func TestReactiveScopeAsserted(t *testing.T) {
	for _, ok := range []string{"single-instance", "Single-Instance", "  single-instance  "} {
		if !reactiveScopeAsserted(ok) {
			t.Errorf("reactiveScopeAsserted(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "multi", "single", "instance", "cluster"} {
		if reactiveScopeAsserted(no) {
			t.Errorf("reactiveScopeAsserted(%q) = true, want false", no)
		}
	}
}

// TestIsLocalSingleWriterBackend covers the backend-class classifier.
func TestIsLocalSingleWriterBackend(t *testing.T) {
	for _, yes := range []string{"embedded", "sqlite", "SQLITE", " embedded "} {
		if !isLocalSingleWriterBackend(yes) {
			t.Errorf("isLocalSingleWriterBackend(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{"postgres", "pg", "redis", "", "mysql"} {
		if isLocalSingleWriterBackend(no) {
			t.Errorf("isLocalSingleWriterBackend(%q) = true, want false", no)
		}
	}
}
