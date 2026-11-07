package rt

import (
	"reflect"
	"testing"
)

func TestSpaPersistStepReferenceOrderAndCounts(t *testing.T) {
	prev := `{"session":{"id":"fixture"},"text":"before"}`
	next := `{"session":null,"text":"after"}`
	var trace []string
	var measured spaPersistMetrics
	cleared := spaPersistStep(prev, next, func(v any) string {
		if v == next {
			trace = append(trace, "encode-next")
		} else {
			trace = append(trace, "encode-prev")
		}
		return v.(string)
	}, func(s string) {
		trace = append(trace, "write-next")
		if s != next {
			t.Fatal("writer received a different model")
		}
	}, []string{"session"}, func(m spaPersistMetrics) { measured = m })
	if !cleared || !reflect.DeepEqual(trace, []string{"encode-next", "write-next", "encode-prev"}) {
		t.Fatalf("reference persistence order/decision changed: cleared=%v trace=%v", cleared, trace)
	}
	if measured.Encodes != 2 || measured.EncodedBytes != len(prev)+len(next) {
		t.Fatalf("incorrect work counters: %+v", measured)
	}
}

func TestSpaPersistStepReferenceFailuresAndNoSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prev      string
		next      string
		protected []string
		encodes   int
		writes    int
	}{
		{"no session", `{}`, `{}`, nil, 1, 1},
		{"next encode failure", `{"session":{}}`, "", []string{"session"}, 2, 0},
		{"previous encode failure", "", `{"session":null}`, []string{"session"}, 2, 1},
		{"invalid next JSON", `{"session":{}}`, "invalid", []string{"session"}, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, writes := 0, 0
			cleared := spaPersistStep(tc.prev, tc.next, func(v any) string {
				calls++
				return v.(string)
			}, func(string) { writes++ }, tc.protected, nil)
			if cleared || calls != tc.encodes || writes != tc.writes {
				t.Fatalf("reference failure semantics changed: cleared=%v encodes=%d writes=%d", cleared, calls, writes)
			}
		})
	}
}

func TestSpaPersistStepDoesNotSuppressUnchangedModelCommands(t *testing.T) {
	// Persistence is downstream of the full transition. A model that remains
	// unchanged is still allowed to start work and reconcile subscriptions.
	updates, renders, posts := 0, 0, 0
	model := `{"session":null,"text":"same"}`
	next := spaTransition(nil, model, func(_, m any) SkyTuple2 {
		updates++
		return SkyTuple2{V0: m, V1: "start-work"}
	}, func(any) { renders++ }, func(cmd any) {
		posts++
		if cmd != "start-work" {
			t.Fatal("command changed")
		}
	}, func(string, any) { t.Fatal("unexpected panic") })
	spaPersistStep(model, next, func(v any) string { return v.(string) }, func(string) {}, []string{"session"}, nil)
	if updates != 1 || renders != 1 || posts != 1 {
		t.Fatalf("unchanged model suppressed transition work: %d/%d/%d", updates, renders, posts)
	}
}
