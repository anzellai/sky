//go:build !js

package rt

import "strings"

// Sky.Spa server-branch FOLLOW-UPS — the backend kernel that makes a server
// branch's returned `Cmd` run (SPA-3, docs/skyspa/auto-split.md §20).
//
// In Sky.Live the whole TEA loop is server-side, so a branch that returns
// `Cmd.perform task ToMsg` has its task run and `ToMsg result` dispatched
// through `update`. The auto-split's generated RPC handler used to bind the
// returned command as `_` when it could not settle it server-side (a lambda
// `toMsg`, a continuation the client also dispatches, a batch it could not
// read), so the follow-up ran nowhere. Now the handler hands the command here:
// every perform leaf's task runs SERVER-side (where its effect is allowed), its
// `toMsg` builds the follow-up Msg, and the Msgs are returned — in command order
// — to the client, which dispatches them through its own `update` (a pure arm
// runs locally, a server arm becomes the next queued RPC). Only data crosses the
// wire, never a task.
//
// A `Std.Native` client effect cannot run here (its host stub answers a
// "client-only capability" Err); its leaf is SKIPPED — the client runs that
// leaf itself when it sends the RPC (the generated `Spa.rpcWith` residual).

// Spa_collectFollowUps walks a `cmdT` tree (batch order), runs each perform
// leaf's task, applies its toMsg, and returns the follow-up Msgs as a Sky list.
// `none` / `publish` leaves produce nothing (a publish is delivered by the push
// broker in push mode).
func Spa_collectFollowUps(cmd any) any {
	out := []any{}
	var walk func(c any)
	walk = func(c any) {
		ct, ok := c.(cmdT)
		if !ok {
			return
		}
		switch ct.kind {
		case "batch":
			for _, b := range ct.batch {
				walk(b)
			}
		case "perform":
			res := sky_call(ct.task, nil)
			if spaIsClientOnlyResult(res) {
				return // a Std.Native client effect: the client runs it
			}
			out = append(out, sky_call(ct.toMsg, res))
		}
	}
	walk(cmd)
	return out
}

// spaIsClientOnlyResult reports whether a task result is the Err every
// `Std.Native` host stub returns (native_notjs.go): the capability exists only
// in the browser/webview, so the leaf belongs to the client.
func spaIsClientOnlyResult(res any) bool {
	if !isErrResult(res) {
		return false
	}
	return strings.Contains(Basics_errorToStringT(asSkyADT(extractErrResultValue(res))), "is a client-only capability")
}

// asSkyADT narrows an Err payload to the SkyADT the Error renderer takes; a
// foreign payload renders as an empty Error (never a panic).
func asSkyADT(v any) SkyADT {
	if a, ok := v.(SkyADT); ok {
		return a
	}
	return SkyADT{}
}

// spaFollowUpOutsideWireClass is the classified error a backend logs when a
// server branch's command produced a follow-up Msg that has no wire codec.
const spaFollowUpOutsideWireClass = "SpaFollowUpOutsideWire"

// spaLogFollowUpOutsideWire writes the classified error (a var so a host test
// can observe it).
var spaLogFollowUpOutsideWire = func(ctor string) {
	logEmit(logLevelError, "error",
		"sky.spa: a server branch's command produced the follow-up Msg `"+ctor+
			"`, which has no wire codec, so it was not sent to the client (see the build warning)",
		map[string]any{"class": spaFollowUpOutsideWireClass, "msg": ctor})
}

// Spa_followUpOutsideWire is the backend encoder arm for a Msg constructor the
// auto-split could not rule out as a follow-up (the branch's command could not
// be read statically) and cannot encode (no wire codec for an argument). The
// build warns about it; if one occurs at run time the Msg is dropped from the
// response and the drop is logged as a classified error, never silently (R1).
// It returns the empty item, which the generated encoder skips.
func Spa_followUpOutsideWire(ctor any) any {
	spaLogFollowUpOutsideWire(AsString(ctor))
	return []any{}
}
