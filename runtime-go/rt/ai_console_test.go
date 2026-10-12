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
	"os"
	"path/filepath"
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

// The registry section: each model Std.Ai.Registry published, with its
// aliases, versions, alias log and eval history, sorted by name. A state that
// does not parse, or names no model, is refused.
func TestConsoleAIShowsTheRegistry(t *testing.T) {
	aiResetRegistryConsole()
	t.Cleanup(aiResetRegistryConsole)
	state := `{"name":"support-lora","store":"file://.sky/ai/registry",
		"aliases":[{"alias":"prod","number":2,"generation":3,"updatedAt":1}],
		"versions":[{"label":"support-lora@v2","number":2,"digest":"aa","baseDigest":"bb","dataset":"support@3","method":"lora","format":"gguf","blocked":null}],
		"events":[{"alias":"prod","seq":3,"action":"rollback","from":3,"to":2,"report":"","reason":"","at":1}],
		"reports":[{"suite":"support","number":2,"digest":"d2","passRate":1,"verdict":"passed"}]}`
	if r := Ai_recordRegistry(state).(func() any)(); !isOkResult(r) {
		t.Fatalf("recordRegistry: %v", r)
	}
	if r := Ai_recordRegistry(`{"name":"another"}`).(func() any)(); !isOkResult(r) {
		t.Fatalf("recordRegistry: %v", r)
	}
	for _, bad := range []string{"{not json", `{"aliases":[]}`} {
		if r := Ai_recordRegistry(bad).(func() any)(); isOkResult(r) {
			t.Fatalf("a bad state was accepted: %s", bad)
		}
	}
	snap := aiConsoleSnapshot("", "")
	if len(snap.Registry) != 2 || snap.Registry[0].Name != "another" || snap.Registry[1].Name != "support-lora" {
		t.Fatalf("registry %+v", snap.Registry)
	}
	m := snap.Registry[1]
	if len(m.Aliases) != 1 || len(m.Versions) != 1 || len(m.Events) != 1 || len(m.Reports) != 1 {
		t.Fatalf("model %+v", m)
	}
	b, _ := json.Marshal(snap)
	if !strings.Contains(string(b), `"registry":[`) || !strings.Contains(string(b), `"action":"rollback"`) {
		t.Fatalf("the API does not carry the registry: %s", b)
	}
}

func TestConsoleAIShowsTrainingRuns(t *testing.T) {
	aiResetTrainConsole()
	t.Cleanup(aiResetTrainConsole)
	dir := filepath.Join(t.TempDir(), "orders-0123456789ab")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, aiTrainJobFile), []byte(`{"name":"orders"}`), 0o644)
	var lines []string
	lines = append(lines, `{"v":1,"type":"hello","backend":"fake","attempt":0,"resumeFrom":0,"totalSteps":500}`)
	for i := 1; i <= 500; i++ {
		lines = append(lines, fmt.Sprintf(`{"v":1,"type":"progress","step":%d,"total":500,"loss":%g}`, i, 2.0/float64(i)))
	}
	lines = append(lines, `{"v":1,"type":"checkpoint","step":400,"path":"checkpoints/0000400"}`,
		`{"v":1,"type":"hello","backend":"fake","attempt":1,"resumeFrom":400,"totalSteps":500}`,
		`{"v":1,"type":"metric","step":500,"name":"val_loss","value":0.7}`,
		`{"v":1,"type":"done","step":500,"trainLoss":0.004,"valLoss":0.7}`)
	os.WriteFile(filepath.Join(dir, aiTrainEventsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	aiTrainNoteRun(dir)

	resp := aiConsoleSnapshot("", "")
	if len(resp.Training) != 1 {
		t.Fatalf("training rows %d", len(resp.Training))
	}
	r := resp.Training[0]
	if r.Status != "converting" || r.Step != 500 || r.Relaunches != 1 || r.ResumedFrom != 400 || r.LastCheckpoint != 400 || *r.ValLoss != 0.7 {
		t.Fatalf("%+v", r)
	}
	if len(r.Losses) != aiTrainConsolePoints || r.Losses[len(r.Losses)-1][0] != 500 {
		t.Fatalf("losses %d, last %v", len(r.Losses), r.Losses[len(r.Losses)-1])
	}
	os.WriteFile(filepath.Join(dir, aiTrainResultFile), []byte(`{"status":"done","adapter":{"registered":"orders@v2"}}`), 0o644)
	r = aiConsoleSnapshot("", "").Training[0]
	if r.Status != "done" || r.Registered != "orders@v2" {
		t.Fatalf("%+v", r)
	}
	os.WriteFile(filepath.Join(dir, aiTrainResultFile), []byte(`{"status":"failed","message":"Std.Ai TrainingCancelled: the training run orders was cancelled at step 3"}`), 0o644)
	if r = aiConsoleSnapshot("", "").Training[0]; r.Status != "cancelled" {
		t.Fatalf("%+v", r)
	}
	b, _ := json.Marshal(aiConsoleSnapshot("", ""))
	if !strings.Contains(string(b), `"training":[{"run":"orders-0123456789ab"`) {
		t.Fatalf("%s", b)
	}
}
