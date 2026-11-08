//go:build !js

package rt

import (
	"reflect"
	"sync/atomic"
	"testing"
)

func TestScrollNoDOMRunnersDoNotDispatch(t *testing.T) {
	msgCh := make(chan any, 1)
	loop := newTeaLoop(msgCh, nil, nil, nil)
	for _, cmd := range []SkyCmd{Nav_scrollToTop(), Nav_scrollIntoView("literal"), Nav_scrollIntoViewIfNeeded("literal")} {
		c := cmd
		// Even irrelevant foreign fields must not turn a scroll into an effect.
		c.task = func(any) any { t.Fatal("scroll task ran"); return nil }
		c.toMsg = func(any) any { t.Fatal("scroll toMsg ran"); return nil }
		loop.runCmd(c)
		model, full := spaSsrSettleRoundFull(7, c, func(any, any) any { t.Fatal("scroll update ran"); return nil })
		if model != 7 || full {
			t.Fatalf("SSR consumed unfinished DOM command: %v/%v", model, full)
		}
	}
	if loop.inflight.Load() != 0 || len(msgCh) != 0 {
		t.Fatal("scroll dispatched a Msg/effect")
	}
}

// Std.Spa.returnedScrolls is phantom in msg: the decoder constructs only
// framework scroll/error data, never an application Msg or callback. A global
// typed error hook may report malformed input; that is a separate diagnostic
// channel, not a payload Msg manufactured by this command.
func TestScrollDecodedCommandsDoNotDispatchMessages(t *testing.T) {
	cases := []struct {
		name, wire, kind string
		leaves           []scrollCmd
		full             bool
	}{
		{name: "top", wire: `{"version":2,"commands":[{"kind":"scroll","target":"","top":true,"ifNeeded":false}]}`, kind: "batch", leaves: []scrollCmd{{Top: true}}},
		{name: "target", wire: `{"version":2,"commands":[{"kind":"scroll","target":"literal[]世界","top":false,"ifNeeded":false}]}`, kind: "batch", leaves: []scrollCmd{{Target: "literal[]世界"}}},
		{name: "needed", wire: `{"version":2,"commands":[{"kind":"scroll","target":"optional","top":false,"ifNeeded":true}]}`, kind: "batch", leaves: []scrollCmd{{Target: "optional", IfNeeded: true}}},
		{name: "ordered", wire: `{"version":2,"commands":[{"kind":"scroll","target":"","top":true,"ifNeeded":false},{"kind":"scroll","target":"part","top":false,"ifNeeded":true}]}`, kind: "batch", leaves: []scrollCmd{{Top: true}, {Target: "part", IfNeeded: true}}},
		{name: "empty", wire: `{"version":2,"commands":[]}`, kind: "batch", full: true},
		{name: "legacy-missing", kind: "none", full: true},
		{name: "malformed", wire: `{`, kind: "spaError"},
		{name: "unknown-kind", wire: `{"version":2,"commands":[{"kind":"perform","target":"x","top":false,"ifNeeded":false}]}`, kind: "spaError"},
		{name: "message", wire: `{"version":2,"commands":[{"kind":"scroll","target":"x","top":false,"ifNeeded":false,"msg":{"tag":"Next"}}]}`, kind: "spaError"},
		{name: "callback", wire: `{"version":2,"commands":[{"kind":"scroll","target":"x","top":false,"ifNeeded":false,"toMsg":"Next","task":"effect"}]}`, kind: "spaError"},
		{name: "envelope-message", wire: `{"version":2,"commands":[],"msg":"Next"}`, kind: "spaError"},
		{name: "envelope-callback", wire: `{"version":2,"commands":[],"callback":"Next"}`, kind: "spaError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := Spa_scrollCommands(tc.wire)
			if command.kind != tc.kind {
				t.Fatalf("decoded kind %q, want %q", command.kind, tc.kind)
			}
			var leaves []scrollCmd
			var inspect func(cmdT)
			inspect = func(c cmdT) {
				if c.task != nil || c.toMsg != nil || c.topic != "" {
					t.Fatal("decoded application effect/message carrier")
				}
				switch c.kind {
				case "none":
					if c.payload != nil || len(c.batch) != 0 {
						t.Fatal("none acquired payload")
					}
				case "batch":
					if _, ok := c.payload.(spaReturnedScrolls); !ok {
						t.Fatal("unexpected batch payload")
					}
					for _, child := range c.batch {
						v, ok := child.(cmdT)
						if !ok {
							t.Fatal("non-command batch child")
						}
						inspect(v)
					}
				case "scroll":
					v, ok := c.payload.(scrollCmd)
					if !ok || len(c.batch) != 0 {
						t.Fatal("unexpected scroll payload")
					}
					leaves = append(leaves, v)
				case "spaError":
					if len(c.batch) != 0 || !reflect.DeepEqual(c.payload, ErrDecode(spaScrollWireProblem)) {
						t.Fatal("unexpected error payload")
					}
				default:
					t.Fatalf("decoder manufactured executable/message kind %q", c.kind)
				}
			}
			inspect(command)
			if !reflect.DeepEqual(leaves, tc.leaves) {
				t.Fatal("decoded scroll data/order changed")
			}
			var calls atomic.Int32
			var poison func(cmdT) cmdT
			poison = func(c cmdT) cmdT {
				c.task = func(any) any { calls.Add(1); return "forbidden-result" }
				c.toMsg = func(any) any { calls.Add(1); return "forbidden-msg" }
				for i, v := range c.batch {
					c.batch[i] = poison(v.(cmdT))
				}
				return c
			}
			poisoned := poison(command)
			msgCh := make(chan any, 32)
			loop := newTeaLoop(msgCh, nil, nil, nil)
			loop.runCmd(poisoned)
			model, full := spaSsrSettleRoundFull(7, poisoned, func(any, any) any { calls.Add(1); return nil })
			if model != 7 || full != tc.full || calls.Load() != 0 || loop.inflight.Load() != 0 || len(msgCh) != 0 {
				t.Fatal("decoded scroll/error dispatched an application Msg/effect or changed SSR completion")
			}
		})
	}
}
