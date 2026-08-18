package rt

import (
	"strings"
	"testing"
)

// The runtime half of the legacy→withX migration LIST (design §8.2): the
// running app detects legacy sky.toml values that seeded its environment via
// the `isSeededDefault` provenance mark, and lists them under the startup
// report. These tests prove the detector is SOUND — it fires for a seeded
// default, stays silent for an operator value or an unset suffix, and
// self-extinguishes when a withX overrides the seed.
//
// (Reuses resetEnvFor / seedLegacy / setOperator from sky_config_test.go, same
// package.)

func joined(lines []string) string { return strings.Join(lines, "\n") }

// resetAllMigratable clears every migratable suffix's env + provenance for a
// test and restores it afterwards. `legacyMigrationNotices` reads ALL of
// `configKeyToEnvSuffix` globally, so a test that asserts on its output must
// neutralise every suffix — otherwise a seed left by an earlier test in the
// same (sequential) package run leaks into this one's result.
func resetAllMigratable(t *testing.T) {
	t.Helper()
	for _, suffix := range configKeyToEnvSuffix {
		resetEnvFor(t, suffix)
	}
}

// A seeded sky.toml default is listed, naming its withX builder.
func TestLegacyMigrationNoticeListsSeededDefault(t *testing.T) {
	resetAllMigratable(t)
	seedLegacy("LIVE_STORE", "postgres")

	out := joined(legacyMigrationNotices())
	if !strings.Contains(out, skyEnvName("LIVE_STORE")) {
		t.Fatalf("a seeded LIVE_STORE must be listed; got:\n%s", out)
	}
	if !strings.Contains(out, "Sky.Config.withSessions") {
		t.Fatalf("the notice must name the withX builder; got:\n%s", out)
	}
}

// An operator's own env value is NOT a migration candidate — the operator chose
// it, and it is not a legacy sky.toml seed.
func TestLegacyMigrationNoticeIgnoresOperatorValue(t *testing.T) {
	resetAllMigratable(t)
	setOperator("LIVE_STORE", "redis")

	if n := legacyMigrationNotices(); n != nil {
		t.Fatalf("an operator-set value must not be listed; got:\n%s", joined(n))
	}
}

// A withX override CLEARS the seed (ApplyConfig → clearSeededDefault), so the
// notice self-extinguishes — the property that makes it safe to print.
func TestLegacyMigrationNoticeSelfExtinguishesAfterWithX(t *testing.T) {
	resetAllMigratable(t)
	seedLegacy("LOG_FORMAT", "json")
	// Sanity: seeded → listed.
	if !strings.Contains(joined(legacyMigrationNotices()), skyEnvName("LOG_FORMAT")) {
		t.Fatal("precondition: a seeded LOG_FORMAT should be listed")
	}
	// The app's withX overrides it.
	ApplyConfig(map[string]any{"LogFormat": "text"})
	if n := legacyMigrationNotices(); n != nil {
		t.Fatalf("after a withX override the notice must be silent; got:\n%s", joined(n))
	}
}

// A fully-configured app (nothing seeded) prints nothing — the byte-identical
// startup output the byte-stability constraint requires.
func TestLegacyMigrationNoticeSilentWhenNothingSeeded(t *testing.T) {
	// Defensively clear every migratable suffix for this test.
	for _, suffix := range configKeyToEnvSuffix {
		resetEnvFor(t, suffix)
	}
	if n := legacyMigrationNotices(); n != nil {
		t.Fatalf("no seeded legacy key → silence; got:\n%s", joined(n))
	}
}

// The startup-report gate forbids any added line containing "listening" (the
// substring the supervisor and verify.sh parse for the port). The migration
// block is appended in printStartupReport, so it is not covered by
// TestNoAddedStartupLineLooksLikeAListeningLine — assert it here directly.
func TestLegacyMigrationNoticeHasNoListeningLine(t *testing.T) {
	resetAllMigratable(t)
	seedLegacy("LIVE_STORE", "postgres")
	seedLegacy("JOBS_STORE", "sqlite")
	for _, line := range legacyMigrationNotices() {
		if strings.Contains(strings.ToLower(line), "listening") {
			t.Fatalf("a migration line must not contain \"listening\": %q", line)
		}
	}
}

// Every configKeyToEnvSuffix key has a builder label — a suffix with no builder
// would print a half-line, and the runtime skips it, so the LIST would silently
// drop a real legacy key. This is the runtime-local half of the cross-language
// coverage the `config-migration` xtask gate enforces end to end.
func TestConfigKeyToBuilderCoversEverySuffix(t *testing.T) {
	for key := range configKeyToEnvSuffix {
		if configKeyToBuilder[key] == "" {
			t.Errorf("configKeyToEnvSuffix has %q but configKeyToBuilder does not — its legacy key would be dropped from the migration list", key)
		}
	}
	for key := range configKeyToBuilder {
		if _, ok := configKeyToEnvSuffix[key]; !ok {
			t.Errorf("configKeyToBuilder has %q with no configKeyToEnvSuffix entry — a builder label for a suffix that is not seed-detectable", key)
		}
	}
}
