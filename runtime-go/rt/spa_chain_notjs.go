//go:build !js

package rt

// Sky.Spa server-internal effect chaining — the BACKEND settle kernel for a
// server RPC branch that returns a `Cmd.perform serverTask ToMsg` (or a
// `Cmd.batch` of them). Design: docs/skyspa/auto-split.md (server-internal
// chaining).
//
// The generated backend RPC handler (rust/crates/project/src/spa_split.rs
// gen_backend) runs the app's own `update` for the triggering Msg, then hands
// the returned command here to be settled ENTIRELY server-side, so the RPC
// answers with the final settled model diff. It references this as
// `Ffi.kernel "Spa_settleServerChain"`.
//
// This is the RPC analogue of `spaSsrSettle` (spa_ssr_notjs.go), forked with
// three deliberate differences:
//
//   a. It chases the command chain to a FIXPOINT (multi-round). An SSR settle
//      runs at most one read-round (a GET loads the page's data once); an RPC
//      must run the whole `Cmd.perform ... ToMsg` -> update -> `Cmd.perform ...`
//      chain to its settled end so the response carries the final model.
//   b. It does NOT enter the SSR-safe suppression (spa_ssr_safe.go). An SSR GET
//      must never mutate, so it self-suppresses destructive kernels; an RPC IS
//      a write, so the chained effects run for real.
//   c. It folds `perform` leaves + recurses `batch`, and accumulates any leaf
//      that is neither (a publish, an unrecognised residual) into a residual
//      command returned alongside the settled model.
//
// TERMINATION. A self-referential Msg (an `update` arm whose command re-performs
// a task that maps back to the same Msg) would loop forever. The settle is
// bounded by a hard cap on the number of performs it will run
// (spaChainMaxRounds); once the cap is hit, remaining commands are returned as
// residual instead of run, so the work-queue drains and the call terminates. It
// returns what settled rather than hanging.

import "reflect"

// spaChainMaxRounds bounds the total number of `perform` leaves the settle will
// run before it stops chasing the chain. It is a safety net against a
// re-dispatch cycle, not a functional limit — a real chain settles in a handful
// of hops. Kept generous so no legitimate chain is truncated.
const spaChainMaxRounds = 64

// Spa_settleServerChain is the `Ffi.kernel "Spa_settleServerChain"` alias the
// generated backend calls: `spaChainSettle_ model cmd update`. It folds every
// server-runnable `perform` leaf of `cmd` back through `update` to a fixpoint
// and returns `( settledModel, residualCmd )` as a `T2[any, any]`.
//
//   - `model`  : the model AFTER the triggering branch's own update ran.
//   - `cmd`    : that branch's returned command (a `cmdT`).
//   - `update` : the app's `update : Msg -> Model -> ( Model, Cmd Msg )`, a
//     typed 2-arg Go func dispatched reflect-tolerantly by SkyCall.
//
// The residual command carries any leaf the chain did not run server-side (a
// `publish` / `publishNoEcho`, or a command left over when the round cap was
// hit). The generated handler currently ignores it (`( mFinal, _ )`); it is
// part of the contract so a future backend can forward it.
func Spa_settleServerChain(model, cmd, update any) any {
	m := model
	// A simple work-queue over the command tree. `perform` runs + enqueues its
	// follow-up command; `batch` fans out; everything else is residual.
	queue := []any{cmd}
	var residual []any
	performs := 0
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		ct, ok := c.(cmdT)
		if !ok {
			// Not a command value — nothing to run, nothing to carry.
			continue
		}
		switch ct.kind {
		case "batch":
			queue = append(queue, ct.batch...)
		case "perform":
			performs++
			if performs > spaChainMaxRounds {
				// Round cap: a re-dispatch cycle must terminate. Stop running
				// performs and carry the rest as residual so the queue drains.
				residual = append(residual, c)
				continue
			}
			// Run the read (task : () -> Result), map its Result to a Msg, and
			// fold it — the same two-step shape runPerformBody / spaSsrSettle use.
			result := sky_call(ct.task, nil)
			msg := sky_call(ct.toMsg, result)
			// `update` returns a TYPED tuple `T2[Model, Cmd]` (not the erased
			// T2[any,any]); extract V0 (next model) + V1 (next command) by field.
			pair := SkyCall(update, msg, m)
			if next, ok := tupleFirstField(pair); ok {
				m = next
			}
			if follow, ok := tupleSecondField(pair); ok {
				queue = append(queue, follow)
			}
		case "none", "":
			// A no-op command — nothing to run, nothing to carry.
		default:
			// A publish / publishNoEcho / any other leaf is not run by the
			// server chain; carry it as residual for the caller.
			residual = append(residual, c)
		}
	}
	return T2[any, any]{V0: m, V1: spaChainResidual(residual)}
}

// spaChainResidual folds the leftover leaves into one command: `Cmd.none` for
// none, the single leaf for one, else a `Cmd.batch`.
func spaChainResidual(residual []any) cmdT {
	switch len(residual) {
	case 0:
		return Cmd_none()
	case 1:
		if c, ok := residual[0].(cmdT); ok {
			return c
		}
		return Cmd_none()
	default:
		return cmdT{kind: "batch", batch: residual}
	}
}

// tupleSecondField extracts field V1 of any `T2[A, B]` tuple value regardless of
// its concrete instantiation — `update`'s result is a TYPED `T2[Model, Cmd]`,
// which a `T2[any, any]` assertion would miss. Returns (value, true) for a
// struct carrying a V1 field, else (nil, false).
func tupleSecondField(pair any) (any, bool) {
	rv := reflect.ValueOf(pair)
	if rv.Kind() == reflect.Struct {
		if f := rv.FieldByName("V1"); f.IsValid() {
			return f.Interface(), true
		}
	}
	return nil, false
}
