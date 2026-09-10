//go:build !js

package rt

import (
	"testing"
	"time"
)

// Sky.Spa server-internal effect chaining (spa_chain_notjs.go). These prove the
// RPC settle folds a server effect chain to a fixpoint AND terminates on a
// self-referential cycle. Design: docs/skyspa/auto-split.md.

// The spa-deeplink shape: a `Reload` branch returns `Cmd.perform (read) Reloaded`
// and `Reloaded (Ok raw)` writes `raw` into `note`. The chain must run the read
// server-side and settle `note`, so the RPC response carries the loaded value —
// the effect must NOT be dropped (the bug this feature fixes).
func TestSpaSettleServerChain_FoldsAServerReadIntoTheModel(t *testing.T) {
	model := map[string]any{"note": ""}

	// Reload's returned command: perform a read, map its Result to `Reloaded`.
	task := func() any { return Ok[any, any]("file body") }
	toReloaded := func(result any) any { return map[string]any{"msg": "Reloaded", "result": result} }
	cmd := cmdT{kind: "perform", task: task, toMsg: toReloaded}

	// update : msg -> model -> ( model, Cmd ). `Reloaded (Ok raw)` sets note=raw.
	update := func(msg any, m any) any {
		mm := msg.(map[string]any)
		if mm["msg"] == "Reloaded" {
			res := mm["result"].(SkyResult[any, any])
			if res.Tag == 0 {
				next := RecordUpdate(m, map[string]any{"note": res.OkValue})
				return SkyTuple2{V0: next, V1: Cmd_none()}
			}
		}
		return SkyTuple2{V0: m, V1: Cmd_none()}
	}

	settled := Spa_settleServerChain(model, cmd, update)
	m2, ok := tupleFirstField(settled)
	if !ok {
		t.Fatalf("Spa_settleServerChain must return a ( model, cmd ) tuple, got %T", settled)
	}
	if Field(m2, "note") != "file body" {
		t.Fatalf("the server read must settle into note; got note=%v", Field(m2, "note"))
	}
}

// A TWO-HOP chain: Reload's perform maps to MsgA, whose arm performs again and
// maps to MsgB, whose arm settles. Both writes must land in the final model.
func TestSpaSettleServerChain_ChasesAMultiHopChainToFixpoint(t *testing.T) {
	model := map[string]any{"a": "", "b": ""}

	performInto := func(val, msgName string) cmdT {
		return cmdT{
			kind:  "perform",
			task:  func() any { return Ok[any, any](val) },
			toMsg: func(r any) any { return map[string]any{"msg": msgName, "val": r} },
		}
	}
	cmd := performInto("A", "MsgA")

	update := func(msg any, m any) any {
		mm := msg.(map[string]any)
		res := mm["val"].(SkyResult[any, any])
		switch mm["msg"] {
		case "MsgA":
			// Write a, then perform the SECOND hop → MsgB.
			next := RecordUpdate(m, map[string]any{"a": res.OkValue})
			return SkyTuple2{V0: next, V1: performInto("B", "MsgB")}
		case "MsgB":
			next := RecordUpdate(m, map[string]any{"b": res.OkValue})
			return SkyTuple2{V0: next, V1: Cmd_none()}
		}
		return SkyTuple2{V0: m, V1: Cmd_none()}
	}

	settled := Spa_settleServerChain(model, cmd, update)
	m2, _ := tupleFirstField(settled)
	if Field(m2, "a") != "A" || Field(m2, "b") != "B" {
		t.Fatalf("a two-hop chain must settle BOTH writes; got a=%v b=%v", Field(m2, "a"), Field(m2, "b"))
	}
}

// A batch of performs settles each leaf, then chases each follow-up.
func TestSpaSettleServerChain_BatchFoldsEachPerform(t *testing.T) {
	model := map[string]any{"x": "", "y": ""}
	mk := func(val, field string) cmdT {
		return cmdT{
			kind:  "perform",
			task:  func() any { return Ok[any, any](val) },
			toMsg: func(r any) any { return map[string]any{"field": field, "val": r} },
		}
	}
	update := func(msg any, m any) any {
		mm := msg.(map[string]any)
		res := mm["val"].(SkyResult[any, any])
		next := RecordUpdate(m, map[string]any{mm["field"].(string): res.OkValue})
		return SkyTuple2{V0: next, V1: Cmd_none()}
	}
	cmd := cmdT{kind: "batch", batch: []any{mk("X", "x"), mk("Y", "y")}}
	settled := Spa_settleServerChain(model, cmd, update)
	m2, _ := tupleFirstField(settled)
	if Field(m2, "x") != "X" || Field(m2, "y") != "Y" {
		t.Fatalf("batch settle must fold each perform; got x=%v y=%v", Field(m2, "x"), Field(m2, "y"))
	}
}

// TERMINATION: a self-referential Msg whose arm re-performs the SAME task must
// NOT hang. The round cap stops the chase and the call returns what settled.
func TestSpaSettleServerChain_TerminatesOnASelfReferentialCycle(t *testing.T) {
	model := map[string]any{"n": 0}

	var loopCmd func() cmdT
	loopCmd = func() cmdT {
		return cmdT{
			kind:  "perform",
			task:  func() any { return Ok[any, any](1) },
			toMsg: func(r any) any { return map[string]any{"msg": "Tick"} },
		}
	}
	update := func(msg any, m any) any {
		n := 0
		if v, ok := Field(m, "n").(int); ok {
			n = v
		}
		next := RecordUpdate(m, map[string]any{"n": n + 1})
		// Always re-perform → an infinite chain but for the round cap.
		return SkyTuple2{V0: next, V1: loopCmd()}
	}

	done := make(chan any, 1)
	go func() {
		done <- Spa_settleServerChain(model, loopCmd(), update)
	}()
	select {
	case settled := <-done:
		m2, _ := tupleFirstField(settled)
		n, _ := Field(m2, "n").(int)
		// The cap bounds the number of performs run; the loop must terminate
		// having advanced the model and stopped, never hang.
		if n < 1 {
			t.Fatalf("cycle settle must run at least one round; n=%v", n)
		}
		if n > spaChainMaxRounds+1 {
			t.Fatalf("cycle settle ran %d rounds, past the cap %d — the bound did not hold", n, spaChainMaxRounds)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Spa_settleServerChain did NOT terminate on a self-referential cycle — the round cap is not bounding the loop")
	}
}
