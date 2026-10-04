package rt

import "sort"

// spa_tick.go — Sub.every in the Sky.Spa client and the page lifecycle
// (portable, host-tested; the js timers live in live_wasm.go).
//
// A tick is a wake-up, and its CLIENT semantics never change: a clock, a
// stopwatch or a countdown ticks while the page is hidden exactly as Elm's
// `Time.every` does. Only the NETWORK work a tick causes is gated:
//
//   - Tick-origin work. While a tick Msg of interval ms runs, the RPCs its
//     Cmds issue (and the client fetches its performs make) are that
//     interval's tick-origin network work (spaSched.curTick / tickNet). An
//     interval that has made such a call is NETWORK-BEARING.
//   - Coalescing. A tick of a network-bearing interval whose previous
//     tick-origin call is still unsettled is skipped: a poll never piles up
//     behind a slow or retrying request.
//   - Hidden or frozen page. The first tick-origin call after the page hides
//     still goes out (a periodic autosave saves once). After that a
//     network-bearing interval's ticks are not run; the interval keeps one
//     OWED marker (the latest wins, never a backlog).
//   - Return. On visible / resume each owed interval gets ONE fresh tick at
//     once (its Msg rebuilt with the current time, never the stale one).
//     A client-only interval gets no catch-up tick: it never stopped.
//
// The gate drops a whole tick BEFORE update, and only for an interval known to
// be server-bound. Gating at the send instead would run the tick's update and
// then withhold its RPC, which leaves the client half of a server branch
// applied without its answer (a `busy = True` that never clears). An interval
// is known to be server-bound once one of its ticks made network work; a
// tick whose Msg makes network work only sometimes is treated as server-bound
// from the first time it does.

// spaTickState is one Sub.every interval's gate state.
type spaTickState struct {
	network bool // a tick of this interval has made network work
	spent   bool // the one call allowed after the page hid has been made
	owed    bool // a tick was withheld while hidden
}

// spaTickGate is the per-client Sub.every gate.
type spaTickGate struct {
	hidden bool
	m      map[int]*spaTickState
}

func newSpaTickGate() *spaTickGate {
	return &spaTickGate{m: map[int]*spaTickState{}}
}

func (g *spaTickGate) state(ms int) *spaTickState {
	st, ok := g.m[ms]
	if !ok {
		st = &spaTickState{}
		g.m[ms] = st
	}
	return st
}

// tick decides whether a tick of interval ms runs. busy reports the
// interval's previous tick-origin network work still unsettled.
func (g *spaTickGate) tick(ms int, busy bool) bool {
	st := g.state(ms)
	if !st.network {
		return true // client-only so far: Elm semantics, always
	}
	if busy {
		return false // coalesced
	}
	if g.hidden && st.spent {
		st.owed = true
		return false
	}
	return true
}

// network records that a tick of interval ms started network work.
func (g *spaTickGate) network(ms int) {
	st := g.state(ms)
	st.network = true
	if g.hidden {
		st.spent = true
	}
}

// setHidden records a visibility change. Becoming visible returns the
// intervals that owe a fresh tick, in ascending order, and clears them.
func (g *spaTickGate) setHidden(hidden bool) []int {
	if hidden {
		if !g.hidden {
			g.hidden = true
			for _, st := range g.m {
				st.spent = false
			}
		}
		return nil
	}
	if !g.hidden {
		return nil
	}
	g.hidden = false
	var owed []int
	for ms, st := range g.m {
		st.spent = false
		if st.owed {
			st.owed = false
			owed = append(owed, ms)
		}
	}
	sort.Ints(owed)
	return owed
}

// forget drops a stopped interval's state.
func (g *spaTickGate) forget(ms int) {
	delete(g.m, ms)
}

// spaTickMsg builds a tick's Msg: a bare Msg, or an (Int -> Msg) called with
// the current epoch milliseconds (the time the tick carries).
func spaTickMsg(msg any, nowMs int) any {
	if isFunc(msg) {
		return sky_call(msg, nowMs)
	}
	return msg
}
