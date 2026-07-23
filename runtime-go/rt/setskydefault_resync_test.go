package rt

import (
	"os"
	"testing"
)

// SetSkyDefault must re-fire the env-prefix refresh hooks so env-derived state
// captured at package-init (logJSON / logThreshold) picks up a sky.toml default.
// The app's generated init() runs SetSkyDefault AFTER rt's package vars were
// evaluated, so without the re-sync a `[log] format = "json"` seed was ignored.
func TestSetSkyDefaultResyncsLogConfig(t *testing.T) {
	// Save + restore the globals + env this test perturbs.
	origJSON, origThreshold := logJSON, logThreshold
	origEnv, hadEnv := os.LookupEnv("SKY_LOG_FORMAT")
	origLvl, hadLvl := os.LookupEnv("SKY_LOG_LEVEL")
	t.Cleanup(func() {
		logJSON, logThreshold = origJSON, origThreshold
		if hadEnv {
			os.Setenv("SKY_LOG_FORMAT", origEnv)
		} else {
			os.Unsetenv("SKY_LOG_FORMAT")
		}
		if hadLvl {
			os.Setenv("SKY_LOG_LEVEL", origLvl)
		} else {
			os.Unsetenv("SKY_LOG_LEVEL")
		}
	})

	// Simulate the pre-init state: unset env, vars captured as plain/info.
	os.Unsetenv("SKY_LOG_FORMAT")
	os.Unsetenv("SKY_LOG_LEVEL")
	logJSON = false
	logThreshold = logLevelInfo

	// The generated init() seeds the sky.toml defaults via SetSkyDefault.
	SetSkyDefault("LOG_FORMAT", "json")
	SetSkyDefault("LOG_LEVEL", "debug")

	if !logJSON {
		t.Fatalf("SetSkyDefault(LOG_FORMAT, json) did not re-sync logJSON (still plain)")
	}
	if logThreshold != logLevelDebug {
		t.Fatalf("SetSkyDefault(LOG_LEVEL, debug) did not re-sync logThreshold, got %d", logThreshold)
	}
}
