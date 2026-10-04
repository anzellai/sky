package rt

import "testing"

// v0.27.3: Sub.every and the page lifecycle in the Sky.Spa client. A tick is a
// wake-up whose client semantics never change (a clock keeps ticking while the
// page is hidden); only the network work a tick causes is gated
// (spa_tick.go). These tests drive the portable gate and scheduler the js
// timers use.

type spaTickHarness struct {
	s     *spaSched
	g     *spaTickGate
	ran   []any
	jobs  []*spaRpcJob
	rpcOn map[any]bool // tick Msgs whose update issues an RPC
}

func newSpaTickHarness() *spaTickHarness {
	h := &spaTickHarness{s: newSpaSched("t"), g: newSpaTickGate(), rpcOn: map[any]bool{}}
	h.s.onTickNet = h.g.network
	return h
}

// run is the TEA step: it records the Msg and, for a server-bound Msg, issues
// an RPC the way interpretCmd does.
func (h *spaTickHarness) run(msg any) {
	h.ran = append(h.ran, msg)
	if h.rpcOn[msg] {
		h.jobs = append(h.jobs, h.s.issue(nil, nil, false))
	}
}

// fire is the timer callback of interval ms.
func (h *spaTickHarness) fire(ms int, msg any) bool {
	if !h.g.tick(ms, h.s.tickBusy(ms)) {
		return false
	}
	h.s.dispatchTick(msg, ms, h.run)
	return true
}

func TestSpaTick_ClientOnlyTickRunsWhileHidden(t *testing.T) {
	h := newSpaTickHarness()
	h.g.setHidden(true)
	for i := 0; i < 5; i++ {
		if !h.fire(1000, "Tick") {
			t.Fatalf("a client-only tick must run while hidden (tick %d)", i)
		}
	}
	if len(h.ran) != 5 {
		t.Fatalf("every client-only tick ran: %d", len(h.ran))
	}
	if owed := h.g.setHidden(false); len(owed) != 0 {
		t.Fatalf("a client-only interval gets no catch-up tick, got %v", owed)
	}
}

func TestSpaTick_NetworkTickWhileHiddenSentOnceThenOwed(t *testing.T) {
	h := newSpaTickHarness()
	h.rpcOn["Poll"] = true
	// Visible: the poll runs and its RPC settles.
	h.fire(5000, "Poll")
	h.s.settle(h.jobs[0], "Applied", h.run)
	h.g.setHidden(true)
	// The first tick-origin call after hiding still goes out.
	if !h.fire(5000, "Poll") || len(h.jobs) != 2 {
		t.Fatal("the first network tick after the page hides is sent")
	}
	h.s.settle(h.jobs[1], "Applied", h.run)
	// Later ticks are withheld; one owed marker, never a backlog.
	for i := 0; i < 4; i++ {
		if h.fire(5000, "Poll") {
			t.Fatal("a later network tick while hidden is not run")
		}
	}
	if len(h.jobs) != 2 {
		t.Fatalf("no RPC was sent for a withheld tick: %d jobs", len(h.jobs))
	}
	owed := h.g.setHidden(false)
	if len(owed) != 1 || owed[0] != 5000 {
		t.Fatalf("one owed marker for the interval, got %v", owed)
	}
	if again := h.g.setHidden(false); len(again) != 0 {
		t.Fatal("the owed marker is cleared once paid")
	}
}

func TestSpaTick_ResumeSendsOneFreshTickWithCurrentTime(t *testing.T) {
	h := newSpaTickHarness()
	toMsg := func(now int) any { return now }
	h.s.onTickNet = h.g.network
	h.g.network(1000) // a known server-bound interval
	h.g.setHidden(true)
	h.g.network(1000) // its one hidden call went out
	h.fire(1000, spaTickMsg(toMsg, 111))
	owed := h.g.setHidden(false)
	if len(owed) != 1 {
		t.Fatalf("the interval owes a tick, got %v", owed)
	}
	// On return the tick's Msg is rebuilt from the current time.
	for _, ms := range owed {
		h.s.dispatchTick(spaTickMsg(toMsg, 999), ms, h.run)
	}
	if len(h.ran) != 1 || h.ran[0] != 999 {
		t.Fatalf("exactly one fresh tick carrying the current time, got %v", h.ran)
	}
}

func TestSpaTick_UserClickNeverGated(t *testing.T) {
	h := newSpaTickHarness()
	h.rpcOn["Poll"] = true
	h.rpcOn["Save"] = true
	h.fire(5000, "Poll")
	h.s.settle(h.jobs[0], "Applied", h.run)
	h.g.setHidden(true)
	h.fire(5000, "Poll")
	h.s.settle(h.jobs[1], "Applied", h.run)
	if h.fire(5000, "Poll") {
		t.Fatal("withheld: owed")
	}
	h.s.dispatch("Save", h.run)
	if h.ran[len(h.ran)-1] != "Save" || len(h.jobs) != 3 {
		t.Fatalf("a click runs and sends its RPC while ticks are gated: ran=%v jobs=%d", h.ran, len(h.jobs))
	}
}

func TestSpaTick_CoalescedWhileLastCallUnsettled(t *testing.T) {
	h := newSpaTickHarness()
	h.rpcOn["Poll"] = true
	h.fire(2000, "Poll")
	if h.fire(2000, "Poll") || h.fire(2000, "Poll") {
		t.Fatal("a poll tick is skipped while its previous RPC is unsettled")
	}
	h.s.settle(h.jobs[0], "Applied", h.run)
	if !h.fire(2000, "Poll") {
		t.Fatal("once the RPC settles the next tick runs")
	}
	// Another interval is independent.
	h.rpcOn["Other"] = true
	if !h.fire(7000, "Other") {
		t.Fatal("coalescing is per interval")
	}
	// A report (red bar) keeps the RPC in flight, so the interval stays busy.
	h.s.report(h.jobs[1], "Err", h.run)
	if h.fire(2000, "Poll") {
		t.Fatal("an RPC waiting on the red bar still coalesces its interval")
	}
}

// Sub.connection state changes run ahead of a hold, in the order they
// happened (a hold RPC that is being re-sent must not hide "Reconnecting").
func TestSpaSched_UrgentConnectionMsgsRunInOrderPastAHold(t *testing.T) {
	s := newSpaSched("t")
	var ran []any
	run := func(m any) { ran = append(ran, m) }
	s.dispatch("Save", func(m any) {
		run(m)
		s.issue(nil, nil, true) // a hold RPC
	})
	s.dispatch("Click", run) // held
	s.dispatchUrgent("Reconnecting", run)
	s.dispatchUrgent("Online", run)
	want := []any{"Save", "Reconnecting", "Online"}
	if len(ran) != 3 || ran[0] != want[0] || ran[1] != want[1] || ran[2] != want[2] {
		t.Fatalf("got %v, want %v (Click stays held)", ran, want)
	}
}
