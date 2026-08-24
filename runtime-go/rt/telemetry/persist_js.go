//go:build js

package telemetry

// persist_js.go — js/wasm stubs for the SQLite/Postgres-backed telemetry
// persistence layer, which cannot compile under GOOS=js (it pulls
// database/sql + pgx + modernc sqlite). Under wasm the telemetry Store is
// in-RAM only, so these are no-ops. The real implementation is persist.go
// (//go:build !js). Keep signatures byte-identical to the !js versions so
// store.go compiles against either.

// persistence is opaque under js — the Store never enables a backend here.
type persistence struct{}

// enqueuePersist drops the entry: no persistence backend exists under wasm.
func (s *Store) enqueuePersist(e persistEntry) {}

// ClosePersistence is a no-op: nothing was opened.
func (s *Store) ClosePersistence() {}
