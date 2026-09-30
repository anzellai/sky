package rt

// The one place an rt test is allowed to not run for want of its environment.
//
// AGENTS.md: "A gate whose prerequisite is missing FAILS, naming what to
// install. Never skip, never pass." Eight rt tests that drive the embedded
// browser clients under Node.js, and nine that run a real PostgreSQL, used to
// end their probe with `t.Skip(...)`. `go test ./rt/...` prints `ok` for a
// skipped test, so a machine or a CI job without node reported the same green
// line as one that ran them. That is the defect `rust/crates/sky/src/live_gate.rs`
// removed from the Rust tests; this is the same mechanism for the Go runtime,
// with the same switch:
//
//	SKY_LIVE_TESTS=skip go test ./rt/...   # the ONLY way to skip a live test
//
// Unset, empty or `require`, an unmet need FAILS the test, naming what to
// install. `skip` lets it skip, out loud. Any other value is an error, not a
// guess: `SKY_LIVE_TESTS=1` meaning "require" to its author and "skip" to the
// parser is how a gate ends up not running.

import (
	"os"
	"os/exec"
	"testing"
)

// liveSkipAllowed reports whether SKY_LIVE_TESTS=skip was asked for. An
// unrecognised value fails the test.
func liveSkipAllowed(t *testing.T) bool {
	t.Helper()
	skip, err := liveModeFrom(os.Getenv("SKY_LIVE_TESTS"))
	if err != "" {
		t.Fatal(err)
	}
	return skip
}

// liveModeFrom parses SKY_LIVE_TESTS: (skip?, error message).
func liveModeFrom(raw string) (bool, string) {
	switch raw {
	case "", "require":
		return false, ""
	case "skip":
		return true, ""
	default:
		return false, "SKY_LIVE_TESTS=" + raw + " is not a mode. Use `require` (the default: " +
			"an unmet environment fails the live tests) or `skip` (an unmet environment " +
			"lets them skip)."
	}
}

// requireLive gates a test on an environment it needs. `available` is the
// caller's own probe. It returns only when the test must proceed; otherwise it
// fails the test (the default) or skips it (SKY_LIVE_TESTS=skip).
func requireLive(t *testing.T, need, howToGetIt string, available bool) {
	t.Helper()
	// Parsed before the probe, so a bad value fails every live test, not only
	// the ones whose environment happens to be missing.
	skip := liveSkipAllowed(t)
	if available {
		return
	}
	if skip {
		t.Skipf("SKY_LIVE_TESTS=skip: %s is not available, so this live test did not run", need)
	}
	t.Fatalf("this test needs %s, which is not available: %s. "+
		"To skip it on purpose, run with SKY_LIVE_TESTS=skip.", need, howToGetIt)
}

// requireNode returns the path of the `node` binary, or fails the test
// (SKY_LIVE_TESTS=skip: skips it).
func requireNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	requireLive(t, "Node.js (`node` on PATH)",
		"install Node.js 18 or newer (`brew install node`, or actions/setup-node in CI); "+
			"these tests run the embedded browser client JavaScript under node", err == nil)
	return node
}

// requirePgBinDir returns a directory holding initdb / pg_ctl / postgres
// (livePgBinDir), or fails the test (SKY_LIVE_TESTS=skip: skips it).
func requirePgBinDir(t *testing.T) string {
	t.Helper()
	dir := livePgBinDir()
	requireLive(t, "PostgreSQL binaries (initdb, pg_ctl, postgres)",
		"install PostgreSQL and point SKY_POSTGRES_BIN at its `bin` directory "+
			"(`brew install postgresql@16`, or `apt-get install postgresql-16`, whose binaries "+
			"land in /usr/lib/postgresql/16/bin and are NOT on PATH)", dir != "")
	return dir
}

func TestLiveModeParse(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "require": false, "skip": true} {
		got, err := liveModeFrom(raw)
		if err != "" || got != want {
			t.Fatalf("SKY_LIVE_TESTS=%q: got (%v, %q), want (%v, \"\")", raw, got, err, want)
		}
	}
	for _, raw := range []string{"1", "true", "Skip", "no"} {
		if _, err := liveModeFrom(raw); err == "" {
			t.Fatalf("SKY_LIVE_TESTS=%q must be an error, not a guess", raw)
		}
	}
}

// requirePostgresDSN returns SKY_TEST_POSTGRES_DSN, or fails the test naming
// it (SKY_LIVE_TESTS=skip: skips it). G-8: the real-PostgreSQL tests used to
// end with `t.Skip` when the DSN was unset, so a run without a database
// printed `ok` for tests that never ran, and the live-gate guard did not see
// them.
func requirePostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SKY_TEST_POSTGRES_DSN")
	requireLive(t, "a PostgreSQL database (SKY_TEST_POSTGRES_DSN)",
		"start one (`docker run -e POSTGRES_PASSWORD=sky -p 5432:5432 postgres:16`, or `sky db start`) "+
			"and set SKY_TEST_POSTGRES_DSN=postgres://…", dsn != "")
	return dsn
}
