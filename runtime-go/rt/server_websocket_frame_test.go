//go:build !js

package rt

// Sky.Http.Server.WebSocket keeps the frame type (v0.27).
//
// The server read loop used to turn a text frame and a binary frame into the
// same `String` before it called `onMessage`, so a handler could not tell a
// binary protocol message from text. `withOnFrame` hands the handler the
// client module's `WebSocketMessage` (`Text String | Binary String`) instead.
// These tests drive the real upgrade path: the cfg goes through
// ServerWebSocket_upgrade (so recordField reads it as the compiled record
// would be read), the sentinel token resolves it, and serveWebSocketUpgrade
// accepts a real client connection.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type frameSeen struct {
	ctor    string // "Text" / "Binary" for onFrame, "String" for onMessage
	payload string
}

// frameServer serves one upgrade built from cfgFields through the real
// ServerWebSocket_upgrade kernel. Every callback records what it received.
func frameServer(t *testing.T, useOnFrame bool) (*httptest.Server, func() []frameSeen) {
	t.Helper()
	clearHostGuardEnv(t)
	var mu sync.Mutex
	var seen []frameSeen
	record := func(f frameSeen) {
		mu.Lock()
		seen = append(seen, f)
		mu.Unlock()
	}
	ok := func() any { return Ok[any, any](skyUnit()) }
	onMessage := func(_ any) any {
		return func(msg any) any {
			record(frameSeen{ctor: "String", payload: msg.(string)})
			return ok
		}
	}
	onFrame := func(_ any) any {
		return func(frame any) any {
			adt := frame.(SkyADT)
			record(frameSeen{ctor: adt.SkyName, payload: adt.Fields[0].(string)})
			return ok
		}
	}
	noop1 := func(_ any) any { return ok }
	noop2 := func(_ any) any { return func(_ any) any { return ok } }
	cfg := map[string]any{
		"onConnect":       noop1,
		"onMessage":       onMessage,
		"onFrame":         onFrame,
		"frameMode":       useOnFrame,
		"onClose":         noop1,
		"onError":         noop2,
		"maxMessageBytes": 1 << 16,
		"originPatterns":  []any{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		task := ServerWebSocket_upgrade(nil, cfg).(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Errorf("upgrade task failed: %v", res.ErrValue)
			return
		}
		token, isWs := extractPendingWebSocketToken(res.OkValue.(SkyResponse).Body)
		if !isWs {
			t.Errorf("upgrade response carries no WebSocket sentinel")
			return
		}
		wsCfg, found := takePendingWebSocketCfg(token)
		if !found {
			t.Errorf("sentinel token %q resolves to no cfg", token)
			return
		}
		serveWebSocketUpgrade(w, r, wsCfg)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []frameSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]frameSeen(nil), seen...)
	}
}

// sendFramesAndWait dials the server, writes the frames in order, and waits
// until the server has recorded `want` callbacks.
func sendFramesAndWait(t *testing.T, srv *httptest.Server, seen func() []frameSeen, frames []struct {
	typ  websocket.MessageType
	data []byte
}) []frameSeen {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	for _, f := range frames {
		if err := conn.Write(ctx, f.typ, f.data); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := seen(); len(got) >= len(frames) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server saw %d of %d frames: %+v", len(seen()), len(frames), seen())
	return nil
}

var textThenBinary = []struct {
	typ  websocket.MessageType
	data []byte
}{
	{websocket.MessageText, []byte("hello")},
	{websocket.MessageBinary, []byte{0x00, 0xff}},
}

// TestServerWebSocket_OnFrameKeepsFrameType — a binary frame `00 ff` arrives
// as `Binary` with exactly those two bytes, and a text frame as `Text`.
func TestServerWebSocket_OnFrameKeepsFrameType(t *testing.T) {
	srv, seen := frameServer(t, true)
	got := sendFramesAndWait(t, srv, seen, textThenBinary)
	if got[0].ctor != "Text" || got[0].payload != "hello" {
		t.Fatalf("frame 1 = %+v, want Text \"hello\"", got[0])
	}
	if got[1].ctor != "Binary" {
		t.Fatalf("frame 2 = %+v, want Binary", got[1])
	}
	if b := []byte(got[1].payload); len(b) != 2 || b[0] != 0x00 || b[1] != 0xff {
		t.Fatalf("binary payload = % x, want 00 ff", b)
	}
}

// TestServerWebSocket_OnMessageOnlyStillGetsString — a cfg that never called
// withOnFrame keeps the old contract: onMessage receives a String for both
// frame types, and onFrame is never called.
func TestServerWebSocket_OnMessageOnlyStillGetsString(t *testing.T) {
	srv, seen := frameServer(t, false)
	got := sendFramesAndWait(t, srv, seen, textThenBinary)
	for i, f := range got {
		if f.ctor != "String" {
			t.Fatalf("frame %d reached %q, want onMessage (String)", i+1, f.ctor)
		}
	}
	if got[0].payload != "hello" || got[1].payload != string([]byte{0x00, 0xff}) {
		t.Fatalf("payloads = %q, %q", got[0].payload, got[1].payload)
	}
}
