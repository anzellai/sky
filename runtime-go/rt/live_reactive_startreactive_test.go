package rt

import (
	"sync"
	"testing"
)

// Phase-2b: startReactive is now reachable from handleSSE (re-establish loops on
// reconnect), so it can be called concurrently for one session (two tabs
// reconnecting at once). The build-then-claim-or-discard guard must keep EXACTLY
// one live registry + one subscription per collection — every loser tears its own
// registry down (close(done) + cancels). Without the guard, N concurrent starts
// would each subscribe + spawn loops and leak N-1 registries (subscription +
// goroutine leak). Asserted via SubscriberCount, and run under -race in the suite.
func TestStartReactiveConcurrentNoLeak(t *testing.T) {
	reg := newTopicRegistry(16)
	// run returns nil → reactiveFoldFromResult yields ok=false → empty fold →
	// reactiveRefreshOnce early-returns (no view/render needed).
	runFn := func(_ any) any { return nil }
	binding := map[string]any{"Coll": "todos", "Run": runFn}
	app := &liveApp{
		reactive: func(_ any) any { return []any{binding} },
		topics:   reg,
	}
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		model:     map[string]any{"todos": []any{}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.startReactive(sess) // losers cancel their subs synchronously before returning
		}()
	}
	wg.Wait()

	if sess.reactive == nil {
		t.Fatal("startReactive established no registry")
	}
	if got := reg.SubscriberCount(bluedbCollTopic("todos")); got != 1 {
		t.Fatalf("subscription leak: SubscriberCount=%d want 1 (loser registries not released)", got)
	}

	// Teardown is clean: the one surviving registry releases its subscription.
	sess.teardownReactive()
	if got := reg.SubscriberCount(bluedbCollTopic("todos")); got != 0 {
		t.Fatalf("teardown left %d subscriptions", got)
	}
}
