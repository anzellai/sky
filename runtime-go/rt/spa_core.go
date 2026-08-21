package rt

// Sky.Spa — client-side TEA entry kernels (portable core).
//
// A Sky.Spa app is written exactly like a Sky.Live app — Model / Msg / pure
// update / view over the renderer-agnostic Element/Html — but the TEA loop
// runs on the CLIENT (compiled to GOOS=js GOARCH=wasm) instead of the server.
//
//   main = Spa.app (Spa.config { init = .., update = .., view = .. })
//
// lowers to:
//
//   rt.AnyTaskRun(rt.TaskCoerceT[Error, struct{}](rt.Spa_app(rt.Spa_config(cfg))))
//
// mirroring Sky.Live's Live_app/Live_config shape (see live.go Live_app), so
// the existing task-forcing codegen path drives it unchanged.
//
// The config record is the Sky record `{ init, update, view }`, lowered to a Go
// struct with capitalised fields (Init/Update/View) — read back with Field().

// Spa_config is the identity on the config record; the withX-builder chain (if
// any) refines it, and Spa_app reads the fields. Kept trivial so the whole
// config lives in the one Sky record, exactly like the Live builder.
func Spa_config(cfg any) any { return cfg }

// Spa_app wraps the client TEA loop in a Task thunk. AnyTaskRun forces it at
// main; spaRun is build-split — the real single-threaded wasm driver lives in
// live_wasm.go (//go:build js); spa_notjs.go carries a no-op for the normal
// build so `sky build` (which go-builds the emitted app for the host) links.
func Spa_app(cfg any) any {
	return func() any { return spaRun(cfg) }
}
