//go:build !js

package rt

import "testing"

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
