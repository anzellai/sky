//go:build !js

package rt

// The Sky Console's AI panel at its API (/_sky/console/api/ai): the slots a
// program resolved (with the source of each setting and no secret), the
// recent calls with their outcome, the calls and slot filters the panel's
// controls send, and the eval runs Std.Ai.Eval records.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func aiCallThunk(t *testing.T, b aiEndpointBuilder) {
	t.Helper()
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	s.call("/chat/completions", `{"model":"","messages":[{"role":"user","content":"hi"}]}`)
}

func TestConsoleAIShowsSlotsCallsAndEvals(t *testing.T) {
	aiTestEnv(t, nil)
	resetReadiness(t)
	aiResetConsole()
	t.Cleanup(aiResetConsole)

	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}))
	defer srv.Close()

	b := endpointBuilder("chat")
	b.url, b.key, b.hasKey, b.model, b.retries = srv.URL+"/v1?token=s3cret", "k-SECRET-1", true, "base-8b", 0
	for i := 0; i < 3; i++ {
		aiCallThunk(t, b)
	}

	report := `{"id":"d1","suite":"support","model":"base-8b","seed":42,"cases":2,"passed":1,"failed":1,"errors":0,
		"passRate":0.5,"scores":{"contains":0.5},"p95LatencyMs":12,"verdict":"failed","failures":["pass rate 50% is below 90%"],
		"caseResults":[{"id":"refund","passed":true,"score":1,"latencyMs":10,"reason":"ok"},{"id":"tone","passed":false,"score":0,"latencyMs":12,"reason":"rude"}]}`
	if r := Ai_recordEval(report).(func() any)(); !isOkResult(r) {
		t.Fatalf("recordEval: %v", r)
	}
	if r := Ai_recordEval("{not json").(func() any)(); isOkResult(r) {
		t.Fatal("a report that does not parse must be refused")
	}

	resp := serveOnce(HandleConsoleAI, http.MethodGet, "/_sky/console/api/ai")
	if resp.Code != 200 {
		t.Fatalf("status %d", resp.Code)
	}
	raw := resp.Body.String()
	for _, secret := range []string{"k-SECRET-1", "s3cret", "token="} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the AI panel's API carries %q: %s", secret, raw)
		}
	}
	var got AiConsoleResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Slots) != 1 || got.Slots[0].Slot != "chat" || got.Slots[0].Mode != "remote" {
		t.Fatalf("slots: %+v", got.Slots)
	}
	s := got.Slots[0]
	if s.Calls != 3 || s.Failures != 1 || s.Overloaded != 1 || !strings.HasPrefix(s.Target, "http://127.0.0.1:") || strings.Contains(s.Target, "/v1") {
		t.Fatalf("slot stats: %+v", s)
	}
	var keySource string
	for _, st := range s.Settings {
		if st["key"] == "KEY" {
			keySource = st["source"] + "=" + st["value"]
		}
	}
	if keySource != "code=(secret)" {
		t.Fatalf("KEY setting %q", keySource)
	}
	if len(got.Calls) != 3 || got.Calls[0].Outcome != "Overloaded" || got.Calls[1].Outcome != "ok" ||
		got.Calls[1].PromptTokens != 11 || got.Calls[1].CompletionTokens != 7 || got.Calls[1].Model != "base-8b" {
		t.Fatalf("calls (newest first): %+v", got.Calls)
	}
	if len(got.Evals) != 1 || got.Evals[0].Suite != "support" || len(got.Evals[0].CaseResults) != 2 || got.Evals[0].Verdict != "failed" {
		t.Fatalf("evals: %+v", got.Evals)
	}

	// The panel's controls: only failed calls, and one slot's calls.
	var failed AiConsoleResponse
	consoleGetJSON(t, HandleConsoleAI, "/_sky/console/api/ai?calls=failed", &failed)
	if len(failed.Calls) != 1 || failed.Calls[0].Outcome != "Overloaded" {
		t.Fatalf("calls=failed: %+v", failed.Calls)
	}
	var other AiConsoleResponse
	consoleGetJSON(t, HandleConsoleAI, "/_sky/console/api/ai?slot=embed", &other)
	if len(other.Calls) != 0 || len(other.Slots) != 1 {
		t.Fatalf("slot=embed: calls %+v (slots are not filtered: %d)", other.Calls, len(other.Slots))
	}
}

func TestConsoleAIRingsAreBounded(t *testing.T) {
	aiResetConsole()
	t.Cleanup(aiResetConsole)
	for i := 0; i < aiCallRingSize+25; i++ {
		aiRecordCall(aiCallRecord{Slot: "chat", Outcome: "ok", LatencyMs: int64(i)})
	}
	for i := 0; i < aiEvalRingSize+5; i++ {
		aiRecordEval(aiEvalRecord{ID: fmt.Sprint(i)})
	}
	snap := aiConsoleSnapshot("", "")
	if len(snap.Calls) != aiCallRingSize || snap.Calls[0].LatencyMs != int64(aiCallRingSize+24) {
		t.Fatalf("call ring: %d rows, newest %d", len(snap.Calls), snap.Calls[0].LatencyMs)
	}
	if len(snap.Evals) != aiEvalRingSize || snap.Evals[0].ID != fmt.Sprint(aiEvalRingSize+4) {
		t.Fatalf("eval ring: %d rows, newest %s", len(snap.Evals), snap.Evals[0].ID)
	}
	if p := aiPercentile([]int64{5, 1, 9, 3, 7}, 95); p != 9 {
		t.Fatalf("p95 = %d", p)
	}
	if p := aiPercentile([]int64{5, 1, 9, 3, 7}, 50); p != 5 {
		t.Fatalf("p50 = %d", p)
	}
}
