// sky_config.go — the `Sky.Config` cross-cutting config value and its
// seed-aware application into the runtime's env namespace.
//
// ## The precedence, and why it is NOT set-if-unset
//
// A Sky app's `config` binding is emitted as `rt.ApplyConfig(Main_config())`,
// the FIRST statement of `main` (lower.rs `lower_main`). By the time it runs,
// every `init()` has already run — including the generated prologue `init()`
// that seeds the legacy `sky.toml` defaults via `SetSkyDefault` (set-if-unset).
//
// So a naive `ApplyConfig` that also used set-if-unset would be INVERTED: the
// legacy seed, running first in `init()`, would already have set the variable,
// and `ApplyConfig` in `main` would see it set and no-op — legacy `sky.toml`
// would beat `withX`, the exact opposite of the intended precedence, and a
// migrated app that left a stale `sky.toml` key would silently keep the old
// value.
//
// `ApplyConfig` is instead SEED-AWARE. It reuses the same provenance the
// working Live resolver already relies on (`isSeededDefault`, dotenv.go): a
// value the prologue seeded is marked, a value an operator set in the shell or
// `.env` is not. So `ApplyConfig` can safely clear-and-override a seeded
// default while deferring to an operator value. The single resulting rule —
// shared with `configLayers` so `Sky.Config.withX` and `Live.withX` cannot
// disagree — is:
//
//	operator env  >  withX (Sky.Config / Live)  >  seeded default (sky.toml)  >  fallback
//
// A `withX`-applied value is recorded in `configApplied` (not `seededDefault`),
// so `configLayers` ranks it as the BUILDER layer: below an operator override,
// above a seeded default. See live_config_precedence.go.
package rt

import (
	"os"
	"sync"
)

// ── The opaque Sky.Config value ────────────────────────────────────────────
//
// Mirrors the Std.Live builder representation (live_config.go): an opaque
// `Config` is a `map[string]any` keyed by the setting names `ApplyConfig`
// reads, and each `withX` shallow-clones the map and sets one (or a few) keys,
// so sibling derivations never alias one base map (Go maps are reference
// types).

// Config_default builds the empty Sky.Config — no settings, every value takes
// its runtime default. Backs `Sky.Config.default`.
func Config_default() any {
	return map[string]any{}
}

// configClone returns a shallow copy of a Config map, tolerating a non-map (a
// defensively-constructed empty base) the same way liveCfgSet does.
func configClone(cfg any) map[string]any {
	src, _ := cfg.(map[string]any)
	out := make(map[string]any, len(src)+2)
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Config_withLog sets the structured-log format and minimum level. The Sky
// surface (`Sky/Config.sky`) has already normalised the `LogFormat` / `LogLevel`
// ADTs to their env strings via a total `case`, so this kernel stores plain
// strings under the keys `ApplyConfig` maps to `LOG_FORMAT` / `LOG_LEVEL`.
func Config_withLog(format, level, cfg any) any {
	out := configClone(cfg)
	if s, ok := format.(string); ok && s != "" {
		out["LogFormat"] = s
	}
	if s, ok := level.(string); ok && s != "" {
		out["LogLevel"] = s
	}
	return out
}

// ── Application into the env namespace ──────────────────────────────────────

// configKeyToEnvSuffix maps a Sky.Config map key to the internal env suffix
// the runtime reads it through (skyEnvName prepends the [env] prefix). This is
// the single registry of which config keys exist; a later phase extends it as
// builders are added.
var configKeyToEnvSuffix = map[string]string{
	"LogFormat": "LOG_FORMAT",
	"LogLevel":  "LOG_LEVEL",
}

// configApplied records the env vars a `withX` config value wrote — distinct
// from `seededDefaults` (a legacy sky.toml seed). It ranks the value as the
// BUILDER layer in `configLayers`: below an operator override, above a seed.
var (
	configAppliedMu sync.Mutex
	configApplied   = map[string]struct{}{}
)

func markConfigApplied(name string) {
	configAppliedMu.Lock()
	defer configAppliedMu.Unlock()
	configApplied[name] = struct{}{}
}

// isConfigApplied reports whether name's current value was written by
// ApplyConfig (a `withX` value) rather than seeded or operator-set.
func isConfigApplied(name string) bool {
	configAppliedMu.Lock()
	defer configAppliedMu.Unlock()
	_, ok := configApplied[name]
	return ok
}

func clearConfigApplied(name string) {
	configAppliedMu.Lock()
	defer configAppliedMu.Unlock()
	delete(configApplied, name)
}

// ApplyConfig applies the app's `Sky.Config` value into the runtime's env
// namespace, seed-aware, at the top of `main`. Emitted by the compiler as
// `rt.ApplyConfig(Main_config())` ONLY when the entry module declares a
// `config` binding — a program without one is byte-identical to before.
//
// For each known setting present in the config value it defers to an operator
// override and otherwise clears-and-overrides a seeded default (or sets when
// unset). After applying, it re-runs the env-prefix refresh hooks so any
// value captured at package init (`logThreshold`, `logJSON`, …) picks up the
// applied value — the same mechanism `SetSkyDefault` uses, and the reason a
// value written here in `main` is still seen by a read cached in `init`.
func ApplyConfig(cfg any) {
	m, ok := cfg.(map[string]any)
	if !ok || m == nil {
		// A missing/empty config applies nothing — every setting keeps its
		// existing (seeded or default) value. A non-map here would mean the
		// compiler emitted ApplyConfig against a non-Config value, which the
		// lowering's config-type guard rejects at build time.
		return
	}
	changed := false
	for key, suffix := range configKeyToEnvSuffix {
		v, present := m[key]
		if !present {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if applyConfigValue(skyEnvName(suffix), s) {
			changed = true
		}
	}
	if changed {
		for _, fn := range envPrefixHooks {
			fn()
		}
	}
}

// applyConfigValue writes a single `withX` value into the env under the shared
// precedence, returning true when it actually wrote.
//
//   - operator-set (env set, not a seeded default): DEFER — the operator wins.
//   - unset OR a seeded default (legacy sky.toml): OVERRIDE — clear the seeded
//     mark and write the withX value, recording it as config-applied so
//     `configLayers` ranks it as the builder layer.
//
// This is deliberately NOT `SetEnvDefault` (set-if-unset): a seeded default is
// already set by the time this runs, and set-if-unset would let it win — the
// inverted precedence this whole mechanism exists to refuse.
func applyConfigValue(name, value string) bool {
	if cur, ok := lookupEnvRaw(name); ok && cur != "" && !isSeededDefault(name) {
		return false // operator-set — defer
	}
	if os.Setenv(name, value) != nil {
		return false
	}
	clearSeededDefault(name)
	markConfigApplied(name)
	return true
}
