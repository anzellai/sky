//go:build !js

package rt

// ai_endpoint.go — the Std.Ai.Endpoint kernels: a resolved slot, its queue,
// and the OpenAI-compatible transport behind the slot's Provider.
//
// The Sky side builds and decodes the OpenAI wire (Std.Ai.Provider); this
// file only resolves where a request goes and carries it there:
//
//	Ai_endpointPost : Config -> String -> String -> Task Error String
//	Ai_endpointWarm : Config -> Task Error Description
//	Ai_endpointDescribe : Config -> Task Error Description
//
// The first call resolves the slot and memoises it for the life of the
// process, so a top-level `Endpoint.provider slot` is enough.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// aiSlot is a resolved slot with its queue and, for a local slot, its engine.
type aiSlot struct {
	cfg *aiSlotConfig
	q   *aiQueue

	engineMu sync.Mutex
	engine   *aiEngine
}

var (
	aiSlotsMu sync.Mutex
	aiSlots   = map[string]*aiSlot{}
)

// aiSlotFor resolves a builder, memoised per builder value. A resolution
// error is not memoised: it is returned to each call.
func aiSlotFor(b aiEndpointBuilder) (*aiSlot, error) {
	fp := b.fingerprint()
	aiSlotsMu.Lock()
	defer aiSlotsMu.Unlock()
	if s, ok := aiSlots[fp]; ok {
		return s, nil
	}
	cfg, err := resolveAiSlot(b)
	if err != nil {
		return nil, err
	}
	s := &aiSlot{cfg: cfg, q: newAiQueue(cfg.slot, cfg.maxInFlight, cfg.queue, cfg.queueWait)}
	aiSlots[fp] = s
	return s, nil
}

// aiResetSlots forgets every resolved slot (tests).
func aiResetSlots() {
	aiSlotsMu.Lock()
	aiSlots = map[string]*aiSlot{}
	aiSlotsMu.Unlock()
}

func aiShortHash(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

// aiCallTransport carries model calls. It is wrapped in the test-mode
// transport, so under SKY_TEST_MODE a call is answered by a fixture under
// tests/mocks/ (or fails closed) and never reaches a network or an engine.
var aiCallClient = &http.Client{Transport: &testHttpTransport{base: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	MaxIdleConnsPerHost: 64,
	IdleConnTimeout:     90 * time.Second,
}}}

// aiTestLocalBase is where a local slot's calls go under test mode: a URL
// no network can answer, so only a fixture can (match it with
// urlContains "/chat/completions" or "/embeddings").
func aiTestLocalBase(slot string) string { return "http://sky-ai-local.invalid/" + slot + "/v1" }

// engine returns the slot's engine, starting it on first use.
func (s *aiSlot) ensureEngine(ctx context.Context) (*aiEngine, error) {
	s.engineMu.Lock()
	if s.engine == nil {
		s.engine = aiEngineFor(s.cfg.slot, s.cfg.local)
	}
	e := s.engine
	s.engineMu.Unlock()
	return e, e.ensure(ctx)
}

// post sends one request body to path (relative to the base, "/chat/completions")
// through the queue, and returns the response body.
func (s *aiSlot) post(path, body string) (string, error) {
	release, err := s.q.acquire()
	if err != nil {
		return "", err
	}
	defer release()

	model := s.cfg.model
	if s.cfg.adapter != "" {
		model = s.cfg.adapter
	}
	if s.cfg.mode == "local" {
		model = orDefault(model, "local")
		model = strings.TrimSpace(model)
		if strings.HasPrefix(model, "hf:") || strings.HasPrefix(model, "file:") {
			model = "local"
		}
	}
	body = aiPatchModel(body, model)

	if s.cfg.mode == "remote" || testModeActive() {
		url := s.cfg.baseURL + path
		if s.cfg.mode == "local" {
			url = aiTestLocalBase(s.cfg.slot) + path
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
		defer cancel()
		return aiRemotePost(ctx, s.cfg, url, body)
	}
	// Starting an engine (a first fetch, a cold load) is bounded by the start
	// deadline, not by the call's timeout; the call's own timeout starts when
	// the engine is ready.
	startCtx, cancelStart := context.WithTimeout(context.Background(), aiStartTimeout())
	e, err := s.ensureEngine(startCtx)
	cancelStart()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
	defer cancel()
	return e.post(ctx, s.cfg.timeout, path, body)
}

// call is post, recorded for the Sky Console's AI panel (ai_console.go).
func (s *aiSlot) call(path, body string) (string, error) {
	start := time.Now()
	out, err := s.post(path, body)
	rec := aiCallRecord{TS: start.UTC(), Slot: s.cfg.slot, Mode: s.cfg.mode, Kind: aiCallKind(path),
		Model: aiConsoleModel(s.cfg), Outcome: aiCallOutcome(err), LatencyMs: aiCallLatency(start)}
	if err == nil {
		rec.PromptTokens, rec.CompletionTokens = aiUsage(out)
	}
	aiRecordCall(rec)
	return out, err
}

// aiPatchModel fills the request's `model` when the Sky side left it empty:
// the slot's model is resolved here, at call time, from env or code. A
// request that names a model (Provider.Options adapter) keeps it.
func aiPatchModel(body, model string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body
	}
	cur, has := m["model"]
	if has && string(cur) != `""` && string(cur) != "null" {
		return body
	}
	if model == "" {
		delete(m, "model")
	} else {
		b, _ := json.Marshal(model)
		m["model"] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(out)
}

// aiRemotePost sends to an OpenAI-compatible server, with retries only on
// Overloaded (429 / 503) and on a connection that never reached the server.
// A timed-out call is never retried: the server may have done the work, and
// a Durable.step around the call is what makes it exactly-once.
func aiRemotePost(ctx context.Context, c *aiSlotConfig, url, body string) (string, error) {
	for attempt := 0; ; attempt++ {
		out, retryAfter, err := aiPostOnce(ctx, url, c.key, c.hasKey, body)
		if err == nil {
			return out, nil
		}
		f, _ := err.(*aiFailure)
		if f == nil || !f.retryable || attempt >= c.retries {
			return "", err
		}
		wait := aiBackoff(attempt, retryAfter)
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return "", err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", aiFailf(aiTimedOut, "slot %q: no answer within %s", c.slot, c.timeout).
				withInt("afterMs", int(c.timeout/time.Millisecond))
		}
	}
}

// aiBackoff is jittered exponential backoff, at least the server's
// Retry-After, at most ten seconds.
func aiBackoff(attempt int, retryAfter time.Duration) time.Duration {
	base := 200 * time.Millisecond << attempt
	if retryAfter > base {
		base = retryAfter
	}
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	return base/2 + time.Duration(rand.Int63n(int64(base)))
}

// aiPostOnce makes one POST and classifies the outcome.
func aiPostOnce(ctx context.Context, url, key string, hasKey bool, body string) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", 0, aiFailf(aiInvalidSetting, "bad URL: %v", err).with("setting", "url")
	}
	req.Header.Set("Content-Type", "application/json")
	if hasKey {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := aiCallClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return "", 0, aiFailf(aiTimedOut, "%s: no answer before the deadline", aiRedactURL(url))
		}
		return "", 0, aiFailf(aiUnreachable, "%s: %v", aiRedactURL(url), aiStripURL(err)).with("target", aiRedactURL(url))
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if rerr != nil {
		if ctx.Err() != nil {
			return "", 0, aiFailf(aiTimedOut, "%s: no answer before the deadline", aiRedactURL(url))
		}
		return "", 0, aiFailf(aiUnreachable, "%s: reading the answer: %v", aiRedactURL(url), rerr).with("target", aiRedactURL(url))
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return string(data), 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		ra := aiRetryAfter(resp.Header.Get("Retry-After"))
		f := aiFailf(aiOverloaded, "%s answered HTTP %d: %s", aiRedactURL(url), resp.StatusCode, aiTail(string(data), 300))
		f.retryAfterMs = int(ra / time.Millisecond)
		f.httpStatus = resp.StatusCode
		return "", ra, f
	default:
		f := aiFailf(aiRemoteRejected, "%s answered HTTP %d: %s", aiRedactURL(url), resp.StatusCode, aiTail(string(data), 500))
		f.httpStatus = resp.StatusCode
		return "", 0, f
	}
}

// aiStripURL keeps a transport error's cause without repeating the full URL
// (its path or query can carry a token).
func aiStripURL(err error) string {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner.Error()
		}
	}
	return err.Error()
}

func aiRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ─── description ────────────────────────────────────────────────────────

// aiDescription is the Sky `Description` record: where a slot's calls go and
// where each setting came from. It never holds a secret or a full URL.
func (s *aiSlot) description() any {
	c := s.cfg
	settings := make([]any, 0, len(c.settings))
	for _, st := range c.settings {
		settings = append(settings, map[string]any{
			"key":    st.key,
			"name":   st.name,
			"value":  aiDescribeValue(st),
			"source": st.source,
		})
	}
	inFlight, waiting := s.q.depth()
	return map[string]any{
		"slot":        c.slot,
		"mode":        c.mode,
		"target":      c.target(),
		"maxInFlight": c.maxInFlight,
		"queue":       c.queue,
		"inFlight":    inFlight,
		"waiting":     waiting,
		"settings":    settings,
	}
}

func aiDescribeValue(st aiSetting) string {
	switch st.key {
	case "KEY":
		return "(secret)"
	case "URL":
		return aiRedactURL(st.value)
	}
	return st.value
}

// ─── kernels ────────────────────────────────────────────────────────────

// Ai_endpointPost : Config -> String -> String -> Task Error String
func Ai_endpointPost(cfgArg, pathArg, bodyArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		out, err := s.call(path, body)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](out)
	}
}

// Ai_endpointDescribe : Config -> Task Error Description
func Ai_endpointDescribe(cfgArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](s.description())
	}
}

// Ai_endpointWarm : Config -> Task Error Description — resolve the slot and,
// for a local slot, start its engine now rather than on the first call.
func Ai_endpointWarm(cfgArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		if s.cfg.mode == "local" && !testModeActive() {
			ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
			defer cancel()
			if _, err := s.ensureEngine(ctx); err != nil {
				return aiTaskErr(err)
			}
		}
		return Ok[any, any](s.description())
	}
}
