package rt

// perf_hook.go — the allocation counters `xtask perf-gate` reads.
//
// `SKY_RT_MEMSTATS_ON_EXIT=1` makes a normal return from a Sky `main` print
// one JSON line to stderr:
//
//	{"skyMemStats":{"mallocs":N,"totalAlloc":N,"erasedTypedCons":N}}
//
// `mallocs` and `totalAlloc` are `runtime.MemStats` at exit. The perf gate
// runs a program at n and at 4n and divides the difference of `mallocs` by
// the difference of the element counts, so the fixed start-up cost cancels
// and the budget is an allocation count per element, which does not depend
// on the machine.
//
// `erasedTypedCons` counts calls of the erased `List_cons` whose tail was a
// typed Go slice (not `[]any`). That path widens the whole tail through
// reflect, so it is O(n) per cons. A typed `::` must lower to
// `List_consT[T]` instead; the gate fails when a perf program reaches the
// erased path with a typed tail.
//
// The counter is one atomic add on a path that already allocates O(n), so
// it is always on. The print is off unless the variable is set.

import (
	"encoding/json"
	"os"
	"runtime"
	"sync/atomic"
)

var erasedTypedConsCount atomic.Int64

// noteErasedTypedCons records one erased cons onto a typed slice tail.
func noteErasedTypedCons() { erasedTypedConsCount.Add(1) }

// memStatsOnExitEnabled reports whether SKY_RT_MEMSTATS_ON_EXIT asks for the
// exit line. Only "1" turns it on.
func memStatsOnExitEnabled() bool {
	return os.Getenv("SKY_RT_MEMSTATS_ON_EXIT") == "1"
}

// memStatsLine renders the exit line for the current process.
func memStatsLine() string {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	body := map[string]map[string]uint64{
		"skyMemStats": {
			"mallocs":         ms.Mallocs,
			"totalAlloc":      ms.TotalAlloc,
			"erasedTypedCons": uint64(erasedTypedConsCount.Load()),
		},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// emitMemStatsOnExit prints the exit line when the variable asks for it.
func emitMemStatsOnExit() {
	if !memStatsOnExitEnabled() {
		return
	}
	_, _ = os.Stderr.WriteString(memStatsLine() + "\n")
}
