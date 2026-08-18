package rt

// Defect 4 regression — a project with `[env] prefix` could not reach
// `SKY_LIVE_FRAME_ANCESTORS` at all.
//
// `crossOriginIframeMode()` read raw `os.Getenv("SKY_LIVE_FRAME_ANCESTORS")`.
// That switch is what puts `SameSite=None; Secure` on BOTH the session and
// the CSRF cookie, so under a custom prefix cross-origin embedding could not
// be turned on — and nothing reported it. The variable was simply never
// found, the feature stayed off, and that is indistinguishable from "the
// feature does not work".
//
// The identical defect had already been found and fixed for `SKY_ENV`, in
// the same functions; the adjacent switch was left raw.
//
// The COVERAGE half — "no tenth raw read" — is
// `rust/crates/xtask/tests/sky_env_reads_honour_the_prefix.rs`, which
// classifies every `os.Getenv("SKY_…")` in the runtime. This file proves the
// switches actually respond to the prefix.

import (
	"os"
	"testing"
)

// withPrefix sets the runtime env-prefix for the duration of the returned
// closure and restores it. SetEnvPrefix mutates package state, so the
// restore is not optional.
func withPrefix(t *testing.T, prefix string) func() {
	t.Helper()
	prev := EnvPrefix()
	SetEnvPrefix(prefix)
	return func() { SetEnvPrefix(prev) }
}

func TestCrossOriginIframeMode_HonoursEnvPrefix(t *testing.T) {
	restorePrefix := withPrefix(t, "FENCE")
	defer restorePrefix()

	// The default-namespaced name must NOT be what a prefixed project reads.
	if err := os.Setenv("SKY_LIVE_FRAME_ANCESTORS", "https://plane.example"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	defer os.Unsetenv("SKY_LIVE_FRAME_ANCESTORS")
	if crossOriginIframeMode() {
		t.Fatal("under prefix FENCE the runtime must not read SKY_LIVE_FRAME_ANCESTORS")
	}

	if err := os.Setenv("FENCE_LIVE_FRAME_ANCESTORS", "https://plane.example"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	defer os.Unsetenv("FENCE_LIVE_FRAME_ANCESTORS")
	if !crossOriginIframeMode() {
		t.Fatal("FENCE_LIVE_FRAME_ANCESTORS is unreachable: a project with " +
			"`[env] prefix = \"FENCE\"` cannot enable cross-origin embedding at all")
	}
}

func TestProductionGate_HonoursEnvPrefix(t *testing.T) {
	restorePrefix := withPrefix(t, "FENCE")
	defer restorePrefix()
	restoreEnv := withEnvVars(t, "", "") // clear ENV and SKY_ENV
	defer restoreEnv()

	if err := os.Setenv("FENCE_ENV", "production"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	defer os.Unsetenv("FENCE_ENV")

	if !productionFromEnv() {
		t.Fatal("FENCE_ENV=production must gate: `ENV` is documented as prefix-affected")
	}
}

// Bare `ENV` stays unprefixed — it is the name users actually type, and
// docs/sky-toml.md lists it as the first lookup regardless of prefix.
func TestProductionGate_BareEnvStaysUnprefixed(t *testing.T) {
	restorePrefix := withPrefix(t, "FENCE")
	defer restorePrefix()
	restoreEnv := withEnvVars(t, "production", "")
	defer restoreEnv()

	if !productionFromEnv() {
		t.Fatal("plain ENV=production must still gate under a custom prefix")
	}
}

// The other prefix-affected switches fixed in the same pass.
func TestPrefixedNamespacesAreReachable(t *testing.T) {
	restorePrefix := withPrefix(t, "FENCE")
	defer restorePrefix()

	for _, tc := range []struct {
		suffix string
		read   func() string
	}{
		{"LIVE_BASE_PATH", func() string { return skyGetenv("LIVE_BASE_PATH") }},
		{"LIVE_NAMESPACE", func() string { return skyGetenv("LIVE_NAMESPACE") }},
		{"DB_OP", func() string { return skyGetenv("DB_OP") }},
	} {
		name := "FENCE_" + tc.suffix
		if err := os.Setenv(name, "set"); err != nil {
			t.Fatalf("setenv %s: %v", name, err)
		}
		got := tc.read()
		_ = os.Unsetenv(name)
		if got != "set" {
			t.Fatalf("%s is unreachable under prefix FENCE (read %q)", name, got)
		}
	}
}
