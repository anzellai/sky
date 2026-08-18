package rt

// The precedence gate for `rt.ApplyConfig` — the load-bearing proof that the
// Sky.Config `withX` layer is NOT inverted against the legacy `sky.toml` seed.
//
// The design's adversarial grill proved the naive mechanism unsound: legacy
// `sky.toml` seeds emit in the prologue `init()` (set-if-unset), Go runs every
// `init()` before `main`, and `ApplyConfig` runs in `main` — so a set-if-unset
// ApplyConfig would see the seed already set and no-op, letting legacy beat
// `withX` (the inverse of the intended precedence). This file asserts the
// WINNER at each layer, and the mutation-contrast test proves that a set-if-unset
// ApplyConfig produces exactly that inversion.
//
// The single precedence, shared with `configLayers` so Sky.Config.withX and
// Live.withX cannot disagree:
//
//	operator env  >  withX  >  seeded default (sky.toml)  >  fallback

import (
	"os"
	"testing"
)

// resetEnvFor clears a suffix's env value + provenance marks for a subtest and
// restores the prior state afterwards, so these globally-mutating tests do not
// leak into each other.
func resetEnvFor(t *testing.T, suffix string) {
	t.Helper()
	name := skyEnvName(suffix)
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

// seedLegacy simulates the prologue init()'s sky.toml seed: set-if-unset, marked
// seeded.
func seedLegacy(suffix, value string) { SetSkyDefault(suffix, value) }

// setOperator simulates a shell / .env value: present and NOT a seeded default.
func setOperator(suffix, value string) {
	name := skyEnvName(suffix)
	clearSeededDefault(name)
	clearConfigApplied(name)
	_ = os.Setenv(name, value)
}

// withX simulates the app's Sky.Config value carrying one setting.
func applyWithLogFormat(value string) { ApplyConfig(map[string]any{"LogFormat": value}) }

// The four-layer precedence gate.
func TestConfigPrecedence(t *testing.T) {
	const suffix = "LOG_FORMAT"
	// Opaque sentinels — this test proves the resolution ORDER, not log
	// semantics, so distinct values make the winner unambiguous.
	cases := []struct {
		name    string
		setup   func()
		want    string
		wantWhy string
	}{
		{
			name:    "only_legacy_beats_fallback",
			setup:   func() { seedLegacy(suffix, "legacyval") },
			want:    "legacyval",
			wantWhy: "a seeded sky.toml default beats the hardcoded fallback",
		},
		{
			name: "legacy_plus_withx_the_withx_wins",
			setup: func() {
				seedLegacy(suffix, "legacyval")
				applyWithLogFormat("withxval")
			},
			want:    "withxval",
			wantWhy: "withX beats a seeded legacy default — THE BUG this gate exists to catch",
		},
		{
			name: "legacy_plus_withx_plus_operator_the_operator_wins",
			setup: func() {
				seedLegacy(suffix, "legacyval")
				setOperator(suffix, "operatorval")
				applyWithLogFormat("withxval")
			},
			want:    "operatorval",
			wantWhy: "an operator's env override beats withX",
		},
		{
			name:    "only_withx_beats_fallback",
			setup:   func() { applyWithLogFormat("withxval") },
			want:    "withxval",
			wantWhy: "withX beats the hardcoded fallback when nothing else is set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetEnvFor(t, suffix)
			tc.setup()
			got := skyGetenv(suffix)
			if got != tc.want {
				t.Fatalf("precedence WRONG: got %q, want %q (%s)", got, tc.want, tc.wantWhy)
			}
		})
	}
}

// applyConfigValueNaive is the INVERTED mutant the grill described: plain
// set-if-unset, the naive `ApplyConfig` design. It exists only in this test, to
// prove that the mutation makes the "withX wins over legacy" case go red.
func applyConfigValueNaive(name, value string) { SetEnvDefault(name, value) }

// The mutation proof: the real seed-aware `applyConfigValue` lets withX win over
// a legacy seed; the set-if-unset mutant lets legacy win — the exact inversion.
func TestApplyConfigMutationContrast(t *testing.T) {
	const suffix = "LOG_FORMAT"
	name := skyEnvName(suffix)

	t.Run("real_seed_aware_applyConfig_withx_wins", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "legacyval") // prologue seed, runs first
		applyWithLogFormat("withxval")  // ApplyConfig in main()
		if got := skyGetenv(suffix); got != "withxval" {
			t.Fatalf("real ApplyConfig should let withX win; got %q", got)
		}
	})

	t.Run("naive_set_if_unset_inverts_legacy_wins", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "legacyval")            // prologue seed, runs first
		applyConfigValueNaive(name, "withxval")    // the mutant, set-if-unset
		if got := skyGetenv(suffix); got != "legacyval" {
			t.Fatalf("the set-if-unset mutant must invert precedence (legacy wins); got %q "+
				"— if this is not \"legacyval\", the gate no longer catches the inversion", got)
		}
	})
}

// The reconciliation: a Sky.Config-applied value is ranked by `configLayers` as
// the BUILDER layer — below an operator override, above a seeded default —
// identically to a `Live.withX` argument. This is what makes Sky.Config.withX
// and Live.withX ONE precedence rather than two.
func TestConfigLayersReconciliation(t *testing.T) {
	// Use a genuine Live suffix so we exercise the real resolver.
	const suffix = "LIVE_STORE"

	t.Run("config_applied_ranks_above_a_seed", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "sqlite")
		// ApplyConfig writes a config value for this suffix (simulated: mark it
		// config-applied the way applyConfigValue does).
		applyConfigValueFor(suffix, "postgres")
		// No explicit Live.withX builder value passed → the config-applied env
		// value is the builder layer and wins over the (now-cleared) seed.
		got := firstNonEmpty(configLayers(suffix, ""))
		if got != "postgres" {
			t.Fatalf("a config-applied value must beat a seed; got %q", got)
		}
	})

	t.Run("operator_still_beats_config_applied", func(t *testing.T) {
		resetEnvFor(t, suffix)
		applyConfigValueFor(suffix, "postgres") // config-applied (builder layer)
		setOperator(suffix, "redis")            // operator override
		// setOperator cleared the config-applied mark (an explicit write is not a
		// config-applied value), so this is now a plain operator value.
		got := firstNonEmpty(configLayers(suffix, ""))
		if got != "redis" {
			t.Fatalf("an operator override must beat a config-applied value; got %q", got)
		}
	})

	t.Run("explicit_live_builder_beats_config_applied_same_layer", func(t *testing.T) {
		resetEnvFor(t, suffix)
		applyConfigValueFor(suffix, "postgres") // config-applied (builder layer)
		// A Live.withStore("memory") passes its value as builderVal; being the
		// more-specific app-shape builder, it wins the shared builder layer.
		got := firstNonEmpty(configLayers(suffix, "memory"))
		if got != "memory" {
			t.Fatalf("an explicit Live.withX must win the shared builder layer; got %q", got)
		}
	})
}

// applyConfigValueFor is a tiny test shim that applies a config value for a
// suffix through the real seed-aware path.
func applyConfigValueFor(suffix, value string) { applyConfigValue(skyEnvName(suffix), value) }

// The behavioural oracle: an app under a legacy `sky.toml [log] format = json`
// seed and the SAME app migrated to `Sky.Config.withLog Json …` must reach
// IDENTICAL effective behaviour — here, the runtime's `logJSON` switch that
// actually branches log formatting (rt.go). This is the real read path, not a
// config-census proxy.
func TestBehaviouralOracleLegacyVsMigratedLogFormat(t *testing.T) {
	const suffix = "LOG_FORMAT"

	effectiveJSONUnderLegacy := func() bool {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "json") // the sky.toml path
		return logJSON
	}
	effectiveJSONUnderMigrated := func() bool {
		resetEnvFor(t, suffix)
		applyWithLogFormat("json") // the withX path
		return logJSON
	}

	legacy := effectiveJSONUnderLegacy()
	migrated := effectiveJSONUnderMigrated()

	if !legacy {
		t.Fatalf("legacy sky.toml [log] format=json did not enable JSON logging (logJSON=false)")
	}
	if legacy != migrated {
		t.Fatalf("effective behaviour DIFFERS between legacy and migrated config: "+
			"logJSON legacy=%v migrated=%v — the migration is not behaviour-preserving",
			legacy, migrated)
	}

	// And the negative: `Text` must turn it OFF on both paths too, so the oracle
	// is not vacuously true (both always JSON).
	resetEnvFor(t, suffix)
	seedLegacy(suffix, "text")
	legacyText := logJSON
	resetEnvFor(t, suffix)
	applyWithLogFormat("text")
	migratedText := logJSON
	if legacyText || migratedText {
		t.Fatalf("format=text must disable JSON on both paths; legacy=%v migrated=%v",
			legacyText, migratedText)
	}
}

// The kernels build the config map the compiler-emitted chain expects.
func TestConfigKernels(t *testing.T) {
	empty, ok := Config_default().(map[string]any)
	if !ok {
		t.Fatalf("Config_default must return a map[string]any, got %T", Config_default())
	}
	if len(empty) != 0 {
		t.Fatalf("Config_default must be empty, got %v", empty)
	}

	cfg := Config_withLog("json", "warn", Config_default())
	m, ok := cfg.(map[string]any)
	if !ok {
		t.Fatalf("Config_withLog must return a map[string]any, got %T", cfg)
	}
	if m["LogFormat"] != "json" || m["LogLevel"] != "warn" {
		t.Fatalf("Config_withLog stored the wrong keys: %v", m)
	}
	// Shallow-clone invariant: the base map must be untouched.
	if len(empty) != 0 {
		t.Fatalf("Config_withLog mutated its base map (must shallow-clone): %v", empty)
	}
}
