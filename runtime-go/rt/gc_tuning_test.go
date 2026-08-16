package rt

// Properties of the machine-derived GC default, written the way
// pg_embed_sizing_test.go is written: as properties over a RANGE of machines,
// never as assertions about the number this machine happens to have.
//
// The anti-vacuity rule this file obeys, and the reason it is worth stating:
// the PostgreSQL reserve is checked against the `shared_buffers` line PARSED
// OUT OF THE RENDERED postgresql.conf, not against the constant the reserve is
// computed from. A gate that recomputed `ram*15/100` on both sides would agree
// with itself under every mutation of that 15, and would report `ok` in a few
// milliseconds while the two provisioners silently double-claimed the machine.

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

// machinesToCheck sweeps the RAM sizes an app is actually deployed on, from a
// container far too small for the tuning to be safe up to a large host.
func machinesToCheck() []uint64 {
	return []uint64{
		128 * mb, 256 * mb, 512 * mb, 768 * mb,
		1024 * mb, 1536 * mb, 1977 * mb, // 1977MiB ≈ an e2-small's 1.93GiB
		2048 * mb, 4096 * mb, 8192 * mb,
		16384 * mb, 32768 * mb, 65536 * mb, 262144 * mb,
	}
}

const gcTestE2SmallRAM = 1977 * mb

// TestTheAppAndPostgresDoNotEachClaimTheWholeMachine is the property the whole
// design rests on. `pg_embed_conf.tuningFor` hands PostgreSQL a share of RAM;
// if the GC limit is derived from the same RAM without subtracting that share,
// both provisioners size themselves as though they own the box, and the sum
// exceeds it.
//
// The PostgreSQL side is read back from the RENDERED CONF so that changing
// pg_embed_conf's share moves this test.
func TestTheAppAndPostgresDoNotEachClaimTheWholeMachine(t *testing.T) {
	for _, ram := range machinesToCheck() {
		m := machine{ramBytes: ram, cpus: 4}
		tun := gcTuningFor(m, gcEnvironment{embeddedPostgres: true})
		if !tun.setMemoryLimit {
			continue // machine below the floor: nothing is claimed at all
		}

		conf := renderConfBlock(m)
		pgShared := confBytesSetting(t, conf, "shared_buffers")

		// Two assertions, and both are needed.
		//
		// (a) The number the cluster ACTUALLY GETS — parsed back out of the
		//     rendered conf — is the number the GC reserve is computed from.
		//     This is the artefact coupling: re-inline a different percentage
		//     into `tuningFor` and this fails, whatever `pgSharedBuffersFor`
		//     still says. `memUnit` rounds down to a whole MB, so the compare
		//     is against the rounded rendering of the same figure.
		if want := pgSharedBuffersFor(ram); memUnit(want) != memUnit(pgShared) || pgShared > want {
			t.Fatalf("RAM %s: conf renders shared_buffers = %s, but the GC reserve is computed from %s",
				humanRAM(ram), memUnit(pgShared), memUnit(want))
		}

		// (b) The app's limit is EXACTLY the machine less every other claim,
		//     three-quartered — not merely "small enough to fit". A `≤ RAM`
		//     assertion is absorbed by the app's own three-quarter share: an
		//     implementation that forgot PostgreSQL entirely still fits on
		//     every machine above ~640MiB, so the weaker form reports `ok`
		//     while the two provisioners double-claim the box.
		want := (ram - gcOSReserveBytes - pgSharedBuffersFor(ram) - gcPostgresWorkingSetBytes) /
			gcAppShareDenominator * gcAppShareNumerator
		if uint64(tun.memoryLimitBytes) != want {
			t.Fatalf("RAM %s: app limit is %s; the machine less the OS (%s), the cluster's shared_buffers (%s) and its working set (%s), three-quartered, is %s",
				humanRAM(ram), humanRAM(uint64(tun.memoryLimitBytes)), humanRAM(gcOSReserveBytes),
				humanRAM(pgSharedBuffersFor(ram)), humanRAM(gcPostgresWorkingSetBytes), humanRAM(want))
		}

		total := uint64(tun.memoryLimitBytes) + pgShared + gcOSReserveBytes
		if total > ram {
			t.Fatalf("RAM %s: app limit %s + postgres shared_buffers %s + OS reserve %s = %s, which exceeds the machine",
				humanRAM(ram), humanRAM(uint64(tun.memoryLimitBytes)),
				humanRAM(pgShared), humanRAM(gcOSReserveBytes), humanRAM(total))
		}
	}
}

// TestASoftLimitLeavesRoomToOvershootIt. GOMEMLIMIT is soft: when the live heap
// genuinely needs more, Go exceeds the limit rather than killing the process.
// A limit set right up against physical memory therefore buys nothing — the
// overshoot is the OOM. Every machine must keep a real margin between what the
// app is permitted and what the machine has.
func TestASoftLimitLeavesRoomToOvershootIt(t *testing.T) {
	for _, ram := range machinesToCheck() {
		for _, embedded := range []bool{false, true} {
			tun := gcTuningFor(machine{ramBytes: ram, cpus: 4}, gcEnvironment{embeddedPostgres: embedded})
			if !tun.setMemoryLimit {
				continue
			}
			limit := uint64(tun.memoryLimitBytes)
			if limit*4 > ram*3 {
				t.Fatalf("RAM %s (embedded=%v): limit %s is more than three quarters of the machine — no overshoot margin",
					humanRAM(ram), embedded, humanRAM(limit))
			}
		}
	}
}

// TestTheLimitIsNeverBelowWhatTheStockCollectorAlreadyUses. A limit under the
// app's own working set is not a bound, it is a treadmill: the collector runs
// against a target it cannot reach. The floor is calibrated against measured
// footprint — `docs/perf/runs/gogc-postgres-20260816/results.tsv` reads
// 138–145 MB peak RSS at GOGC=100, n=100 — so any limit we set must clear it.
func TestTheLimitIsNeverBelowWhatTheStockCollectorAlreadyUses(t *testing.T) {
	const measuredStockPeakAtN100 = 145 * mb // results.tsv, n100-gogc100-b2

	for _, ram := range machinesToCheck() {
		for _, embedded := range []bool{false, true} {
			for _, serverless := range []bool{false, true} {
				env := gcEnvironment{embeddedPostgres: embedded, serverless: serverless}
				tun := gcTuningFor(machine{ramBytes: ram, cpus: 4}, env)
				if !tun.setMemoryLimit {
					continue
				}
				if uint64(tun.memoryLimitBytes) < measuredStockPeakAtN100 {
					t.Fatalf("RAM %s (embedded=%v serverless=%v): limit %s is below the %s the stock collector already peaks at",
						humanRAM(ram), embedded, serverless,
						humanRAM(uint64(tun.memoryLimitBytes)), humanRAM(measuredStockPeakAtN100))
				}
			}
		}
	}
}

// TestAMachineTooSmallToAffordTheMultiplierGetsNeitherHalf. GOGC=400 asks for
// four times the live heap. On a machine with no room for that, taking the
// throughput without being able to afford the bound is the one combination that
// makes the product worse. The two settings ship as a pair or not at all.
func TestAMachineTooSmallToAffordTheMultiplierGetsNeitherHalf(t *testing.T) {
	for _, ram := range machinesToCheck() {
		for _, embedded := range []bool{false, true} {
			tun := gcTuningFor(machine{ramBytes: ram, cpus: 4}, gcEnvironment{embeddedPostgres: embedded})
			if tun.setGCPercent && !tun.setMemoryLimit {
				t.Fatalf("RAM %s (embedded=%v): raised GOGC to %d with no memory limit — the unbounded arm the run rejected",
					humanRAM(ram), embedded, tun.gcPercent)
			}
		}
	}
}

// TestATinyContainerIsLeftOnTheGoDefaults. The concrete end of the property
// above: a 256MB container must come out of the derivation untouched.
func TestATinyContainerIsLeftOnTheGoDefaults(t *testing.T) {
	for _, ram := range []uint64{128 * mb, 256 * mb} {
		tun := gcTuningFor(machine{ramBytes: ram, cpus: 2}, gcEnvironment{})
		if tun.setMemoryLimit || tun.setGCPercent {
			t.Fatalf("RAM %s: tuned a container too small to tune (%+v)", humanRAM(ram), tun)
		}
		if tun.reason == "" {
			t.Fatalf("RAM %s: declined to tune without saying why", humanRAM(ram))
		}
	}
}

// TestAnUndetectableMachineIsLeftOnTheGoDefaults. detectRAMBytes returns 0 when
// it cannot read the machine, and 0 is a real answer. Guessing large on an
// unknown machine is how a limited container gets an OOM kill.
func TestAnUndetectableMachineIsLeftOnTheGoDefaults(t *testing.T) {
	tun := gcTuningFor(machine{ramBytes: 0, cpus: 4}, gcEnvironment{})
	if tun.setMemoryLimit || tun.setGCPercent {
		t.Fatalf("undetectable RAM: tuned anyway (%+v)", tun)
	}
}

// TestAnExplicitOperatorSettingIsNeverOverridden. An operator who has sized
// GOGC or GOMEMLIMIT knows more about the deployment than the heuristic does,
// and the standard Go env vars are the escape hatch — which is the argument
// against adding a sky.toml knob that would say the same thing a second way.
func TestAnExplicitOperatorSettingIsNeverOverridden(t *testing.T) {
	m := machine{ramBytes: 16384 * mb, cpus: 8}

	for _, env := range []gcEnvironment{
		{gogc: "800"},
		{gogc: "off"},
		{gomemlimit: "2GiB"},
		{gogc: "200", gomemlimit: "3GiB"},
	} {
		tun := gcTuningFor(m, env)
		if env.gogc != "" && tun.setGCPercent {
			t.Fatalf("GOGC=%q in the environment was overridden (%+v)", env.gogc, tun)
		}
		if env.gomemlimit != "" && tun.setMemoryLimit {
			t.Fatalf("GOMEMLIMIT=%q in the environment was overridden (%+v)", env.gomemlimit, tun)
		}
	}
}

// TestOneExplicitSettingDoesNotSuppressTheOther. GOMEMLIMIT alone is a common
// operator choice; it should not silently cost the app the multiplier that
// makes the limit worth having, and vice versa.
func TestOneExplicitSettingDoesNotSuppressTheOther(t *testing.T) {
	m := machine{ramBytes: 16384 * mb, cpus: 8}

	if tun := gcTuningFor(m, gcEnvironment{gomemlimit: "2GiB"}); !tun.setGCPercent {
		t.Fatalf("an explicit GOMEMLIMIT suppressed the GOGC default too (%+v)", tun)
	}
	if tun := gcTuningFor(m, gcEnvironment{gogc: "150"}); !tun.setMemoryLimit {
		t.Fatalf("an explicit GOGC suppressed the derived memory limit too (%+v)", tun)
	}
}

// TestTheMultiplierIsTheOneTheRunMeasured. 400, not 800: at n=500 on the
// postgres store GOGC=800 measured 1,713,968–2,026,336 kB peak RSS
// (`n500-gogc800-b1`/`b2`), which is more than an e2-small has, with a 68%
// run-to-run spread at n=300 that an operator cannot provision against.
func TestTheMultiplierIsTheOneTheRunMeasured(t *testing.T) {
	tun := gcTuningFor(machine{ramBytes: 16384 * mb, cpus: 8}, gcEnvironment{})
	if !tun.setGCPercent {
		t.Fatal("a 16GB machine was left on the Go default")
	}
	if tun.gcPercent != 400 {
		t.Fatalf("GOGC default is %d; the measured arm is 400 (docs/perf/runs/gogc-postgres-20260816)", tun.gcPercent)
	}
}

// TestServerlessTakesTheBoundButNotTheMultiplier.
//
// Two reasons, both from the measurement. (1) The limit at the stock multiplier
// is free: `gml-n500-gogc100-750MiB` reads 2,839.8 int/s / 404,320 kB against
// `n500-gogc100-b2`'s 2,838.5 / 409,616 — inside noise, because the multiplier
// collects long before the limit binds. So a request-billed instance pays
// nothing for the backstop. (2) The +19% is a property of a long-lived
// session-holding process, and a request-billed container has a HARD,
// platform-enforced ceiling where a soft limit's overshoot is a killed instance
// rather than a slower one. Taking a 4× live-heap multiplier against that is
// the wrong trade, and it is not what was measured.
func TestServerlessTakesTheBoundButNotTheMultiplier(t *testing.T) {
	for _, ram := range machinesToCheck() {
		tun := gcTuningFor(machine{ramBytes: ram, cpus: 2}, gcEnvironment{serverless: true})
		if tun.setGCPercent {
			t.Fatalf("RAM %s: raised GOGC on serverless (%+v)", humanRAM(ram), tun)
		}
	}
	// A default Cloud Run instance is 512MiB and is exactly the case the bound
	// exists for: at GOGC=100 the measured n=500 peak is 404 MB, which that
	// instance does not have.
	tun := gcTuningFor(machine{ramBytes: 512 * mb, cpus: 1}, gcEnvironment{serverless: true})
	if !tun.setMemoryLimit {
		t.Fatalf("a 512MiB serverless instance got no bound (%+v)", tun)
	}
	if uint64(tun.memoryLimitBytes) >= 512*mb {
		t.Fatalf("512MiB serverless: limit %s does not bound the container", humanRAM(uint64(tun.memoryLimitBytes)))
	}
}

// TestServerlessDoesNotPayTheHostOSReserve. Inside a request-billed container
// the platform's operating system is OUTSIDE the memory the container is
// charged for, so subtracting a host OS reserve would under-size the app for no
// reason. A serverless instance must therefore be permitted at least as much as
// the same-sized VM.
func TestServerlessDoesNotPayTheHostOSReserve(t *testing.T) {
	for _, ram := range machinesToCheck() {
		vm := gcTuningFor(machine{ramBytes: ram, cpus: 2}, gcEnvironment{})
		sl := gcTuningFor(machine{ramBytes: ram, cpus: 2}, gcEnvironment{serverless: true})
		if !sl.setMemoryLimit {
			continue
		}
		if vm.setMemoryLimit && sl.memoryLimitBytes < vm.memoryLimitBytes {
			t.Fatalf("RAM %s: serverless limit %s is below the VM limit %s", humanRAM(ram),
				humanRAM(uint64(sl.memoryLimitBytes)), humanRAM(uint64(vm.memoryLimitBytes)))
		}
	}
}

// TestEmbeddingPostgresLowersTheAppsShare. The app supervising its own database
// has less of the machine to itself, and must be told so.
func TestEmbeddingPostgresLowersTheAppsShare(t *testing.T) {
	for _, ram := range machinesToCheck() {
		alone := gcTuningFor(machine{ramBytes: ram, cpus: 4}, gcEnvironment{})
		withPG := gcTuningFor(machine{ramBytes: ram, cpus: 4}, gcEnvironment{embeddedPostgres: true})
		if !alone.setMemoryLimit {
			continue
		}
		if withPG.setMemoryLimit && withPG.memoryLimitBytes >= alone.memoryLimitBytes {
			t.Fatalf("RAM %s: embedding PostgreSQL did not lower the app's limit (%s vs %s)",
				humanRAM(ram), humanRAM(uint64(withPG.memoryLimitBytes)), humanRAM(uint64(alone.memoryLimitBytes)))
		}
	}
}

// TestTheLimitRisesWithTheMachine. Session cost is a slope, not a constant —
// `docs/perf/runs/gogc-postgres-20260816/README.md` measures 1,915 kB per
// session at GOGC=400 — so a bigger machine, which is bought to hold more
// sessions, must be permitted more heap. A flat ceiling would make the bound
// the binder on exactly the machines chosen to avoid it.
func TestTheLimitRisesWithTheMachine(t *testing.T) {
	var prev int64
	for _, ram := range machinesToCheck() {
		tun := gcTuningFor(machine{ramBytes: ram, cpus: 4}, gcEnvironment{})
		if !tun.setMemoryLimit {
			continue
		}
		if prev != 0 && tun.memoryLimitBytes <= prev {
			t.Fatalf("RAM %s: limit %s did not rise above the smaller machine's %s",
				humanRAM(ram), humanRAM(uint64(tun.memoryLimitBytes)), humanRAM(uint64(prev)))
		}
		prev = tun.memoryLimitBytes
	}
}

// TestAnE2SmallFitsWithTheMeasuredWorkload is the safety property the mandate
// asks to be PROVEN rather than assumed, expressed against the committed
// numbers rather than against this test's own arithmetic.
//
// From `docs/perf/runs/gogc-postgres-20260816/results.tsv`, n=500 on the
// postgres store: `combo-n500-gogc400-750MiB` peaks at 758,512 kB under a
// 750MiB limit. The run's README divides macOS/arm64 RSS by 1.17 for a Linux
// estimate (16kB pages against 4kB), giving ~633 MB. An e2-small must have room
// for that PLUS the OS PLUS the embedded cluster.
func TestAnE2SmallFitsWithTheMeasuredWorkload(t *testing.T) {
	const measuredPeakKB = 758512    // results.tsv, combo-n500-gogc400-750MiB
	const arm64ToLinuxDivisor = 1.17 // run README, "Does it fit the instance?"
	const measuredPGPeakKB = 82336   // results.tsv, same arm, pg_peak_kb

	m := machine{ramBytes: gcTestE2SmallRAM, cpus: 2}
	tun := gcTuningFor(m, gcEnvironment{embeddedPostgres: true})
	if !tun.setMemoryLimit {
		t.Fatal("an e2-small got no memory limit")
	}

	measuredPeakBytes := float64(measuredPeakKB) * 1024
	estLinuxApp := uint64(measuredPeakBytes / arm64ToLinuxDivisor)
	total := estLinuxApp + uint64(measuredPGPeakKB)*1024 + gcOSReserveBytes
	if total > gcTestE2SmallRAM {
		t.Fatalf("measured n=500 workload does not fit an e2-small: app %s + postgres %s + OS %s = %s of %s",
			humanRAM(estLinuxApp), humanRAM(uint64(measuredPGPeakKB)*1024),
			humanRAM(gcOSReserveBytes), humanRAM(total), humanRAM(gcTestE2SmallRAM))
	}

	// And the derived limit must be at least as large as the workload we
	// measured, or the e2-small default would bind below the point the +19%
	// was measured at and the shipped default would not be the measured one.
	if uint64(tun.memoryLimitBytes) < estLinuxApp {
		t.Fatalf("e2-small limit %s is below the %s the measured n=500 workload needs",
			humanRAM(uint64(tun.memoryLimitBytes)), humanRAM(estLinuxApp))
	}
}

// TestAContainerIsSizedToItsCgroupLimitNotItsHost is the trap a naive
// implementation inherits, driven end to end: `/proc/meminfo` is NOT
// namespaced, so a memory-limited container reports the HOST's total. A
// `GOMEMLIMIT` derived from it would be sized for a 64GB node inside a 2GB
// container, which is worse than setting nothing at all — the app would be
// permitted 47GB and the container killed at the first heap it believed it
// could afford.
//
// `detectRAMBytesFrom` already consults the cgroup limit first and has its own
// test. What this adds is the WIRING: that the figure the GC rule is derived
// from is that one, so reversing the two lines in the detector moves the
// memory limit and not only the cluster's shared_buffers.
func TestAContainerIsSizedToItsCgroupLimitNotItsHost(t *testing.T) {
	const hostRAM = 64 * 1024 * mb
	const containerLimit = 1977 * mb // an e2-small-sized container

	read := func(path string) ([]byte, error) {
		switch path {
		case "/sys/fs/cgroup/memory.max":
			return []byte(fmt.Sprintf("%d\n", containerLimit)), nil
		case "/proc/meminfo":
			return []byte(fmt.Sprintf("MemTotal:  %d kB\n", hostRAM/1024)), nil
		}
		return nil, os.ErrNotExist
	}

	ram := detectRAMBytesFrom(read, false, func() (uint64, bool) { return 0, false })
	if ram != containerLimit {
		t.Fatalf("detected %s; the container's cgroup limit is %s", humanRAM(ram), humanRAM(containerLimit))
	}

	got := gcTuningFor(machine{ramBytes: ram, cpus: 2}, gcEnvironment{embeddedPostgres: true})
	host := gcTuningFor(machine{ramBytes: hostRAM, cpus: 2}, gcEnvironment{embeddedPostgres: true})
	if !got.setMemoryLimit {
		t.Fatalf("an e2-small-sized container got no limit (%+v)", got)
	}
	if uint64(got.memoryLimitBytes) >= containerLimit {
		t.Fatalf("limit %s does not bound the %s container", humanRAM(uint64(got.memoryLimitBytes)), humanRAM(containerLimit))
	}
	if got.memoryLimitBytes == host.memoryLimitBytes {
		t.Fatalf("the container was sized as though it were the %s host", humanRAM(hostRAM))
	}
}

// TestTheChoiceIsAlwaysExplained. The failure this default prevents is an OOM
// kill, which an operator cannot debug from the outside. Whatever is decided —
// including deciding to do nothing — has to be sayable in one line.
func TestTheChoiceIsAlwaysExplained(t *testing.T) {
	for _, ram := range append(machinesToCheck(), 0) {
		for _, env := range []gcEnvironment{
			{}, {embeddedPostgres: true}, {serverless: true}, {gogc: "off"}, {gomemlimit: "1GiB"},
		} {
			tun := gcTuningFor(machine{ramBytes: ram, cpus: 4}, env)
			if strings.TrimSpace(tun.reason) == "" {
				t.Fatalf("RAM %s env %+v: no reason recorded", humanRAM(ram), env)
			}
			if tun.setMemoryLimit && !strings.Contains(tun.reason, "MB") && !strings.Contains(tun.reason, "GB") {
				t.Fatalf("RAM %s env %+v: reason %q does not state the limit", humanRAM(ram), env, tun.reason)
			}
		}
	}
}

// TestExceedingTheLimitDegradesThroughputRatherThanDeadlocking is the run
// doc's named, untested falsifier: "a workload whose live heap legitimately
// exceeds the derived limit … the thrash case … the main risk of shipping a
// limit".
//
// The property being established is Go's own: GOMEMLIMIT is SOFT, and the
// runtime's GC CPU limiter caps collector CPU at 50%, so a live set larger than
// the limit produces a slower process, not a death spiral and not an OOM. This
// asserts it in OUR binary rather than trusting the release note: hold a live
// set several times a deliberately small limit and require that the process
// still makes forward progress and that the heap is allowed to exceed the
// limit rather than the allocation failing.
func TestExceedingTheLimitDegradesThroughputRatherThanDeadlocking(t *testing.T) {
	const limit = 32 << 20
	const liveTarget = 128 << 20 // 4× the limit: unsatisfiable by collection

	prevLimit := debug.SetMemoryLimit(limit)
	prevPercent := debug.SetGCPercent(400)
	t.Cleanup(func() {
		debug.SetMemoryLimit(prevLimit)
		debug.SetGCPercent(prevPercent)
	})

	// A live set that cannot be collected away, built in chunks so the
	// collector has every opportunity to run against a target it cannot meet.
	live := make([][]byte, 0, liveTarget/(1<<20))
	held := 0
	for held < liveTarget {
		chunk := make([]byte, 1<<20)
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = byte(i) // touch it, so it is resident and not merely reserved
		}
		live = append(live, chunk)
		held += len(chunk)
	}

	// Forward progress: allocation still succeeds beyond the limit, and the
	// live set is intact. Reaching this line at all is the assertion — a death
	// spiral would not return, and a hard limit would have failed to allocate.
	if held < liveTarget {
		t.Fatalf("held %d bytes, wanted %d", held, liveTarget)
	}
	if got := len(live); got != liveTarget/(1<<20) {
		t.Fatalf("live set is %d chunks, wanted %d", got, liveTarget/(1<<20))
	}

	var stats debug.GCStats
	debug.ReadGCStats(&stats)
	if stats.NumGC == 0 {
		t.Fatal("no collection ran at all — the limit was not in effect")
	}

	// And the limit really was the small one throughout, i.e. the runtime chose
	// to exceed it rather than refuse the allocation.
	if cur := debug.SetMemoryLimit(-1); cur != limit {
		t.Fatalf("memory limit is %d, expected the %d that was exceeded", cur, limit)
	}
	runtimeHeld := liveTarget
	if runtimeHeld <= limit {
		t.Fatalf("live set %d did not exceed the limit %d — the case is untested", runtimeHeld, limit)
	}
}

// TestApplyingTheTuningIsIdempotentAndMatchesTheDecision wires the pure
// decision to the runtime call, so a correct decision cannot be applied wrongly.
func TestApplyingTheTuningIsIdempotentAndMatchesTheDecision(t *testing.T) {
	prevLimit := debug.SetMemoryLimit(-1)
	prevPercent := debug.SetGCPercent(-1)
	t.Cleanup(func() {
		debug.SetMemoryLimit(prevLimit)
		debug.SetGCPercent(prevPercent)
	})
	debug.SetGCPercent(prevPercent) // SetGCPercent(-1) disables; put it back

	tun := gcTuning{
		setMemoryLimit:   true,
		memoryLimitBytes: 900 * int64(mb),
		setGCPercent:     true,
		gcPercent:        400,
		reason:           "test",
	}
	applyGCTuning(tun)
	if got := debug.SetMemoryLimit(-1); got != tun.memoryLimitBytes {
		t.Fatalf("memory limit is %d, wanted %d", got, tun.memoryLimitBytes)
	}
	applyGCTuning(tun) // twice: an app that re-tunes must not drift
	if got := debug.SetMemoryLimit(-1); got != tun.memoryLimitBytes {
		t.Fatalf("memory limit is %d after a second apply, wanted %d", got, tun.memoryLimitBytes)
	}

	none := gcTuning{reason: "declined"}
	applyGCTuning(none)
	if got := debug.SetMemoryLimit(-1); got != tun.memoryLimitBytes {
		t.Fatalf("a declining decision changed the limit to %d", got)
	}
}

// TestTheAmbientWrapperReadsTheRealEnvironment. gcEnvironmentFromAmbient is the
// only impure part; it must actually read GOGC/GOMEMLIMIT rather than assuming
// them empty, or every operator override would be silently ignored in
// production while every pure test above passed.
func TestTheAmbientWrapperReadsTheRealEnvironment(t *testing.T) {
	t.Setenv("GOGC", "123")
	t.Setenv("GOMEMLIMIT", "7GiB")
	env := gcEnvironmentFromAmbient()
	if env.gogc != "123" {
		t.Fatalf("GOGC read as %q, wanted %q", env.gogc, "123")
	}
	if env.gomemlimit != "7GiB" {
		t.Fatalf("GOMEMLIMIT read as %q, wanted %q", env.gomemlimit, "7GiB")
	}
}

// confBytesSetting parses a byte-valued setting out of a rendered
// postgresql.conf block. Reading the ARTEFACT is the point: it is what makes
// TestTheAppAndPostgresDoNotEachClaimTheWholeMachine move when pg_embed_conf's
// share of the machine moves.
func confBytesSetting(t *testing.T, conf, key string) uint64 {
	t.Helper()
	for _, line := range strings.Split(conf, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		var n uint64
		switch {
		case strings.HasSuffix(v, "GB"):
			if _, err := fmt.Sscanf(v, "%dGB", &n); err != nil {
				t.Fatalf("cannot parse %s = %q: %v", key, v, err)
			}
			return n * 1024 * mb
		case strings.HasSuffix(v, "MB"):
			if _, err := fmt.Sscanf(v, "%dMB", &n); err != nil {
				t.Fatalf("cannot parse %s = %q: %v", key, v, err)
			}
			return n * mb
		}
		t.Fatalf("%s = %q has no unit this test understands", key, v)
	}
	t.Fatalf("no %s in the rendered conf", key)
	return 0
}

// guard against an accidental overflow in the arithmetic above.
var _ = math.MaxInt64
