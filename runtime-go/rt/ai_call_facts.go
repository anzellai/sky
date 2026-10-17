//go:build !js

package rt

// ai_call_facts.go — what one measured model call did, for Std.Ai.Trace.
//
// A measured call (Provider.measure, Trace.chat) asks for a token, calls one
// of the measured kernels with it, and reads the facts back with
// Ai_callFacts once the answer (or the failure) is in. The facts travel by
// token, never inside the response body or the failure: the device bridge
// (ai_device_serve.go) forwards slot bodies, stream events and failure
// attributes to browsers, and the facts (the slot, the served adapter, the
// queue) are the server's own business.
//
// The facts:
//
//   - queueWaitMs: time waiting for a place in the slot's queue (a call that
//     timed out in the queue waited all of it),
//   - startMs: time from the place to the request being sent: resolving a
//     registry adapter and starting or loading an engine (0 when warm),
//   - latencyMs: time from the request being sent to the whole answer, with
//     the slot's retries; for a stream, to the end of the stream as it is read,
//   - firstTokenMs: for a stream, time from the request being sent to the
//     first piece.
//
// A value the call never reached (a call refused before it was sent) is
// absent, never 0.

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

type aiCallFacts struct {
	mu           sync.Mutex
	slot         string
	tier         string
	adapter      string
	queueWait    *time.Duration
	start        *time.Duration
	latency      *time.Duration
	firstToken   *time.Duration
	sentAt       time.Time
	firstTokenAt bool
	// From the engine's own report (llama-server's `timings`), when it sends one.
	tokensPerSecond *float64
	draftAcceptance *float64
}

// aiEngineTimings is the `timings` object llama-server adds to an answer (and
// to the last chunk of a stream). draft_n and draft_n_accepted are present
// when the engine runs a draft model.
type aiEngineTimings struct {
	PredictedPerSecond *float64 `json:"predicted_per_second"`
	DraftN             *int     `json:"draft_n"`
	DraftNAccepted     *int     `json:"draft_n_accepted"`
}

// setTimings keeps the engine's own tokens per second and draft acceptance.
func (f *aiCallFacts) setTimings(t aiEngineTimings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.PredictedPerSecond != nil && *t.PredictedPerSecond > 0 {
		v := *t.PredictedPerSecond
		f.tokensPerSecond = &v
	}
	if t.DraftN != nil && t.DraftNAccepted != nil && *t.DraftN > 0 {
		v := float64(*t.DraftNAccepted) / float64(*t.DraftN)
		f.draftAcceptance = &v
	}
}

// timingsFrom reads `timings` from a whole answer body, when it has one.
func (f *aiCallFacts) timingsFrom(body string) {
	var b struct {
		Timings *aiEngineTimings `json:"timings"`
	}
	if body == "" || json.Unmarshal([]byte(body), &b) != nil || b.Timings == nil {
		return
	}
	f.setTimings(*b.Timings)
}

func newAiCallFacts(slot, tier string) *aiCallFacts {
	return &aiCallFacts{slot: slot, tier: tier}
}

func (f *aiCallFacts) setQueueWait(d time.Duration) {
	f.mu.Lock()
	f.queueWait = &d
	f.mu.Unlock()
}

func (f *aiCallFacts) setAdapter(a string) {
	f.mu.Lock()
	f.adapter = a
	f.mu.Unlock()
}

// sent marks the request as sent: start is the time since the queue place.
func (f *aiCallFacts) sent(placed time.Time) {
	now := time.Now()
	d := now.Sub(placed)
	f.mu.Lock()
	f.start = &d
	f.sentAt = now
	f.mu.Unlock()
}

// startFailed records the start time of a call that failed before it was sent.
func (f *aiCallFacts) startFailed(placed time.Time) {
	d := time.Since(placed)
	f.mu.Lock()
	f.start = &d
	f.mu.Unlock()
}

// answered records the latency, once: the first call wins.
func (f *aiCallFacts) answered() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sentAt.IsZero() || f.latency != nil {
		return
	}
	d := time.Since(f.sentAt)
	f.latency = &d
}

// firstPiece records the time to the first piece of a stream, once.
func (f *aiCallFacts) firstPiece() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sentAt.IsZero() || f.firstTokenAt {
		return
	}
	f.firstTokenAt = true
	d := time.Since(f.sentAt)
	f.firstToken = &d
}

func aiMs(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return int64(*d / time.Millisecond)
}

func aiFloatOrNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// json is the facts as Provider.measure reads them.
func (f *aiCallFacts) json() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(map[string]any{
		"slot":            f.slot,
		"tier":            f.tier,
		"adapter":         f.adapter,
		"queueWaitMs":     aiMs(f.queueWait),
		"startMs":         aiMs(f.start),
		"latencyMs":       aiMs(f.latency),
		"firstTokenMs":    aiMs(f.firstToken),
		"tokensPerSecond": aiFloatOrNil(f.tokensPerSecond),
		"draftAcceptance": aiFloatOrNil(f.draftAcceptance),
	})
	return string(b)
}

// queueWaitMs is the queue wait for the console record (0 when unknown).
func (f *aiCallFacts) queueWaitMs() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queueWait == nil {
		return 0
	}
	return int64(*f.queueWait / time.Millisecond)
}

// The facts of measured calls, by token, until Ai_callFacts reads them. A
// token nobody reads (the program stopped between the call and the read) is
// dropped once aiCallFactsCap newer tokens exist.
const aiCallFactsCap = 4096

var (
	aiCallFactsMu    sync.Mutex
	aiCallFactsByTok = map[string]*aiCallFacts{}
	aiCallFactsOrder []string
	aiCallFactsSeq   int64
)

func aiNewCallToken() string {
	aiCallFactsMu.Lock()
	defer aiCallFactsMu.Unlock()
	aiCallFactsSeq++
	return "call-" + strconv.FormatInt(aiCallFactsSeq, 10)
}

func aiPutCallFacts(token string, f *aiCallFacts) {
	if token == "" {
		return
	}
	aiCallFactsMu.Lock()
	defer aiCallFactsMu.Unlock()
	if _, ok := aiCallFactsByTok[token]; !ok {
		aiCallFactsOrder = append(aiCallFactsOrder, token)
	}
	aiCallFactsByTok[token] = f
	for len(aiCallFactsOrder) > aiCallFactsCap {
		delete(aiCallFactsByTok, aiCallFactsOrder[0])
		aiCallFactsOrder = aiCallFactsOrder[1:]
	}
}

func aiTakeCallFacts(token string) (*aiCallFacts, bool) {
	aiCallFactsMu.Lock()
	defer aiCallFactsMu.Unlock()
	f, ok := aiCallFactsByTok[token]
	if !ok {
		return nil, false
	}
	delete(aiCallFactsByTok, token)
	for i, t := range aiCallFactsOrder {
		if t == token {
			aiCallFactsOrder = append(aiCallFactsOrder[:i], aiCallFactsOrder[i+1:]...)
			break
		}
	}
	return f, true
}

// aiBodyModel is the model a request body names: the adapter or model the
// server answers with.
func aiBodyModel(body string) string {
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	return m.Model
}

// ─── kernels ────────────────────────────────────────────────────────────

// Ai_callToken : () -> Task Error String — a token for one measured call.
func Ai_callToken(_ any) any {
	return func() any { return Ok[any, any](aiNewCallToken()) }
}

// Ai_callFacts : String -> Task Error String — the facts of the measured call
// with this token, as JSON, read once. A token with no facts (the call never
// reached a slot or an engine) gives "{}".
func Ai_callFacts(tokenArg any) any {
	token := AsString(tokenArg)
	return func() any {
		f, ok := aiTakeCallFacts(token)
		if !ok {
			return Ok[any, any]("{}")
		}
		return Ok[any, any](f.json())
	}
}

// Ai_endpointPostMeasured : Config -> String -> String -> String -> Task Error String
// — Ai_endpointPost whose facts are kept under the token.
func Ai_endpointPostMeasured(cfgArg, tokenArg, pathArg, bodyArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	token, path, body := AsString(tokenArg), AsString(pathArg), AsString(bodyArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		f := newAiCallFacts(s.cfg.slot, s.cfg.mode)
		aiPutCallFacts(token, f)
		out, err := s.callWith(path, body, f)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](out)
	}
}

// Ai_endpointStreamOpenMeasured : Config -> String -> String -> String -> Task Error String
// — Ai_endpointStreamOpen whose facts are kept under the token. The facts are
// complete once the stream has ended.
func Ai_endpointStreamOpenMeasured(cfgArg, tokenArg, pathArg, bodyArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	token, path, body := AsString(tokenArg), AsString(pathArg), AsString(bodyArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		f := newAiCallFacts(s.cfg.slot, s.cfg.mode)
		aiPutCallFacts(token, f)
		id, err := s.openStreamWith(path, body, f)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](id)
	}
}

// Ai_localPostMeasured : Int -> String -> String -> String -> Task Error String
// — Ai_localPost whose facts are kept under the token.
func Ai_localPostMeasured(idArg, tokenArg, pathArg, bodyArg any) any {
	return aiLocalPost(idArg, pathArg, bodyArg, AsString(tokenArg))
}

// Ai_localStreamOpenMeasured : Int -> String -> String -> String -> Task Error String
// — Ai_localStreamOpen whose facts are kept under the token.
func Ai_localStreamOpenMeasured(idArg, tokenArg, pathArg, bodyArg any) any {
	return aiLocalStreamOpen(idArg, pathArg, bodyArg, AsString(tokenArg))
}

// factsAdapter is the adapter a slot's request answers with: the model it
// names for a remote slot, and for a local slot only a name other than the
// "local" placeholder.
func (s *aiSlot) factsAdapter(body string) string {
	if s.cfg.mode == "local" {
		return aiLocalRequestAdapter(body)
	}
	return aiBodyModel(body)
}

// aiLocalRequestAdapter is the adapter a local engine answers with: the request's
// model when it names one, else none ("local" is the placeholder name).
func aiLocalRequestAdapter(body string) string {
	if m := aiBodyModel(body); m != "local" {
		return m
	}
	return ""
}
