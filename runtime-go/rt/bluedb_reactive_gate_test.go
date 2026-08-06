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
