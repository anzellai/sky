//go:build !js

package rt

import (
	"crypto/sha256"
	"math"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

func loggingHashDeepValue(value any) any {
	for i := 0; i < hashMaxDepth+3; i++ {
		value = struct{ Child any }{value}
	}
	return value
}

func TestMsgLogHashExpiredIsUnknown(t *testing.T) {
	past := time.Unix(1, 0)
	a, ac := hashAnyResult(map[string]any{"x": 1}, past)
	b, bc := hashAnyResult(map[string]any{"x": 2}, past)
	if a != b {
		t.Fatal("expired-prefix collision premise changed")
	}
	if ac || bc {
		t.Fatal("expired prefixes cannot certify equal changed models")
	}
}

func TestMsgLogHashDepthLimitIsUnknown(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	a, ac := hashAnyResult(loggingHashDeepValue(1), deadline)
	b, bc := hashAnyResult(loggingHashDeepValue(2), deadline)
	if a != b {
		t.Fatal("depth-prefix collision premise changed")
	}
	if ac || bc {
		t.Fatal("depth-limited prefixes cannot certify equal changed models")
	}
}

func TestMsgLogHashCompleteCompatibility(t *testing.T) {
	values := []any{
		nil, true, int64(42), "public fixture", math.Copysign(0, -1),
		[]any{1, "two", false}, map[string]any{"x": 1, "y": "public"},
		struct {
			Name  string
			Count int
		}{"public", 2},
		SkyADT{Tag: 1, SkyName: "PublicVariant", Fields: []any{"public", 3}},
	}
	for i, value := range values {
		a, ac := hashAnyResult(value, time.Now().Add(time.Hour))
		b, bc := hashAnyResult(value, time.Now().Add(time.Hour))
		if !ac || !bc || a != b {
			t.Fatalf("complete unchanged fixture %d did not certify equal", i)
		}
		// The RED baseline is the original production walker with carrier-only
		// scaffolding. Comparing these numeric vectors preserves its exact feed.
		t.Logf("COMPLETE_HASH %d %016x", i, a)
	}
	a, ac := hashAnyResult(1, time.Now().Add(time.Hour))
	b, bc := hashAnyResult(2, time.Now().Add(time.Hour))
	if !ac || !bc || a == b {
		t.Fatal("complete changed fixtures must remain distinct")
	}
}

func TestMsgLogHashUnknownPoolReset(t *testing.T) {
	h := &hashState{digest: sha256.New(), incomplete: true}
	h.reset()
	if h.incomplete {
		t.Fatal("pooled hash state retained an unknown-result flag")
	}
	_, _ = hashAnyResult(1, time.Unix(1, 0))
	_, complete := hashAnyResult(1, time.Now().Add(time.Hour))
	if !complete {
		t.Fatal("complete hash inherited a prior call's cutoff")
	}
}

func loggingHashCounterNoop(t *testing.T, name string) string {
	t.Helper()
	for _, sample := range telemetry.Default().Snapshot() {
		if sample.Name == "sky_live_msg_total" && sample.Labels["name"] == name {
			if sample.Value != 1 || sample.Labels["outcome"] != "ok" {
				t.Fatal("controlled dispatch did not meter exactly once")
			}
			return sample.Labels["noop"]
		}
	}
	t.Fatal("controlled dispatch counter missing")
	return ""
}

func TestMsgLogHashUnknownNeverNoop(t *testing.T) {
	for _, tc := range []struct {
		name        string
		expiredBoth bool
		oldUnknown  bool
		expiredNew  bool
		want        string
	}{
		{"ExpiredBoth", true, false, false, "false"},
		{"UnknownOldEqualDigest", false, true, false, "false"},
		{"CompleteUnchanged", false, false, false, "true"},
		{"OldCompleteNewUnknown", false, false, true, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry.ResetDefault()
			withServerlessEnv(t, nil)
			name := "HashCompleteness" + tc.name + "Fixture"
			deadline := time.Now().Add(time.Hour)
			oldModel, newModel := any(1), any(1)
			if tc.expiredBoth {
				deadline = time.Unix(1, 0)
				newModel = 2
			}
			oldDeadline := deadline
			if tc.expiredNew {
				oldModel, newModel = "<timeout>", "changed"
				deadline = time.Unix(1, 0)
			}
			hash, complete := hashAnyResult(oldModel, oldDeadline)
			if tc.expiredNew {
				newHash, _ := hashAnyResult(newModel, deadline)
				if !complete || hash != newHash {
					t.Fatal("real complete-string/expired-prefix collision premise changed")
				}
			}
			ctx := MsgLogContext{MsgName: name, StartTime: time.Now(), OldModelHash: hash,
				OldModelHashIncomplete: !complete || tc.oldUnknown}
			// This is the same private body the public ObserveMsgLog invokes.
			// Its explicit deadline makes the old/new incomplete case deterministic.
			observeMsgLogWithDeadline(ctx, newModel, cmdT{kind: "none"}, nil, deadline)
			if got := loggingHashCounterNoop(t, name); got != tc.want {
				t.Fatalf("controlled noop classification=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestMsgLogHashPublicBeginKeepsUnknown(t *testing.T) {
	telemetry.ResetDefault()
	withServerlessEnv(t, nil)
	const name = "HashPublicBeginUnknownFixture"
	model := loggingHashDeepValue(1)
	ctx := BeginMsgLog(SkyADT{SkyName: name}, model)
	if !ctx.OldModelHashIncomplete {
		t.Fatal("public BeginMsgLog discarded a depth cutoff")
	}
	ObserveMsgLog(ctx, model, cmdT{kind: "none"}, nil)
	if loggingHashCounterNoop(t, name) != "false" {
		t.Fatal("public ObserveMsgLog suppressed an incomplete model comparison")
	}
}

func TestMsgLogHashPointerCycleStops(t *testing.T) {
	var cycle any
	cycle = &cycle
	done := make(chan bool, 1)
	// A future deadline proves entry into the old pointer/interface unwrap
	// loop. The old loop never rechecks it or recursive depth. The focused
	// RED test process exits after the bounded test failure, killing its
	// spinning goroutine; no external process or runtime hook is introduced.
	go func() {
		_, complete := hashAnyResult(cycle, time.Now().Add(time.Hour))
		done <- complete
	}()
	select {
	case complete := <-done:
		if complete {
			t.Fatal("pointer/interface cycle returned a complete hash")
		}
	case <-time.After(time.Second):
		t.Fatal("pointer/interface cycle did not stop within the bounded fixture")
	}
}
