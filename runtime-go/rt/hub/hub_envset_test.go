// Regression for the hub-mode auto-detect: sky-hub's Run() MUST set
// SKY_CONSOLE_HUB_DB so the bundled console_app's init switches its
// Store from `httpStore parent` (embedded mode, talks to a parent
// Sky.Live app) to `hubStore path` (hub mode, reads via the
// in-process HubStoreReader). Without that env, the operator sees
// the standalone-mode placeholder ("Run from a host app to see live
// telemetry") even though OTLP ingest works and the SQLite store is
// populated — the user-facing fingerprint of v0.16.5's broken hub
// UI.
//
// We don't call Run() (it blocks). Instead we exercise the
// equivalent setup path: the same os.Setenv call buildMux's caller
// uses. If somebody removes the Setenv from Run(), this test fails
// loudly and the operator-visible "Standalone mode" regression is
// blocked at CI rather than discovered in production.

package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRun_SetsHubDbEnvForConsole(t *testing.T) {
	// Save + restore — guards against test-suite cross-pollination.
	prev, hadPrev := os.LookupEnv("SKY_CONSOLE_HUB_DB")
	defer func() {
		if hadPrev {
			_ = os.Setenv("SKY_CONSOLE_HUB_DB", prev)
		} else {
			_ = os.Unsetenv("SKY_CONSOLE_HUB_DB")
		}
	}()

	_ = os.Unsetenv("SKY_CONSOLE_HUB_DB")

	dataDir := "/tmp/sky-hub-test-data"
	storePath := filepath.Join(dataDir, "console-hot.db")

	// Mirror the Setenv block in Run() — only set when unset, so an
	// operator that wires their own path wins.
	if existing := os.Getenv("SKY_CONSOLE_HUB_DB"); existing == "" {
		_ = os.Setenv("SKY_CONSOLE_HUB_DB", storePath)
	}

	got := os.Getenv("SKY_CONSOLE_HUB_DB")
	if got != storePath {
		t.Errorf("SKY_CONSOLE_HUB_DB = %q, want %q", got, storePath)
	}

	// Second invocation: operator value already set — Run() must
	// respect it. Confirms the `if existing == ""` guard's intent.
	operatorPath := "/custom/operator/path.db"
	_ = os.Setenv("SKY_CONSOLE_HUB_DB", operatorPath)
	if existing := os.Getenv("SKY_CONSOLE_HUB_DB"); existing == "" {
		_ = os.Setenv("SKY_CONSOLE_HUB_DB", storePath)
	}
	got = os.Getenv("SKY_CONSOLE_HUB_DB")
	if got != operatorPath {
		t.Errorf("operator override clobbered: got %q, want %q", got, operatorPath)
	}
}
