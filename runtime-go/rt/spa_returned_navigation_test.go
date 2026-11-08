package rt

import (
	"reflect"
	"strings"
	"testing"
)

func TestSpaReturnedNavigationActualCommandOrderAndNoTasks(t *testing.T) {
	calls := 0
	poison := cmdT{kind: "perform", task: func(any) any { calls++; panic("task executed") }, toMsg: func(any) any { calls++; panic("message executed") }}
	cmd := Cmd_batch([]any{Nav_pushUrl("/items/server-id"), poison, Cmd_batch([]any{Nav_scrollIntoView("row"), Nav_replaceUrl("#")})})
	got, err := spaDecodeScrolls(Spa_collectScrolls(cmd))
	if err != nil {
		t.Fatal(err)
	}
	want := []any{Nav_pushUrl("/items/server-id"), Nav_scrollIntoView("row"), Nav_replaceUrl("#")}
	if !reflect.DeepEqual(got.batch, want) || calls != 0 {
		t.Fatalf("commands/order/task count: %v / %d", got.batch, calls)
	}
	if !spaIsReturnedScrolls(Spa_scrollCommands(Spa_collectScrolls(cmd))) {
		t.Fatal("lost stale-response marker")
	}
}
func TestSpaReturnedNavigationRejectsWholeMalformedBatch(t *testing.T) {
	prefix := `{"version":2,"commands":[{"kind":"nav","url":"/safe","replace":false},`
	for _, bad := range []string{
		`{"kind":"nav","url":"//foreign","replace":false}`,
		`{"kind":"nav","url":"/bad\\path","replace":false}`,
		`{"kind":"nav","url":"/bad\npath","replace":false}`,
		`{"kind":"nav","url":"/safe","replace":false,"target":"x"}`,
		`{"kind":"nav","url":"/safe","replace":false,"replace":true}`,
		`{"kind":"nav","url":"/safe"}`,
		`{"kind":"nav","url":7,"replace":false}`,
		`{"kind":"perform","url":"/safe","replace":false}`,
		`{"kind":"nav","url":"/safe","replace":false,"toMsg":"Next"}`,
	} {
		wire := prefix + bad + `]}`
		cmd, err := spaDecodeScrolls(wire)
		if err == nil || cmd.kind != "none" || len(cmd.batch) != 0 || Spa_scrollCommands(wire).kind != "spaError" {
			t.Fatalf("accepted partial or malformed batch: %s", bad)
		}
	}
}
func TestSpaReturnedNavigationClearFragmentData(t *testing.T) {
	wire := Spa_collectScrolls(Nav_replaceUrl("#"))
	if !strings.Contains(wire, `"kind":"nav"`) {
		t.Fatal("missing nav tag")
	}
	c, e := spaDecodeScrolls(wire)
	if e != nil || !reflect.DeepEqual(c.batch, []any{Nav_replaceUrl("#")}) {
		t.Fatal("clear fragment lost replace semantics")
	}
}

func TestSpaReturnedNavigationCommitBarrierKeepsWholeBatch(t *testing.T) {
	q := spaScrollQueue{}
	var got []cmdT
	apply := func(c cmdT) { got = append(got, c) }
	q.committedBrowser(true, false, apply)
	cmd := asPortableReturnedCommand(t, Cmd_batch([]any{Nav_pushUrl("/next"), Nav_scrollIntoView("row"), Nav_replaceUrl("#")}))
	q.browserCommand(cmd, apply)
	released := 0
	q.afterRender(0, func() { released++ })
	if len(got) != 0 || released != 0 {
		t.Fatal("effects or release before DOM commit")
	}
	q.committedBrowser(false, true, apply)
	if len(got) != 1 || len(got[0].batch) != 3 || released != 1 {
		t.Fatal("lost/duplicated batch or barrier release")
	}
	q.committedBrowser(false, true, apply)
	if len(got) != 1 || released != 1 {
		t.Fatal("replayed committed effects")
	}
	q.committedBrowser(true, false, apply)
	q.browserCommand(cmd, apply)
	q.committedBrowser(false, false, apply)
	if len(got) != 1 {
		t.Fatal("superseded effects executed")
	}
}
func asPortableReturnedCommand(t *testing.T, cmd any) cmdT {
	t.Helper()
	c, e := spaDecodeScrolls(Spa_collectScrolls(cmd))
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestSpaReturnedNavigationRunsLeavesInBatchOrder(t *testing.T) {
	cmd := Cmd_batch([]any{Nav_pushUrl("/a"), Cmd_batch([]any{Nav_scrollIntoView("row"), Nav_pushUrl("/b")}), Cmd_none(), Nav_replaceUrl("#x")})
	leaves := spaReturnedLeaves(cmd)
	want := []any{Nav_pushUrl("/a"), Nav_scrollIntoView("row"), Nav_pushUrl("/b"), Nav_replaceUrl("#x")}
	if len(leaves) != len(want) {
		t.Fatalf("leaves: %v", leaves)
	}
	for i := range want {
		if !reflect.DeepEqual(leaves[i], want[i]) {
			t.Fatalf("leaf %d: %v want %v", i, leaves[i], want[i])
		}
	}
	if !spaReturnedHasNav(leaves) || spaReturnedHasNav(leaves[1:2]) {
		t.Fatal("nav detection")
	}
	// Each own path navigation supersedes exactly once; scrolls and a
	// fragment-only change do not.
	auth := uint64(4)
	var ran []string
	spaRunReturnedInOrder(leaves, 4, func() uint64 { return auth }, func(c cmdT) {
		if nc, ok := navCmdOf(c); ok {
			ran = append(ran, nc.URL)
			if !strings.HasPrefix(nc.URL, "#") {
				auth++
			}
			return
		}
		ran = append(ran, "scroll")
	})
	if strings.Join(ran, ",") != "/a,scroll,/b,#x" {
		t.Fatalf("order: %v", ran)
	}
}

func TestSpaReturnedNavigationStaleAndForeignAuthorityStopTheRest(t *testing.T) {
	leaves := spaReturnedLeaves(Cmd_batch([]any{Nav_pushUrl("/a"), Nav_pushUrl("/b")}))
	// A newer navigation before the batch runs: nothing runs.
	ran := 0
	spaRunReturnedInOrder(leaves, 4, func() uint64 { return 5 }, func(cmdT) { ran++ })
	if ran != 0 {
		t.Fatalf("stale batch ran %d leaves", ran)
	}
	// Something else supersedes while the first leaf runs (two steps): the
	// sibling must not run against the newer page.
	auth := uint64(4)
	ran = 0
	spaRunReturnedInOrder(leaves, 4, func() uint64 { return auth }, func(cmdT) { ran++; auth += 2 })
	if ran != 1 {
		t.Fatalf("foreign supersession: ran %d leaves", ran)
	}
	// A newer navigation between leaves: the rest stop.
	auth = 4
	ran = 0
	spaRunReturnedInOrder(leaves, 4, func() uint64 { return auth }, func(cmdT) {
		ran++
		auth++ // own step
		if ran == 1 {
			auth++ // then a newer direct navigation lands before the sibling
		}
	})
	if ran != 1 {
		t.Fatalf("newer navigation between leaves: ran %d leaves", ran)
	}
}
