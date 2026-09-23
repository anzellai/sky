package rt

import (
	"fmt"
	"strconv"
)

// spa_rpcqueue.go — the Sky.Spa client's RPC consistency core (portable, no
// build tag, so it is unit-tested on the host; the js wiring that sends a job
// and paints the result lives in live_wasm.go).
//
// Why it exists. Sky.Live runs the whole TEA loop on the server: every Msg is
// applied to the current model, in the order it was dispatched, one at a time.
// The auto-split moves each SERVER branch behind a `POST /_rpc/<Msg>` round
// trip, and before this core the client got that ordering wrong three ways:
//
//   - two quick server Msgs each built their request from the model at DISPATCH
//     time, so the second request carried a stale read-set (Inc twice -> 1);
//   - every perform ran on its own goroutine and dispatched in COMPLETION order,
//     so a slow first response overwrote a fast second one (name "a" after "ab");
//   - a response folded its write-set over the CURRENT model, so a client edit
//     made while the request was in flight was erased (a draft typed during Save).
//
// The fix gives the client Live's semantics exactly:
//
//  1. Server-branch RPCs are SERIALISED per client: one job in flight, the rest
//     queued in dispatch order.
//  2. A job's request is built when it is SENT, from the then-current model
//     (`mk snapshot rid`), never from the model at dispatch.
//  3. While a job is in flight every other Msg the client dispatches is applied
//     optimistically (the user sees it at once) AND recorded. When the response
//     arrives, the result Msg is applied to the SNAPSHOT the request was built
//     from — the model Live would have held at that point — and the recorded
//     Msgs are then REPLAYED on top, in order. The result is the model Live
//     computes for the same Msg sequence: a field the server branch writes takes
//     the server's value, and every later client edit is re-applied after it.
//     Replay is sound because a client arm is pure (the auto-split rule: pure ->
//     client, any effect -> server), so re-running it only recomputes a model;
//     its Cmd already ran on the first application and is discarded on replay.
//
// Each job carries a request id (`rid`) that is reused on a retry, so the
// backend can answer a re-sent request from its dedupe cache instead of running
// a non-idempotent effect twice (spa_rpc_dedupe.go).

// spaRpcJob is one queued server-branch RPC.
type spaRpcJob struct {
	// mk builds the request task from the model snapshot + the request id:
	// `model -> String -> Task Error a` (the generated `Spa.rpc` wraps the
	// branch's read-set build, the shared codec and the POST).
	mk any
	// toMsg maps the RPC Result to the Applied<Msg> constructor.
	toMsg any
	// residual is the optional CLIENT part of the server branch's command
	// (`model -> Cmd msg`, a Std.Native effect), run with the snapshot when the
	// job is sent (Spa.rpcWith).
	residual any
	// rid is the stable request id, reused verbatim by every retry.
	rid string
	// sent is true once the job left the queue head for the network.
	sent bool
	// snapshot is the model the request was built from (set when sent).
	snapshot any
	// log is every Msg the client dispatched after the job was sent, in order.
	log []any
}

// spaRpcQueue is the per-client FIFO of server-branch RPCs. jobs[0] is the job
// in flight once it is sent.
type spaRpcQueue struct {
	jobs  []*spaRpcJob
	nonce string
	seq   int
}

// newSpaRpcQueue builds a queue whose request ids are `<nonce>-<seq>`. The
// nonce is a per-page-load random value (the js driver supplies it), so ids
// from two tabs or two reloads never collide.
func newSpaRpcQueue(nonce string) *spaRpcQueue {
	return &spaRpcQueue{nonce: nonce}
}

// enqueue appends a job built from an `rpc` Cmd leaf and returns it.
func (q *spaRpcQueue) enqueue(mk, residual, toMsg any) *spaRpcJob {
	q.seq++
	j := &spaRpcJob{mk: mk, residual: residual, toMsg: toMsg, rid: q.nonce + "-" + strconv.Itoa(q.seq)}
	q.jobs = append(q.jobs, j)
	return j
}

// inFlight returns the job currently on the network, or nil.
func (q *spaRpcQueue) inFlight() *spaRpcJob {
	if len(q.jobs) > 0 && q.jobs[0].sent {
		return q.jobs[0]
	}
	return nil
}

// startHead marks the queue head as sent with `model` as its snapshot and
// returns it — or nil when a job is already in flight or the queue is empty.
// The caller then builds + sends the request from job.snapshot.
func (q *spaRpcQueue) startHead(model any) *spaRpcJob {
	if len(q.jobs) == 0 || q.jobs[0].sent {
		return nil
	}
	j := q.jobs[0]
	j.sent = true
	j.snapshot = model
	j.log = nil
	return j
}

// record appends a dispatched Msg to the in-flight job's replay log. A no-op
// when nothing is in flight (the Msg then needs no rebase).
func (q *spaRpcQueue) record(msg any) {
	if j := q.inFlight(); j != nil {
		j.log = append(j.log, msg)
	}
}

// complete settles the in-flight job with its result Msg. It applies `result`
// to the job's snapshot with `apply` (the unguarded update — a result Msg is
// runtime-internal), then replays the recorded Msgs with `replay` (the guarded
// update, so a replayed Msg is re-checked against the model Live would hold).
// It returns the rebased model and the result Msg's Cmd (which the caller must
// interpret: it is new work, e.g. a follow-up the server returned). The job is
// removed from the queue. If the replay panics (a client arm that is not pure
// after all), it falls back to applying `result` to `current` — the pre-fix
// behaviour — and returns the recovered panic in err so the caller reports it.
func (q *spaRpcQueue) complete(
	result, current any,
	apply func(msg, model any) SkyTuple2,
	replay func(msg, model any) SkyTuple2,
) (model any, cmd any, err error) {
	j := q.inFlight()
	if j == nil {
		pair := apply(result, current)
		return pair.V0, pair.V1, nil
	}
	q.jobs = q.jobs[1:]
	defer func() {
		if r := recover(); r != nil {
			pair := apply(result, current)
			model, cmd = pair.V0, pair.V1
			err = fmt.Errorf("rpc rebase replay panicked: %v", r)
		}
	}()
	pair := apply(result, j.snapshot)
	m := pair.V0
	for _, msg := range j.log {
		m = replay(msg, m).V0
	}
	return m, pair.V1, nil
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
