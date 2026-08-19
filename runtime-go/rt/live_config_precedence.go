package rt

// Where the precedence of a Sky.Live setting is decided — for all of them, in
// one place.
//
// ## Why one place
//
// Four settings in this package resolved through three different orders, and
// the divergence was measured rather than argued (docs/coverage/config-matrix.json):
//
//	live.port       env -> builder -> toml -> 8000
//	live.storePath  BUILDER -> env -> toml
//	live.ttl        env -> toml -> BUILDER NEVER WINS
//	live.idleEvict  env -> builder
//
// `live.storePath` inverted `live.ttl` exactly, one module apart. Nobody chose
// that; it accumulated, because the ordering lived in `resolveLivePort`,
// `selectStore` and `parseTTL` and nothing forced those three to agree. Adding
// a fourth hand-written order would have been the same mistake with a better
// intention, so the settings now share a resolver: they agree because there is
// only one of it.

import (
	"strconv"
	"strings"
	"time"
)

// configLayers returns the candidate raw values for a setting, in precedence
// order, skipping layers that supplied nothing.
//
//  1. the environment, set by the OPERATOR (shell or .env)
//  2. an explicit `withX` builder call in the app's own code
//  3. the environment, SEEDED by the generated prologue from sky.toml
//     (or from the compiler's hardcoded fallback)
//
// Layers 1 and 3 are the same variable, which is why this used to be wrong.
// `<PREFIX>_LIVE_TTL` set by an operator and the same name seeded by
// `rt.SetSkyDefault("LIVE_TTL", "1800")` — which `lower.rs:822` emits into
// EVERY program — are indistinguishable in os.Environ(). `SetEnvDefault`
// records which ones it seeded (dotenv.go:34-66) and `setEnvRaw` clears the
// mark, so a `.env` value counts as operator-set and a sky.toml value does
// not. That provenance already existed and already worked; until now
// `resolveLivePort` was its only consumer, which is precisely why `withPort`
// was the only builder of the four that could win.
//
// A LIST rather than a single winner, because a value that is present but
// unparseable must fall through to the next layer instead of collapsing to the
// hardcoded fallback — the behaviour `parseTTL` and `resolveLivePort` already
// had, and the reason `SKY_LIVE_PORT=not-a-port` does not bind port 0.
func configLayers(suffix, builderVal string) []string {
	name := skyEnvName(suffix)
	envVal, envSet := lookupEnvRaw(name)
	// A value written into this env by `rt.ApplyConfig` (a `Sky.Config.withX`
	// value) is the BUILDER layer, not an operator override — the same layer as
	// the explicit `builderVal` a `Live.withX` passes. Distinguishing it keeps
	// ONE precedence: Sky.Config.withX and Live.withX resolve identically and
	// cannot disagree.
	cfgApplied := envSet && envVal != "" && isConfigApplied(name)
	operatorSet := envSet && envVal != "" && !isSeededDefault(name) && !cfgApplied

	out := make([]string, 0, 3)
	if operatorSet {
		out = append(out, envVal)
	}
	// The builder layer. An explicit `Live.withX` argument is more specific than
	// a `Sky.Config.withX`-applied env value, so it comes first; they are the
	// same layer, and when a caller sets both the app-shape builder wins.
	if builderVal != "" {
		out = append(out, builderVal)
	} else if cfgApplied {
		out = append(out, envVal)
	}
	// The seeded layer, only when it was not already emitted as layer 1 or 2.
	if envSet && envVal != "" && !operatorSet && !cfgApplied {
		out = append(out, envVal)
	}
	return out
}

// firstNonEmpty picks the winning layer for a setting whose value needs no
// parsing, so it cannot fall through.
func firstNonEmpty(vals []string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// defaultSessionTTL is the ONE default for a resolved Sky.Live session TTL — the
// value used when neither an operator env, a `withX` builder, nor a seeded
// sky.toml default supplies one. Both the sliding session (live.go /
// subapp_inprocess.go) and the CSRF cookie that guards it (csrf_middleware.go)
// derive their sliding window from a session TTL resolved with THIS default, so
// the two cannot key to two different defaults. It replaces the 30-minute /
// 30-day split §1.7 recorded: the session resolved LIVE_TTL with a 30-minute
// default while the CSRF cookie re-resolved the SAME variable with an
// independent 30-day default. The long cookie window is now purely a property of
// slidingCookieMaxAgeSeconds's floor, not of a second TTL default.
const defaultSessionTTL = 30 * time.Minute

// resolveTTL — the session TTL, resolved across all layers.
//
// `builderVal` is `Live.withTtl`'s value, absent as "". Each layer accepts
// either a Go-duration string ("30m", "24h", "1h30m") or a bare integer read
// as seconds; empty or unparseable values fall through to the next layer.
func resolveTTL(builderVal string, def time.Duration) time.Duration {
	return parseTTL(configLayers("LIVE_TTL", builderVal), def)
}

// resolveSessionTTL resolves the sliding session TTL through the shared
// precedence with no per-cookie builder override — the value the CSRF cookie's
// Max-Age derives from. It is deliberately the SAME resolution live.go applies
// to the session itself (default `defaultSessionTTL`), so the CSRF window tracks
// the resolved session it guards rather than a second, independent LIVE_TTL
// default.
//
// It is NOT a THIRD LIVE_TTL reader: the CSRF path already resolved LIVE_TTL
// here (via `resolveTTL("", …)`); this only names that read and aligns its
// default with the session's. Every LIVE_TTL read still funnels through the one
// `configLayers("LIVE_TTL", …)` above.
func resolveSessionTTL() time.Duration {
	return resolveTTL("", defaultSessionTTL)
}

// resolveIdleEvict — the tiered-session-cache idle-evict window.
func resolveIdleEvict(builderVal string, def time.Duration) time.Duration {
	return parseIdleEvict(configLayers("LIVE_IDLE_EVICT", builderVal), def)
}

// resolveStoreKind — the session store backend name ("memory" / "sqlite" /
// "postgres" / "redis"). `builderVal` is `Live.withStore`'s value.
func resolveStoreKind(builderVal string) string {
	return firstNonEmpty(configLayers("LIVE_STORE", builderVal))
}

// resolveStorePath — the session store path or DSN. `builderVal` is
// `Live.withStorePath`'s value.
func resolveStorePath(builderVal string) string {
	return firstNonEmpty(configLayers("LIVE_STORE_PATH", builderVal))
}

// `resolveLivePortLayers` USED TO LIVE HERE and it was a decoy: a one-line
// wrapper around `configLayers("LIVE_PORT", …)` with ZERO callers in
// production and in tests, while `resolveLivePort` (live.go:3866) called
// `configLayers` directly. Every other resolver in this file has at least one
// production call site; this one existed only to be read.
//
// It was found because inverting ITS precedence left `config-matrix --check:
// OK` — the file that exists to hold "one precedence rule" contained a second,
// unreachable copy of the rule that a reader would reasonably edit. Deleted
// rather than wired: wiring it would add indirection to reach the same
// `configLayers` call, and a decoy in this file is worse than no file.

// parsePortLayers returns the first candidate that is a positive integer, or 0.
func parsePortLayers(vals []string) int {
	for _, raw := range vals {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
