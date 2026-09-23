package rt

import (
	"testing"
)

// The Sky.Spa RPC consistency core (spa_rpcqueue.go), exercised host-side with
// a hand-written TEA app that mirrors the audit's race/order repros. The
// "server" is a function that computes a branch's write-set from the request's
// snapshot, exactly as the generated backend does from `init () + read-set`.

type rqModel struct {
	Count int
	Draft string
	Saved string
	Name  string
}

type rqInc struct{}
type rqDraft struct{ S string }
type rqSave struct{}
type rqSetName struct{ S string }

// Applied results carry the server's write-set.
type rqAppliedInc struct{ Count int }
type rqAppliedSave struct{ Saved, Draft string }
type rqAppliedName struct{ Name string }

// rqUpdate is the client update: server arms return the model unchanged plus
// an "rpc" Cmd (the queue job), pure arms edit locally, Applied arms fold the
// write-set into the model they are given.
func rqUpdate(q *spaRpcQueue) func(msg, model any) SkyTuple2 {
	return func(msg, model any) SkyTuple2 {
		m := model.(rqModel)
		none := cmdT{kind: "none"}
		switch v := msg.(type) {
		case rqInc:
			return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: "inc"}}
		case rqSave:
			return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: "save"}}
		case rqSetName:
			return SkyTuple2{V0: m, V1: cmdT{kind: "rpc", task: "name:" + v.S}}
		case rqDraft:
			m.Draft = v.S
			return SkyTuple2{V0: m, V1: none}
		case rqAppliedInc:
			m.Count = v.Count
			return SkyTuple2{V0: m, V1: none}
		case rqAppliedSave:
			m.Saved, m.Draft = v.Saved, v.Draft
			return SkyTuple2{V0: m, V1: none}
		case rqAppliedName:
			m.Name = v.Name
			return SkyTuple2{V0: m, V1: none}
		}
		return SkyTuple2{V0: m, V1: none}
	}
}

// rqServer answers a job from the model it was SENT with (the snapshot).
func rqServer(j *spaRpcJob) any {
	m := j.snapshot.(rqModel)
	switch k := j.mk.(string); {
	case k == "inc":
		return rqAppliedInc{Count: m.Count + 1}
	case k == "save":
		return rqAppliedSave{Saved: m.Draft, Draft: ""}
	case len(k) > 5 && k[:5] == "name:":
		return rqAppliedName{Name: k[5:]}
	}
	return nil
}

// rqClient drives the queue like live_wasm.go's step / interpretCmd / pump.
type rqClient struct {
	q     *spaRpcQueue
	model any
	upd   func(msg, model any) SkyTuple2
}

func newRqClient() *rqClient {
	q := newSpaRpcQueue("t")
	return &rqClient{q: q, model: rqModel{}, upd: rqUpdate(q)}
}

func (c *rqClient) step(msg any) {
	c.q.record(msg)
	pair := c.upd(msg, c.model)
	c.model = pair.V0
	if cmd, ok := pair.V1.(cmdT); ok && cmd.kind == "rpc" {
		c.q.enqueue(cmd.task, nil, nil)
	}
	c.q.startHead(c.model)
}

// respond settles the in-flight job with the server's answer and sends the
// next queued job.
func (c *rqClient) respond(t *testing.T) {
	t.Helper()
	j := c.q.inFlight()
	if j == nil {
		t.Fatal("no RPC in flight")
	}
	res := rqServer(j)
	m, _, err := c.q.complete(res, c.model, c.upd, c.upd)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	c.model = m
	c.q.startHead(c.model)
}

// SPA-1: two quick Inc clicks must count to 2 — the second request is built
// when it is SENT (after the first response), not from the dispatch-time model.
func TestSpaRpcQueue_TwoIncsCountToTwo(t *testing.T) {
	c := newRqClient()
	c.step(rqInc{})
	c.step(rqInc{})
	c.respond(t)
	c.respond(t)
	if got := c.model.(rqModel).Count; got != 2 {
		t.Fatalf("Inc x2: count = %d, want 2 (Sky.Live semantics)", got)
	}
}

// SPA-1: a draft typed while Save is in flight survives the response. Live
// applies Save (draft := "") and THEN the later keystrokes, so the draft is
// the new text and saved is the text at the time of Save.
func TestSpaRpcQueue_EditDuringFlightSurvivesResponse(t *testing.T) {
	c := newRqClient()
	c.step(rqDraft{S: "first"})
	c.step(rqSave{})
	c.step(rqDraft{S: "s"})
	c.step(rqDraft{S: "second"})
	c.respond(t)
	m := c.model.(rqModel)
	if m.Saved != "first" || m.Draft != "second" {
		t.Fatalf("Save+type: saved=%q draft=%q, want saved=\"first\" draft=\"second\"", m.Saved, m.Draft)
	}
}

// SPA-2: responses apply in dispatch order even when the server answers the
// second request first — serialisation means the second is not even sent
// until the first settles, so the last-dispatched value wins.
func TestSpaRpcQueue_ResponsesApplyInDispatchOrder(t *testing.T) {
	c := newRqClient()
	c.step(rqSetName{S: "a"})
	c.step(rqSetName{S: "ab"})
	if n := len(c.q.jobs); n != 2 {
		t.Fatalf("queued jobs = %d, want 2", n)
	}
	if c.q.jobs[1].sent {
		t.Fatal("second RPC was sent while the first is in flight (not serialised)")
	}
	c.respond(t)
	c.respond(t)
	if got := c.model.(rqModel).Name; got != "ab" {
		t.Fatalf("name = %q, want \"ab\"", got)
	}
}

// SPA-6: the request id is stable per job (a retry re-sends the same id) and
// unique across jobs.
func TestSpaRpcQueue_RequestIDsStableAndUnique(t *testing.T) {
	q := newSpaRpcQueue("n1")
	a := q.enqueue("x", nil, nil)
	b := q.enqueue("y", nil, nil)
	if a.rid == b.rid || a.rid != "n1-1" || b.rid != "n1-2" {
		t.Fatalf("rids = %q, %q", a.rid, b.rid)
	}
	q.startHead(rqModel{})
	// A retry of the in-flight job keeps its id (the driver re-sends job.rid).
	if q.inFlight().rid != "n1-1" {
		t.Fatalf("in-flight rid changed: %q", q.inFlight().rid)
	}
}

// A replay panic falls back to folding the result over the current model and
// reports the panic, rather than killing the client.
func TestSpaRpcQueue_ReplayPanicFallsBack(t *testing.T) {
	q := newSpaRpcQueue("p")
	q.enqueue("inc", nil, nil)
	q.startHead(rqModel{Count: 0})
	q.record(rqDraft{S: "x"})
	apply := rqUpdate(q)
	boom := func(msg, model any) SkyTuple2 { panic("impure replay") }
	m, _, err := q.complete(rqAppliedInc{Count: 1}, rqModel{Count: 0, Draft: "x"}, apply, boom)
	if err == nil {
		t.Fatal("expected the replay panic to be reported")
	}
	if got := m.(rqModel); got.Count != 1 || got.Draft != "x" {
		t.Fatalf("fallback model = %+v", got)
	}
	if len(q.jobs) != 0 {
		t.Fatal("job not removed after completion")
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
	upd := rqUpdate(nil)
	pair, rejected, _ := spaGuardedUpdate(guard, upd, rqDraft{S: "x"}, rqModel{Draft: "keep"})
	if !rejected || pair.V0.(rqModel).Draft != "keep" {
		t.Fatalf("guard did not reject: rejected=%v model=%+v", rejected, pair.V0)
	}
	pair, rejected, _ = spaGuardedUpdate(guard, upd, rqInc{}, rqModel{})
	if rejected {
		t.Fatal("guard rejected an allowed Msg")
	}
	if c, _ := pair.V1.(cmdT); c.kind != "rpc" {
		t.Fatalf("allowed Msg lost its Cmd: %+v", pair.V1)
	}
}
