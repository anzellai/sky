package rt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// One live SSE per session: when a second /_sky/sse connects for the same
// session, the first must be superseded (its handler returns, freeing the
// goroutine + connection immediately). This bounds server-side SSE connections
// to one-per-active-session regardless of client behaviour — the scalable
// defence against multi-tab / rapid-reconnect / buggy-client connection pileup.
func TestSSESupersedesPreviousConnection(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	app.store.Set("sid-sup", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
	})
	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	open := func(ctx context.Context) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
			req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-sup"})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body) // returns when the server closes us
		}()
		return done
	}

	// Connection #1 with a long context — it should stay open (blocked in the
	// for-select) until superseded, NOT until the context expires.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	d1 := open(ctx1)
	time.Sleep(400 * time.Millisecond) // let #1 establish + register its cancel

	// Connection #2 for the SAME session supersedes #1.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	d2 := open(ctx2)

	// #1 must return well before its 5s context — because it was superseded.
	select {
	case <-d1:
		// superseded ✓
	case <-time.After(2 * time.Second):
		t.Fatal("first SSE connection was NOT superseded when a second connected " +
			"for the same session — server-side connections are unbounded")
	}

	// #2 (the current one) must still be open.
	select {
	case <-d2:
		t.Fatal("the current (second) SSE connection returned unexpectedly")
	default:
	}
	cancel2()
}
