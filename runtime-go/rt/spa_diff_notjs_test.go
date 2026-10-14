//go:build !js

package rt

import "testing"

// The diff harness's shape kernel names the leaf and the command kind without
// running a task.
func TestSpaDiffCmdShape_NamesLeafAndKind(t *testing.T) {
	ran := false
	perform := cmdT{kind: "perform", task: func() any { ran = true; return nil }, toMsg: func(r any) any { return r }}
	cases := map[string]any{
		"none":                          Cmd_none(),
		"perform":                       perform,
		"leaf2(perform)":                Spa_diffTagCmd(2, perform),
		"leaf0(none)":                   Spa_diffTagCmd(0, Cmd_none()),
		"rpc":                           Spa_diffRpcCmd(nil),
		"batch[none,leaf1(perform)]":    cmdT{kind: "batch", batch: []any{Cmd_none(), Spa_diffTagCmd(1, perform)}},
		"leaf4(batch[perform,perform])": Spa_diffTagCmd(4, cmdT{kind: "batch", batch: []any{perform, perform}}),
	}
	for want, cmd := range cases {
		if got := Spa_diffCmdShape(cmd); got != want {
			t.Errorf("shape: got %q, want %q", got, want)
		}
	}
	if ran {
		t.Fatal("the shape kernel must not run a task")
	}
	// Two leaves that both perform differ by their tag: a branch divergence is
	// visible even when the command kinds agree.
	if Spa_diffCmdShape(Spa_diffTagCmd(1, perform)) == Spa_diffCmdShape(Spa_diffTagCmd(2, perform)) {
		t.Fatal("different leaves must render different shapes")
	}
}
