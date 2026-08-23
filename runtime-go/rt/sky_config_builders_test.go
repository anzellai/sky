package rt

// The precedence + behavioural gates for the SECOND wave of Sky.Config
// builders (withDatabase / withSessions / withJobs / withCsrf / withTelemetry)
// and the two new Std.Live builders (withMaxBodyBytes / withInput), added on
// the foundation proven by sky_config_test.go.
//
// Each builder is verified two ways, per the foundation's pattern:
//
//   1. PRECEDENCE — the one rule holds for the builder's env name:
//      operator env > withX > seeded default (sky.toml) > fallback. For the two
//      LITERAL names (DATABASE_URL / OTEL_EXPORTER_OTLP_ENDPOINT) there is no
//      seed layer — the compiler never seeds them — so the order is the
//      three-layer operator > withX > fallback.
//
//   2. BEHAVIOUR — the strategy actually changes what the runtime DOES, read at
//      the real point of consumption (csrfEnabled, resolveStoreKind,
//      chooseJobsStore, embeddedDSNConflict, telemetry.LoadTracerConfigFromEnv,
//      resolveInputMode/resolveMaxBodyBytes), never merely "the env var is set".
//
// The withX values below are the strings the Sky surface normalises each ADT
// to via a total `case` (Sky/Config.sky): SharedWithDatabase → "postgres",
// withCsrf False → "off", and so on. The Go kernels store those strings; the
// Sky normalisation itself is exercised end-to-end by the emission tests in
// rust/crates/project/tests/sky_config_entry.rs.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sky-app/rt/telemetry"
)

// applyCfg runs a Sky.Config value (built via the real kernels) through
// ApplyConfig, exactly as the compiler-emitted `main` would.
func applyCfg(cfg any) { ApplyConfig(cfg) }

// resetLiteralEnv clears a LITERAL (non-`[env]`-prefixed) env var and its
// provenance marks for a subtest, restoring the prior state after. The prefixed
// sibling is resetEnvFor (sky_config_test.go).
func resetLiteralEnv(t *testing.T, name string) {
	t.Helper()
	orig, had := os.LookupEnv(name)
	clearSeededDefault(name)
	clearConfigApplied(name)
	_ = os.Unsetenv(name)
	t.Cleanup(func() {
		clearSeededDefault(name)
		clearConfigApplied(name)
		if had {
			_ = os.Setenv(name, orig)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// fourLayerPrecedence asserts operator > withX > seed > fallback for a
// PREFIXED, seeded suffix, driving the withX layer through the real builder
// chain (applyBuilder) and checking the winner with skyGetenv — the same read
// the runtime consumer uses. `withXVal` is the normalised string the builder
// writes.
func fourLayerPrecedence(t *testing.T, suffix string, applyBuilder func(), withXVal string) {
	t.Helper()
	t.Run("seed_only_beats_fallback", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "seededval")
		if got := skyGetenv(suffix); got != "seededval" {
			t.Fatalf("a seed must beat the fallback; got %q", got)
		}
	})
	t.Run("withx_beats_seed", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "seededval")
		applyBuilder()
		if got := skyGetenv(suffix); got != withXVal {
			t.Fatalf("withX must beat a seeded default — the dead-builder bug this gate catches; "+
				"got %q want %q", got, withXVal)
		}
	})
	t.Run("operator_beats_withx", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "seededval")
		setOperator(suffix, "operatorval")
		applyBuilder()
		if got := skyGetenv(suffix); got != "operatorval" {
			t.Fatalf("an operator override must beat withX; got %q", got)
		}
	})
	t.Run("withx_beats_fallback", func(t *testing.T) {
		resetEnvFor(t, suffix)
		applyBuilder()
		if got := skyGetenv(suffix); got != withXVal {
			t.Fatalf("withX must beat the fallback when nothing else is set; got %q want %q", got, withXVal)
		}
	})
}

// ── Precedence gates — prefixed, seeded suffixes ────────────────────────────

func TestConfigPrecedence_Database_Sqlite(t *testing.T) {
	fourLayerPrecedence(t, "DB_PATH",
		func() { applyCfg(Config_withDatabase("sqlite", "app.db", Config_default())) },
		"app.db")
}

func TestConfigPrecedence_Sessions_Kind(t *testing.T) {
	fourLayerPrecedence(t, "LIVE_STORE",
		func() { applyCfg(Config_withSessions("postgres", "", Config_default())) },
		"postgres")
}

func TestConfigPrecedence_Sessions_Path(t *testing.T) {
	fourLayerPrecedence(t, "LIVE_STORE_PATH",
		func() { applyCfg(Config_withSessions("redis", "redis://h:6379", Config_default())) },
		"redis://h:6379")
}

func TestConfigPrecedence_Jobs_Kind(t *testing.T) {
	fourLayerPrecedence(t, "JOBS_STORE",
		func() { applyCfg(Config_withJobs("sqlite", "jobs.db", Config_default())) },
		"sqlite")
}

func TestConfigPrecedence_Csrf(t *testing.T) {
	// Applying CSRF re-runs refreshCsrfEnabled (the env-prefix hook), mutating
	// the process-global switch; restore it so sibling csrf tests are unaffected.
	prior := csrfEnabled.Load()
	t.Cleanup(func() { csrfEnabled.Store(prior) })
	fourLayerPrecedence(t, "CSRF",
		func() { applyCfg(Config_withCsrf("off", Config_default())) },
		"off")
}

// ── Precedence gates — literal, UNSEEDED names (three layers) ───────────────

func TestConfigPrecedence_LiteralNames(t *testing.T) {
	cases := []struct{ name, key, val string }{
		{"DATABASE_URL", "DatabaseUrl", "postgres://x/db"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "OtelEndpoint", "http://c:4318"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// withX beats the fallback (unset). No seed layer exists — the
			// compiler never seeds these operator-owned names.
			resetLiteralEnv(t, tc.name)
			ApplyConfig(map[string]any{tc.key: tc.val})
			if got, _ := lookupEnvRaw(tc.name); got != tc.val {
				t.Fatalf("withX must beat the fallback; got %q want %q", got, tc.val)
			}
			// operator beats withX.
			resetLiteralEnv(t, tc.name)
			_ = os.Setenv(tc.name, "operatorval")
			ApplyConfig(map[string]any{tc.key: tc.val})
			if got, _ := lookupEnvRaw(tc.name); got != "operatorval" {
				t.Fatalf("an operator override must beat withX; got %q", got)
			}
		})
	}
}

// TestConfigPrecedence_LiveBroker pins the withLiveBroker builder end to end:
// it is a PREFIXED but UNSEEDED suffix (no legacy [live] broker key), so it has
// three layers (operator > withX > fallback), and — the point of the whole
// feature — the value it applies is what effectiveBrokerUrl reads back for the
// Sky.Live path (live.go passes "" there). Operator env still overrides it.
func TestConfigPrecedence_LiveBroker(t *testing.T) {
	const suffix = "LIVE_BROKER_URL"

	t.Run("withx_beats_fallback_and_effectiveBrokerUrl_reads_it", func(t *testing.T) {
		resetEnvFor(t, suffix)
		applyCfg(Config_withLiveBroker("redis://from-config:6379", Config_default()))
		if got := skyGetenv(suffix); got != "redis://from-config:6379" {
			t.Fatalf("withLiveBroker must write the broker URL; got %q", got)
		}
		// The Sky.Live path passes "" and relies on effectiveBrokerUrl reading
		// the ApplyConfig-written env back.
		if got := effectiveBrokerUrl(""); got != "redis://from-config:6379" {
			t.Fatalf("effectiveBrokerUrl(\"\") must return the applied config URL; got %q", got)
		}
	})

	t.Run("operator_beats_withx", func(t *testing.T) {
		resetEnvFor(t, suffix)
		setOperator(suffix, "redis://from-operator:6379")
		applyCfg(Config_withLiveBroker("redis://from-config:6379", Config_default()))
		if got := skyGetenv(suffix); got != "redis://from-operator:6379" {
			t.Fatalf("an operator SKY_LIVE_BROKER_URL must beat withLiveBroker; got %q", got)
		}
		if got := effectiveBrokerUrl(""); got != "redis://from-operator:6379" {
			t.Fatalf("effectiveBrokerUrl must honour the operator override; got %q", got)
		}
	})

	t.Run("kernel_stores_the_url", func(t *testing.T) {
		m := Config_withLiveBroker("redis://h:6379", Config_default()).(map[string]any)
		if m["LiveBroker"] != "redis://h:6379" {
			t.Fatalf("Config_withLiveBroker must store LiveBroker: %v", m)
		}
	})
}

// The mutation contrast extended to a NEW seeded suffix: the real seed-aware
// applyConfigValue lets withSessions win over a legacy [live] store seed; the
// set-if-unset mutant lets the seed win — the same inversion the foundation
// proves on LOG_FORMAT, re-checked on LIVE_STORE so the shared mechanism is
// falsified for the new keys too.
func TestApplyConfigMutationContrast_Sessions(t *testing.T) {
	const suffix = "LIVE_STORE"
	t.Run("real_seed_aware_withx_wins", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "memory")
		applyCfg(Config_withSessions("postgres", "", Config_default()))
		if got := skyGetenv(suffix); got != "postgres" {
			t.Fatalf("real ApplyConfig should let withSessions win over the seed; got %q", got)
		}
	})
	t.Run("naive_set_if_unset_inverts", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "memory")
		SetEnvDefault(skyEnvName(suffix), "postgres") // the set-if-unset mutant
		if got := skyGetenv(suffix); got != "memory" {
			t.Fatalf("the set-if-unset mutant must invert precedence (seed wins); got %q "+
				"— if not \"memory\", the gate no longer catches the inversion", got)
		}
	})
}

// ── Behavioural oracles — the strategy changes what the runtime does ─────────

func TestBehaviouralOracle_Csrf(t *testing.T) {
	const suffix = "CSRF"
	// csrfEnabled is a process-global switch; restore it (resynced to the
	// restored env) after this test so sibling csrf tests are unaffected.
	prior := csrfEnabled.Load()
	t.Cleanup(func() { csrfEnabled.Store(prior) })

	// withCsrf False DISABLES the actual middleware switch.
	resetEnvFor(t, suffix)
	applyCfg(Config_withCsrf("off", Config_default()))
	if csrfEnabled.Load() {
		t.Fatalf("withCsrf False must disable csrfEnabled")
	}
	// withCsrf True KEEPS it on (the default-secure state).
	resetEnvFor(t, suffix)
	applyCfg(Config_withCsrf("on", Config_default()))
	if !csrfEnabled.Load() {
		t.Fatalf("withCsrf True must keep csrfEnabled on")
	}
	// Legacy vs migrated: a seeded [security] csrf=off and withCsrf False reach
	// IDENTICAL effective state (both disable the switch).
	resetEnvFor(t, suffix)
	seedLegacy(suffix, "off")
	legacyOff := csrfEnabled.Load()
	resetEnvFor(t, suffix)
	applyCfg(Config_withCsrf("off", Config_default()))
	migratedOff := csrfEnabled.Load()
	if legacyOff || migratedOff {
		t.Fatalf("csrf=off must disable on both paths; legacy=%v migrated=%v", legacyOff, migratedOff)
	}
}

func TestBehaviouralOracle_Sessions(t *testing.T) {
	// withSessions SharedWithDatabase → the resolver the store builder switches
	// on returns "postgres" with no explicit path (so selectStore falls back to
	// DATABASE_URL).
	resetEnvFor(t, "LIVE_STORE")
	resetEnvFor(t, "LIVE_STORE_PATH")
	applyCfg(Config_withSessions("postgres", "", Config_default()))
	if got := resolveStoreKind(""); got != "postgres" {
		t.Fatalf("withSessions SharedWithDatabase must make resolveStoreKind return postgres; got %q", got)
	}
	// withSessions (Redis url) reaches BOTH the kind and the path resolvers.
	resetEnvFor(t, "LIVE_STORE")
	resetEnvFor(t, "LIVE_STORE_PATH")
	applyCfg(Config_withSessions("redis", "redis://h:6379", Config_default()))
	if got := resolveStoreKind(""); got != "redis" {
		t.Fatalf("withSessions Redis must reach resolveStoreKind; got %q", got)
	}
	if got := resolveStorePath(""); got != "redis://h:6379" {
		t.Fatalf("withSessions Redis url must reach resolveStorePath; got %q", got)
	}
}

// The documented reconciliation: withSessions and Live.withStore land in the
// SAME builder layer, and the more-specific app-shape Live.withStore wins when
// both are set.
func TestConfigSessionsReconciliation(t *testing.T) {
	const suffix = "LIVE_STORE"
	resetEnvFor(t, suffix)
	applyCfg(Config_withSessions("postgres", "", Config_default())) // config-applied builder layer
	if got := resolveStoreKind("memory"); got != "memory" {
		t.Fatalf("an explicit Live.withStore must win the shared builder layer over withSessions; got %q", got)
	}
}

func TestBehaviouralOracle_Jobs(t *testing.T) {
	// Default (no config) → a memory store.
	resetEnvFor(t, "JOBS_STORE")
	resetEnvFor(t, "JOBS_STORE_PATH")
	mem := chooseJobsStore()
	if !strings.Contains(strings.ToLower(fmt.Sprintf("%T", mem)), "memory") {
		t.Fatalf("default jobs store must be memory; got %T", mem)
	}
	// withJobs JobsSqlite(tmp) → the sqlite store constructor actually runs.
	resetEnvFor(t, "JOBS_STORE")
	resetEnvFor(t, "JOBS_STORE_PATH")
	tmp := filepath.Join(t.TempDir(), "jobs.db")
	applyCfg(Config_withJobs("sqlite", tmp, Config_default()))
	sqlite := chooseJobsStore()
	if !strings.Contains(strings.ToLower(fmt.Sprintf("%T", sqlite)), "sqlite") {
		t.Fatalf("withJobs JobsSqlite must select a sqlite store; got %T", sqlite)
	}
}

func TestBehaviouralOracle_Database_EmbedConflict(t *testing.T) {
	// The --embed refusal: rt.ApplyConfig runs immediately BEFORE
	// rt.MaybeStartEmbeddedPostgres (emitted order, asserted in
	// sky_config_entry.rs), and MaybeStartEmbeddedPostgres calls
	// embeddedDSNConflict on the DSN env names ApplyConfig just wrote. So a
	// withDatabase DSN under --embed is refused identically to an explicit
	// env/sky.toml DSN — the ambiguity the design will not resolve silently.
	t.Run("postgres_dsn_triggers_conflict", func(t *testing.T) {
		resetLiteralEnv(t, "DATABASE_URL")
		resetEnvFor(t, "DB_PATH")
		if err := embeddedDSNConflict(embeddedDSNSources(osEnv)); err != nil {
			t.Fatalf("no DSN set → no --embed conflict expected; got %v", err)
		}
		applyCfg(Config_withDatabase("postgres", "postgres://h/db", Config_default()))
		err := embeddedDSNConflict(embeddedDSNSources(osEnv))
		if err == nil {
			t.Fatalf("withDatabase Postgres + --embed MUST be refused (embeddedDSNConflict), got nil")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Fatalf("the conflict must name DATABASE_URL; got %v", err)
		}
	})
	t.Run("sqlite_path_triggers_conflict", func(t *testing.T) {
		resetLiteralEnv(t, "DATABASE_URL")
		resetEnvFor(t, "DB_PATH")
		applyCfg(Config_withDatabase("sqlite", "app.db", Config_default()))
		err := embeddedDSNConflict(embeddedDSNSources(osEnv))
		if err == nil {
			t.Fatalf("withDatabase Sqlite + --embed MUST be refused (embeddedDSNConflict), got nil")
		}
		if !strings.Contains(err.Error(), skyEnvName("DB_PATH")) {
			t.Fatalf("the conflict must name %s; got %v", skyEnvName("DB_PATH"), err)
		}
	})
}

func TestBehaviouralOracle_Database_SqliteReachesDbPath(t *testing.T) {
	const suffix = "DB_PATH"
	// Legacy [database] path seed vs migrated withDatabase Sqlite reach an
	// IDENTICAL DB_PATH — the value Db_connect reads first.
	resetEnvFor(t, suffix)
	seedLegacy(suffix, "legacy.db")
	legacy := skyGetenv(suffix)
	resetEnvFor(t, suffix)
	applyCfg(Config_withDatabase("sqlite", "legacy.db", Config_default()))
	migrated := skyGetenv(suffix)
	if legacy != "legacy.db" || migrated != "legacy.db" || legacy != migrated {
		t.Fatalf("sqlite path must reach DB_PATH identically on both paths; legacy=%q migrated=%q",
			legacy, migrated)
	}
}

func TestBehaviouralOracle_Telemetry(t *testing.T) {
	const name = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// Unset → the exporter config the runtime builds has no endpoint (no-op
	// tracer). :4318 (http) is used rather than :4317 because the loader
	// deliberately clears a :4317 gRPC endpoint with a warning.
	resetLiteralEnv(t, name)
	if ep := telemetry.LoadTracerConfigFromEnv(false).Endpoint; ep != "" {
		t.Fatalf("unset OTEL endpoint should yield empty exporter endpoint; got %q", ep)
	}
	// withTelemetry sets the endpoint the exporter would dial.
	resetLiteralEnv(t, name)
	applyCfg(Config_withTelemetry("http://collector:4318", Config_default()))
	if ep := telemetry.LoadTracerConfigFromEnv(false).Endpoint; ep != "http://collector:4318" {
		t.Fatalf("withTelemetry must set the exporter endpoint; got %q", ep)
	}
	// operator wins.
	resetLiteralEnv(t, name)
	_ = os.Setenv(name, "http://operator:4318")
	applyCfg(Config_withTelemetry("http://collector:4318", Config_default()))
	if ep := telemetry.LoadTracerConfigFromEnv(false).Endpoint; ep != "http://operator:4318" {
		t.Fatalf("an operator OTEL endpoint must beat withTelemetry; got %q", ep)
	}
}

// ── Kernel map-shape (the kernels build what the emitted chain expects) ──────

func TestConfigKernels_SecondWave(t *testing.T) {
	base := Config_default()

	dbP := Config_withDatabase("postgres", "postgres://h/db", base).(map[string]any)
	if dbP["DatabaseUrl"] != "postgres://h/db" || dbP["DbPath"] != nil {
		t.Fatalf("withDatabase Postgres must set only DatabaseUrl: %v", dbP)
	}
	dbS := Config_withDatabase("sqlite", "a.db", base).(map[string]any)
	if dbS["DbPath"] != "a.db" || dbS["DatabaseUrl"] != nil {
		t.Fatalf("withDatabase Sqlite must set only DbPath: %v", dbS)
	}

	sh := Config_withSessions("postgres", "", base).(map[string]any)
	if sh["LiveStore"] != "postgres" || sh["LiveStorePath"] != nil {
		t.Fatalf("withSessions SharedWithDatabase must set LiveStore only: %v", sh)
	}
	rd := Config_withSessions("redis", "redis://h", base).(map[string]any)
	if rd["LiveStore"] != "redis" || rd["LiveStorePath"] != "redis://h" {
		t.Fatalf("withSessions Redis must set both keys: %v", rd)
	}

	jb := Config_withJobs("sqlite", "j.db", base).(map[string]any)
	if jb["JobsStore"] != "sqlite" || jb["JobsStorePath"] != "j.db" {
		t.Fatalf("withJobs JobsSqlite must set both keys: %v", jb)
	}

	cs := Config_withCsrf("off", base).(map[string]any)
	if cs["Csrf"] != "off" {
		t.Fatalf("withCsrf must set Csrf: %v", cs)
	}

	tl := Config_withTelemetry("http://c:4318", base).(map[string]any)
	if tl["OtelEndpoint"] != "http://c:4318" {
		t.Fatalf("withTelemetry must set OtelEndpoint: %v", tl)
	}

	// Shallow-clone invariant: the shared base is never mutated.
	if b, ok := base.(map[string]any); !ok || len(b) != 0 {
		t.Fatalf("a builder mutated the shared base map (must shallow-clone): %v", base)
	}
}
