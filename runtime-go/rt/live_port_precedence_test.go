package rt

// Regression gate: `Live.withPort` must beat the COMPILER-INJECTED sky.toml
// port default, and must still lose to a port the operator actually set.
//
// Every emitted `init()` calls `rt.SetPortDefault(<sky.toml port>)`, and
// sky.toml's port always has a value (8000 when unset), so `SKY_LIVE_PORT` is
// effectively ALWAYS present by the time Live.app resolves the port. The
// resolver then let any non-empty `SKY_LIVE_PORT` overwrite `cfg.Port`
// unconditionally, so the injected default beat the user's explicit
// `withPort` every time — inverting the precedence the code's own comment
// claimed two lines earlier ("cfg.Port wins over env"). `Live.withPort` was
// dead: apps/fieldbook/src/Main.sky documents `PORT=8421 ./app` landing on
// :8000.
//
// The distinction that makes this fixable is between an env var a HUMAN set
// (shell / .env — which must keep winning, per SetEnvDefault's contract) and
// one the compiler's own init() seeded as a default (which must not beat an
// explicit call). Both look identical in os.Environ(), so the seeding is now
// recorded and the resolver consults it.

import "testing"

// withCleanPortEnv isolates each case: no LIVE_PORT set, no seeding recorded.
func withCleanPortEnv(t *testing.T) {
	t.Helper()
	name := skyEnvName("LIVE_PORT")
	prev, had := lookupEnvRaw(name)
	clearSeededDefault(name)
	unsetEnvRaw(name)
	t.Cleanup(func() {
		clearSeededDefault(name)
		if had {
			setEnvRaw(name, prev)
		} else {
			unsetEnvRaw(name)
		}
	})
}

// The defect, stated as a verdict: an explicit withPort must survive the
// compiler-injected default that init() always seeds.
func TestLivePort_WithPortBeatsInjectedDefault(t *testing.T) {
	withCleanPortEnv(t)
	SetPortDefault("8000") // what every generated init() does
	cfg := map[string]any{"Port": 8421}
	if got := resolveLivePort(cfg); got != 8421 {
		t.Fatalf("Live.withPort 8421 with an injected sky.toml default of 8000: "+
			"got :%d want :8421 — the injected default beat the explicit call, "+
			"so withPort is dead", got)
	}
}

// The other half of the contract, so the fix cannot be "cfg.Port always wins":
// a port the operator genuinely exported still beats withPort.
func TestLivePort_RealEnvBeatsWithPort(t *testing.T) {
	withCleanPortEnv(t)
	setEnvRaw(skyEnvName("LIVE_PORT"), "8555")
	// init() runs SetPortDefault too; being set-if-unset it must not disturb
	// the operator's value or mark it as seeded.
	SetPortDefault("8000")
	cfg := map[string]any{"Port": 8421}
	if got := resolveLivePort(cfg); got != 8555 {
		t.Fatalf("SKY_LIVE_PORT=8555 with withPort 8421: got :%d want :8555 — "+
			"an operator-set port must still win", got)
	}
}

// No withPort: the injected sky.toml default is exactly what should apply.
func TestLivePort_InjectedDefaultAppliesWithoutWithPort(t *testing.T) {
	withCleanPortEnv(t)
	SetPortDefault("8000")
	if got := resolveLivePort(map[string]any{}); got != 8000 {
		t.Fatalf("no withPort, sky.toml default 8000: got :%d want :8000", got)
	}
}

// Nothing set anywhere falls back to the documented 8080.
func TestLivePort_FallsBackTo8080(t *testing.T) {
	withCleanPortEnv(t)
	if got := resolveLivePort(map[string]any{}); got != 8080 {
		t.Fatalf("nothing set: got :%d want :8080", got)
	}
}

// A malformed env value is ignored rather than collapsing the port to 0.
func TestLivePort_IgnoresUnparseableEnv(t *testing.T) {
	withCleanPortEnv(t)
	setEnvRaw(skyEnvName("LIVE_PORT"), "not-a-port")
	if got := resolveLivePort(map[string]any{"Port": 8421}); got != 8421 {
		t.Fatalf("unparseable SKY_LIVE_PORT with withPort 8421: got :%d want :8421", got)
	}
}
