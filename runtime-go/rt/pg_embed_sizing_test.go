package rt

// Gates for the embedded cluster's connection sizing and for the managed
// conf block being re-rendered per boot.
//
// These are written as PROPERTIES over a range of machines, not as assertions
// about particular numbers. A test that pinned "52" would pass while the
// cluster strangled the app at 8 cores — that is precisely what the previous
// arrangement did — and would then have to be edited every time a clamp
// moved, which is how a gate stops meaning anything.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// coreCountsToCheck is the realistic range. 1 covers the smallest container
// anyone runs; 64 is past the point where every clamp in the pool sizing has
// saturated, so nothing new happens above it.
func coreCountsToCheck() []int {
	var out []int
	for c := 1; c <= 64; c++ {
		out = append(out, c)
	}
	return out
}

// confMaxConnections pulls the effective `max_connections` out of a rendered
// block, reading the FILE rather than re-calling the function that wrote it —
// otherwise the gate only proves the function agrees with itself.
func confMaxConnections(t *testing.T, conf string) int {
	t.Helper()
	got := -1
	for _, line := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "max_connections") {
			continue
		}
		v := strings.TrimSpace(strings.SplitN(t, "=", 2)[1])
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		got = n // last occurrence wins, as PostgreSQL does
	}
	if got < 0 {
		t.Fatalf("no max_connections in the rendered conf:\n%s", conf)
	}
	return got
}

// TestEmbeddedClusterGrantsEveryPoolThisProcessOpens is THE sizing property.
//
// For every core count in a realistic range, the connections one app process
// can demand — ALL of its pools, not just the app's — plus the slots
// PostgreSQL reserves for superusers, must fit inside the `max_connections`
// the generated conf asks for. Anything else is an app that exhausts the
// database it just started, with the user having configured nothing to
// deserve it.
func TestEmbeddedClusterGrantsEveryPoolThisProcessOpens(t *testing.T) {
	for _, cpus := range coreCountsToCheck() {
		conf := renderConfBlock(machine{ramBytes: 16 * 1024 * mb, cpus: cpus})
		maxConn := confMaxConnections(t, conf)
		demand := dbProcessConnectionDemand(cpus, false)
		usable := maxConn - pgSuperuserReserved

		if demand > usable {
			t.Errorf("cpus=%2d: the process can demand %d connections but only %d of the "+
				"cluster's %d are usable (%d are reserved for superusers) — "+
				"the app exhausts its own database",
				cpus, demand, usable, maxConn, pgSuperuserReserved)
		}

		// The RESTART-OVERLAP claim, asserted separately because the baseline
		// above is too weak on its own to catch the frame error this whole
		// change exists to fix.
		//
		// Deriving max_connections from the APP POOL alone — the historical
		// mistake — still satisfies the baseline, because the ×2 overlap
		// factor happens to leave enough slack to cover the three aux pools
		// by accident. A mutation that did exactly that passed the baseline
		// and was caught only here. A gate that a known-wrong implementation
		// passes is not a gate.
		overlap := demand * pgRestartOverlapFactor
		if overlap > usable {
			t.Errorf("cpus=%2d: two overlapping processes demand %d connections but only %d "+
				"of the cluster's %d are usable — every restart under load "+
				"(sky watch, a rolling deploy, a supervisor relaunch) is a "+
				"`too many clients` incident",
				cpus, overlap, usable, maxConn)
		}
	}
}

// TestPoolDemandCountsEveryPoolNotJustTheApps pins the frame error itself.
//
// The demand must exceed the app pool alone, because the process opens the
// app's pool AND one per runtime consumer. A future refactor that quietly
// made `dbProcessConnectionDemand` return the app pool's size would satisfy
// the property above (it would just size the server smaller to match) and
// reintroduce the original bug; this is what stops that.
func TestPoolDemandCountsEveryPoolNotJustTheApps(t *testing.T) {
	if len(dbAuxPoolConsumers) == 0 {
		t.Fatal("dbAuxPoolConsumers is empty — the runtime opens pools that nothing accounts for")
	}
	for _, cpus := range []int{1, 2, 4, 6, 8, 16, 64} {
		app := defaultPostgresPoolConfigFor(cpus, false).MaxOpenConns
		aux := dbAuxPoolSizeFor(cpus, false)
		demand := dbProcessConnectionDemand(cpus, false)
		want := app + aux*len(dbAuxPoolConsumers)
		if demand != want {
			t.Errorf("cpus=%d: demand = %d, want %d (app %d + %d aux pools of %d)",
				cpus, demand, want, app, len(dbAuxPoolConsumers), aux)
		}
		if demand <= app {
			t.Errorf("cpus=%d: demand (%d) does not exceed the app pool (%d) — "+
				"the runtime's own pools are not being counted", cpus, demand, app)
		}
	}
}

// historicalMaxConnections is the formula this branch shipped before, kept
// verbatim so the gate above can be shown to CATCH the defect rather than
// merely to pass today.
//
//	maxConn := clampInt(4*cpus+20, 25, 200)
//
// with a comment claiming that "4×CPU+20 keeps the app's ceiling below the
// server's". It reasoned about the app's pool; the process opens four.
func historicalMaxConnections(cpus int) int { return clampInt(4*cpus+20, 25, 200) }

// TestTheHistoricalSizingViolatesTheProperty is the gate's own falsification,
// recorded permanently instead of only in a commit message.
//
// If this ever stops failing for 6–9 cores, the property gate above has been
// weakened to the point where it would no longer have caught the bug it was
// written for.
func TestTheHistoricalSizingViolatesTheProperty(t *testing.T) {
	var broken []int
	for _, cpus := range coreCountsToCheck() {
		usable := historicalMaxConnections(cpus) - pgSuperuserReserved
		if dbProcessConnectionDemand(cpus, false) > usable {
			broken = append(broken, cpus)
		}
	}
	if len(broken) == 0 {
		t.Fatal("the historical `4*cpus+20` sizing now satisfies the demand property — " +
			"either the pool sizing changed or the property has been weakened until it " +
			"no longer detects the defect it exists for")
	}
	for _, want := range []int{6, 7, 8, 9} {
		found := false
		for _, c := range broken {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the historical sizing was known to under-provision at %d cores, "+
				"but this gate no longer detects it there", want)
		}
	}
	t.Logf("historical `4*cpus+20` under-provisions at core counts %v; the current sizing does not", broken)
}

// TestTheBackgroundWritersCannotStarveTheSessionStore is the bulkhead
// property that pool SHARING must not cost.
//
// Sharing one *sql.DB removes the isolation four separate pools provided. The
// caps in db_pool.go are what put it back: analytics and telemetry are capped,
// the session store is not, so however hard the two background writers work,
// the request path keeps the rest of the pool.
func TestTheBackgroundWritersCannotStarveTheSessionStore(t *testing.T) {
	for _, cpus := range coreCountsToCheck() {
		owned := dbAuxPoolSizeFor(cpus, false) // what it had with a pool to itself
		shared := dbSharedAuxPoolSizeFor(cpus, false)
		guaranteed := dbGuaranteedSessionShare(shared)
		if guaranteed < owned {
			t.Errorf("cpus=%d: sharing left the session store %d guaranteed connections "+
				"out of a pool of %d, where owning a pool outright gave it %d — "+
				"sharing must not cost the request path anything",
				cpus, guaranteed, shared, owned)
		}
		// And sharing must actually reduce the process's footprint, or there
		// is no reason to give up separate pools at all.
		unshared := owned * len(dbAuxPoolConsumers)
		if shared > unshared {
			t.Errorf("cpus=%d: the shared pool (%d) is larger than the %d separate pools "+
				"it replaces (%d) — sharing is costing connections, not saving them",
				cpus, shared, len(dbAuxPoolConsumers), unshared)
		}
	}
	if dbSessionShare != 0 {
		t.Errorf("dbSessionShare = %d, want 0 (uncapped) — capping the request path buys "+
			"nothing; capping the background writers is what protects it", dbSessionShare)
	}
}

// ── the managed conf block is re-rendered per boot ──────────────────

// TestTheTunedConfFollowsTheMachineAcrossARestart is the resize round-trip.
//
// Initialise as if on a small machine, restart presenting a larger one, and
// the rendered `max_connections` must MOVE and still cover demand for the
// larger machine — then the reverse. Frozen-at-initdb sizing passes neither
// direction.
func TestTheTunedConfFollowsTheMachineAcrossARestart(t *testing.T) {
	small := machine{ramBytes: 2 * 1024 * mb, cpus: 2}
	large := machine{ramBytes: 32 * 1024 * mb, cpus: 16}

	// First boot on the small machine.
	conf, changed := ensureSkyConf("# stock postgresql.conf\n", renderConfBlock(small))
	if !changed {
		t.Fatal("the first render changed nothing")
	}
	smallMax := confMaxConnections(t, conf)

	// Same machine again: idempotent, nothing rewritten.
	if _, changed := ensureSkyConf(conf, renderConfBlock(small)); changed {
		t.Error("re-rendering for an unchanged machine rewrote the file — not idempotent")
	}

	// Resized up.
	grown, changed := ensureSkyConf(conf, renderConfBlock(large))
	if !changed {
		t.Fatal("the host grew from 2 to 16 cores and the managed block did not change — " +
			"the conf is frozen at initdb while the pools track the machine")
	}
	largeMax := confMaxConnections(t, grown)
	if largeMax <= smallMax {
		t.Errorf("max_connections went %d → %d when the host grew 2 → 16 cores", smallMax, largeMax)
	}
	if demand := dbProcessConnectionDemand(large.cpus, false); demand > largeMax-pgSuperuserReserved {
		t.Errorf("after the resize the process demands %d connections and only %d of %d are usable",
			demand, largeMax-pgSuperuserReserved, largeMax)
	}

	// And back down.
	shrunk, changed := ensureSkyConf(grown, renderConfBlock(small))
	if !changed {
		t.Fatal("the host shrank 16 → 2 cores and the managed block did not change")
	}
	if got := confMaxConnections(t, shrunk); got != smallMax {
		t.Errorf("shrinking back to the original machine gave max_connections %d, want %d",
			got, smallMax)
	}
	// Exactly one managed block, however many times it has been re-rendered.
	if n := strings.Count(shrunk, skyConfMarker); n != 1 {
		t.Errorf("the conf holds %d managed blocks after three renders, want 1", n)
	}
}

// TestOperatorSettingsOutsideTheManagedBlockSurviveARetune — re-rendering
// must not eat what the operator wrote, in either the delimited or the
// legacy un-delimited form.
func TestOperatorSettingsOutsideTheManagedBlockSurviveARetune(t *testing.T) {
	const mine = "log_min_duration_statement = 250  # mine, not sky's"

	t.Run("delimited block", func(t *testing.T) {
		base, _ := ensureSkyConf("# stock\n", renderConfBlock(machine{ramBytes: 2 * 1024 * mb, cpus: 2}))
		withMine := base + mine + "\n"
		out, _ := ensureSkyConf(withMine, renderConfBlock(machine{ramBytes: 32 * 1024 * mb, cpus: 16}))
		if !strings.Contains(out, mine) {
			t.Fatalf("the retune deleted the operator's own setting:\n%s", out)
		}
		if !strings.Contains(out, "# stock") {
			t.Error("the retune deleted the stock conf above the block")
		}
	})

	t.Run("legacy block with no end marker", func(t *testing.T) {
		// What clusters initialised before the end marker existed look like:
		// the marker, the managed keys, and then whatever the operator added.
		legacy := "# stock\n\n" + skyConfMarker + "\n" +
			"# Sized for 2.0GB of RAM and 2 CPUs.\n" +
			"shared_buffers = 307MB  # 15% of RAM\n" +
			"max_connections = 28  # the old formula\n" +
			mine + "\n"
		out, changed := ensureSkyConf(legacy, renderConfBlock(machine{ramBytes: 32 * 1024 * mb, cpus: 16}))
		if !changed {
			t.Fatal("a legacy block was not retuned")
		}
		if !strings.Contains(out, mine) {
			t.Fatalf("retuning a legacy block ate the operator's setting that followed it:\n%s", out)
		}
		if strings.Contains(out, "max_connections = 28") {
			t.Errorf("the stale max_connections survived the retune:\n%s", out)
		}
		if n := strings.Count(out, skyConfMarker); n != 1 {
			t.Errorf("%d managed blocks after retuning a legacy conf, want 1", n)
		}
	})
}

// TestEveryTunedSettingIsAResourceKnob — the standing rule for both the
// embedded and the shared cluster: nothing that changes what a query MEANS,
// so an app behaves the same against this cluster as against a managed one.
//
// `synchronous_commit` is named explicitly because this change set turns it
// off for the analytics and telemetry sinks. That is done per transaction with
// `SET LOCAL`, and it must never migrate into the cluster's own conf, where it
// would silently weaken durability for the app's data too.
func TestEveryTunedSettingIsAResourceKnob(t *testing.T) {
	forbidden := []string{
		"fsync", "synchronous_commit", "wal_level", "full_page_writes",
		"default_transaction_isolation", "data_checksums", "zero_damaged_pages",
	}
	conf := renderConfBlock(machine{ramBytes: 16 * 1024 * mb, cpus: 8})
	for _, key := range forbidden {
		for _, line := range strings.Split(conf, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, key) && strings.Contains(trimmed, "=") {
				t.Errorf("the generated conf sets %q, which changes what a query means "+
					"or how durable it is:\n  %s", key, trimmed)
			}
		}
	}
	_ = fmt.Sprint()
}
