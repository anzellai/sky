package rt

import (
	"reflect"
	"testing"
)

func scrollTest(target string, top, needed bool) cmdT {
	return cmdT{kind: "scroll", payload: scrollCmd{Target: target, Top: top, IfNeeded: needed}}
}
func scrollLeaves(t *testing.T, wire string) []scrollCmd {
	t.Helper()
	c, e := spaDecodeScrolls(wire)
	if e != nil {
		t.Fatal(e)
	}
	out := []scrollCmd{}
	for _, v := range c.batch {
		s, ok := scrollCmdOf(v.(cmdT))
		if !ok {
			t.Fatal("not scroll")
		}
		out = append(out, s)
	}
	return out
}
func TestSpaScrollWireCollectsActualBatchWithoutRunningEffects(t *testing.T) {
	tasks, msgs := 0, 0
	perform := cmdT{kind: "perform", task: func(any) any { tasks++; return 1 }, toMsg: func(any) any { msgs++; return 2 }}
	a := scrollTest("part-\"[]\n世界", false, false)
	b := scrollTest("", true, false)
	c := scrollTest("optional", false, true)
	cmd := cmdT{kind: "batch", batch: []any{a, perform, Cmd_none(), cmdT{kind: "batch", batch: []any{b, c}}}}
	got := scrollLeaves(t, Spa_collectScrolls(cmd))
	want := []scrollCmd{a.payload.(scrollCmd), b.payload.(scrollCmd), c.payload.(scrollCmd)}
	if !reflect.DeepEqual(got, want) || tasks != 0 || msgs != 0 {
		t.Fatalf("data/order/effect calls: %v %d %d", got, tasks, msgs)
	}
	if len(scrollLeaves(t, Spa_collectScrolls(Cmd_none()))) != 0 {
		t.Fatal("none acquired a command")
	}
}
func TestSpaScrollWireMalformedIsLoudAndPreservesAppliedModel(t *testing.T) {
	bad := []string{`null`, `{}`, `{"version":99,"commands":[]}`, `{"version":2,"commands":null}`, `{"version":2,"version":2,"commands":[]}`, `{"Version":2,"commands":[]}`, `{"version":2,"commands":[{}]}`, `{"version":2,"commands":[{"kind":"scroll","target":"x","top":true,"ifNeeded":false}]}`, `{"version":2,"commands":[{"kind":"scroll","target":"x","top":false,"ifNeeded":false,"unknown":0}]}`, `{"version":2,"commands":[]} []`}
	for _, wire := range bad {
		c := Spa_scrollCommands(wire)
		if c.kind != "spaError" {
			t.Fatalf("malformed response not loud: %q", wire)
		}
		rendered, posted := false, false
		kept := spaTransition(nil, 1, func(any, any) SkyTuple2 { return T2[any, any]{V0: 2, V1: c} }, func(m any) { rendered = m == 2 }, func(v any) { posted = v.(cmdT).kind == "spaError" }, func(string, any) { t.Fatal("wire error panicked") })
		if kept != 2 || !rendered || !posted {
			t.Fatal("wire error lost applied delta/render")
		}
	}
}
func TestSpaScrollWireLegacyMissingAndValidEmpty(t *testing.T) {
	if Spa_scrollCommands("").kind != "none" {
		t.Fatal("legacy response acquired effect")
	}
	if len(scrollLeaves(t, `{"version":2,"commands":[]}`)) != 0 {
		t.Fatal("empty acquired effect")
	}
}
func TestSpaScrollInitEnvelopeVersionAndLegacy(t *testing.T) {
	cmd := scrollTest("new-part", false, true)
	follows := `[["Loaded","{}"]]`
	read := Spa_readInitScrolls(Spa_initScrollResponse(cmd, follows)).(SkyResult[any, any])
	if read.Tag != 0 {
		t.Fatal("new envelope failed")
	}
	pair := read.OkValue.(T2[string, string])
	if pair.V0 != follows || len(scrollLeaves(t, pair.V1)) != 1 {
		t.Fatal("envelope lost data")
	}
	old := Spa_readInitScrolls(follows).(SkyResult[any, any])
	if old.Tag != 0 || old.OkValue.(T2[string, string]).V1 != "" {
		t.Fatal("legacy failed")
	}
	for _, wire := range []string{`{"version":99,"followUps":"[]","scrolls":""}`, `{"version":2,"followUps":"[]","scrolls":"bad"}`, `{"version":2,"followUps":"[]","scrolls":"","x":0}`} {
		if Spa_readInitScrolls(wire).(SkyResult[any, any]).Tag != 1 {
			t.Fatal("bad envelope accepted")
		}
	}
}
func TestSpaScrollWireDeepBatchesRemainDataOnly(t *testing.T) {
	var c any = scrollTest("deep", false, false)
	for i := 0; i < 10000; i++ {
		c = cmdT{kind: "batch", batch: []any{c}}
	}
	got := scrollLeaves(t, Spa_collectScrolls(c))
	if len(got) != 1 || got[0].Target != "deep" {
		t.Fatal("deep batch lost")
	}
}

func TestSpaScrollRpcHoldMarksOnlySchedulerPayload(t *testing.T) {
	calls := 0
	c := cmdT{kind: "rpc", payload: false, task: func(string) any { calls++; return nil }, toMsg: func(any) any { calls++; return nil }}
	marked := Spa_scrollRpcHold(c)
	hold, dom := spaRpcHoldFlags(marked.payload)
	if !hold || !dom || marked.kind != c.kind || calls != 0 || reflect.ValueOf(marked.task).Pointer() != reflect.ValueOf(c.task).Pointer() || reflect.ValueOf(marked.toMsg).Pointer() != reflect.ValueOf(c.toMsg).Pointer() {
		t.Fatal("RPC marker changed execution")
	}
	for _, old := range []bool{false, true} {
		h, d := spaRpcHoldFlags(old)
		if h != old || d {
			t.Fatal("ordinary RPC hold semantics changed")
		}
	}
	if Spa_scrollRpcHold(cmdT{kind: "none"}).kind != "spaError" {
		t.Fatal("malformed barrier accepted")
	}
}

func TestSpaScrollWireReturnedMarkerIsExactAndPreservesLeaves(t *testing.T) {
	wire := Spa_collectScrolls(Cmd_batch([]any{scrollTest("part", false, false), scrollTest("", true, false)}))
	raw, err := spaDecodeScrolls(wire)
	if err != nil {
		t.Fatal(err)
	}
	marked := Spa_scrollCommands(wire)
	if !spaIsReturnedScrolls(marked) || !reflect.DeepEqual(marked.batch, raw.batch) {
		t.Fatal("returned marker changed command leaves")
	}
	if spaIsReturnedScrolls(raw) || spaIsReturnedScrolls(Spa_scrollCommands("")) || spaIsReturnedScrolls(Spa_scrollCommands(`{"version":99,"commands":[]}`)) {
		t.Fatal("marker widened beyond successfully decoded sideband")
	}
	if !spaIsReturnedScrolls(Spa_scrollCommands(`{"version":2,"commands":[]}`)) {
		t.Fatal("valid empty sideband lost provenance")
	}
}

func TestSpaScrollRpcScopePreservesOrderedCommandData(t *testing.T) {
	calls := 0
	original := Cmd_batch([]any{scrollTest("old", false, false), cmdT{kind: "rpc", task: func(string) any { calls++; return nil }}})
	scope := Spa_scrollRpcScope(original)
	inside, ok := scope.payload.(cmdT)
	if scope.kind != "scrollRpcScope" || !ok || calls != 0 || !reflect.DeepEqual(inside.batch[0], original.batch[0]) || len(inside.batch) != len(original.batch) || reflect.ValueOf(inside.batch[1].(cmdT).task).Pointer() != reflect.ValueOf(original.batch[1].(cmdT).task).Pointer() {
		t.Fatal("scope changed or executed ordered command data")
	}
	if Spa_scrollRpcScope(nil).kind != "spaError" {
		t.Fatal("malformed scope accepted")
	}
}
