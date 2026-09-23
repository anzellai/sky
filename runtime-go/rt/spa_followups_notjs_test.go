//go:build !js

package rt

import "testing"

// SPA-3: a server branch's returned command RUNS — every perform leaf's task
// runs server-side and its follow-up Msg is returned, in command order; a
// Std.Native client-only leaf is skipped (the client runs it itself).
func TestSpaCollectFollowUps_RunsEveryPerformInOrder(t *testing.T) {
	ran := 0
	task := func(v any) any {
		return func() any { ran++; return Ok[any, any](v) }
	}
	toMsg := func(tag string) any {
		return func(r any) any { return tag }
	}
	cmd := cmdT{kind: "batch", batch: []any{
		cmdT{kind: "perform", task: task(1), toMsg: toMsg("Load")},
		cmdT{kind: "none"},
		cmdT{kind: "batch", batch: []any{
			cmdT{kind: "perform", task: task(2), toMsg: toMsg("GotA")},
			cmdT{kind: "perform", task: Native_clipboardWrite("x"), toMsg: toMsg("Copied")},
		}},
		cmdT{kind: "perform", task: task(3), toMsg: toMsg("GotB")},
	}}
	got := AsList(Spa_collectFollowUps(cmd))
	want := []string{"Load", "GotA", "GotB"}
	if len(got) != len(want) {
		t.Fatalf("follow-ups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("follow-ups = %v, want %v (in command order)", got, want)
		}
	}
	if ran != 3 {
		t.Fatalf("server tasks ran %d times, want 3", ran)
	}
}
