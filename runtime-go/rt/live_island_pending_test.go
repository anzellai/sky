//go:build !js

package rt

import "testing"

// A Cmd.toIsland frame pushed while the session has no live SSE connection
// is kept and delivered to the next connection, in order. Before, the relay
// fanned it out to nobody: on a page load or reload the widget mounted and
// asked for its state before the page's SSE connection was up, and the reply
// (a Std.Ui.Terminal scrollback replay) was lost, so the terminal stayed
// blank. Patch frames are still not kept (a connecting tab gets a resync).
func TestLiveIsland_CommandsWaitForTheNextConnection(t *testing.T) {
	s := &liveSession{}
	s.fanOutFrame(sseFrame{event: "island", data: "a"}, "")
	s.fanOutFrame(sseFrame{event: "patches", data: "p"}, "")
	s.fanOutFrame(sseFrame{event: "island", data: "b"}, "")

	id, ch, _ := s.registerSSEConn("tab-1")
	var got []string
	for len(ch) > 0 {
		fr := <-ch
		got = append(got, fr.event+":"+fr.data)
	}
	if len(got) != 2 || got[0] != "island:a" || got[1] != "island:b" {
		t.Fatalf("the new connection received %v, want [island:a island:b]", got)
	}

	// With a connection live, a command goes straight to it and nothing is
	// kept for later.
	s.fanOutFrame(sseFrame{event: "island", data: "c"}, "")
	if fr := <-ch; fr.data != "c" {
		t.Fatalf("a live connection received %q, want c", fr.data)
	}
	if len(s.islandPending) != 0 {
		t.Fatalf("%d frames kept while a connection was live", len(s.islandPending))
	}
	s.unregisterSSEConn(id)

	// The queue is bounded: the oldest commands give way.
	for i := 0; i < islandPendingMax+10; i++ {
		s.fanOutFrame(sseFrame{event: "island", data: "x"}, "")
	}
	if len(s.islandPending) != islandPendingMax {
		t.Fatalf("%d frames kept, want the bound %d", len(s.islandPending), islandPendingMax)
	}
}
