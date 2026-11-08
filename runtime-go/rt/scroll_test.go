package rt

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNavScrollPayloads(t *testing.T) {
	id := `odd[x]#" literal`
	tests := []struct {
		cmd  SkyCmd
		want scrollCmd
		wire string
	}{
		{Nav_scrollToTop(), scrollCmd{Top: true}, `{"target":"","top":true,"ifNeeded":false}`},
		{Nav_scrollIntoView(id), scrollCmd{Target: id}, ""},
		{Nav_scrollIntoViewIfNeeded(id), scrollCmd{Target: id, IfNeeded: true}, ""},
	}
	for _, tt := range tests {
		c := tt.cmd
		got, ok := scrollCmdOf(c)
		if c.kind != "scroll" || !ok || got != tt.want || c.task != nil || c.toMsg != nil || len(c.batch) != 0 {
			t.Fatalf("unexpected scroll command: %#v", c)
		}
		wire, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if tt.wire != "" && string(wire) != tt.wire {
			t.Fatalf("wire=%s", wire)
		}
		var decoded scrollCmd
		if err := json.Unmarshal(wire, &decoded); err != nil || decoded != got {
			t.Fatalf("payload roundtrip: %v", err)
		}
	}
	if _, ok := scrollCmdOf(cmdT{payload: "foreign"}); ok {
		t.Fatal("accepted foreign payload")
	}
}

func TestSpaScrollQueueLatestHeldRenderOnly(t *testing.T) {
	var q spaScrollQueue
	var got []scrollCmd
	apply := func(c scrollCmd) { got = append(got, c) }
	old := scrollCmd{Target: "same-id"}
	fresh := scrollCmd{Target: "same-id", IfNeeded: true}
	q.committed(true, false, apply)
	q.command(old, apply)
	q.committed(true, false, apply)
	q.command(fresh, apply)
	q.committed(true, true, apply)
	if len(got) != 0 {
		t.Fatal("held commands ran before commit")
	}
	q.committed(false, true, apply)
	q.committed(false, true, apply)
	if !reflect.DeepEqual(got, []scrollCmd{fresh}) || q.pending != nil {
		t.Fatalf("stale/replayed command: %#v", got)
	}
}

func TestSpaScrollQueueNewCommittedRenderDiscardsOld(t *testing.T) {
	var q spaScrollQueue
	var got []scrollCmd
	apply := func(c scrollCmd) { got = append(got, c) }
	q.committed(true, false, apply)
	q.command(scrollCmd{Target: "same-id"}, apply)
	q.committed(false, false, apply)
	q.command(scrollCmd{Target: "new"}, apply)
	if !reflect.DeepEqual(got, []scrollCmd{{Target: "new"}}) {
		t.Fatalf("stale command escaped: %#v", got)
	}
}

func TestSpaScrollQueueSameRenderOrder(t *testing.T) {
	var q spaScrollQueue
	var got []scrollCmd
	apply := func(c scrollCmd) { got = append(got, c) }
	want := []scrollCmd{{Top: true}, {Target: "a"}, {Target: "b", IfNeeded: true}}
	q.committed(true, false, apply)
	for _, c := range want {
		q.command(c, apply)
	}
	q.committed(false, true, apply)
	if !reflect.DeepEqual(got, want) || q.pending != nil {
		t.Fatalf("batch order: %#v", got)
	}
	q.command(scrollCmd{Target: "now"}, apply)
	if len(got) != 4 {
		t.Fatal("unheld command was deferred")
	}
}
