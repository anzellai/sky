// console_app hooks registration shim (v0.16.1 PR 8).
//
// PR 3 (v0.16.1) introduced the isolated SSE channel + POST endpoint
// for /_sky/console, but the channel only carried INBOUND events — no
// update loop, no broadcast. The plumbing existed; the producer didn't.
//
// PR 8 closes that loop. The console_app package owns the typed
// Sky-source init/update/view shapes; this shim lets rt drive them
// without an import cycle. The pattern mirrors RegisterInlineConsoleHook
// — console_app's init() pushes its implementations into rt's
// package-level variable; rt's update-loop goroutine then dispatches
// through them when SSE events arrive.
//
// Why a shim rather than direct calls: console_app imports rt (the
// bundled console's generated Go calls hundreds of rt.* helpers).
// rt importing console_app would create a circular import. The hook
// keeps the dependency arrow strictly console_app → rt and gives rt
// a typed-but-opaque seam to drive the TEA loop.

package rt

import (
	"net/http"
	"sync"
)

// ConsoleAppHooks bundles the four typed-but-opaque callables rt needs
// to drive console_app's TEA loop:
//
//   - InitFromRequest: produces the starter Model + initial Cmd given a
//     map[string]any-shaped request (path + query — the same shape
//     handleConsoleRoot already passes to init_).
//   - Update: applies a Msg to a Model, returning (newModel, Cmd).
//     msg + model are opaque to rt — type-erased to `any` at the
//     boundary because rt cannot name State_Model_R / State_Msg
//     without importing console_app.
//   - View: produces the Sky.Html value for a given Model. The result
//     is rt.SkyValue-shaped (HtmlToVNode + assignSkyIDs + renderVNode
//     consume it directly).
//   - DecodeMsg: takes a Msg name + raw JSON args and reconstructs a
//     concrete Msg value the Update closure can consume. This lives
//     in console_app because:
//       * It needs to look up the Msg ADT's tag (registered via
//         LookupAdtTag from console_app's init).
//       * For typed-constructor args (curried lambdas like
//         `onInput LogFilterQuery` whose underlying ctor takes a
//         String), it can wire through the same path live.go uses
//         via applyMsgArgs.
//
// The DecodeMsg slot is OPTIONAL — when nil, rt falls back to the
// generic "construct SkyADT from name + raw Args" shape that
// dispatchEventJSON uses (live.go:3674). console_app may register a
// richer decoder later if Sky-source-typed Msg ctors need it.
type ConsoleAppHooks struct {
	// InitFromRequest builds the starter Model + initial Cmd. req is a
	// map[string]any with keys "path" / "query" (mirrors what
	// handleConsoleRoot passes). Returns (model, cmd) — both opaque.
	// Panics inside InitFromRequest are recovered by the caller; a
	// failed init falls back to an empty model with Cmd_none.
	InitFromRequest func(req map[string]any) (model any, cmd any)

	// Update is the TEA reducer. Caller guarantees that msg + model
	// were originally produced by InitFromRequest / DecodeMsg /
	// previous Update calls, so the inner case-on-Tag dispatch is
	// statically safe. Returns (newModel, cmd) — both opaque.
	Update func(msg any, model any) (newModel any, cmd any)

	// View renders a Sky.Html value from Model. Caller wraps it in
	// HtmlToVNode + renderVNode to produce the wire HTML body.
	View func(model any) any

	// DecodeMsg reconstructs a Msg value from a wire envelope. When
	// nil, rt falls back to a generic SkyADT-from-name path. When
	// present, console_app gets first dibs (it can route through its
	// own typed-ctor map for ADT branches whose payloads are
	// structured records rather than primitive scalars).
	//
	// Returns (msg, true) on success or (_, false) for unknown names —
	// rt then either drops or logs depending on the sentinel prefix.
	DecodeMsg func(envelope map[string]any) (msg any, ok bool)
}

// consoleAppHooks is the package-level slot set by console_app's init()
// via RegisterConsoleAppHooks. nil → no hooks registered → the update
// loop noops (channel still drains; events get counted but not
// dispatched).
var (
	consoleAppHooksMu sync.RWMutex
	consoleAppHooks   *ConsoleAppHooks
)

// RegisterConsoleAppHooks installs the console_app TEA closures so
// the rt-side update loop can drive them. Called once from
// console_app's init() in register.go (sibling of the existing
// RegisterInlineConsoleHook call).
//
// Idempotent: a second call replaces the first under the mutex. The
// caller is responsible for ensuring all four / three (depending on
// DecodeMsg presence) function fields are non-nil — passing a struct
// with any required field nil produces a no-op update loop.
//
// Exported because the registration crosses a package boundary, but
// not part of the rt public API for user code.
func RegisterConsoleAppHooks(h ConsoleAppHooks) {
	consoleAppHooksMu.Lock()
	c := h
	consoleAppHooks = &c
	consoleAppHooksMu.Unlock()
}

// loadConsoleAppHooks returns the currently registered hooks under
// read lock. Returns (nil, false) when no hooks have been registered
// (console_app not linked, or test that resets state).
//
// Internal helper — only the update loop + tests call this.
func loadConsoleAppHooks() (*ConsoleAppHooks, bool) {
	consoleAppHooksMu.RLock()
	h := consoleAppHooks
	consoleAppHooksMu.RUnlock()
	if h == nil {
		return nil, false
	}
	return h, true
}

// ResetConsoleAppHooksForTesting clears the hook slot so tests can
// build a fake ConsoleAppHooks (no real console_app) and exercise
// the update loop without dragging in the bundled generated code.
// Test-only; not part of the public API.
func ResetConsoleAppHooksForTesting() {
	consoleAppHooksMu.Lock()
	consoleAppHooks = nil
	consoleAppHooksMu.Unlock()
}

// ConsoleAppHooksRegistered reports whether console_app has called
// RegisterConsoleAppHooks. Used by the update-loop boot path to
// short-circuit when no hooks are available — avoids spinning a
// goroutine that has nothing to do.
func ConsoleAppHooksRegistered() bool {
	_, ok := loadConsoleAppHooks()
	return ok
}

// startConsoleUpdateLoopFromMount is the integration point called from
// MountConsoleSSE. Exists so MountConsoleSSE can stay focused on
// wire-surface setup; the actual goroutine spawn + lifecycle live in
// console_loop.go.
//
// Returns false when no hooks are registered (e.g. legacy host that
// links rt but doesn't link console_app). The SSE wire surface still
// works (event channel drains, hello + heartbeats fire); only the
// click→update→broadcast loop is absent.
func startConsoleUpdateLoopFromMount() bool {
	if !ConsoleAppHooksRegistered() {
		return false
	}
	StartConsoleUpdateLoop()
	return true
}

// Compile-time assertion that *http.ServeMux can be passed where a
// caller in console_app expects it. This is a no-op at runtime but
// guards against an accidental signature drift if rt's mount helpers
// ever rename their mux arg.
var _ = func(_ *http.ServeMux) {}
