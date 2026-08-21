//go:build js

package rt

// main_preamble_js.go — js/wasm stubs for the server lifecycle functions that
// codegen emits into EVERY Sky app's main() (see the emitted `func main`).
// Their real implementations pull embedded-Postgres / console / os-signal
// machinery and are //go:build !js. A Sky.Spa client runs in the browser: no
// Postgres to start, no console to mount, no migrations to apply. These keep
// the emitted main() linkable under GOOS=js so the same codegen serves both
// the server (Sky.Live) and the client (Sky.Spa) targets unchanged.

// LogPanicAndExit is deferred at the top of main(); under wasm a panic surfaces
// in the browser console on its own, so this is a no-op.
func LogPanicAndExit() {}

// EnableConsolePersistence — the dev console is server-only.
func EnableConsolePersistence() {}

// MaybeStartEmbeddedPostgres / StopEmbeddedPostgres — no embedded cluster in a
// browser tab.
func MaybeStartEmbeddedPostgres() {}
func StopEmbeddedPostgres()       {}

// MaybeApplyEmbeddedMigrationsAndExit — no DB to migrate client-side.
func MaybeApplyEmbeddedMigrationsAndExit() {}

// RegisterSkyGobTypes — codegen emits an init() registering the app's ADT
// types for gob (session serialization). No gob wire in a browser tab.
func RegisterSkyGobTypes(vals []any) {}

// System_exit — System.exit on the host drains pools + exits (rt_server.go,
// !js). A browser tab cannot be force-exited, and there is no embedded cluster
// to orphan under wasm (the reason the os.Exit audit bans a raw exit here), so
// the client just logs the requested code and returns; codegen has already
// logged the error before calling this on its failure path.
func System_exit(code any) any {
	logEmit(logLevelError, "error", "Sky.Spa: System.exit requested (code "+itoa(AsInt(code))+"); no-op in the browser", nil)
	return nil
}
