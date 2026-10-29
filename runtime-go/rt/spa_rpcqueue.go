package rt

import (
	"strconv"
	"strings"
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
//     completions of the server branch's own Cmds. One exception keeps the
//     same model: an input Msg the auto-split lists as hold-free for that
//     hold (it commutes with it, Spa.withHoldFree) runs at once when no other
//     Msg waits ahead of it (holdFreeNow).
//
// While a Msg from a form control's input event waits, the render leaves that
// control's value alone (typingSources, spa_typing.go): the model is behind
// what the control shows until the Msg runs.
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
	// name is the constructor of the Msg whose step issued this RPC (the
	// server branch), "" when no Msg was running (init's command). It keys
	// the hold-free table (spaSched.holdFree).
	name string
	// rid is the stable request id, reused verbatim by every retry.
	rid string
	// tick is the Sub.every interval (ms) whose tick issued this RPC; 0 for
	// any other Msg (spa_tick.go).
	tick int
}

// spaQueued is one Msg waiting to run. urgent marks a runtime-internal Msg
// that runs ahead of a hold: the hold RPC's own result (or its transport-error
// report), which must run to release the hold.
type spaQueued struct {
	msg    any
	urgent bool
	tick   int // the Sub.every interval this Msg is a tick of; 0 otherwise
	// src is the form control whose input or change event made this Msg (a
	// js.Value in the browser), nil for every other Msg. While a Msg from a
	// control waits, the render does not write that control's value: the
	// model is behind what the control shows (spaKeepTyped).
	src any
}

// spaSched is the per-client Msg scheduler.
type spaSched struct {
	queue    []spaQueued
	hold     *spaRpcJob
	running  bool
	inflight map[string]*spaRpcJob
	nonce    string
	seq      int
	// curTick is the Sub.every interval whose tick Msg is running now (0 when
	// the running Msg is not a tick). tickNet counts each interval's
	// tick-origin network work still unsettled; onTickNet is told when a tick
	// starts network work (spaTickGate.network).
	curTick   int
	tickNet   map[int]int
	onTickNet func(ms int)
	// curMsg is the Msg whose step runs now (nil between steps).
	curMsg any
	// holdFree maps a hold branch's constructor to the constructors of the
	// input Msgs that may run while it is in flight (Spa.withHoldFree, built
	// by the auto-split). The split lists a pair only when the two Msgs
	// commute: the input arm is client code with no command, and it reads and
	// writes no model field that the hold's result, the hold's follow-up arms
	// or `subscriptions` read or write. Empty: every Msg waits (the default).
	holdFree map[string]map[string]bool
}

// newSpaSched builds a scheduler whose request ids are `<nonce>-<seq>`. The
// nonce is a per-page-load random value (the js driver supplies it), so ids
// from two tabs or two reloads never collide.
func newSpaSched(nonce string) *spaSched {
	return &spaSched{nonce: nonce, inflight: map[string]*spaRpcJob{}, tickNet: map[int]int{}}
}

// dispatch queues msg behind every Msg that arrived before it and runs the
// queue. run is the TEA step (update, render, Cmds, subscriptions).
func (s *spaSched) dispatch(msg any, run func(any)) {
	s.queue = append(s.queue, spaQueued{msg: msg})
	s.drain(run)
}

// dispatchFrom is dispatch for a Msg that a form control's input or change
// event made; src identifies the control. The Msg keeps its place in the
// queue (once, in arrival order), and while it waits the render leaves that
// control's value alone (typingSources). One case runs it at once during a
// hold: the hold-free table lists it for the hold in flight, and no other Msg
// waits ahead of it, so running it now gives the same model as running it
// after the hold (holdFreeNow).
func (s *spaSched) dispatchFrom(msg, src any, run func(any)) {
	q := spaQueued{msg: msg, src: src}
	if s.holdFreeNow(msg) {
		// Runs ahead of the hold. No non-urgent Msg waits (holdFreeNow), so
		// appending it keeps arrival order.
		q.urgent = true
	}
	s.queue = append(s.queue, q)
	s.drain(run)
}

// holdFreeNow reports whether msg, an input Msg, may run now although a hold
// RPC is in flight: the split listed it as commuting with that hold, no step
// runs now, and every queued Msg is urgent (nothing ordinary waits ahead of it,
// so it cannot overtake one).
func (s *spaSched) holdFreeNow(msg any) bool {
	if s.hold == nil || s.running || s.hold.name == "" || len(s.holdFree) == 0 {
		return false
	}
	for _, q := range s.queue {
		if !q.urgent {
			return false
		}
	}
	free := s.holdFree[s.hold.name]
	name := spaMsgCtor(msg)
	return name != "" && free[name]
}

// setHoldFree installs the hold-free table from the split's "Hold Input"
// pairs (Spa.withHoldFree). A malformed entry is ignored, which only keeps
// that Msg waiting.
func (s *spaSched) setHoldFree(pairs []string) {
	for _, p := range pairs {
		f := strings.Fields(p)
		if len(f) != 2 {
			continue
		}
		if s.holdFree == nil {
			s.holdFree = map[string]map[string]bool{}
		}
		if s.holdFree[f[0]] == nil {
			s.holdFree[f[0]] = map[string]bool{}
		}
		s.holdFree[f[0]][f[1]] = true
	}
}

// typingSources returns the sources of the input Msgs still in the queue: the
// controls whose value the model has not caught up with yet.
func (s *spaSched) typingSources() []any {
	var out []any
	for _, q := range s.queue {
		if q.src != nil {
			out = append(out, q.src)
		}
	}
	return out
}

// spaMsgCtor is the constructor name of a Msg value, read without reflection
// (the wasm client): a sealed variant, a runtime ADT or an all-nullary enum.
// "" for anything else.
func spaMsgCtor(msg any) string {
	switch m := msg.(type) {
	case SkyVariant:
		return m.SkyVariantName()
	case SkyADT:
		return m.SkyName
	case interface{ SkyEnumName() string }:
		return m.SkyEnumName()
	}
	return ""
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
		s.curTick = q.tick
		s.curMsg = q.msg
		run(q.msg)
		s.curMsg = nil
		s.curTick = 0
	}
}

// issue registers an RPC the running step's Cmd started and returns its job.
// A hold RPC holds the queue from now until it settles.
func (s *spaSched) issue(mk, toMsg any, hold bool) *spaRpcJob {
	s.seq++
	j := &spaRpcJob{mk: mk, toMsg: toMsg, hold: hold, rid: s.nonce + "-" + strconv.Itoa(s.seq)}
	if s.curMsg != nil {
		j.name = spaMsgCtor(s.curMsg)
	}
	if s.curTick > 0 {
		j.tick = s.curTick
		s.tickNetBegin(j.tick)
	}
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
	if _, live := s.inflight[j.rid]; live && j.tick > 0 {
		s.tickNetEnd(j.tick)
	}
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

// dispatchTick queues a Sub.every tick Msg of interval ms. While it runs, the
// RPCs its Cmds issue are counted as that interval's tick-origin network work
// (spa_tick.go: coalescing and the hidden-page gate key on it).
func (s *spaSched) dispatchTick(msg any, ms int, run func(any)) {
	s.queue = append(s.queue, spaQueued{msg: msg, tick: ms})
	s.drain(run)
}

// dispatchUrgent runs a runtime-internal Msg ahead of a hold (a
// Sub.connection state change: the app must see "Reconnecting" while the hold
// RPC it reports on is still being re-sent).
func (s *spaSched) dispatchUrgent(msg any, run func(any)) {
	// After the urgent Msgs already queued (their order is kept), before the
	// first Msg a hold is holding back.
	i := 0
	for i < len(s.queue) && s.queue[i].urgent {
		i++
	}
	q := append([]spaQueued{}, s.queue[:i]...)
	q = append(q, spaQueued{msg: msg, urgent: true})
	s.queue = append(q, s.queue[i:]...)
	s.drain(run)
}

// tickNetBegin / tickNetEnd bracket one piece of tick-origin network work of
// interval ms (an RPC from issue to settle, or a client fetch).
func (s *spaSched) tickNetBegin(ms int) {
	s.tickNet[ms]++
	if s.onTickNet != nil {
		s.onTickNet(ms)
	}
}

func (s *spaSched) tickNetEnd(ms int) {
	if s.tickNet[ms] <= 1 {
		delete(s.tickNet, ms)
		return
	}
	s.tickNet[ms]--
}

// tickBusy reports whether interval ms still has tick-origin network work
// unsettled.
func (s *spaSched) tickBusy(ms int) bool {
	return s.tickNet[ms] > 0
}
