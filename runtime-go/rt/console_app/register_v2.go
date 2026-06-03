package console_app

// register_v2.go — console_app side of the v0.16.1 PR 8 update loop
// hook. Registers init / update / view / render closures into rt's
// ConsoleAppHooks slot so the rt-side goroutine (console_loop.go)
// can drive the TEA loop without an import cycle.
//
// Why a second register file rather than appending to register.go:
//
//   - register.go was hand-written for PR 1 (v0.16.0) to install
//     MountInlineConsole. Its sole purpose is the mount-shim, and
//     PR 1 explicitly contracted that file as "committed alongside
//     the generated main.go and NEVER overwritten by
//     scripts/regenerate-console.sh." Adding more hooks there would
//     blur its single-purpose role.
//   - register_v2.go is the new shim for the update loop hooks.
//     Same regen-immunity contract — scripts/regenerate-console.sh
//     only overwrites main.go, never the *_v2.go siblings.
//   - The two init()s run in undefined relative order (Go spec),
//     but each is idempotent + targets its own rt slot, so no
//     ordering hazard.

import (
	"net/http"

	rt "sky-app/rt"
)

func init() {
	rt.RegisterConsoleAppHooks(rt.ConsoleAppHooks{
		InitFromRequest: hookInit,
		Update:          hookUpdate,
		View:            hookView,
		DecodeMsg:       hookDecodeMsg,
	})
}

// hookInit is the seam between rt's update-loop session creation
// (getOrCreateConsoleLoopSession) and console_app's generated
// `init_` function. The wire shape rt passes is a map[string]any
// with "path" + "query" keys (mirrors what handleConsoleRoot already
// uses for the GET-driven first render). console_app's init_
// accepts `_req T1` generically and ignores the value — it reads
// SKY_PARENT_URL out of the environment to build the starter Model.
//
// Returns (model, cmd) as opaque `any`. Caller (rt) doesn't need
// to know the concrete types — it just routes the values back
// through Update / View / runCmd.
func hookInit(req map[string]any) (model any, cmd any) {
	// init_ is generic `[T1 any](_req T1)` and returns rt.SkyTuple2
	// with V0 = State_Model_R and V1 = rt.SkyCmd. Type-erase on
	// the way out so rt doesn't need to know either type name.
	tuple := init_[any](req)
	return tuple.V0, tuple.V1
}

// hookUpdate is the seam between rt's dispatch path
// (dispatchConsoleMsg) and console_app's generated `update`
// function. Caller guarantees that msg is a State_Msg value
// produced by hookDecodeMsg + that model is a State_Model_R
// returned by an earlier hookInit / hookUpdate; rt loses the
// concrete types at its boundary but never produces values that
// don't satisfy those constraints.
//
// Panic-prone Sky-side update code (e.g. an unhandled case branch
// because the regenerated console diverged from the Sky source)
// surfaces here. The rt caller wraps this call in a recover, so
// a single bad event drops cleanly without taking the goroutine
// down.
func hookUpdate(msg any, model any) (newModel any, cmd any) {
	concrete, ok := model.(State_Model_R)
	if !ok {
		// Caller passed something that isn't a State_Model_R —
		// shouldn't happen in production. Return inputs unchanged
		// so the loop can recover.
		return model, rt.Cmd_none()
	}
	concreteMsg, ok := msg.(State_Msg)
	if !ok {
		return model, rt.Cmd_none()
	}
	tuple := update(concreteMsg, concrete)
	return tuple.V0, tuple.V1
}

// hookView is the seam between rt's render pipeline
// (dispatchConsoleMsg → HtmlToVNode + renderVNode) and
// console_app's generated `viewWrapped` function. viewWrapped
// returns the Std.Ui-layout-wrapped Html value rt's renderer
// expects.
func hookView(model any) any {
	concrete, ok := model.(State_Model_R)
	if !ok {
		// Fallback: empty Html node so HtmlToVNode doesn't trip.
		return nil
	}
	return viewWrapped(concrete)
}

// hookDecodeMsg is the optional, console_app-side typed wire
// decoder for Msg envelopes. rt's generic fallback
// (decodeConsoleEventMsg's LookupAdtTag branch) handles primitive
// args fine; this hook exists so future typed-record Args (e.g.
// onSubmit FormCreds) can be json.Unmarshaled into their
// declared Go struct via the standard live.go decodeMsgArg
// machinery.
//
// PR 8 keeps this minimal: it ONLY routes through the generic
// path (returning ok=false to delegate to rt's fallback) UNLESS
// it can resolve via LookupAdtTag locally. The richer typed-arg
// decoding lands when we add typed-record Msg args to the
// generated console_app source (none exist in the current
// generated main.go).
//
// Envelope shape:
//
//	{"msg": "<MsgCtorName>", "args": [<arg0>, ...]}
//
// Returns (msg, true) on resolution; (nil, false) otherwise.
//
// Implementation note (v0.16.1 PR 8): the generated console_app/main.go
// emits ADT constructors (`State_Msg_SelectTab`, `State_Msg_Tick`,
// etc.) but does NOT call `rt.RegisterAdtTag` at package init — v0.16.0
// PR 2 deliberately stripped those calls to avoid colliding with the
// user app's own Msg type in the global registry.
//
// That means rt's generic fallback (decodeConsoleEventMsg →
// LookupAdtTag) returns "unknown" for every console Msg name, and the
// loop drops the event. We close the gap here with a LOCAL map: each
// Msg constructor name resolves to either the bare zero-arg State_Msg
// value OR a builder that consumes the first wire arg.
//
// The map is local to console_app — it never touches the global ADT
// registry, so the user-app's Msg type stays free of collision.
func hookDecodeMsg(envelope map[string]any) (any, bool) {
	name, _ := envelope["msg"].(string)
	if name == "" {
		return nil, false
	}
	args, _ := envelope["args"].([]any)

	// Helpers to extract typed args.
	stringAt := func(i int) string {
		if i >= len(args) {
			return ""
		}
		switch v := args[i].(type) {
		case string:
			return v
		case nil:
			return ""
		default:
			return ""
		}
	}

	switch name {
	case "Tick":
		return State_Msg_Tick, true
	case "LogFilterClear":
		return State_Msg_LogFilterClear, true
	case "SelectTab":
		// SelectTab takes a State_Tab; the wire form is a string of
		// the tab name ("OverviewTab" / "LogsTab" / …). Map back.
		tab := stringAt(0)
		switch tab {
		case "OverviewTab":
			return State_Msg_SelectTab(State_Tab_OverviewTab), true
		case "LogsTab":
			return State_Msg_SelectTab(State_Tab_LogsTab), true
		case "MetricsTab":
			return State_Msg_SelectTab(State_Tab_MetricsTab), true
		case "TracesTab":
			return State_Msg_SelectTab(State_Tab_TracesTab), true
		case "ErrorsTab":
			return State_Msg_SelectTab(State_Tab_ErrorsTab), true
		}
		// Unknown tab name — drop.
		return nil, false
	case "LogFilterQuery":
		return State_Msg_LogFilterQuery(stringAt(0)), true
	case "LogFilterToggleLevel":
		return State_Msg_LogFilterToggleLevel(stringAt(0)), true
	case "LogFilterPickSession":
		return State_Msg_LogFilterPickSession(stringAt(0)), true
	case "TraceFilterQuery":
		return State_Msg_TraceFilterQuery(stringAt(0)), true
	case "PivotToTrace":
		return State_Msg_PivotToTrace(stringAt(0)), true
	}
	// GotOverview / GotLogs / GotMetrics / GotTraces / GotErrors are
	// the result-Msg constructors — they're never dispatched from the
	// client; the update loop's runConsolePerform feeds them back via
	// sky_call(toMsg, result). No external decode path needed.
	return nil, false
}

// Compile-time sanity check — make sure console_app's package
// imports stay clean. *http.ServeMux is here to keep the
// std-net/http import alive even when the only call site lives
// in register.go's MountInlineConsole hook.
var _ = func(_ *http.ServeMux) {}
