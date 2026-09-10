//go:build !js

package rt

// Sky.Spa client-result perform — the BACKEND kernel for a server RPC branch
// that returns `Cmd.perform serverTask ResultMsg` where `ResultMsg` is a CLIENT
// arm (pattern-2). Design: docs/skyspa/auto-split.md (client-result perform).
//
// The generated backend RPC handler (rust/crates/project/src/spa_split.rs
// gen_backend) runs the app's own `update` for the triggering Msg, then hands the
// returned command here. This kernel finds the single server `perform` leaf, RUNS
// its task server-side, and returns the task RESULT (`Result Error a`) — which the
// RPC answers with, so the wasm CLIENT can dispatch `ResultMsg result` through the
// client `update`. The server task NEVER crosses to the client; only its typed
// result does. It references this as `Ffi.kernel "Spa_runServerPerform"`.
//
// It is the pattern-2 complement of Spa_settleServerChain (spa_chain_notjs.go):
// that kernel FOLDS the task result back through `update` to a server-side
// fixpoint (the whole chain is server-side); this one RETURNS the result for the
// CLIENT to fold (the result Msg's arm is client-pure).

// Spa_runServerPerform is the `Ffi.kernel "Spa_runServerPerform"` alias the
// generated backend calls: `spaRunPerform_ cmd`. It walks `cmd` (a `cmdT` tree),
// runs the FIRST server `perform` leaf's task, and returns its `Result` value. A
// `batch` is searched; `none` / `publish` leaves are skipped. When no runnable
// perform is present (the auto-split analysis guarantees exactly one — this is
// defensive), it returns a classified `Err` rather than a malformed value.
func Spa_runServerPerform(cmd any) any {
	queue := []any{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		ct, ok := c.(cmdT)
		if !ok {
			continue
		}
		switch ct.kind {
		case "batch":
			queue = append(queue, ct.batch...)
		case "perform":
			// Run the task (`task : () -> Result`) and return its `Result` value
			// directly — the same first step spaChainSettle / spaSsrSettle take,
			// but WITHOUT mapping it back through a toMsg + update: the client does
			// that with the returned result.
			return sky_call(ct.task, nil)
		default:
			// "none" / "publish" / "publishNoEcho" / unrecognised — not a runnable
			// server task; keep draining the queue.
		}
	}
	return Err[any, any](makeError(2, "Ffi", "spaRunServerPerform: the branch's command carried no server task to run"))
}
