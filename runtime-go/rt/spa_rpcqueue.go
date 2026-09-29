package rt

import (
	"strconv"
)

// spa_rpcqueue.go — the Sky.Spa client's Msg scheduler: the order in which Msgs
// and server-branch RPC results run through `update` (portable, no build tag,
// so it is unit-tested on the host; the js wiring that sends a request and
// paints the result lives in live_wasm.go).
//
// The contract is Sky.Live's TEA loop: every Msg's `update` runs EXACTLY ONCE,
// in arrival order, and the Cmd it returns runs. A server branch does not break
// that contract; it is a Msg whose `update` runs on the backend:
//
//   - An ASYNC RPC (the default) behaves like a `Cmd.perform` whose task is the
//     round trip. The server branch's own model write, if any, was computed in
//     the client when the Msg ran (the auto-split emits it there when it reads
//     no server data). The request is sent at once; several RPCs can be in
//     flight together. The response arrives as its own Msg (Applied<Msg>) and
//     joins the queue in arrival order, like any perform result. Msgs that
//     arrive while it is in flight run at once, against the current model, with
//     their Cmds.
//   - A HOLD RPC is a server branch whose own model write needs server data
//     (an inline `Task.run`, a server-tainted value), or whose continuation
//     chain settles on the server. Sky.Live runs such an update as one
//     synchronous step: nothing else runs on the session until it returns. The
//     client does the same: Msgs that arrive while a hold RPC is in flight wait
//     in the queue, in order, and run (once, with their Cmds) after the response
//     has been applied. The response's follow-up Msgs run first, as the
//     completions of the server branch's own Cmds.
//
// There is no optimistic apply, no snapshot and no replay. Before this the
// client applied Msgs during an RPC, then re-applied them on top of the answer.
// A re-run is not safe (a `Noise.encrypt` on a single-use state fails the
// second time), it moved the re-run Msgs after the result (a timer tick that
// saw `busy = True` saw `busy = False` in the replay), and it dropped the
// re-run Msgs' Cmds. Every Msg now runs once.
//
// Each RPC carries a request id (`rid`) that a retry reuses, so the backend
// answers a re-sent request from its dedupe cache instead of running a
// non-idempotent effect twice (spa_rpc_dedupe.go).

// spaRpcJob is one server-branch RPC in flight.
type spaRpcJob struct {
	// mk builds the request task from the request id: `String -> Task Error a`
	// (the generated `Spa.rpc` has already encoded the read-set + Msg args with
	// the shared codec, from the model the Msg ran on).
	mk any
	// toMsg maps the RPC Result to the Applied<Msg> constructor.
	toMsg any
	// hold is true for a hold RPC: later Msgs wait until it answers.
	hold bool
	// rid is the stable request id, reused verbatim by every retry.
	rid string
}

// spaQueued is one Msg waiting to run. urgent marks a runtime-internal Msg
// that runs ahead of a hold: the hold RPC's own result (or its transport-error
// report), which must run to release the hold.
type spaQueued struct {
	msg    any
	urgent bool
}

// spaSched is the per-client Msg scheduler.
type spaSched struct {
	queue    []spaQueued
	hold     *spaRpcJob
	running  bool
	inflight map[string]*spaRpcJob
	nonce    string
	seq      int
}

// newSpaSched builds a scheduler whose request ids are `<nonce>-<seq>`. The
// nonce is a per-page-load random value (the js driver supplies it), so ids
// from two tabs or two reloads never collide.
func newSpaSched(nonce string) *spaSched {
	return &spaSched{nonce: nonce, inflight: map[string]*spaRpcJob{}}
}

// dispatch queues msg behind every Msg that arrived before it and runs the
// queue. run is the TEA step (update, render, Cmds, subscriptions).
func (s *spaSched) dispatch(msg any, run func(any)) {
	s.queue = append(s.queue, spaQueued{msg: msg})
	s.drain(run)
}

// dispatchFirst puts msgs at the HEAD of the queue, in order, and runs the
// queue: the follow-up Msgs of a server branch, which are the completions of
// its own Cmds and run before the Msgs that waited behind it.
func (s *spaSched) dispatchFirst(msgs []any, run func(any)) {
	if len(msgs) == 0 {
		return
	}
	head := make([]spaQueued, 0, len(msgs)+len(s.queue))
	for _, m := range msgs {
		head = append(head, spaQueued{msg: m})
	}
	s.queue = append(head, s.queue...)
	s.drain(run)
}

// drain runs queued Msgs in order until the queue is empty or a hold RPC is in
// flight. It is not re-entrant: a Msg dispatched while a step runs (a DOM event
// a render fires, a follow-up) is queued and run by the outer loop, after the
// current step.
func (s *spaSched) drain(run func(any)) {
	if s.running {
		return
	}
	s.running = true
	defer func() { s.running = false }()
	for len(s.queue) > 0 {
		if s.hold != nil && !s.queue[0].urgent {
			return
		}
		q := s.queue[0]
		s.queue = s.queue[1:]
		run(q.msg)
	}
}

// issue registers an RPC the running step's Cmd started and returns its job.
// A hold RPC holds the queue from now until it settles.
func (s *spaSched) issue(mk, toMsg any, hold bool) *spaRpcJob {
	s.seq++
	j := &spaRpcJob{mk: mk, toMsg: toMsg, hold: hold, rid: s.nonce + "-" + strconv.Itoa(s.seq)}
	s.inflight[j.rid] = j
	if hold && s.hold == nil {
		s.hold = j
	}
	return j
}

// settle delivers an RPC's result Msg. An async result joins the queue in
// arrival order. A hold result runs first and releases the hold, and the Msgs
// that waited behind it then run in order.
func (s *spaSched) settle(j *spaRpcJob, resultMsg any, run func(any)) {
	delete(s.inflight, j.rid)
	if s.hold == j {
		s.hold = nil
		s.queue = append([]spaQueued{{msg: resultMsg, urgent: true}}, s.queue...)
	} else {
		s.queue = append(s.queue, spaQueued{msg: resultMsg})
	}
	s.drain(run)
}

// report delivers a transport-error Msg for an RPC that stays in flight (a
// network failure the Retry overlay will re-send). A hold RPC's report runs
// ahead of the Msgs it holds, so the app can show the failure; the hold stays.
func (s *spaSched) report(j *spaRpcJob, errMsg any, run func(any)) {
	if s.hold == j {
		s.queue = append([]spaQueued{{msg: errMsg, urgent: true}}, s.queue...)
	} else {
		s.queue = append(s.queue, spaQueued{msg: errMsg})
	}
	s.drain(run)
}

// pending reports whether any server-branch RPC is in flight.
func (s *spaSched) pending() bool {
	return len(s.inflight) > 0
}

// held reports whether a hold RPC is in flight (later Msgs wait).
func (s *spaSched) held() bool {
	return s.hold != nil
}

// spaGuardedUpdate runs `guard msg model` (when set) before `update`, the
// client half of `App.withGuard` — Sky.Live's dispatch does the same before
// every update (live.go). A guard that returns `Err` rejects the Msg: the model
// is kept and no Cmd runs. The backend re-checks every server branch (the
// trusted check); this client check gives pure branches the same behaviour
// Live has, where a rejected Msg never runs. Returns (pair, rejected, reason).
func spaGuardedUpdate(
	guard any,
	update func(msg, model any) SkyTuple2,
	msg, model any,
) (SkyTuple2, bool, any) {
	if guard != nil && isFunc(guard) {
		g := sky_call2(guard, msg, model)
		if isErrResult(g) {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}, true, extractErrResultValue(g)
		}
	}
	return update(msg, model), false, nil
}
