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
//	1. the environment, set by the OPERATOR (shell or .env)
//	2. an explicit `withX` builder call in the app's own code
//	3. the environment, SEEDED by the generated prologue from sky.toml
//	   (or from the compiler's hardcoded fallback)
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
	operatorSet := envSet && envVal != "" && !isSeededDefault(name)

	out := make([]string, 0, 3)
	if operatorSet {
		out = append(out, envVal)
	}
	if builderVal != "" {
		out = append(out, builderVal)
	}
	// The seeded layer, only when it was not already emitted as layer 1.
	if envSet && envVal != "" && !operatorSet {
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

// resolveTTL — the session TTL, resolved across all layers.
//
// `builderVal` is `Live.withTtl`'s value, absent as "". Each layer accepts
// either a Go-duration string ("30m", "24h", "1h30m") or a bare integer read
// as seconds; empty or unparseable values fall through to the next layer.
func resolveTTL(builderVal string, def time.Duration) time.Duration {
	return parseTTL(configLayers("LIVE_TTL", builderVal), def)
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

// resolveLivePortLayers — the listen port, as an ordered candidate list.
// Kept separate from `resolveLivePort` because the port's fallback is a
// number and its "unparseable" test is `n > 0` rather than a parse error.
func resolveLivePortLayers(builderVal string) []string {
	return configLayers("LIVE_PORT", builderVal)
}

// parsePortLayers returns the first candidate that is a positive integer, or 0.
func parsePortLayers(vals []string) int {
	for _, raw := range vals {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
