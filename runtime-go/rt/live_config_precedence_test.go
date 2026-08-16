package rt

// Regression gate: the FOUR Sky.Live settings the config matrix covers resolve
// through ONE precedence rule, and that rule is:
//
//	operator env  >  builder (withX)  >  seeded default (sky.toml / compiler)  >  fallback
//
// This file is the unit-level half of the proof; `xtask config-matrix` is the
// end-to-end half, observing the same rule from a running binary's own startup
// output. Both are needed: the matrix cannot see `live.store` (no fixture can
// select three distinct store kinds without a live server), and this file
// cannot see the emitted prologue.
//
// ## Why the rule needs a gate at all
//
// Before this gate the four settings had THREE different orders, in one
// module, measured rather than reasoned (docs/coverage/config-matrix.json at
// c360e11d):
//
//	live.port       env -> builder -> toml -> 8000     (correct)
//	live.storePath  BUILDER -> env -> toml             (inverted)
//	live.ttl        env -> toml -> BUILDER NEVER WINS   (builder dead)
//	live.idleEvict  env -> builder                      (correct by luck)
//
// Nobody chose that. It accumulated because the ordering lived in Go, spread
// across `resolveLivePort`, `selectStore` and `parseTTL`, and nothing forced
// those three to agree. The fix is not three fixes: it is `configLayers`, one
// function they all call, so "they agree" is a property of the source rather
// than of somebody remembering.
//
// ## The discriminator
//
// `<PREFIX>_LIVE_TTL` set by an operator and `<PREFIX>_LIVE_TTL` seeded by the
// generated prologue are the same variable and look identical in os.Environ().
// `SetEnvDefault` records which it seeded (dotenv.go:34-66) and `setEnvRaw`
// clears the mark, so a `.env` value counts as operator-set and a sky.toml
// value does not. `resolveLivePort` was the only consumer of that provenance;
// now all four are.
//
// ## live.idleEvict is here even though it already passed
//
// `live.idleEvict` observed env -> builder before this change and observes it
// after. It was correct BY LUCK: nothing seeds `LIVE_IDLE_EVICT`, so the
// env-first `parseIdleEvict` never had a seeded default to lose to. The
// `SeededDefaultLosesToBuilder` case below seeds one explicitly, which is the
// only way to tell "correct" from "never exercised" — and before this change
// it fails.

import (
	"testing"
	"time"
)

// withCleanEnv isolates a case: the named suffixes unset, no seeding recorded,
// restored afterwards. The seeded-default mark is process-global, so a case
// that left one set would silently decide the next case's verdict.
func withCleanEnv(t *testing.T, suffixes ...string) {
	t.Helper()
	for _, sfx := range suffixes {
		name := skyEnvName(sfx)
		prev, had := lookupEnvRaw(name)
		clearSeededDefault(name)
		unsetEnvRaw(name)
		t.Cleanup(func() {
			clearSeededDefault(name)
			if had {
				setEnvRaw(name, prev)
			} else {
				unsetEnvRaw(name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// live.ttl — THE case. `lower.rs:822` emits
// `rt.SetSkyDefault("LIVE_TTL", "1800")` unconditionally into every program,
// so the variable the builder loses to is ALWAYS set.
// ---------------------------------------------------------------------------

func TestTTL_BuilderBeatsCompilerSeededDefault(t *testing.T) {
	withCleanEnv(t, "LIVE_TTL")
	SetSkyDefault("LIVE_TTL", "1800") // what every generated init() does
	if got := resolveTTL("41m", 30*time.Minute); got != 41*time.Minute {
		t.Fatalf("Live.withTtl \"41m\" against the unconditional 1800s seed: got %s want 41m0s "+
			"— the compiler's own default beat the explicit call, so withTtl is dead", got)
	}
}

func TestTTL_BuilderBeatsSkyTomlSeededDefault(t *testing.T) {
	withCleanEnv(t, "LIVE_TTL")
	SetSkyDefault("LIVE_TTL", "38m") // `[live] ttl = "38m"` in sky.toml
	if got := resolveTTL("41m", 30*time.Minute); got != 41*time.Minute {
		t.Fatalf("Live.withTtl \"41m\" against sky.toml ttl=38m: got %s want 41m0s "+
			"— code the developer wrote must beat the manifest default", got)
	}
}

// The other half, so the fix cannot be "the builder always wins": an operator
// who exported the variable still beats a builder compiled into the binary.
func TestTTL_OperatorEnvBeatsBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_TTL")
	setEnvRaw(skyEnvName("LIVE_TTL"), "37m")
	SetSkyDefault("LIVE_TTL", "1800") // set-if-unset: must not disturb or re-mark
	if got := resolveTTL("41m", 30*time.Minute); got != 37*time.Minute {
		t.Fatalf("SKY_LIVE_TTL=37m with withTtl \"41m\": got %s want 37m0s "+
			"— an operator must be able to override a compiled-in builder call", got)
	}
}

// No builder: the seeded default is exactly what should apply. This is the
// "preserve the effective value" half of the rule — an app that never called
// the builder must observe what it observes today.
func TestTTL_SeededDefaultAppliesWithoutBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_TTL")
	SetSkyDefault("LIVE_TTL", "1800")
	if got := resolveTTL("", 30*time.Minute); got != 30*time.Minute {
		t.Fatalf("no withTtl, 1800s seed: got %s want 30m0s", got)
	}
}

// Layer fall-through survives. An unparseable value at one layer must fall to
// the NEXT layer rather than collapsing to the hardcoded fallback — the
// behaviour `parseTTL` already had, which a single-winner resolver would lose.
func TestTTL_UnparseableOperatorEnvFallsThroughToBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_TTL")
	setEnvRaw(skyEnvName("LIVE_TTL"), "not-a-duration")
	if got := resolveTTL("41m", 30*time.Minute); got != 41*time.Minute {
		t.Fatalf("unparseable SKY_LIVE_TTL with withTtl \"41m\": got %s want 41m0s", got)
	}
}

// ---------------------------------------------------------------------------
// live.storePath — the exact inverse of live.ttl, one module away.
//
// `selectStore`'s `if path == "" { path = skyGetenv(...) }` shape consults the
// builder FIRST, so an operator's `SKY_LIVE_STORE_PATH` is silently ignored by
// any app that called `withStorePath`. That is the same defect as the dead
// `withTtl` pointed the other way, and both the Sky-side docstring
// (Std/Live.sky:167-168) and live.go's own comment already claim env wins —
// so this gate makes the code match documentation that was already written.
// ---------------------------------------------------------------------------

func TestStorePath_OperatorEnvBeatsBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_STORE_PATH")
	setEnvRaw(skyEnvName("LIVE_STORE_PATH"), "cfgmx_env.db")
	if got := resolveStorePath("cfgmx_builder.db"); got != "cfgmx_env.db" {
		t.Fatalf("SKY_LIVE_STORE_PATH=cfgmx_env.db with withStorePath \"cfgmx_builder.db\": "+
			"got %q want \"cfgmx_env.db\" — an operator pointing the store at a mounted "+
			"volume must not be silently overridden by a compiled-in path", got)
	}
}

func TestStorePath_BuilderBeatsSeededDefault(t *testing.T) {
	withCleanEnv(t, "LIVE_STORE_PATH")
	SetSkyDefault("LIVE_STORE_PATH", "cfgmx_toml.db")
	if got := resolveStorePath("cfgmx_builder.db"); got != "cfgmx_builder.db" {
		t.Fatalf("withStorePath against sky.toml storePath: got %q want \"cfgmx_builder.db\"", got)
	}
}

func TestStorePath_SeededDefaultAppliesWithoutBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_STORE_PATH")
	SetSkyDefault("LIVE_STORE_PATH", "cfgmx_toml.db")
	if got := resolveStorePath(""); got != "cfgmx_toml.db" {
		t.Fatalf("no withStorePath, sky.toml storePath: got %q want \"cfgmx_toml.db\"", got)
	}
}

// ---------------------------------------------------------------------------
// live.store — the OTHER branch of the same two lines in `selectStore`.
//
// The config matrix is structurally blind to this one: a cell needs three
// pairwise-distinct arm values and only `memory` and `sqlite` can be selected
// without a live server, so it sits in the matrix's [[deferred]] bucket. This
// test is therefore the ONLY gate on its precedence — which is exactly why
// fixing `storePath` and leaving `store` on the old order would have rebuilt
// the defect in the line below the one being fixed.
// ---------------------------------------------------------------------------

func TestStoreKind_OperatorEnvBeatsBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_STORE")
	setEnvRaw(skyEnvName("LIVE_STORE"), "sqlite")
	if got := resolveStoreKind("memory"); got != "sqlite" {
		t.Fatalf("SKY_LIVE_STORE=sqlite with withStore \"memory\": got %q want \"sqlite\"", got)
	}
}

func TestStoreKind_BuilderBeatsSeededDefault(t *testing.T) {
	withCleanEnv(t, "LIVE_STORE")
	SetSkyDefault("LIVE_STORE", "memory")
	if got := resolveStoreKind("sqlite"); got != "sqlite" {
		t.Fatalf("withStore \"sqlite\" against sky.toml store=memory: got %q want \"sqlite\"", got)
	}
}

// ---------------------------------------------------------------------------
// live.idleEvict — the control. Observed correct BEFORE this change because
// nothing seeds `LIVE_IDLE_EVICT`; the seeded case below is what tells
// "correct" from "never exercised".
// ---------------------------------------------------------------------------

func TestIdleEvict_SeededDefaultLosesToBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_IDLE_EVICT")
	SetSkyDefault("LIVE_IDLE_EVICT", "6m")
	if got := resolveIdleEvict("11m", defaultIdleEvict); got != 11*time.Minute {
		t.Fatalf("Live.withIdleEvict \"11m\" against a seeded 6m: got %s want 11m0s "+
			"— idleEvict is env-first like ttl and only escapes the same defect "+
			"because nothing seeds it today", got)
	}
}

func TestIdleEvict_OperatorEnvBeatsBuilder(t *testing.T) {
	withCleanEnv(t, "LIVE_IDLE_EVICT")
	setEnvRaw(skyEnvName("LIVE_IDLE_EVICT"), "6m")
	if got := resolveIdleEvict("11m", defaultIdleEvict); got != 6*time.Minute {
		t.Fatalf("SKY_LIVE_IDLE_EVICT=6m with withIdleEvict \"11m\": got %s want 6m0s", got)
	}
}

// The explicit-off escape hatch must survive the reordering: an operator
// turning the tiered cache off with `=0` still turns it off.
func TestIdleEvict_OperatorCanStillDisable(t *testing.T) {
	withCleanEnv(t, "LIVE_IDLE_EVICT")
	setEnvRaw(skyEnvName("LIVE_IDLE_EVICT"), "0")
	if got := resolveIdleEvict("11m", defaultIdleEvict); got != 0 {
		t.Fatalf("SKY_LIVE_IDLE_EVICT=0 with withIdleEvict \"11m\": got %s want 0s", got)
	}
}

// ---------------------------------------------------------------------------
// The uniformity claim itself.
//
// The three cases above prove each setting obeys the rule. This one proves
// they obey it for the SAME reason — that the agreement is structural. If a
// later change re-implements one setting's order by hand, the settings can
// still each pass their own case while diverging again; this fails instead.
// ---------------------------------------------------------------------------

func TestAllFourSettingsShareOneResolver(t *testing.T) {
	suffixes := []string{"LIVE_PORT", "LIVE_TTL", "LIVE_STORE_PATH", "LIVE_IDLE_EVICT"}
	withCleanEnv(t, suffixes...)
	for _, sfx := range suffixes {
		// operator env, builder, seeded default — all three present at once,
		// which is the only arrangement that can distinguish all three orders.
		setEnvRaw(skyEnvName(sfx), "operator")
		SetSkyDefault(sfx, "seeded") // set-if-unset, so this is a no-op here
		got := configLayers(sfx, "builder")
		want := []string{"operator", "builder"}
		if len(got) != len(want) {
			t.Fatalf("%s: configLayers gave %q, want %q", sfx, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: configLayers gave %q, want %q", sfx, got, want)
			}
		}
		unsetEnvRaw(skyEnvName(sfx))
		clearSeededDefault(skyEnvName(sfx))

		// Now with the env value SEEDED rather than operator-set, the builder
		// must move ahead of it.
		SetSkyDefault(sfx, "seeded")
		got = configLayers(sfx, "builder")
		want = []string{"builder", "seeded"}
		if len(got) != len(want) {
			t.Fatalf("%s (seeded): configLayers gave %q, want %q", sfx, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s (seeded): configLayers gave %q, want %q", sfx, got, want)
			}
		}
		unsetEnvRaw(skyEnvName(sfx))
		clearSeededDefault(skyEnvName(sfx))
	}
}
