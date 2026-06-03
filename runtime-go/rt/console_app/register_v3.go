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
// CFG SHAPE
//
//   - Init: typed wrapper around the generic `init_[T1 any](_req T1)`.
//     Wrapping closes the type parameter so reflect sees a concrete
//     `func(any) SkyTuple2` callable through sky_call.
//   - Update: the bundled `update(msg State_Msg, model State_Model_R) SkyTuple2`.
//     sky_call2 handles the 2-arg dispatch via reflect; the reflect
//     path coerces opaque msg + model values back to the typed shape.
//   - View: `viewWrapped(model State_Model_R) SkyValue`. sky_call's
//     reflect path coerces model back to State_Model_R.
//   - Subscriptions: `subscriptions(model State_Model_R) SkySub` —
//     reflect-coerces model back to State_Model_R.
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
	// Wrap the generic init_ to a concrete `func(any) SkyTuple2`
	// closure. The bundled console's init ignores _req's contents —
	// it only reads SKY_PARENT_URL — so any value passed through
	// here flows safely. The wrapping ALSO lets us elide the type
	// argument that reflect can't infer (`init_[T1 any](_req T1)`
	// has a phantom T1).
	initFn := func(req any) rt.SkyTuple2 {
		return init_[any](req)
	}

	return map[string]any{
		"Init":          initFn,
		"Update":        update,
		"View":          viewWrapped,
		"Subscriptions": subscriptions,
		"Store":         "memory",
		"Ttl":           "30m",
		// All other Live.app cfg fields fall through to Field(cfg, X) == nil.
	}
}
