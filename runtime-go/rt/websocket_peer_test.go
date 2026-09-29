package rt

import (
	"reflect"
	"testing"
	"time"
)

// The Sky.Spa client WebSocket reader (websocket_peer.go), host-side: the
// browser glue (websocket_wasm.go) only feeds it events.

func textEv(s string) wsEvent { return wsEvent{kind: wsMessageEv, text: s} }

func TestWsPeer_TaskReceivesInOrderThenNothing(t *testing.T) {
	p := newWsPeer()
	p.push(wsEvent{kind: wsOpenEv}, nil)
	p.push(textEv("a"), nil)
	p.push(wsEvent{kind: wsMessageEv, isBinary: true, binary: "\x00\xff"}, nil)
	p.push(wsEvent{kind: wsCloseEv, closeCode: 1000}, nil)
	p.finish()
	if !p.claim(wsOwnerTask) {
		t.Fatal("a Task could not claim an unread socket")
	}
	out, v := p.awaitFrame(false, 0)
	if out != wsTaskFrame || v.(SkyADT).SkyName != "Text" || v.(SkyADT).Fields[0] != "a" {
		t.Fatalf("first = %v %v, want Text a (the Open event is skipped)", out, v)
	}
	out, v = p.awaitFrame(false, 0)
	if out != wsTaskFrame || v.(SkyADT).SkyName != "Binary" || v.(SkyADT).Fields[0] != "\x00\xff" {
		t.Fatalf("second = %v %v, want Binary", out, v)
	}
	if out, _ = p.awaitFrame(false, 0); out != wsTaskClosed {
		t.Fatalf("after the close = %v, want Nothing", out)
	}
	if out, _ = p.awaitFrame(false, 0); out != wsTaskClosed {
		t.Fatalf("again = %v, want Nothing", out)
	}
}

func TestWsPeer_WaitingReceiveWakesOnAFrame(t *testing.T) {
	p := newWsPeer()
	done := make(chan any, 1)
	go func() {
		_, v := p.awaitFrame(false, 0)
		done <- v
	}()
	time.Sleep(20 * time.Millisecond)
	p.push(textEv("late"), nil)
	select {
	case v := <-done:
		if v.(SkyADT).Fields[0] != "late" {
			t.Fatalf("got %v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiting receive did not wake on a frame")
	}
}

func TestWsPeer_ReceiveWithinTimesOutWithoutConsuming(t *testing.T) {
	p := newWsPeer()
	if out, _ := p.awaitFrame(true, 30*time.Millisecond); out != wsTaskTimeout {
		t.Fatalf("empty queue: %v, want Timeout", out)
	}
	if out, _ := p.awaitFrame(true, 0); out != wsTaskTimeout {
		t.Fatalf("poll on empty queue: %v, want Timeout", out)
	}
	p.push(textEv("x"), nil)
	if out, _ := p.awaitFrame(true, 0); out != wsTaskFrame {
		t.Fatalf("poll with a queued frame: %v, want the frame", out)
	}
}

func TestWsPeer_LostConnectionIsAnError(t *testing.T) {
	p := newWsPeer()
	p.push(wsEvent{kind: wsErrorEv, err: ErrNetwork("lost")}, nil)
	p.finish()
	if out, _ := p.awaitFrame(false, 0); out != wsTaskFailed {
		t.Fatalf("got %v, want Failed", out)
	}
}

// A Sub claims the socket and first receives the events queued before it
// subscribed (the Open event of the handshake, early frames), in order; later
// events go straight to it.
func TestWsPeer_SubGetsBacklogThenLiveEvents(t *testing.T) {
	p := newWsPeer()
	p.push(wsEvent{kind: wsOpenEv}, nil)
	p.push(textEv("early"), nil)
	var got []any
	dispatch := func(m any) { got = append(got, m) }
	subs := map[string]any{
		"open":    "Opened",
		"message": func(m any) any { return "Got " + m.(SkyADT).Fields[0].(string) },
		"close":   func(c any) any { return "Closed " + c.(SkyADT).SkyName },
	}
	if !p.setSubs(subs, dispatch) {
		t.Fatal("Sub refused on an unread socket")
	}
	p.push(textEv("live"), dispatch)
	p.push(wsEvent{kind: wsCloseEv, closeCode: 1000}, dispatch)
	want := []any{"Opened", "Got early", "Got live", "Closed Normal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatched %v, want %v", got, want)
	}
	if p.claim(wsOwnerTask) {
		t.Fatal("a Task claimed a Sub-read socket")
	}
}

func TestWsPeer_SubRefusedOnTaskSocket(t *testing.T) {
	p := newWsPeer()
	p.claim(wsOwnerTask)
	if p.setSubs(map[string]any{"message": "M"}, func(any) {}) {
		t.Fatal("a Sub was installed on a socket a Task reads")
	}
}

func TestWsPeer_DecoderPanicDropsOnlyThatEvent(t *testing.T) {
	var got []any
	subs := map[string]any{"message": func(m any) any { panic("boom") }}
	wsDispatchToSubs(subs, textEv("x"), func(m any) { got = append(got, m) })
	if len(got) != 0 {
		t.Fatalf("dispatched %v after a decoder panic", got)
	}
}

func TestCollectWebSocketSubs(t *testing.T) {
	root := subT{kind: "batch", batch: []any{
		Sub_subscribeWebSocket(int64(3), "message", "M"),
		subT{kind: "every", ms: 100},
		subT{kind: "batch", batch: []any{Sub_subscribeWebSocket(int64(3), "close", "C"), Sub_subscribeWebSocket(int64(4), "open", "O")}},
	}}
	out := map[int64]map[string]any{}
	collectWebSocketSubs(root, out)
	want := map[int64]map[string]any{3: {"message": "M", "close": "C"}, 4: {"open": "O"}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
}

// A path or relative URL connects to the page's own origin, with the scheme
// that matches the page (the default passes connect-src 'self').
func TestWsBrowserURL(t *testing.T) {
	cases := []struct{ raw, page, want string }{
		{"/ws", "http://127.0.0.1:8000/app?x=1", "ws://127.0.0.1:8000/ws"},
		{"/ws?room=a", "https://example.test/", "wss://example.test/ws?room=a"},
		{"feed", "https://example.test/a/b", "wss://example.test/a/feed"},
		{"https://other.test/s", "http://127.0.0.1/", "wss://other.test/s"},
		{"ws://peer.test:9/", "https://example.test/", "ws://peer.test:9/"},
	}
	for _, c := range cases {
		got, err := wsBrowserURL(c.raw, c.page)
		if err != nil || got != c.want {
			t.Fatalf("wsBrowserURL(%q, %q) = %q, %v; want %q", c.raw, c.page, got, err, c.want)
		}
	}
	if _, err := wsBrowserURL("ftp://x/", "http://h/"); err == nil {
		t.Fatal("an ftp: URL was accepted")
	}
}

func TestWsBrowserCloseCodeOK(t *testing.T) {
	for code, ok := range map[int]bool{1000: true, 1001: false, 1011: false, 2999: false, 3000: true, 4999: true, 5000: false} {
		if wsBrowserCloseCodeOK(code) != ok {
			t.Fatalf("code %d: got %v", code, !ok)
		}
	}
}
