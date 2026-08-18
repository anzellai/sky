package rt

// SILENTLY-DEAD-FEATURE regression: telemetry persistence under
// `./app --embed`.
//
// EnablePersistenceFromEnv runs from rt's init() (observability.go)
// and falls back to DATABASE_URL when SKY_CONSOLE_DB_PATH is unset.
// But under --embed, DATABASE_URL is set by startEmbeddedPostgres —
// from main, long AFTER init() — so the boot-time call saw an empty
// environment and telemetry persistence never turned on: documented
// behaviour, silently absent, on exactly the deployment shape (a
// single self-contained binary) most likely to rely on it.
//
// The fix is a re-invocation from the embed DSN handoff, right
// after the env vars are exported. This gate pins the wiring at the
// source level — an integration run needs a live PostgreSQL, and
// what regressed here was the CALL being absent, which is precisely
// what a source assertion can hold in place. The telemetry side's
// behaviour (second call with env now set → persistence active) is
// pinned functionally by
// TestEnablePersistenceFromEnv_HonoursEnvSetAfterBoot in
// rt/telemetry.

import (
	"os"
	"strings"
	"testing"
)

func TestEmbeddedDSNHandoff_ReenablesTelemetryPersistence(t *testing.T) {
	src, err := os.ReadFile("pg_embed.go")
	if err != nil {
		t.Fatalf("read pg_embed.go: %v", err)
	}
	s := string(src)
	fnStart := strings.Index(s, "func startEmbeddedPostgres(")
	if fnStart < 0 {
		t.Fatal("startEmbeddedPostgres not found in pg_embed.go — the embed boot path moved; update this gate to follow it")
	}
	body := s[fnStart:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	setenvIdx := strings.Index(body, `os.Setenv("DATABASE_URL"`)
	if setenvIdx < 0 {
		t.Fatal("the DATABASE_URL handoff is no longer in startEmbeddedPostgres — the embed boot path moved; update this gate to follow it")
	}
	reenableIdx := strings.Index(body, "EnablePersistenceFromEnv")
	if reenableIdx < 0 {
		t.Fatal("startEmbeddedPostgres exports DATABASE_URL but never re-invokes telemetry EnablePersistenceFromEnv — " +
			"the init()-time call ran before this env var existed, so under --embed telemetry persistence is silently dead")
	}
	if reenableIdx < setenvIdx {
		t.Fatal("EnablePersistenceFromEnv is invoked BEFORE the DATABASE_URL export in startEmbeddedPostgres — it must run after, or it sees the same empty environment init() did")
	}
}
