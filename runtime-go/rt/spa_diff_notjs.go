//go:build !js

package rt

import (
	"fmt"
	"strings"
)

// Sky.Spa differential split fuzzer — the kernels its generated harness binds
// (rust/crates/project/src/spa_diff_harness.rs, `xtask harness --only
// spa-diff-fuzz`). The harness runs a server branch's `update` two ways, the
// way Sky.Live runs it and the way the split's backend runs it from the RPC
// request, and compares the result. These kernels let it compare the COMMAND
// and the LEAF each run chose, not only the model: a branch decision the
// request cannot reproduce (a dropped read) can leave the model equal and
// change only the command.
//
// They are pure and run no task. The harness is a generated project (`[spa]
// generated = true`), the only kind of project that may bind `Spa_*` kernels.

// Spa_diffTagCmd wraps a command in a leaf tag: the harness's copy of
// `update` wraps each `( model', cmd )` leaf's command as `spaDiffLeaf_ <id>
// (cmd)`, so the command shape names the leaf that produced it.
func Spa_diffTagCmd(id any, cmd any) any {
	return cmdT{kind: "spaDiffLeaf", topic: fmt.Sprint(id), batch: []any{cmd}}
}

// Spa_diffRpcCmd is the RPC stand-in in the harness's copy of a client arm:
// the client leg's command shape is `rpc` where the client sends the request.
func Spa_diffRpcCmd(_ any) any {
	return cmdT{kind: "spaDiffRpc"}
}

// Spa_diffCmdShape renders a command's shape without running anything:
// `none`, `perform`, `rpc`, `batch[a,b]`, `leaf3(perform)`, and the kind name
// of any other command (`publish`, `nav`, `island`).
func Spa_diffCmdShape(cmd any) any {
	return spaDiffShape(cmd)
}

func spaDiffShape(c any) string {
	ct, ok := c.(cmdT)
	if !ok {
		return fmt.Sprintf("?%T", c)
	}
	switch ct.kind {
	case "batch":
		parts := make([]string, 0, len(ct.batch))
		for _, b := range ct.batch {
			parts = append(parts, spaDiffShape(b))
		}
		return "batch[" + strings.Join(parts, ",") + "]"
	case "spaDiffLeaf":
		inner := "none"
		if len(ct.batch) == 1 {
			inner = spaDiffShape(ct.batch[0])
		}
		return "leaf" + ct.topic + "(" + inner + ")"
	case "spaDiffRpc":
		return "rpc"
	default:
		return ct.kind
	}
}
