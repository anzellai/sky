//go:build !js

package rt

import "testing"

// Sky.Spa client-result perform (spa_perform_notjs.go, pattern-2). These prove
// the backend kernel RUNS the branch's server task and returns its RAW result —
// WITHOUT folding it back through `update` (that is Spa_settleServerChain's job).
// The result is what the RPC answers with, for the wasm client to dispatch.
// Design: docs/skyspa/auto-split.md.

// The pattern-2 shape: `Upload` returns `Cmd.perform (saveBlob data) Saved`. The
// kernel must run `saveBlob` and return its `Result` value (`Ok "blob.txt"`) —
// the raw result, NOT a model diff.
func TestSpaRunServerPerform_RunsTheTaskAndReturnsItsResult(t *testing.T) {
	ran := false
	task := func() any {
		ran = true
		return Ok[any, any]("blob.txt")
	}
	// A toMsg is present (the real command carries one), but the kernel must NOT
	// apply it — it returns the task result, not a mapped Msg.
	toSaved := func(r any) any { return map[string]any{"msg": "Saved", "result": r} }
	cmd := cmdT{kind: "perform", task: task, toMsg: toSaved}

	out := Spa_runServerPerform(cmd)
	if !ran {
		t.Fatal("Spa_runServerPerform must RUN the server task (saveBlob never fired)")
	}
	list, ok := out.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("Spa_runServerPerform must return the task RESULT as a one-element list, got %#v", out)
	}
	res, ok := list[0].(SkyResult[any, any])
	if !ok {
		t.Fatalf("Spa_runServerPerform must return the task RESULT (a Result), got %T", list[0])
	}
	if res.Tag != 0 || res.OkValue != "blob.txt" {
		t.Fatalf("must return the RAW task result Ok \"blob.txt\" (never folded through toMsg/update); got %+v", res)
	}
}

// A batched command (one server perform beside a no-op) must still resolve the
// perform.
func TestSpaRunServerPerform_FindsThePerformInABatch(t *testing.T) {
	task := func() any { return Ok[any, any]("saved-id") }
	perform := cmdT{kind: "perform", task: task, toMsg: func(r any) any { return r }}
	cmd := cmdT{kind: "batch", batch: []any{Cmd_none(), perform}}

	out := Spa_runServerPerform(cmd)
	list, ok := out.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("must find + run the server perform inside a batch; got %#v", out)
	}
	res, ok := list[0].(SkyResult[any, any])
	if !ok || res.Tag != 0 || res.OkValue != "saved-id" {
		t.Fatalf("must find + run the server perform inside a batch; got %#v", out)
	}
}

// A command with no runnable server perform (the branch took its `Cmd.none`
// leaf: `if model.flag then ( model, Cmd.perform t Got ) else ( model,
// Cmd.none )`) answers the EMPTY list: no task ran, so the client dispatches
// nothing, as Sky.Live does. It must never be an `Err`: the client would
// dispatch it as `Got (Err …)`, a failure the app never had. A batch of no-ops
// is the same answer.
func TestSpaRunServerPerform_NoPerformAnswersNoTaskRan(t *testing.T) {
	for name, cmd := range map[string]any{
		"none":          Cmd_none(),
		"batch of none": cmdT{kind: "batch", batch: []any{Cmd_none(), Cmd_none()}},
	} {
		out := Spa_runServerPerform(cmd)
		list, ok := out.([]any)
		if !ok || len(list) != 0 {
			t.Fatalf("%s: a command with no server perform must answer the empty list (no task ran), never an Err; got %#v", name, out)
		}
	}
}
