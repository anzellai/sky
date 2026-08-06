package rt

// live_reactive_hooks.go — the bluedb-free seam between live.go and the Phase-4b reactive
// integration (bluedb_reactive.go). live.go calls these hooks; bluedb_reactive.go's init() wires
// them to the real implementations. bluedb_reactive.go imports sky-app/bluedb and is GATED out of
// non-Persist projects (build.rs), so live.go — which is ALWAYS emitted — must not reference it
// directly. When bluedb_reactive.go is absent the hooks stay no-ops (a non-Persist app has no
// reactive bindings anyway), keeping the emitted rt package free of the Pebble engine.

var (
	// reactiveEnsureStartedHook starts a session's BlueDB reactive loops once (idempotent).
	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession) {}
	// reactiveTeardownHook stops every reactive loop bound to a session (from markDone).
	reactiveTeardownHook = func(sess *liveSession) {}
)
