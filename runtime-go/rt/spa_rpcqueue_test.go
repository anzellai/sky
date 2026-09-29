package rt

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// The Sky.Spa client Msg scheduler (spa_rpcqueue.go), exercised host-side with
// a hand-written TEA app shaped like the generated client: pure arms edit the
// model, a server arm returns an "rpc" Cmd (async or hold), and the server
// answers from the request it was sent. The property test at the end drives
// random Msg sequences with interleaved RPC completions through the scheduler
// and through a reference Sky.Live session, and requires the same update trace.

type rqModel struct {
	Count   int
	Draft   string
	Saved   string
	Busy    bool
	Started int
	Done    int
	Ticks   int
	Acc     int
	// State is a single-use state value (a Noise transport): each Seal spends
	// it. The old rebase re-ran Seal on a snapshot, spending a value twice.
	State int
}

// Client Msgs (each carries an id, so the test can count how often it ran).
type rqDraft struct {
	ID int
	S  string
}
type rqSeal struct{ ID int }
type rqTick struct{ ID int }

// Server Msgs: rqInc and rqSave are HOLD branches (their model write needs
// server data); rqCall is an ASYNC branch (its own model write, if any, runs in
// the client; its result is dispatched as rqGot when the round trip ends).
type rqInc struct{ ID int }
type rqSave struct{ ID int }
type rqCall struct {
	ID int
	N  int
}

// Result Msgs (runtime-internal Applied<Msg> shapes).
type rqAppliedInc struct {
	ID    int
	Count int
}
type rqAppliedSave struct {
	ID           int
	Saved, Draft string
}
type rqGot struct {
	ID int
	V  int
}

// rqReq is what the generated `Spa.rpc` encodes: the branch + its read-set,
// taken from the model the Msg ran on.
type rqReq struct {
	kind  string
	id    int
	count int
	draft string
	n     int
	acc   int
}

// rqAnswer is the backend: it answers from the request alone.
func rqAnswer(r rqReq) any {
	switch r.kind {
	case "inc":
		return rqAppliedInc{ID: r.id, Count: r.count + 1}
	case "save":
		return rqAppliedSave{ID: r.id, Saved: r.draft, Draft: ""}
	case "call":
		return rqGot{ID: r.id, V: r.n*10 + r.acc%7}
	}
	panic("unknown rpc " + r.kind)
}

// rqClientUpdate is the generated client `update`.
func rqClientUpdate(msg, model any) SkyTuple2 {
	m := model.(rqModel)
	none := cmdT{kind: "none"}
	switch v := msg.(type) {
	case rqDraft:
		m.Draft = v.S
		return SkyTuple2{V0: m, V1: none}
	case rqSeal:
		m.State++
		return SkyTuple2{V0: m, V1: none}
	case rqTick:
		m.Ticks++
		if m.Busy {
			return SkyTuple2{V0: m, V1: none}
		}
		m.Busy = true
		m.Started++
		return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: rqReq{kind: "call", id: v.ID, n: m.Started, acc: m.Acc}, payload: false}}
	case rqInc:
		return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: rqReq{kind: "inc", id: v.ID, count: m.Count}, payload: true}}
	case rqSave:
		return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: rqReq{kind: "save", id: v.ID, draft: m.Draft}, payload: true}}
	case rqCall:
		// The async branch's own model write runs here, in the client.
		m.Acc += v.N
		return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: rqReq{kind: "call", id: v.ID, n: v.N, acc: m.Acc}, payload: false}}
	case rqAppliedInc:
		m.Count = v.Count
		return SkyTuple2{V0: m, V1: none}
	case rqAppliedSave:
		m.Saved, m.Draft = v.Saved, v.Draft
		return SkyTuple2{V0: m, V1: none}
	case rqGot:
		m.Busy = false
		m.Done++
		m.Acc = m.Acc*3 + v.V
		return SkyTuple2{V0: m, V1: none}
	}
	panic(fmt.Sprintf("unknown msg %T", msg))
}

// rqMsgID is the label + id a Msg carries (result Msgs carry their branch's id).
func rqMsgID(msg any) string {
	switch v := msg.(type) {
	case rqDraft:
		return fmt.Sprintf("draft#%d", v.ID)
	case rqSeal:
		return fmt.Sprintf("seal#%d", v.ID)
	case rqTick:
		return fmt.Sprintf("tick#%d", v.ID)
	case rqInc:
		return fmt.Sprintf("inc#%d", v.ID)
	case rqSave:
		return fmt.Sprintf("save#%d", v.ID)
	case rqCall:
		return fmt.Sprintf("call#%d", v.ID)
	case rqAppliedInc:
		return fmt.Sprintf("inc.result#%d", v.ID)
	case rqAppliedSave:
		return fmt.Sprintf("save.result#%d", v.ID)
	case rqGot:
		return fmt.Sprintf("call.result#%d", v.ID)
	}
	return fmt.Sprintf("%T", msg)
}

// rqSpa is the scheduler-driven client: step = update + interpret the Cmd.
type rqSpa struct {
	s     *spaSched
	model rqModel
	trace []string
	jobs  map[*spaRpcJob]rqReq
	order []*spaRpcJob
}

func newRqSpa() *rqSpa {
	return &rqSpa{s: newSpaSched("t"), jobs: map[*spaRpcJob]rqReq{}}
}

func (c *rqSpa) step(msg any) {
	pair := rqClientUpdate(msg, c.model)
	c.model = pair.V0.(rqModel)
	c.trace = append(c.trace, rqMsgID(msg))
	if cmd, ok := pair.V1.(cmdT); ok && cmd.kind == "rpc" {
		hold, _ := cmd.payload.(bool)
		j := c.s.issue(cmd.task, nil, hold)
		c.jobs[j] = cmd.task.(rqReq)
		c.order = append(c.order, j)
	}
}

func (c *rqSpa) dispatch(msg any) { c.s.dispatch(msg, c.step) }

// answer settles the in-flight job at index i of the in-flight list.
func (c *rqSpa) answer(i int) {
	j := c.order[i]
	c.order = append(c.order[:i:i], c.order[i+1:]...)
	res := rqAnswer(c.jobs[j])
	delete(c.jobs, j)
	c.s.settle(j, res, c.step)
}

// Bug 1 (downstream repro: a spent Noise state after an RPC): a client arm
// that spends a single-use state while an RPC is in flight must run ONCE. The
// old rebase re-ran it on the send-time snapshot, which spent the old state a
// second time ("this state value was already used").
func TestSpaSched_ClientArmDuringRpcRunsOnce(t *testing.T) {
	c := newRqSpa()
	c.dispatch(rqCall{ID: 1, N: 1})
	c.dispatch(rqSeal{ID: 2})
	if c.model.State != 1 {
		t.Fatalf("seal did not run while the RPC was in flight: state=%d", c.model.State)
	}
	c.answer(0)
	if c.model.State != 1 {
		t.Fatalf("seal re-ran after the RPC answered: state=%d, want 1", c.model.State)
	}
	want := []string{"call#1", "seal#2", "call.result#1"}
	if !reflect.DeepEqual(c.trace, want) {
		t.Fatalf("trace = %v, want %v", c.trace, want)
	}
}

// Bug 2 (downstream repro: a timer that stalls after an RPC): a tick that runs
// while the call is in flight sees busy = True and does nothing; the result
// clears busy; the NEXT tick starts the next call and its Cmd runs.
func TestSpaSched_TimerKeepsCalling(t *testing.T) {
	c := newRqSpa()
	id := 0
	tick := func() { id++; c.dispatch(rqTick{ID: id}) }
	for round := 0; round < 5; round++ {
		tick() // starts a call
		tick()
		tick() // two ticks during the call: busy, nothing started
		if n := len(c.order); n != 1 {
			t.Fatalf("round %d: %d calls in flight, want 1", round, n)
		}
		c.answer(0)
	}
	if c.model.Started != 5 || c.model.Done != 5 || c.model.Busy || c.model.Ticks != 15 {
		t.Fatalf("model = %+v, want started=5 done=5 ticks=15 busy=false", c.model)
	}
}

// A hold branch (Inc reads the server's count): the second Inc waits behind the
// first, so it reads the first one's result — Inc twice counts to 2.
func TestSpaSched_HoldSerialisesDependentMsgs(t *testing.T) {
	c := newRqSpa()
	c.dispatch(rqInc{ID: 1})
	c.dispatch(rqInc{ID: 2})
	if len(c.order) != 1 {
		t.Fatalf("second Inc sent while the first holds: %d in flight", len(c.order))
	}
	c.answer(0)
	c.answer(0)
	if c.model.Count != 2 {
		t.Fatalf("Inc x2: count = %d, want 2", c.model.Count)
	}
}

// A draft typed while Save (a hold branch) is in flight is applied after the
// response, as Live applies it after its synchronous Save.
func TestSpaSched_EditDuringHoldAppliesAfter(t *testing.T) {
	c := newRqSpa()
	c.dispatch(rqDraft{ID: 1, S: "first"})
	c.dispatch(rqSave{ID: 2})
	c.dispatch(rqDraft{ID: 3, S: "s"})
	c.dispatch(rqDraft{ID: 4, S: "second"})
	c.answer(0)
	if c.model.Saved != "first" || c.model.Draft != "second" {
		t.Fatalf("Save+type: saved=%q draft=%q, want saved=first draft=second", c.model.Saved, c.model.Draft)
	}
}

// Async RPCs overlap: two are in flight together and their results run in
// completion order.
func TestSpaSched_AsyncRpcsRunConcurrently(t *testing.T) {
	c := newRqSpa()
	c.dispatch(rqCall{ID: 1, N: 1})
	c.dispatch(rqCall{ID: 2, N: 2})
	if len(c.order) != 2 || !c.s.pending() {
		t.Fatalf("in flight = %d, want 2", len(c.order))
	}
	c.answer(1) // the second answers first
	c.answer(0)
	want := []string{"call#1", "call#2", "call.result#2", "call.result#1"}
	if !reflect.DeepEqual(c.trace, want) || c.s.pending() {
		t.Fatalf("trace = %v, want %v", c.trace, want)
	}
}

// Follow-up Msgs of a hold branch run before the Msgs that waited behind it;
// a Msg dispatched while a step runs waits for that step.
func TestSpaSched_FollowUpsRunFirstAndNoReentry(t *testing.T) {
	s := newSpaSched("f")
	var trace []string
	var run func(any)
	run = func(m any) {
		trace = append(trace, m.(string))
		switch m {
		case "a":
			// A Msg dispatched from inside a step (a DOM event fired by the
			// render) runs after the step, not inside it.
			s.dispatch("inner", run)
			trace = append(trace, "a-end")
		case "result":
			s.dispatchFirst([]any{"f1", "f2"}, run)
		}
	}
	j := s.issue(nil, nil, true)
	s.dispatch("a", run) // held
	s.dispatch("b", run)
	if len(trace) != 0 {
		t.Fatalf("ran while held: %v", trace)
	}
	s.settle(j, "result", run)
	// "b" arrived before "inner", so it runs first.
	want := []string{"result", "f1", "f2", "a", "a-end", "b", "inner"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
}

// A hold RPC's transport-error report runs ahead of the Msgs it holds, and the
// hold stays until the retried request settles.
func TestSpaSched_HoldErrorReportRunsAndHoldStays(t *testing.T) {
	s := newSpaSched("e")
	var trace []string
	run := func(m any) { trace = append(trace, m.(string)) }
	j := s.issue(nil, nil, true)
	s.dispatch("later", run)
	s.report(j, "err", run)
	if !reflect.DeepEqual(trace, []string{"err"}) || !s.held() {
		t.Fatalf("after report: trace=%v held=%v", trace, s.held())
	}
	s.settle(j, "ok", run)
	if !reflect.DeepEqual(trace, []string{"err", "ok", "later"}) || s.held() || s.pending() {
		t.Fatalf("after settle: trace=%v held=%v pending=%v", trace, s.held(), s.pending())
	}
}

// An async RPC's transport-error report joins the queue like any result.
func TestSpaSched_AsyncErrorReportQueuesInOrder(t *testing.T) {
	s := newSpaSched("a")
	var trace []string
	run := func(m any) { trace = append(trace, m.(string)) }
	h := s.issue(nil, nil, true)
	a := s.issue(nil, nil, false)
	s.dispatch("later", run)
	s.report(a, "async-err", run) // waits behind the hold, after "later"
	s.settle(h, "hold-ok", run)
	s.settle(a, "async-ok", run)
	want := []string{"hold-ok", "later", "async-err", "async-ok"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
}

// SPA-6: the request id is stable per job (a retry re-sends job.rid) and
// unique across jobs.
func TestSpaSched_RequestIDsStableAndUnique(t *testing.T) {
	s := newSpaSched("n1")
	a := s.issue("x", nil, false)
	b := s.issue("y", nil, false)
	if a.rid == b.rid || a.rid != "n1-1" || b.rid != "n1-2" {
		t.Fatalf("rids = %q, %q", a.rid, b.rid)
	}
}

// ── The Live reference ────────────────────────────────────────────────────

// rqLive is a Sky.Live session: update runs on the server, one Msg at a time.
// A hold branch's update runs to completion at dispatch (its effect is inline)
// and keeps the session busy until the effect returns: Msgs and perform
// results that arrive meanwhile wait, in arrival order. An async branch's own
// model write runs at dispatch and its perform result arrives later as a Msg.
type rqLive struct {
	model   rqModel
	trace   []string
	busy    bool
	queue   []any
	pending []rqReq // async performs in flight, in dispatch order
}

func (l *rqLive) run(msg any) {
	switch v := msg.(type) {
	case rqInc:
		l.trace = append(l.trace, rqMsgID(msg))
		l.model.Count = rqAnswer(rqReq{kind: "inc", id: v.ID, count: l.model.Count}).(rqAppliedInc).Count
		l.busy = true
		return
	case rqSave:
		l.trace = append(l.trace, rqMsgID(msg))
		res := rqAnswer(rqReq{kind: "save", id: v.ID, draft: l.model.Draft}).(rqAppliedSave)
		l.model.Saved, l.model.Draft = res.Saved, res.Draft
		l.busy = true
		return
	}
	l.trace = append(l.trace, rqMsgID(msg))
	pair := rqClientUpdate(msg, l.model)
	l.model = pair.V0.(rqModel)
	if cmd, ok := pair.V1.(cmdT); ok && cmd.kind == "rpc" {
		l.pending = append(l.pending, cmd.task.(rqReq))
	}
}

func (l *rqLive) arrive(msg any) {
	if l.busy {
		l.queue = append(l.queue, msg)
		return
	}
	l.run(msg)
}

// finishHold ends the hold in progress and runs what waited, in order, until
// another hold starts.
func (l *rqLive) finishHold() {
	l.busy = false
	for len(l.queue) > 0 && !l.busy {
		m := l.queue[0]
		l.queue = l.queue[1:]
		l.run(m)
	}
}

// rqSpaLogicalTrace maps the Spa trace onto Live's update invocations: a hold
// branch runs as ONE update on Live (the effect is inline), and as a client
// step that only sends the request plus the result on the Spa client. Nothing
// else runs between the two on the client (the queue is held), so the client
// step is dropped and the result stands for the branch's update.
func rqSpaLogicalTrace(tr []string) []string {
	out := make([]string, 0, len(tr))
	for _, e := range tr {
		switch {
		case strings.HasPrefix(e, "inc#"), strings.HasPrefix(e, "save#"):
			continue
		case strings.HasPrefix(e, "inc.result#"):
			out = append(out, "inc#"+strings.TrimPrefix(e, "inc.result#"))
		case strings.HasPrefix(e, "save.result#"):
			out = append(out, "save#"+strings.TrimPrefix(e, "save.result#"))
		default:
			out = append(out, e)
		}
	}
	return out
}

// TestSpaSched_MatchesLiveOnRandomInterleavings is the differential property:
// random Msg sequences with interleaved RPC completions give the SAME update
// trace and the SAME model on the Sky.Spa client as on a Sky.Live session, and
// every Msg's update runs exactly once.
func TestSpaSched_MatchesLiveOnRandomInterleavings(t *testing.T) {
	for seed := int64(1); seed <= 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		spa := newRqSpa()
		live := &rqLive{}
		id := 0
		for op := 0; op < 80; op++ {
			switch k := r.Intn(10); {
			case k < 6:
				id++
				var msg any
				switch r.Intn(6) {
				case 0:
					msg = rqDraft{ID: id, S: fmt.Sprintf("d%d", id)}
				case 1:
					msg = rqSeal{ID: id}
				case 2:
					msg = rqTick{ID: id}
				case 3:
					msg = rqInc{ID: id}
				case 4:
					msg = rqSave{ID: id}
				default:
					msg = rqCall{ID: id, N: r.Intn(5) + 1}
				}
				spa.dispatch(msg)
				live.arrive(msg)
			case k < 8:
				// An async round trip ends (Spa: its response; Live: its
				// perform result): the same job on both sides.
				var asyncIdx []int
				for i, j := range spa.order {
					if !j.hold {
						asyncIdx = append(asyncIdx, i)
					}
				}
				if len(live.pending) != len(asyncIdx) {
					t.Fatalf("seed %d: async in flight: spa %d live %d", seed, len(asyncIdx), len(live.pending))
				}
				if len(asyncIdx) == 0 {
					continue
				}
				pick := r.Intn(len(asyncIdx))
				req := live.pending[pick]
				live.pending = append(live.pending[:pick:pick], live.pending[pick+1:]...)
				spa.answer(asyncIdx[pick])
				live.arrive(rqAnswer(req))
			default:
				// The hold in progress returns.
				hi := -1
				for i, j := range spa.order {
					if j.hold {
						hi = i
					}
				}
				if (hi >= 0) != live.busy {
					t.Fatalf("seed %d: hold in flight: spa %v live %v", seed, hi >= 0, live.busy)
				}
				if hi < 0 {
					continue
				}
				spa.answer(hi)
				live.finishHold()
			}
		}
		// Let every round trip end: the hold first, then the async ones in
		// dispatch order, on both sides.
		for len(spa.order) > 0 {
			hi := -1
			for i, j := range spa.order {
				if j.hold {
					hi = i
				}
			}
			if hi >= 0 {
				spa.answer(hi)
				live.finishHold()
				continue
			}
			req := live.pending[0]
			live.pending = live.pending[1:]
			spa.answer(0)
			live.arrive(rqAnswer(req))
		}
		got := rqSpaLogicalTrace(spa.trace)
		if !reflect.DeepEqual(got, live.trace) {
			t.Fatalf("seed %d: update trace differs\n spa:  %v\n live: %v", seed, got, live.trace)
		}
		if spa.model != live.model {
			t.Fatalf("seed %d: model differs\n spa:  %+v\n live: %+v", seed, spa.model, live.model)
		}
		seen := map[string]int{}
		for _, e := range got {
			seen[e]++
			if seen[e] > 1 {
				t.Fatalf("seed %d: %s ran %d times", seed, e, seen[e])
			}
		}
	}
}

// SPA-7: every failed perform is kept and retried in failure order; a later
// failure never replaces an earlier one.
func TestSpaRetryQueue_KeepsEveryFailureInOrder(t *testing.T) {
	var ran []string
	var q []func()
	q = spaAppendRetry(q, func() { ran = append(ran, "hit") })
	q = spaAppendRetry(q, func() { ran = append(ran, "inc") })
	q = spaAppendRetry(q, nil)
	if len(q) != 2 {
		t.Fatalf("pending retries = %d, want 2", len(q))
	}
	spaRunRetries(q)
	if len(ran) != 2 || ran[0] != "hit" || ran[1] != "inc" {
		t.Fatalf("retried %v, want [hit inc]", ran)
	}
}

// SPA-4: the client guard rejects a Msg the way Live does — model kept, no Cmd.
func TestSpaGuardedUpdate_RejectsLikeLive(t *testing.T) {
	guard := func(msg, model any) any {
		if _, ok := msg.(rqDraft); ok {
			return Err[any, any]("denied")
		}
		return Ok[any, any](struct{}{})
	}
	pair, rejected, _ := spaGuardedUpdate(guard, rqClientUpdate, rqDraft{S: "x"}, rqModel{Draft: "keep"})
	if !rejected || pair.V0.(rqModel).Draft != "keep" {
		t.Fatalf("guard did not reject: rejected=%v model=%+v", rejected, pair.V0)
	}
	pair, rejected, _ = spaGuardedUpdate(guard, rqClientUpdate, rqInc{}, rqModel{})
	if rejected {
		t.Fatal("guard rejected an allowed Msg")
	}
	if c, _ := pair.V1.(cmdT); c.kind != "rpc" {
		t.Fatalf("allowed Msg lost its Cmd: %+v", pair.V1)
	}
}
