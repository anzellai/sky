package rt

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helloPayloadOf drives handleSSE for one pre-seeded session and returns the
// decoded data of its hello event.
func helloPayloadOf(t *testing.T) map[string]any {
	t.Helper()
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	app.store.Set("sid-epoch", &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
	})
	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-epoch"})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sawHello := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: hello") {
			sawHello = true
			continue
		}
		if sawHello && strings.HasPrefix(line, "data: ") {
			var hp map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &hp); err != nil {
				t.Fatalf("hello data is not JSON: %v (%s)", err, line)
			}
			return hp
		}
	}
	t.Fatal("no hello event in the SSE stream")
	return nil
}

// L7 (process epoch). The app-wide broadcast counter restarts at 1 in every
// process. The client drops a broadcast whose globalSeq is not above the
// largest it applied, so after a restart it would drop every broadcast of the
// new process. The hello carries this process's epoch (`pe`); the client
// resets its broadcast guard when the epoch changes. This test pins the server
// half: the hello names the epoch, and a later process has a later one.
func TestSSEHello_CarriesTheProcessEpoch(t *testing.T) {
	hp := helloPayloadOf(t)
	pe, ok := hp["pe"].(string)
	if !ok || pe == "" {
		t.Fatalf("hello has no process epoch `pe`: %v", hp)
	}
	if pe != liveProcessEpoch {
		t.Fatalf("hello pe = %q, want this process's epoch %q", pe, liveProcessEpoch)
	}
	started, err := strconv.ParseInt(pe, 36, 64)
	if err != nil {
		t.Fatalf("pe %q is not a base-36 wall-clock floor: %v", pe, err)
	}
	// A process started now (a restart) must announce a DIFFERENT, later
	// epoch, or the client would keep its guard and drop the new frames.
	time.Sleep(2 * time.Millisecond)
	if next := liveSeqFloor(); next <= started {
		t.Fatalf("a restarted process epoch %d is not after this one %d", next, started)
	}
}

// The client half: the hello handler resets the broadcast guard when `pe`
// changes. The browser e2e (scripts/live-client-verify.mjs "L7 restart")
// drives it end to end; this pins the reset in the served page.
func TestLiveClientResetsBroadcastGuardOnNewEpoch(t *testing.T) {
	js := liveJS("test-sid")
	for _, want := range []string{
		"var __skyProcEpoch = null;",
		"if (__skyProcEpoch !== null && hp.pe !== __skyProcEpoch) __skyLastGlobalSeq = 0;",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("the Sky.Live client does not reset its broadcast guard on a new process epoch (missing %q)", want)
		}
	}
}
