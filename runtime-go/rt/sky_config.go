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
	"fmt"
	"os"
	"sort"
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

// Config_withTelemetryAggregationWindow sets the counter-coalescing window (the
// literal SKY_TELEMETRY_AGGREGATION_WINDOW). The Sky surface passes Int seconds;
// stored as a Go-duration string ("10s") the flusher parses. Operator env wins.
func Config_withTelemetryAggregationWindow(seconds, cfg any) any {
	return configSetStr(cfg, "TelemetryAggregationWindow", fmt.Sprintf("%ds", AsInt(seconds)))
}

// Config_withTelemetryHistogramWindow sets the histogram-coalescing window (the
// literal SKY_TELEMETRY_HISTOGRAM_AGGREGATION_WINDOW). Int seconds → "10s".
func Config_withTelemetryHistogramWindow(seconds, cfg any) any {
	return configSetStr(cfg, "TelemetryHistogramWindow", fmt.Sprintf("%ds", AsInt(seconds)))
}

// Config_withTelemetryDbCapacity sets the DB capacity for the size-report danger
// flag (the literal SKY_TELEMETRY_DB_CAPACITY). The Sky Capacity ADT is reduced
// to a byte count (pure Sky arithmetic) and stored as a decimal string, which
// parseHumanBytes accepts as a bare byte count.
func Config_withTelemetryDbCapacity(bytes, cfg any) any {
	return configSetStr(cfg, "TelemetryDbCapacity", fmt.Sprintf("%d", AsInt(bytes)))
}

// Config_withTelemetrySynchronousCommit sets synchronous_commit for the
// telemetry writer's Postgres batches (the literal SKY_TELEMETRY_SYNCHRONOUS_COMMIT).
// The Sky surface passes the already-normalised "on"/"off" string.
func Config_withTelemetrySynchronousCommit(mode, cfg any) any {
	return configSetStr(cfg, "TelemetrySynchronousCommit", mode)
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
	// Telemetry storage/tuning — operator/deploy settings the runtime reads
	// verbatim (never `[env]`-prefixed, never seeded), given withX builders so
	// the config front door is complete. Operator env still wins.
	"TelemetryAggregationWindow": "SKY_TELEMETRY_AGGREGATION_WINDOW",
	"TelemetryHistogramWindow":   "SKY_TELEMETRY_HISTOGRAM_AGGREGATION_WINDOW",
	"TelemetryDbCapacity":        "SKY_TELEMETRY_DB_CAPACITY",
	"TelemetrySynchronousCommit": "SKY_TELEMETRY_SYNCHRONOUS_COMMIT",
}

// configKeyToBuilder names the `withX` builder each config key is set by — the
// "which builder sets each" the migration LIST inverts. It is colocated with
// configKeyToEnvSuffix ON PURPOSE: the two describe the same setting from two
// sides (its env suffix, its builder), so keeping them adjacent is what stops
// a suffix and its builder name from drifting apart (design §1.3, the failure
// this whole config layer exists to close). The `config-migration` xtask gate
// asserts this map's keys are EXACTLY configKeyToEnvSuffix's, so a new builder
// cannot add a suffix without naming its builder here.
var configKeyToBuilder = map[string]string{
	"LogFormat":     "Sky.Config.withLog",
	"LogLevel":      "Sky.Config.withLog",
	"DbPath":        "Sky.Config.withDatabase",
	"LiveStore":     "Sky.Config.withSessions",
	"LiveStorePath": "Sky.Config.withSessions",
	"JobsStore":     "Sky.Config.withJobs",
	"JobsStorePath": "Sky.Config.withJobs",
	"Csrf":          "Sky.Config.withCsrf",
}

// legacyMigrationNotices lists the legacy `sky.toml` runtime settings that
// seeded THIS process's environment and were NOT overridden by a `withX`
// builder or an operator — the migration LIST the user asked for, printed on
// the running app's console (startup_report.go).
//
// # Why `isSeededDefault` is the SOUND detector here (design §8.2)
//
// The runtime never sees `sky.toml`; it sees the environment the compiler's
// generated prologue seeded. `SetSkyDefault` marks each value it seeds in
// `seededDefaults`, and `ApplyConfig` CLEARS that mark when a `withX` value
// overrides the suffix (`clearSeededDefault`), while an operator's own env var
// is never marked at all. So for a suffix in `configKeyToEnvSuffix`,
// `isSeededDefault` is true IFF the value came from a legacy `sky.toml` key and
// nothing in code or the environment replaced it — exactly the "legacy key
// present AND withX not used" condition. It is self-extinguishing: migrate the
// key into a `withX` and the mark clears, so this block falls silent.
//
// This is sound ONLY for the `configKeyToEnvSuffix` suffixes, and that is why
// it is keyed off that map rather than a broader one: those eight suffixes are
// seeded ONLY from their `sky.toml` key. The unconditional prologue fallbacks —
// `LIVE_TTL`, `LIVE_PORT`, the `AUTH_*` block (lower.rs `prologue_init`) — are
// seeded for EVERY program regardless of `sky.toml`, so `isSeededDefault` would
// false-positive on them; none is in `configKeyToEnvSuffix`, so none is
// considered here. Those (the `[auth]` REMOVED class, the `[live] ttl`
// DefaultChanged class) are surfaced by the build-time hint, which reads the
// actual `sky.toml` and can tell them apart.
//
// Returns nil when nothing legacy is seeded — the common, fully-configured
// case, and the reason a clean app's startup output is unchanged.
func legacyMigrationNotices() []string {
	type notice struct{ env, builder string }
	var found []notice
	for key, suffix := range configKeyToEnvSuffix {
		name := skyEnvName(suffix)
		if !isSeededDefault(name) {
			continue
		}
		builder := configKeyToBuilder[key]
		if builder == "" {
			// A suffix with no builder label would be a drift the gate is meant
			// to catch; skip it here rather than print a half-line, so a gate
			// gap never becomes a garbled console message.
			continue
		}
		found = append(found, notice{env: name, builder: builder})
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].env < found[j].env })

	width := 0
	for _, n := range found {
		if len(n.env) > width {
			width = len(n.env)
		}
	}
	lines := make([]string, 0, len(found)+2)
	lines = append(lines,
		fmt.Sprintf("  %-11s  legacy sky.toml settings are seeding this app — move them into a `config` binding:", "migrate"))
	for _, n := range found {
		lines = append(lines, fmt.Sprintf("  %-11s  %-*s  ->  %s", "", width, n.env, n.builder))
	}
	lines = append(lines,
		fmt.Sprintf("  %-11s  they still work as defaults; run `sky build` for the full list", ""))
	return lines
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
