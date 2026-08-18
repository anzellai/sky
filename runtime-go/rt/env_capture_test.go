package rt

// Regression gate: a runtime setting read from the environment must be
// readable AT ALL.
//
// ## The ordering that makes this a real class
//
// Go evaluates every package-level `var` initializer BEFORE any `init()` in
// the same package. `dotenv.go`'s `init()` (`:106`) is what loads `.env` into
// the process environment. So a package-level var whose initializer calls
// `os.Getenv` reads the environment at the one moment `.env` has provably not
// been applied yet — and, because the var is a plain value rather than a
// lookup, it never reads again.
//
// The failure is total and silent. `SKY_STREAM_DEBUG=1` in a `.env` does not
// enable stream debugging; it does nothing, forever, and nothing says so.
// There is no error, no warning, and the variable is spelled correctly.
//
// ## Why these tests can see it
//
// A test binary reaches `TestXxx` long after every `init()` has run, so
// `t.Setenv` here sits at exactly the same point in time as a `.env` line
// relative to the capture: after it. A var that captured eagerly returns its
// stale value and the test fails; a var that reads on demand returns the new
// one.
//
// ## The two members
//
// Stage 1 found `streamDebug` by inspection. The mechanical sweep that became
// `env_init_order_audit_test.go` found the second one, `skyHttpClient`, which
// no one had looked at — it is two call levels deep
// (`newSkyHttpClient` -> `httpEnvTimeout` -> `os.Getenv`) and invisible to a
// reader scanning for `os.Getenv` at the point of declaration. That is the
// argument for the audit test existing at all.

import (
	"testing"
	"time"
)

// `SKY_STREAM_DEBUG` set after package init — i.e. from a `.env` file — must
// enable stream debug logging.
func TestStreamDebug_HonoursEnvSetAfterPackageInit(t *testing.T) {
	t.Setenv("SKY_STREAM_DEBUG", "1")
	if !streamDebugEnabled() {
		t.Fatal("SKY_STREAM_DEBUG=1 set after package init (as a .env line is): " +
			"streamDebugEnabled() = false, want true — the value was captured " +
			"into a package-level var before dotenv's init() ran, so a .env can " +
			"never reach it")
	}
}

// The other direction, so a fix cannot be "always on".
func TestStreamDebug_OffByDefault(t *testing.T) {
	t.Setenv("SKY_STREAM_DEBUG", "")
	if streamDebugEnabled() {
		t.Fatal("SKY_STREAM_DEBUG unset: streamDebugEnabled() = true, want false")
	}
}

// `SKY_HTTP_CLIENT_TIMEOUT` set after package init must reach the shared
// outbound HTTP client. The escape hatch is documented at
// stdlib_extra.go:1318 — "Apps that call slow upstreams — LLM APIs especially
// — routinely need more than 30s" — and setting it the documented way in a
// `.env` did nothing at all.
// Asserted on the RESOLVER, not on `skyHTTPClient()`. The shared client is
// built once (it owns the connection pool, so rebuilding it per request would
// silently disable keep-alive), which means a test reading its `.Timeout` can
// only ever observe whichever test in this package ran first — it would pass
// or fail on test ORDER rather than on the behaviour under test. The resolver
// is the thing that has to see `.env`, and it is what the client is built
// from (stdlib_extra.go, `newSkyHttpClient`).
func TestHTTPClientTimeout_HonoursEnvSetAfterPackageInit(t *testing.T) {
	t.Setenv("SKY_HTTP_CLIENT_TIMEOUT", "180s")
	if got := skyHTTPClientTimeout(); got != 180*time.Second {
		t.Fatalf("SKY_HTTP_CLIENT_TIMEOUT=180s set after package init: "+
			"resolved timeout = %s, want 3m0s — every outbound Http.get stays "+
			"pinned at the 30s default no matter what the .env says", got)
	}
}

func TestHTTPClientTimeout_DefaultsTo30s(t *testing.T) {
	t.Setenv("SKY_HTTP_CLIENT_TIMEOUT", "")
	if got := skyHTTPClientTimeout(); got != 30*time.Second {
		t.Fatalf("SKY_HTTP_CLIENT_TIMEOUT unset: resolved timeout = %s, want 30s", got)
	}
}

// The resolver is not decorative: a freshly built client must actually carry
// it. Built directly rather than through `skyHTTPClient()` for the same
// order-independence reason. This is what makes the two tests above a claim
// about outbound HTTP rather than about an unused function.
func TestHTTPClientTimeout_IsWhatTheClientIsBuiltWith(t *testing.T) {
	t.Setenv("SKY_HTTP_CLIENT_TIMEOUT", "90s")
	if got := newSkyHttpClient().Timeout; got != 90*time.Second {
		t.Fatalf("client built with SKY_HTTP_CLIENT_TIMEOUT=90s: timeout = %s, want 1m30s", got)
	}
}
