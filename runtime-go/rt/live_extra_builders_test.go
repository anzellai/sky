package rt

// Precedence + behaviour gates for the two new Std.Live app-shape builders,
// Live.withInput and Live.withMaxBodyBytes.
//
// resolveInputMode / resolveMaxBodyBytes are the functions liveAppRun uses to
// populate app.inputMode / app.maxBodyBytes. Before these builders their
// consumers read skyGetenv directly, so a seeded `[live] input` /
// `[live] maxBodyBytes` default was read straight and NO builder could win —
// the same dead-builder shape Live.withTtl had before stage 3. Routing through
// configLayers gives the builders a live layer, proven here by "builder beats a
// seed". The env-reset / seed / operator helpers live in sky_config_test.go.

import "testing"

func TestLiveInputBuilder(t *testing.T) {
	const suffix = "LIVE_INPUT_MODE"
	t.Run("builder_beats_seed", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "debounce")
		if got := resolveInputMode("blur"); got != "blur" {
			t.Fatalf("Live.withInput must beat a seed (the dead-builder fix); got %q", got)
		}
	})
	t.Run("operator_beats_builder", func(t *testing.T) {
		resetEnvFor(t, suffix)
		setOperator(suffix, "blur")
		if got := resolveInputMode("debounce"); got != "blur" {
			t.Fatalf("an operator override must beat Live.withInput; got %q", got)
		}
	})
	t.Run("builder_beats_fallback", func(t *testing.T) {
		resetEnvFor(t, suffix)
		if got := resolveInputMode("blur"); got != "blur" {
			t.Fatalf("Live.withInput must beat the fallback; got %q", got)
		}
	})
	t.Run("unrecognised_winner_falls_to_default", func(t *testing.T) {
		resetEnvFor(t, suffix)
		if got := resolveInputMode("bogus"); got != "debounce" {
			t.Fatalf("an unrecognised winner must fall back to debounce; got %q", got)
		}
	})
	t.Run("unset_is_default", func(t *testing.T) {
		resetEnvFor(t, suffix)
		if got := resolveInputMode(""); got != "debounce" {
			t.Fatalf("unset must be debounce; got %q", got)
		}
	})
}

func TestLiveMaxBodyBytesBuilder(t *testing.T) {
	const suffix = "LIVE_MAX_BODY_BYTES"
	const def = int64(5 << 20)
	t.Run("builder_beats_seed", func(t *testing.T) {
		resetEnvFor(t, suffix)
		seedLegacy(suffix, "1000")
		if got := resolveMaxBodyBytes("2000", def); got != 2000 {
			t.Fatalf("Live.withMaxBodyBytes must beat a seed (the dead-builder fix); got %d", got)
		}
	})
	t.Run("operator_beats_builder", func(t *testing.T) {
		resetEnvFor(t, suffix)
		setOperator(suffix, "3000")
		if got := resolveMaxBodyBytes("2000", def); got != 3000 {
			t.Fatalf("an operator override must beat Live.withMaxBodyBytes; got %d", got)
		}
	})
	t.Run("builder_beats_fallback", func(t *testing.T) {
		resetEnvFor(t, suffix)
		if got := resolveMaxBodyBytes("2000", def); got != 2000 {
			t.Fatalf("Live.withMaxBodyBytes must beat the fallback; got %d", got)
		}
	})
	t.Run("unset_is_default", func(t *testing.T) {
		resetEnvFor(t, suffix)
		if got := resolveMaxBodyBytes("", def); got != def {
			t.Fatalf("unset must be the 5 MiB default; got %d", got)
		}
	})
	t.Run("unparseable_winner_falls_through", func(t *testing.T) {
		resetEnvFor(t, suffix)
		// A non-positive / garbage builder value falls through to the default.
		if got := resolveMaxBodyBytes("nonsense", def); got != def {
			t.Fatalf("an unparseable value must fall through to the default; got %d", got)
		}
	})
}
