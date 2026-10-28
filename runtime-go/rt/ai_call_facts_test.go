//go:build !js

package rt

// The facts of a measured call (ai_call_facts.go): queue wait, start,
// latency, time to the first piece, the engine's own tokens per second and
// draft acceptance, kept under a token and read once. The answer itself is
// never changed: the device bridge forwards slot bodies and stream events to
// browsers, so the facts must not travel inside them.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const factsAnswer = `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7},"timings":{"predicted_per_second":42.5,"draft_n":10,"draft_n_accepted":7}}`

type factsJSON struct {
	Slot            *string  `json:"slot"`
	Tier            *string  `json:"tier"`
	Adapter         *string  `json:"adapter"`
	QueueWaitMs     *int64   `json:"queueWaitMs"`
	StartMs         *int64   `json:"startMs"`
	LatencyMs       *int64   `json:"latencyMs"`
	FirstTokenMs    *int64   `json:"firstTokenMs"`
	TokensPerSecond *float64 `json:"tokensPerSecond"`
	DraftAcceptance *float64 `json:"draftAcceptance"`
}

func readFacts(t *testing.T, token string) (factsJSON, string) {
	t.Helper()
	r := Ai_callFacts(token).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("facts: %+v", r)
	}
	raw := r.OkValue.(string)
	var f factsJSON
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("facts %q: %v", raw, err)
	}
	return f, raw
}

func newToken(t *testing.T) string {
	t.Helper()
	r := Ai_callToken(struct{}{}).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("token: %+v", r)
	}
	return r.OkValue.(string)
}

func TestAiMeasuredPostKeepsTheAnswerAndReportsItsFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, factsAnswer)
	}))
	defer srv.Close()
	cfg := remoteStreamCfg("factspost", srv.URL+"/v1", -1)
	token := newToken(t)
	r := Ai_endpointPostMeasured(cfg, token, "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("post: %+v", r)
	}
	if got := r.OkValue.(string); got != factsAnswer {
		t.Fatalf("the measured answer changed:\n got %s\nwant %s", got, factsAnswer)
	}
	f, raw := readFacts(t, token)
	if f.Slot == nil || *f.Slot != "factspost" || f.Tier == nil || *f.Tier != "remote" || f.Adapter == nil || *f.Adapter != "m" {
		t.Fatalf("slot, tier, adapter: %s", raw)
	}
	if f.QueueWaitMs == nil || *f.QueueWaitMs != 0 || f.StartMs == nil {
		t.Fatalf("queue wait and start: %s", raw)
	}
	if f.LatencyMs == nil || *f.LatencyMs < 80 {
		t.Fatalf("latency: %s; want at least the server's 80ms", raw)
	}
	if f.FirstTokenMs != nil {
		t.Fatalf("a whole answer has no first piece: %s", raw)
	}
	if f.TokensPerSecond == nil || *f.TokensPerSecond != 42.5 || f.DraftAcceptance == nil || *f.DraftAcceptance != 0.7 {
		t.Fatalf("engine timings: %s", raw)
	}
	if _, again := readFacts(t, token); again != "{}" {
		t.Fatalf("facts are read once; the second read gave %s", again)
	}
}

func TestAiMeasuredPostFailureStillHasFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad"}}`)
	}))
	defer srv.Close()
	token := newToken(t)
	r := Ai_endpointPostMeasured(remoteStreamCfg("factsfail", srv.URL+"/v1", -1), token, "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag == 0 {
		t.Fatalf("expected a failure, got %+v", r)
	}
	f, raw := readFacts(t, token)
	if f.Tier == nil || *f.Tier != "remote" || f.LatencyMs == nil || f.TokensPerSecond != nil {
		t.Fatalf("a failed call that reached the server has a latency and no rate: %s", raw)
	}
}

func TestAiMeasuredQueueWaitIsNotLatency(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(readBody(r), "first") {
			<-release
		}
		fmt.Fprint(w, factsAnswer)
	}))
	defer srv.Close()
	cfg := remoteStreamCfg("factsqueue", srv.URL+"/v1", -1)
	cfg["maxInFlight"], cfg["queue"], cfg["queueWaitMs"] = 1, 1, 5000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Ai_endpointPostMeasured(cfg, newToken(t), "/chat/completions", chatBody("first")).(func() any)()
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(release)
	}()
	token := newToken(t)
	r := Ai_endpointPostMeasured(cfg, token, "/chat/completions", chatBody("second")).(func() any)().(SkyResult[any, any])
	wg.Wait()
	if r.Tag != 0 {
		t.Fatalf("second: %+v", r)
	}
	f, raw := readFacts(t, token)
	if f.QueueWaitMs == nil || *f.QueueWaitMs < 100 {
		t.Fatalf("queue wait: %s; want the time the first call held the only place", raw)
	}
	if f.LatencyMs == nil || *f.LatencyMs >= *f.QueueWaitMs {
		t.Fatalf("latency %s must not include the queue wait", raw)
	}
}

func readBody(r *http.Request) string {
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func TestAiMeasuredStreamReportsTheFirstPieceAndLeavesEventsAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"one \"}}]}\n\n")
		fl.Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"two\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":2},\"timings\":{\"predicted_per_second\":9.5,\"draft_n\":4,\"draft_n_accepted\":1}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()
	token := newToken(t)
	r := Ai_endpointStreamOpenMeasured(remoteStreamCfg("factsstream", srv.URL+"/v1", -1), token, "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("open: %+v", r)
	}
	id := r.OkValue.(string)
	var events []string
	for i := 0; i < 10; i++ {
		ev := Ai_streamNext(id).(func() any)().(SkyResult[any, any])
		if ev.Tag != 0 {
			t.Fatalf("next: %+v", ev)
		}
		events = append(events, ev.OkValue.(string))
		if strings.Contains(ev.OkValue.(string), `"done":true`) {
			break
		}
	}
	for _, ev := range events {
		var m map[string]any
		_ = json.Unmarshal([]byte(ev), &m)
		for k := range m {
			if k != "delta" && k != "done" && k != "body" {
				t.Fatalf("a stream event gained %q: %s", k, ev)
			}
		}
	}
	f, raw := readFacts(t, token)
	if f.FirstTokenMs == nil || *f.FirstTokenMs < 60 {
		t.Fatalf("first piece: %s; want at least the server's 60ms", raw)
	}
	if f.LatencyMs == nil || *f.LatencyMs < *f.FirstTokenMs+50 {
		t.Fatalf("latency: %s; want the whole stream", raw)
	}
	if f.TokensPerSecond == nil || *f.TokensPerSecond != 9.5 || f.DraftAcceptance == nil || *f.DraftAcceptance != 0.25 {
		t.Fatalf("engine timings from the last chunk: %s", raw)
	}
}

func TestAiCallFactsAreBounded(t *testing.T) {
	first := aiNewCallToken()
	aiPutCallFacts(first, newAiCallFacts("s", "remote"))
	for i := 0; i < aiCallFactsCap; i++ {
		aiPutCallFacts(aiNewCallToken(), newAiCallFacts("s", "remote"))
	}
	if _, ok := aiTakeCallFacts(first); ok {
		t.Fatal("the oldest unread facts must be dropped past the cap")
	}
}
