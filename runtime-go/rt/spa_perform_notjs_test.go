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
	res, ok := out.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Spa_runServerPerform must return the task RESULT (a Result), got %T", out)
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
	res, ok := out.(SkyResult[any, any])
	if !ok || res.Tag != 0 || res.OkValue != "saved-id" {
		t.Fatalf("must find + run the server perform inside a batch; got %#v", out)
	}
}

// Defensive: a command with no runnable server perform returns a classified Err
// (never a malformed value that would break the response codec). The auto-split
// analysis guarantees one perform, so this only guards a mis-generation.
func TestSpaRunServerPerform_NoPerformReturnsClassifiedErr(t *testing.T) {
	out := Spa_runServerPerform(Cmd_none())
	res, ok := out.(SkyResult[any, any])
	if !ok || res.Tag != 1 {
		t.Fatalf("a command with no server perform must return an Err Result; got %#v", out)
	}
}
