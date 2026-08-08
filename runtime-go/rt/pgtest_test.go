package rt

// pgtest_test.go — the SINGLE gate every real-Postgres test in package `rt`
// goes through.
//
// Why this file exists (the bug it closes). Two Postgres-backed suites grew
// independently and each invented its OWN env-var name for "the DSN of a test
// Postgres":
//
//   • rt/live_store_postgres_test.go  → SKY_TEST_POSTGRES_DSN  (what CI sets)
//   • rt/persist_writeskew_test.go    → SKY_TEST_PG_URL / DATABASE_URL
//
// CI supplies only SKY_TEST_POSTGRES_DSN, so TestWriteSkewPostgres — the ONLY
// cross-backend test that DISCRIMINATES serializable isolation from READ
// COMMITTED — silently `t.Skip`ped on every CI run since it was written. A
// skipped test is indistinguishable from a passing one in the job summary, so
// the gap was invisible.
//
// Two mechanisms make that class of bug impossible to repeat:
//
//  1. ONE canonical name, resolved in ONE place. `SKY_TEST_POSTGRES_DSN` is the
//     only accepted variable. Any other spelling that was ever in use is listed
//     in legacyPostgresDSNVars and, if set while the canonical one is not,
//     FAILS the test loudly instead of skipping — so a half-finished rename
//     cannot degrade back into a silent skip.
//
//  2. `SKY_TEST_REQUIRE_POSTGRES=1` turns the skip into a hard failure. CI sets
//     it on the job that owns a live `postgres:16` service container, which
//     asserts "these tests RAN" rather than "these tests did not fail".
//
// DATABASE_URL is deliberately NOT accepted. It is a PRODUCT variable — a
// developer's shell frequently points it at a real application database — and
// these tests are destructive (`DROP TABLE IF EXISTS doctors`). Opting a real
// database into a destructive test suite via an ambient product variable is a
// footgun, not a convenience.
//
// Local use:
//
//	SKY_TEST_POSTGRES_DSN='postgres://sky:sky@localhost:5432/sky?sslmode=disable' \
//	  go test ./rt/ -run Postgres -v

import (
	"os"
	"testing"
)

// skyTestPostgresDSNVar is the ONE accepted env var. Referenced by name in
// .github/workflows/rust-ci.yml (integration-postgres job).
const skyTestPostgresDSNVar = "SKY_TEST_POSTGRES_DSN"

// skyTestRequirePostgresVar, when "1", converts "no DSN → skip" into a hard
// failure. CI sets it wherever a Postgres service container is guaranteed.
const skyTestRequirePostgresVar = "SKY_TEST_REQUIRE_POSTGRES"

// legacyPostgresDSNVars are spellings that were once used by an individual test
// file. They are NOT honoured — they are TRIPWIRES: setting one without the
// canonical variable fails loudly, which is what a silent skip should have done.
var legacyPostgresDSNVars = []string{
	"SKY_TEST_PG_URL",
	"SKY_TEST_PG_DSN",
	"SKY_TEST_POSTGRES_URL",
}

// requireTestPostgresDSN returns the DSN of the test Postgres, or skips the
// calling test. See the file comment for the two anti-silent-skip mechanisms.
func requireTestPostgresDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv(skyTestPostgresDSNVar)
	if dsn != "" {
		return dsn
	}

	// Tripwire: a legacy spelling is set but the canonical one is not. That is
	// exactly the half-renamed state that hid TestWriteSkewPostgres from CI.
	for _, legacy := range legacyPostgresDSNVars {
		if os.Getenv(legacy) != "" {
			t.Fatalf("%s is set but %s is not — %s is the only accepted name; "+
				"export %s instead (see rt/pgtest_test.go)",
				legacy, skyTestPostgresDSNVar, skyTestPostgresDSNVar, skyTestPostgresDSNVar)
		}
	}

	if os.Getenv(skyTestRequirePostgresVar) == "1" {
		t.Fatalf("%s=1 but %s is unset — this job MUST run the real-Postgres "+
			"tests; a skip here is the failure mode it exists to catch",
			skyTestRequirePostgresVar, skyTestPostgresDSNVar)
	}

	t.Skipf("%s unset — real-Postgres test skipped (set it to run; "+
		"%s=1 makes a skip a failure)", skyTestPostgresDSNVar, skyTestRequirePostgresVar)
	return ""
}
