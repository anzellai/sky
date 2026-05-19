package telemetry

import "math"

// float64FromBits / bitsFromFloat64 — Prometheus client_golang trick.
// `sync/atomic.Float64` doesn't exist; we store the IEEE 754 bit
// pattern in an atomic.Uint64 and convert at the boundary. CAS on
// the bits stays correct because float64 → uint64 → float64 is a
// total bijection (NaN bit patterns aside, but we never store NaN
// in counters or gauges; histogram sum could but Observe(NaN) is
// undefined Prometheus behaviour anyway).
func float64FromBits(b uint64) float64 { return math.Float64frombits(b) }
func bitsFromFloat64(f float64) uint64 { return math.Float64bits(f) }
