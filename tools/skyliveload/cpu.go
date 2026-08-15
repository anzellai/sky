package main

import "syscall"

// processCPU returns the CPU seconds (user + system) consumed by this
// process so far.
//
// This backs the generator-saturation check. A load generator that is
// itself CPU-bound reports latencies that are mostly its own scheduling
// delay, and a throughput ceiling that is its own -- the classic way a
// load test produces a confident number about the wrong machine. The
// run is marked invalid when the generator exceeds 70% of the host's
// total CPU capacity.
func processCPU() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return user + sys
}
