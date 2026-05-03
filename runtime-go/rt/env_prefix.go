// env_prefix.go — env-var namespace prefix for Sky.Live / Std.Auth /
// Std.Log / Std.Db runtime reads.
//
// Default prefix is "SKY", so the runtime reads `SKY_LIVE_PORT`,
// `SKY_AUTH_TOKEN_TTL`, `SKY_LOG_FORMAT`, etc. — the documented names.
//
// Projects that need a private namespace (e.g. running multiple Sky
// binaries on the same host where SKY_LIVE_PORT would collide) can
// declare a custom prefix in sky.toml:
//
//     [env]
//     prefix = "FENCE"
//
// The compiler emits a single `rt.SetEnvPrefix("FENCE")` call at the
// top of the generated `init()` block. From that point on, the runtime
// reads `FENCE_LIVE_PORT`, `FENCE_AUTH_TOKEN_TTL`, etc. The user's
// shell / .env / docker env supplies the prefixed names too.
//
// The prefix only affects Sky's INTERNAL namespace (LIVE_*, AUTH_*,
// LOG_*, DB_*, ENV, STATIC_DIR, plus the legacy STATIC_DIR alias).
// User code calling `System.getenv "DATABASE_URL"` reads the raw name
// — only the runtime's own SKY_* reads route through the prefix.
//
// The compile-time `SKY_SOLVER_BUDGET` knob is read by the Haskell
// compiler itself, NOT by the generated app, so it's unaffected.
package rt

import (
	"os"
	"strings"
)

// envPrefix is the prefix prepended to every runtime SKY_* env-var
// read. Default is "SKY". Mutated only by SetEnvPrefix from the
// compiler-generated init().
var envPrefix = "SKY"

// SetEnvPrefix overrides the default "SKY" namespace. Called from the
// compiler-generated init() when sky.toml declares [env] prefix = "X".
//
// Trims a trailing `_` so both `prefix = "FENCE"` and
// `prefix = "FENCE_"` produce `FENCE_LIVE_PORT`. Empty input falls
// back to the "SKY" default. Safe to call multiple times — last call
// wins (though sky.toml only emits one).
//
// Re-runs registered refresh hooks (`onEnvPrefixChange`) so any
// package-level cached env reads — `logThreshold`, `logJSON`, etc.
// — pick up the new prefix even though those were initialised
// before main.init() ran.
func SetEnvPrefix(prefix string) {
	p := strings.TrimRight(prefix, "_")
	if p == "" {
		p = "SKY"
	}
	envPrefix = p
	for _, fn := range envPrefixHooks {
		fn()
	}
}

// envPrefixHooks holds re-init callbacks for any package-level state
// that captured an env-var value at startup. Registered via
// `onEnvPrefixChange` from each rt-source-file's init().
var envPrefixHooks []func()

// onEnvPrefixChange registers a refresh callback. Use from a package-
// level init() to re-read any env-derived state when the prefix
// changes. Order between hooks is unspecified.
func onEnvPrefixChange(fn func()) {
	envPrefixHooks = append(envPrefixHooks, fn)
}

// EnvPrefix returns the currently-configured prefix (without the
// trailing `_`). Mainly useful for diagnostics / tests.
func EnvPrefix() string { return envPrefix }

// skyEnvName returns the prefixed env-var name. Pass the suffix only:
// `skyEnvName("LIVE_PORT")` → `"SKY_LIVE_PORT"` by default, or
// `"FENCE_LIVE_PORT"` when the prefix is configured.
func skyEnvName(suffix string) string {
	return envPrefix + "_" + suffix
}

// skyLookupEnv reads an env var in Sky's internal namespace. Pass the
// suffix only ("LIVE_PORT", not "SKY_LIVE_PORT").
func skyLookupEnv(suffix string) (string, bool) {
	return os.LookupEnv(skyEnvName(suffix))
}

// skyGetenv: convenience wrapper matching os.Getenv's "missing →
// empty string" shape. Used at the runtime call sites where the
// "unset" case naturally falls through (`if v := skyGetenv(...); v
// != "" { ... }`).
func skyGetenv(suffix string) string {
	return os.Getenv(skyEnvName(suffix))
}

// SetSkyDefault: set a Sky-namespaced env-var default if not already
// set. Pass the suffix only ("LIVE_TTL", not "SKY_LIVE_TTL"); the
// configured prefix is prepended.
//
// Generated init() functions call this for each sky.toml-derived
// default (port, session store, TTL, static dir, log format/level,
// auth secret/cookie/driver, db driver/path), so shell + .env always
// take precedence — same precedence rule as the older
// SetEnvDefault.
func SetSkyDefault(suffix, value string) {
	SetEnvDefault(skyEnvName(suffix), value)
}
