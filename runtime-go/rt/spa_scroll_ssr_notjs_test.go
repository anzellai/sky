//go:build !js

package rt

import (
	"html"
	"strings"
	"testing"
)

func TestSpaScrollSSRSettlesOnceAndOwnsOnlyFinishedScrolls(t *testing.T) {
	tasks, msgs, updates := 0, 0, 0
	perform := cmdT{kind: "perform", task: func(any) any { tasks++; return 7 }, toMsg: func(v any) any { msgs++; return v }}
	cmd := cmdT{kind: "batch", batch: []any{perform, scrollTest("initial", false, false)}}
	update := func(msg, model any) any {
		updates++
		return T2[int, cmdT]{V0: model.(int) + msg.(int), V1: scrollTest("follow", false, false)}
	}
	result := Spa_ssrSettleFullScrolls(1, cmd, update).(T3[any, bool, string])
	if result.V0 != 8 || !result.V1 || tasks != 1 || msgs != 1 || updates != 1 {
		t.Fatalf("settle count/result: %+v %d/%d/%d", result, tasks, msgs, updates)
	}
	got := scrollLeaves(t, result.V2)
	if len(got) != 2 || got[0].Target != "initial" || got[1].Target != "follow" {
		t.Fatal("post-mount command order")
	}
}
func TestSpaScrollSSRUnfinishedGroupDoesNotDuplicateBootCommand(t *testing.T) {
	cmd := cmdT{kind: "batch", batch: []any{scrollTest("later", false, false), cmdT{kind: "unknown"}}}
	result := Spa_ssrSettleFullScrolls(1, cmd, func(any, any) any { t.Fatal("no update expected"); return nil }).(T3[any, bool, string])
	if result.V1 || result.V2 != "" {
		t.Fatal("unfinished group published boot scroll")
	}
}
func TestSpaScrollSSRPageAttributeIsEscapedAndOnlyFrameworkRootChanged(t *testing.T) {
	head := `<meta name="probe" content='</head>` + "\n" + `<body><div id="app" '>`
	body := `<div id="app">user body</div>`
	wire := Spa_collectScrolls(scrollTest(`x"<&世界`, false, false))
	got := Spa_ssrPageSeededScrolls(head, body, "main.wasm", "{}", true, false, []string{"part"}, wire)
	attr := `data-sky-scrolls="` + html.EscapeString(wire) + `" `
	if strings.Count(got, attr) != 1 {
		t.Fatal("attribute missing/duplicated")
	}
	if strings.Replace(got, attr, "", 1) != Spa_ssrPageSeeded(head, body, "main.wasm", "{}", true, false, []string{"part"}) {
		t.Fatal("non-attribute page bytes changed")
	}
	if strings.Index(got, attr) < strings.Index(got, head)+len(head) {
		t.Fatal("app head redirected framework attribute")
	}
}
func TestSpaScrollSSRStripDeepBatchPreservesOtherLeaves(t *testing.T) {
	var c any = cmdT{kind: "batch", batch: []any{scrollTest("deep", false, false), cmdT{kind: "unknown"}}}
	for i := 0; i < 10000; i++ {
		c = cmdT{kind: "batch", batch: []any{c}}
	}
	clean, commands, bad := spaStripScrolls(c)
	if bad || len(commands) != 1 || *commands[0].Target != "deep" {
		t.Fatal("deep data extraction")
	}
	for i := 0; i < 10000; i++ {
		clean = clean.(cmdT).batch[0]
	}
	leaf := clean.(cmdT).batch[1].(cmdT)
	if leaf.kind != "unknown" {
		t.Fatal("other leaf changed")
	}
}

func TestSpaReturnedNavigationSSRFullAndUnfinishedOwnership(t *testing.T) {
	command := Cmd_batch([]any{Nav_replaceUrl("#"), Nav_scrollIntoView("ready"), Nav_pushUrl("/next")})
	result := Spa_ssrSettleFullScrolls(1, command, func(any, any) any { t.Fatal("unexpected update"); return nil }).(T3[any, bool, string])
	if !result.V1 {
		t.Fatal("data-only browser commands did not settle")
	}
	decoded, e := spaDecodeScrolls(result.V2)
	if e != nil || len(decoded.batch) != 3 {
		t.Fatal("missing full boot effects")
	}
	joined, e := spaDecodeScrolls(Spa_joinScrolls([]any{result.V2, "", result.V2}))
	if e != nil || len(joined.batch) != 6 {
		t.Fatal("join lost command data")
	}
	unfinished := Spa_ssrSettleFullScrolls(1, Cmd_batch([]any{command, cmdT{kind: "rpc"}}), func(any, any) any { t.Fatal("unexpected update"); return nil }).(T3[any, bool, string])
	if unfinished.V1 || unfinished.V2 != "" {
		t.Fatal("unfinished group published browser effects")
	}
}
