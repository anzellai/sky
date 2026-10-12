//go:build !js

package rt

// ai_console.go — what the Sky Console's AI panel shows, and the JSON API it
// reads (`/_sky/console/api/ai`).
//
// The panel has four parts (docs/design/std-ai-lifecycle.md §8.10):
//
//   - slots: every Std.Ai.Endpoint slot this process resolved, with the
//     source of each setting (env, code or default), never a key and never
//     more of a URL than its scheme, host and port;
//   - engines: the managed llama-server engines this process holds;
//   - recent calls: the last aiCallRingSize model calls, each with its slot,
//     latency, token counts and outcome (`ok` or the Std.Ai.Failure code);
//   - eval runs: the last aiEvalRingSize Std.Ai.Eval reports, this
//     process's and the ones in the app's database (ai_eval_history.go), so
//     a `sky ai eval` run in another process appears too;
//   - the model registry: each Std.Ai.Registry model this process read or
//     changed, with its aliases, newest versions, alias log and attached eval
//     reports (ai_model_registry.go).
//
// The rings are in-process and bounded. They hold no prompt, no answer and no
// secret: a call row is metadata, and an eval row carries the case ids, scores
// and the scorer's one-line reason, which the suite author wrote.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	aiCallRingSize = 200
	aiEvalRingSize = 50
)

// aiCallRecord is one model call.
type aiCallRecord struct {
	TS               time.Time `json:"ts"`
	Slot             string    `json:"slot"`
	Mode             string    `json:"mode"` // local or remote
	Kind             string    `json:"kind"` // chat or embed
	Model            string    `json:"model"`
	Outcome          string    `json:"outcome"` // ok, or the failure code
	LatencyMs        int64     `json:"latencyMs"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
}

// aiEvalCase is one case of an eval run, as the panel shows it.
type aiEvalCase struct {
	ID        string             `json:"id"`
	Passed    bool               `json:"passed"`
	Score     float64            `json:"score"`
	LatencyMs int64              `json:"latencyMs"`
	Reason    string             `json:"reason"`
	Scores    map[string]float64 `json:"scores,omitempty"`
}

// aiEvalRecord is one Std.Ai.Eval report.
type aiEvalRecord struct {
	ID           string             `json:"id"`
	TS           time.Time          `json:"ts"`
	Suite        string             `json:"suite"`
	Model        string             `json:"model"`
	Seed         int                `json:"seed"`
	Cases        int                `json:"cases"`
	Passed       int                `json:"passed"`
	Failed       int                `json:"failed"`
	Errors       int                `json:"errors"`
	PassRate     float64            `json:"passRate"`
	Scores       map[string]float64 `json:"scores"`
	P95LatencyMs int64              `json:"p95LatencyMs"`
	Verdict      string             `json:"verdict"` // passed or failed
	Failures     []string           `json:"failures"`
	CaseResults  []aiEvalCase       `json:"caseResults"`
	// Source is "process" for a run this process made, "database" for one
	// read from the app's database (another process's, such as
	// `sky ai eval`).
	Source string `json:"source,omitempty"`
}

var (
	aiConsoleMu sync.Mutex
	aiCallRing  []aiCallRecord
	aiEvalRing  []aiEvalRecord
)

// aiRecordCall appends a call to the ring.
func aiRecordCall(r aiCallRecord) {
	aiConsoleMu.Lock()
	defer aiConsoleMu.Unlock()
	aiCallRing = append(aiCallRing, r)
	if over := len(aiCallRing) - aiCallRingSize; over > 0 {
		aiCallRing = append([]aiCallRecord(nil), aiCallRing[over:]...)
	}
}

// aiRecordEval appends an eval run to the ring.
func aiRecordEval(r aiEvalRecord) {
	aiConsoleMu.Lock()
	defer aiConsoleMu.Unlock()
	aiEvalRing = append(aiEvalRing, r)
	if over := len(aiEvalRing) - aiEvalRingSize; over > 0 {
		aiEvalRing = append([]aiEvalRecord(nil), aiEvalRing[over:]...)
	}
}

// aiResetConsole empties both rings (tests).
func aiResetConsole() {
	aiConsoleMu.Lock()
	aiCallRing, aiEvalRing = nil, nil
	aiConsoleMu.Unlock()
	aiForgetEvalDbs()
}

// aiCallOutcome is a call's outcome label: "ok" or the failure code.
func aiCallOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if f, ok := err.(*aiFailure); ok {
		return string(f.code)
	}
	return "Unexpected"
}

// aiCallKind names a request path for the panel.
func aiCallKind(path string) string {
	switch {
	case strings.Contains(path, "embeddings"):
		return "embed"
	case strings.HasSuffix(path, "/tokenize"):
		return "tokenize"
	}
	return "chat"
}

// aiUsage reads the token counts of an OpenAI-compatible answer, best effort.
func aiUsage(body string) (prompt, completion int) {
	var r struct {
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(body), &r) == nil {
		return r.Usage.Prompt, r.Usage.Completion
	}
	return 0, 0
}

// Ai_recordEval : String -> Task Error () — Std.Ai.Eval hands its report (as
// the JSON the console reads) to the panel's ring. A report that does not
// parse is refused, so a broken writer is a failing test, not a blank panel.
func Ai_recordEval(jsonArg any) any {
	raw := AsString(jsonArg)
	return func() any {
		var r aiEvalRecord
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return Err[any, any](ErrInvalidInput("Std.Ai.Eval: the report for the console does not parse: " + err.Error()))
		}
		if r.TS.IsZero() {
			r.TS = time.Now().UTC()
		}
		aiRecordEval(r)
		return Ok[any, any](struct{}{})
	}
}

// ─── the console API ────────────────────────────────────────────────────

// aiConsoleSlot is a slot as the panel shows it.
type aiConsoleSlot struct {
	Slot        string              `json:"slot"`
	Mode        string              `json:"mode"`
	Target      string              `json:"target"`
	MaxInFlight int                 `json:"maxInFlight"`
	Queue       int                 `json:"queue"`
	InFlight    int                 `json:"inFlight"`
	Waiting     int                 `json:"waiting"`
	Settings    []map[string]string `json:"settings"`
	Calls       int                 `json:"calls"`
	Failures    int                 `json:"failures"`
	Overloaded  int                 `json:"overloaded"`
	P50Ms       int64               `json:"p50Ms"`
	P95Ms       int64               `json:"p95Ms"`
}

// AiConsoleResponse is the shape served by /_sky/console/api/ai.
type AiConsoleResponse struct {
	Slots    []aiConsoleSlot   `json:"slots"`
	Engines  []map[string]any  `json:"engines"`
	Calls    []aiCallRecord    `json:"calls"`
	Evals    []aiEvalRecord    `json:"evals"`
	Registry []aiRegistryModel `json:"registry"`
}

// aiConsoleSnapshot builds the panel's data. outcome is "" (every call) or
// "failed"; slot is "" (every slot) or one slot's name. The newest call and
// the newest eval run come first.
func aiConsoleSnapshot(outcome, slot string) AiConsoleResponse {
	aiConsoleMu.Lock()
	calls := append([]aiCallRecord(nil), aiCallRing...)
	evals := append([]aiEvalRecord(nil), aiEvalRing...)
	aiConsoleMu.Unlock()

	resp := AiConsoleResponse{Slots: []aiConsoleSlot{}, Engines: []map[string]any{}, Calls: []aiCallRecord{}, Evals: []aiEvalRecord{}, Registry: aiRegistrySnapshot()}

	aiSlotsMu.Lock()
	seen := map[string]bool{}
	for _, s := range aiSlots {
		if seen[s.cfg.slot] {
			continue
		}
		seen[s.cfg.slot] = true
		d := s.description().(map[string]any)
		cs := aiConsoleSlot{
			Slot: s.cfg.slot, Mode: s.cfg.mode, Target: s.cfg.target(),
			MaxInFlight: s.cfg.maxInFlight, Queue: s.cfg.queue,
			InFlight: d["inFlight"].(int), Waiting: d["waiting"].(int),
		}
		for _, st := range d["settings"].([]any) {
			m := st.(map[string]any)
			cs.Settings = append(cs.Settings, map[string]string{
				"key": m["key"].(string), "name": m["name"].(string),
				"value": m["value"].(string), "source": m["source"].(string),
			})
		}
		var lat []int64
		for _, c := range calls {
			if c.Slot != cs.Slot {
				continue
			}
			cs.Calls++
			if c.Outcome != "ok" {
				cs.Failures++
			}
			if c.Outcome == string(aiOverloaded) {
				cs.Overloaded++
			}
			lat = append(lat, c.LatencyMs)
		}
		cs.P50Ms, cs.P95Ms = aiPercentile(lat, 50), aiPercentile(lat, 95)
		resp.Slots = append(resp.Slots, cs)
	}
	aiSlotsMu.Unlock()
	sort.Slice(resp.Slots, func(i, j int) bool { return resp.Slots[i].Slot < resp.Slots[j].Slot })

	aiEnginesMu.Lock()
	var engines []*aiEngine
	for _, e := range aiEngineIDs {
		engines = append(engines, e)
	}
	aiEnginesMu.Unlock()
	for _, e := range engines {
		st := e.status().(map[string]any)
		st["slot"] = e.slot
		resp.Engines = append(resp.Engines, st)
	}
	sort.Slice(resp.Engines, func(i, j int) bool {
		return resp.Engines[i]["slot"].(string) < resp.Engines[j]["slot"].(string)
	})

	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if outcome == "failed" && c.Outcome == "ok" {
			continue
		}
		if slot != "" && c.Slot != slot {
			continue
		}
		resp.Calls = append(resp.Calls, c)
	}
	ring := make([]aiEvalRecord, 0, len(evals))
	for i := len(evals) - 1; i >= 0; i-- {
		ring = append(ring, evals[i])
	}
	resp.Evals = aiMergeEvals(ring, aiStoredEvals(aiEvalRingSize), aiEvalRingSize)
	if resp.Evals == nil {
		resp.Evals = []aiEvalRecord{}
	}
	return resp
}

// aiPercentile is the nearest-rank percentile of latencies (0 for none).
func aiPercentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := (p*len(s) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}

// HandleConsoleAI serves /_sky/console/api/ai?calls=failed&slot=<name>.
func HandleConsoleAI(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	q := r.URL.Query()
	outcome := q.Get("calls")
	if outcome != "failed" {
		outcome = ""
	}
	resp := aiConsoleSnapshot(outcome, strings.TrimSpace(q.Get("slot")))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// aiConsoleModel is the model a slot's calls name, for the panel: the
// adapter when one is set, else the served model, else the local model
// reference. Never a key or a URL.
func aiConsoleModel(c *aiSlotConfig) string {
	switch {
	case c.adapter != "":
		return c.adapter
	case c.model != "":
		return c.model
	case c.mode == "local":
		return c.local.model.String()
	}
	return ""
}

// aiCallLatency is the elapsed time of a call in whole milliseconds.
func aiCallLatency(start time.Time) int64 {
	return int64(time.Since(start) / time.Millisecond)
}
