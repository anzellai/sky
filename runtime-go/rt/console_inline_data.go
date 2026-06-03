// Public accessors used by the inline console_app subpackage to
// hydrate its initial Model directly from rt-side state instead of
// blocking the first HTTP render on a localhost loopback fetch.
//
// Lives in rt (not console_app) because the underlying state — build
// metadata injected via -ldflags, production-mode atomic — are
// rt-private. Exposing thin wrappers keeps console_app's data-shaping
// code colocated with the rest of its inline-mount logic without
// reaching into rt's unexported package surface.
//
// Naming convention: ConsoleX → "the inline console reads this".
// Distinct from `HandleConsoleX` (HTTP handlers) and `ConsoleAuthX`
// (auth surface).

package rt

// ConsoleCurrentBuildInfo returns the same build-info snapshot the
// /_sky/buildinfo handler + the /_sky/console/api/overview response
// publish. Cheap; safe to call per request.
//
// Exists because `currentBuildInfo` itself is unexported and lives
// next to the HTTP handlers that consume it — the inline console_app
// renders the same fields (skyVersion / commit / builtAt) on its
// Overview tab and needs a typed accessor.
func ConsoleCurrentBuildInfo() BuildInfo {
	return currentBuildInfo()
}

// ConsoleIsProductionMode reports whether the runtime is in
// production gating mode (ENV / SKY_ENV set to a non-dev marker).
// The inline console renders this as a chip on the Overview tab so
// operators can confirm at a glance which mode the binary booted
// into.
//
// Mirrors `isProductionMode()` (unexported); exists for the inline
// mount path so console_app doesn't need to re-implement the same
// check from env (which would risk drift with the canonical source).
func ConsoleIsProductionMode() bool {
	return isProductionMode()
}
