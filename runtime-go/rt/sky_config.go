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

// configSetStr stores `value` under `key` in a shallow clone of `cfg`, skipping
// an empty string so an absent ADT arm (e.g. `Memory`'s empty store PATH)
// leaves the key unset rather than blanking it. The Sky surface has already
// normalised each ADT to its env string via a total `case`, so these kernels
// only ever see plain strings.
func configSetStr(cfg any, key string, value any) map[string]any {
	out := configClone(cfg)
	if s, ok := value.(string); ok && s != "" {
		out[key] = s
	}
	return out
}

// Config_withDatabase sets the database DSN. `kind` is "sqlite" | "postgres"
// (normalised in Sky). SQLite writes the prefixed `DB_PATH` (a file path);
// Postgres writes the literal `DATABASE_URL` — the name Db_connect falls back
// to and the session/analytics/jobs stores read. Both names are exactly the
// ones `rt.embeddedDSNConflict` checks, so a `withDatabase` DSN in an `--embed`
// app is refused at startup identically to an explicit env/sky.toml DSN
// (ApplyConfig runs immediately before MaybeStartEmbeddedPostgres).
func Config_withDatabase(kind, value, cfg any) any {
	k, _ := kind.(string)
	switch k {
	case "sqlite":
		return configSetStr(cfg, "DbPath", value)
	case "postgres":
		return configSetStr(cfg, "DatabaseUrl", value)
	default:
		return configClone(cfg)
	}
}

// Config_withSessions sets the Sky.Live session store kind + path
// (`LIVE_STORE` / `LIVE_STORE_PATH`). `SharedWithDatabase` normalises to
// kind="postgres" with an empty path, so `selectStore` falls back to
// `DATABASE_URL` (live_store.go). This OVERLAPS `Live.withStore`; both land in
// the builder layer and the more-specific `Live.withStore` wins (configLayers).
func Config_withSessions(kind, path, cfg any) any {
	out := configSetStr(cfg, "LiveStore", kind)
	return configSetStr(out, "LiveStorePath", path)
}

// Config_withJobs sets the Std.Jobs store kind + path (`JOBS_STORE` /
// `JOBS_STORE_PATH`). `JobsSharedWithDatabase` normalises to kind="postgres"
// with an empty path, so `chooseJobsStore` falls back to `DATABASE_URL`.
func Config_withJobs(kind, path, cfg any) any {
	out := configSetStr(cfg, "JobsStore", kind)
	return configSetStr(out, "JobsStorePath", path)
}

// Config_withCsrf toggles the global CSRF middleware (`CSRF`). The Sky surface
// normalises the Bool to "on"/"off"; `refreshCsrfEnabled` reads
// off/false/0 → disabled, anything else → enabled. ApplyConfig re-runs the
// env-prefix hooks after writing, so the change reaches `csrfEnabled` even
// though `refreshCsrfEnabled` captured its value at init.
func Config_withCsrf(value, cfg any) any {
	return configSetStr(cfg, "Csrf", value)
}

// Config_withTelemetry points the OTLP exporter at a collector (the literal
// `OTEL_EXPORTER_OTLP_ENDPOINT`, an industry-standard name that is NOT
// env-prefixed).
func Config_withTelemetry(endpoint, cfg any) any {
	return configSetStr(cfg, "OtelEndpoint", endpoint)
}

// ── Application into the env namespace ──────────────────────────────────────

// configKeyToEnvSuffix maps a Sky.Config map key to the internal env SUFFIX the
// runtime reads it through — `skyEnvName` prepends the configured `[env]`
// prefix, so `LOG_FORMAT` becomes e.g. `SKY_LOG_FORMAT`. Every suffix here is
// one the compiler ALREADY seeds from the corresponding `sky.toml` section
// (config-surface's `seeded_suffixes`), so a builder that writes it wins over
// that seed while still losing to an operator override.
var configKeyToEnvSuffix = map[string]string{
	"LogFormat":     "LOG_FORMAT",
	"LogLevel":      "LOG_LEVEL",
	"DbPath":        "DB_PATH",
	"LiveStore":     "LIVE_STORE",
	"LiveStorePath": "LIVE_STORE_PATH",
	"JobsStore":     "JOBS_STORE",
	"JobsStorePath": "JOBS_STORE_PATH",
	"Csrf":          "CSRF",
}

// configKeyToLiteralEnv maps a Sky.Config map key to a LITERAL env var name
// that is NOT `[env]`-prefixed — the industry-standard names the runtime reads
// verbatim (`DATABASE_URL` for a Postgres DSN, `OTEL_EXPORTER_OTLP_ENDPOINT`
// for the OTLP collector). These are never seeded by the prologue, so they sit
// outside config-surface's census; a builder writes one only when an operator
// has not, and defers to the operator otherwise (applyConfigValue).
var configKeyToLiteralEnv = map[string]string{
	"DatabaseUrl":  "DATABASE_URL",
	"OtelEndpoint": "OTEL_EXPORTER_OTLP_ENDPOINT",
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
	// Prefixed suffixes — resolved through the configured [env] prefix. Each is
	// already a seeded default, so applyConfigValue clears-and-overrides the seed
	// while deferring to an operator override.
	for key, suffix := range configKeyToEnvSuffix {
		if applyConfigKey(m, key, skyEnvName(suffix)) {
			changed = true
		}
	}
	// Literal names — written verbatim (DATABASE_URL / OTEL_…). Never seeded, so
	// the seed branch of applyConfigValue is simply never taken for these.
	for key, name := range configKeyToLiteralEnv {
		if applyConfigKey(m, key, name) {
			changed = true
		}
	}
	if changed {
		for _, fn := range envPrefixHooks {
			fn()
		}
	}
}

// applyConfigKey applies one config-map key to a resolved env NAME, returning
// true when it actually wrote. A missing key or an empty/non-string value is a
// no-op — an unset builder must leave the env untouched.
func applyConfigKey(m map[string]any, key, name string) bool {
	v, present := m[key]
	if !present {
		return false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	return applyConfigValue(name, s)
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
