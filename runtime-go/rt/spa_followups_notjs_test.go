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

// R1: a follow-up Msg outside the wire set is dropped with a classified error
// log, never silently, and yields the empty item the encoder skips.
func TestSpaFollowUpOutsideWire_LogsAClassifiedErrorAndYieldsNothing(t *testing.T) {
	var logged []string
	prev := spaLogFollowUpOutsideWire
	spaLogFollowUpOutsideWire = func(ctor string) { logged = append(logged, ctor) }
	defer func() { spaLogFollowUpOutsideWire = prev }()
	got := AsList(Spa_followUpOutsideWire("GotConfig"))
	if len(got) != 0 {
		t.Fatalf("an outside-wire follow-up must encode as the empty item; got %v", got)
	}
	if len(logged) != 1 || logged[0] != "GotConfig" {
		t.Fatalf("the drop must be logged once, naming the constructor; got %v", logged)
	}
	if spaFollowUpOutsideWireClass != "SpaFollowUpOutsideWire" {
		t.Fatalf("classified error name changed: %q", spaFollowUpOutsideWireClass)
	}
}
