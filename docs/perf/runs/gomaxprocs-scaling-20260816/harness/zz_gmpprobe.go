package main

// zz_gmpprobe.go — MEASUREMENT INSTRUMENT ONLY, added to a COPY of the emitted
// Go package for the GOMAXPROCS scaling study. It is not part of the Sky
// runtime, not part of the compiler, and never lands in the repository's
// source: it exists so a `mutex` and `block` profile can be taken from the
// same application binary whose throughput is being measured.
//
// The plain arm of the study runs the UNMODIFIED binary
// (sha256 168f4d5f9968c1f4efb230ab4a1ca655fd7f6337c1044094d2daebae809ce782),
// so nothing here can influence the scaling curve. This file is compiled into
// a SECOND binary used only for the lock-attribution arm, and the two are run
// against each other at the same GOMAXPROCS so the instrument's own cost is
// measured rather than assumed.
//
// Everything is off unless SKY_PROBE_ADDR is set, so the same binary can also
// serve as an uninstrumented control.

import (
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strconv"
)

func init() {
	addr := os.Getenv("SKY_PROBE_ADDR")
	if addr == "" {
		return
	}
	if v := os.Getenv("SKY_PROBE_MUTEX_FRACTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			runtime.SetMutexProfileFraction(n)
		}
	}
	if v := os.Getenv("SKY_PROBE_BLOCK_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			runtime.SetBlockProfileRate(n)
		}
	}
	go func() {
		// DefaultServeMux carries net/http/pprof's handlers; the application
		// serves its own mux on its own port, so this cannot collide.
		_ = http.ListenAndServe(addr, nil)
	}()
}
