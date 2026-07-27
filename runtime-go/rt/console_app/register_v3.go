// register_v3.go — console_app side of the v0.16.1 PR10-E cfg-provider
// shim. Registers a Sky.Live-cfg-shaped factory into rt's slot so
// rt.MountEmbeddedConsole can mount the inline console via the
// canonical MountLiveSubAppInProcess primitive instead of the bespoke
// PR3/PR8 wiring.
//
// Why a third register file (register / register_v2 / register_v3):
//
//   - register.go (PR 1) installs MountInlineConsole — the bespoke
//     one-shot HTML render path. Deleted in PR 10-G once the canonical
//     Sky.Live machinery takes over.
//   - register_v2.go (PR 8) installs ConsoleAppHooks — init / update /
//     view / decodeMsg closures the rt-side console_loop.go consumed.
//     Deleted in PR 10-G alongside console_loop.go itself.
//   - register_v3.go (PR 10-E, this file) installs the cfg PROVIDER.
//     It returns a map[string]any with the same key/value shape that
//     `Live.app` consumes for a user app — Init / Update / View /
//     Subscriptions. rt's MountEmbeddedConsole hands this cfg to
//     MountLiveSubAppInProcess; the resulting *liveApp's existing
//     handleEvent / handleSSE / handleInitial paths drive the bundled
//     console UI with ZERO extra rt code.
//
// All three init()s coexist for the duration of the PR 10-E/F/G
// landing window. PR 10-G is the deletion step that strips register.go
// + register_v2.go.

package console_app

import (
	rt "sky-app/rt"
)

func init() {
	rt.RegisterInlineConsoleCfgProvider(buildInlineConsoleCfg)
}

// buildInlineConsoleCfg returns the Sky.Live cfg shape the bundled
// inline console exposes to rt's mount path. The keys mirror what the
// generated `main.go` of any user Sky.Live app would put on the cfg
// record — rt.Field(cfg, "Init") etc. resolves them via reflect.
//
// CFG SHAPE (Rust-compiler symbol names — the primary toolchain)
//
// The bundled console's Go is emitted by `sky build` (Rust) via
// scripts/regenerate-console.sh — which builds the Sky.Live entry
// (Main.sky) with the sibling Sky.Tui entry (MainTui.sky) dropped, so
// the Live `main` wins and the Live-only bindings survive DCE. The
// entry points carry the Rust codegen's module-prefixed names + typed
// signatures, and are referenced DIRECTLY here exactly as a real
// Rust-compiled Sky.Live app's generated `main()` passes them to
// rt.Live_app:
//
//   - Init: `Main_init_(_req any) rt.T2[State_Model_R, any]` — the Live
//     `init _req` is row-polymorphic so its param lowers to `any`; the
//     console ignores the request (it reads SKY_PARENT_URL from the
//     environment).
//   - Update: `Main_update(msg State_Msg, model State_Model_R) rt.T2[State_Model_R, any]`
//     — the mount's sky_call2 reflect path coerces the opaque msg +
//     model back to the typed shape.
//   - View: `Main_viewWrapped(model State_Model_R) any` — the Live
//     wrapper that returns `Ui.layout [] (view model)`, i.e. a
//     renderable `Html`. (The bare `view` in View.sky returns a raw
//     `Element` for the Tui backend; the Live mount's HtmlToVNode needs
//     the layout-wrapped Html, so we point at the wrapper.)
//   - Subscriptions: `Main_subscriptions(model State_Model_R) any`.
//   - Store: "memory" — the inline console doesn't need to survive
//     restarts; admin tools that need history use the persistent
//     telemetry store the console READS from.
//   - Ttl: "30m" — same default as a user app; closes the SSE channel
//     when an admin tab idles out.
//
// CFG OMISSIONS (defaults take over)
//
//   - Routes: empty. The bundled console is a single-page UI; URL
//     routing inside it is Sky-side tab state, not HTTP routing.
//   - Notfound: nil. Routes are empty, so notFound is unreachable.
//   - Api: nil. No custom REST endpoints inside the console.
//   - ConsoleAuth: nil. Auth lives at the mount boundary
//     (rt.ConsoleGate wraps the sub-app routes); the canonical
//     Sky.Live consoleAuth field is for OUTER console gates, not for
//     the console itself.
//   - Static: nil. Console UI is fully Std.Ui-rendered; no
//     filesystem-served assets.
//   - Port: nil. The host owns the listener; sub-apps don't bind ports.
//   - Guard: nil. The console_app's own update + view are trusted
//     server-side code; no need for the Live-app-level message guard
//     (which exists for user code that wants to short-circuit specific
//     Msg shapes before they reach update).
//   - Head: nil. The inline console body lives inside the host's HTML
//     page wrapper.
func buildInlineConsoleCfg() any {
	return map[string]any{
		"Init":          Main_init_,
		"Update":        Main_update,
		"View":          Main_viewWrapped,
		"Subscriptions": Main_subscriptions,
		// Routes + NotFound mirror the console's Sky `main` cfg
		// (`routes = [ route "/" () ]`, `notFound = ()`), matching what a
		// standalone `sky console` build passes to rt.Live_app. The
		// single `/` route is load-bearing: handleInitial's guard 404s a
		// GET whose path matches no route once a session exists, so
		// omitting Routes leaves `routed == false` and the sub-app
		// renders an empty body. `()` (Go `struct{}{}`) is the unit page
		// — the console is single-page and dispatches on model.tab, not
		// on URL routing.
		"Routes":   []any{rt.Live_route(any("/"), any(struct{}{}))},
		"NotFound": struct{}{},
		"Store":    "memory",
		"Ttl":      "30m",
		// All other Live.app cfg fields fall through to Field(cfg, X) == nil.
	}
}
