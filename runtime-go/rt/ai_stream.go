//go:build !js

package rt

// Std.Ai.Provider.chatStream on a server slot (Std.Ai.Endpoint, Std.Ai.Local):
// the answer arrives as OpenAI-compatible server-sent events and is handed to
// Sky one piece at a time. The Sky side pulls: `Ai_endpointStreamOpen` (or
// `Ai_localStreamOpen`) sends the request and returns a stream id,
// `Ai_streamNext` returns the next event, and `Ai_streamClose` ends a stream
// early. An event is {"delta":"<text>"} or, last,
// {"done":true,"body":<an OpenAI chat-completions response>} whose content is
// every delta joined, so the caller decodes it as `chat` would.
//
// A server that answers a streaming request with one JSON body (a gateway
// without streaming, a test fixture under tests/mocks/) is read as one delta
// followed by the body itself. An abandoned stream is closed by its deadline:
// no loop polls for it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// aiStreamGrace is how long a stream outlives its call timeout before it is
// closed for a caller that stopped reading it.
const aiStreamGrace = 30 * time.Second

type aiStream struct {
	mu       sync.Mutex
	target   string
	resp     *http.Response
	rd       *bufio.Reader
	sse      bool
	whole    string
	sent     bool
	content  strings.Builder
	finish   string
	model    string
	prompt   int
	complete int
	closed   bool
	onClose  []func(err error)
	timer    *time.Timer
}

var (
	aiStreamsMu sync.Mutex
	aiStreams   = map[string]*aiStream{}
	aiStreamSeq int64
)

// aiStreamRegister keeps a stream under a new id and arms its deadline.
func aiStreamRegister(st *aiStream, life time.Duration) string {
	aiStreamsMu.Lock()
	aiStreamSeq++
	id := "s" + strconv.FormatInt(aiStreamSeq, 10)
	aiStreams[id] = st
	aiStreamsMu.Unlock()
	st.timer = time.AfterFunc(life+aiStreamGrace, func() {
		aiStreamDrop(id, aiFailf(aiTimedOut, "the stream was not read to its end within %s", life+aiStreamGrace))
	})
	return id
}

func aiStreamGet(id string) (*aiStream, error) {
	aiStreamsMu.Lock()
	defer aiStreamsMu.Unlock()
	st, ok := aiStreams[id]
	if !ok {
		return nil, aiFailf(aiInvalidSetting, "stream %q is not open (it ended, was closed, or was never opened)", id).
			with("setting", "stream").with("reason", "not open")
	}
	return st, nil
}

// aiStreamDrop forgets a stream and closes it. err is the outcome the call
// record keeps (nil for an answer read to its end).
func aiStreamDrop(id string, err error) {
	aiStreamsMu.Lock()
	st := aiStreams[id]
	delete(aiStreams, id)
	aiStreamsMu.Unlock()
	if st != nil {
		st.close(err)
	}
}

func (st *aiStream) close(err error) {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	if st.timer != nil {
		st.timer.Stop()
	}
	if st.resp != nil {
		st.resp.Body.Close()
	}
	hooks := st.onClose
	st.onClose = nil
	st.mu.Unlock()
	for _, h := range hooks {
		h(err)
	}
}

// aiStreamFrom reads a response's status: a 2xx starts a stream, anything
// else is the classified failure aiPostOnce gives the same answer.
func aiStreamFrom(ctx context.Context, resp *http.Response, target string) (*aiStream, time.Duration, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			ra := aiRetryAfter(resp.Header.Get("Retry-After"))
			f := aiFailf(aiOverloaded, "%s answered HTTP %d: %s", target, resp.StatusCode, aiTail(string(data), 300))
			f.retryAfterMs = int(ra / time.Millisecond)
			f.httpStatus = resp.StatusCode
			return nil, ra, f
		}
		f := aiFailf(aiRemoteRejected, "%s answered HTTP %d: %s", target, resp.StatusCode, aiTail(string(data), 500))
		f.httpStatus = resp.StatusCode
		return nil, 0, f
	}
	st := &aiStream{target: target, resp: resp}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		st.sse = true
		st.rd = bufio.NewReaderSize(resp.Body, 64<<10)
		return st, 0, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	resp.Body.Close()
	st.resp = nil
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, aiFailf(aiTimedOut, "%s: no answer before the deadline", target)
		}
		return nil, 0, aiFailf(aiUnreachable, "%s: reading the answer: %v", target, err).with("target", target)
	}
	st.whole = string(data)
	return st, 0, nil
}

// aiStreamRequest posts a streaming request once.
func aiStreamRequest(ctx context.Context, client *http.Client, url, key string, hasKey bool, body string) (*aiStream, time.Duration, error) {
	target := aiRedactURL(url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, 0, aiFailf(aiInvalidSetting, "bad URL: %v", err).with("setting", "url")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if hasKey {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, 0, aiFailf(aiTimedOut, "%s: no answer before the deadline", target)
		}
		return nil, 0, aiFailf(aiUnreachable, "%s: %v", target, aiStripURL(err)).with("target", target)
	}
	return aiStreamFrom(ctx, resp, target)
}

// aiRemoteStream is aiRemotePost for a stream: the same retries (Overloaded,
// a connection that never reached the server), all before the first byte of
// the answer. A stream that has started is never retried.
func aiRemoteStream(ctx context.Context, c *aiSlotConfig, url, body string) (*aiStream, error) {
	for attempt := 0; ; attempt++ {
		st, retryAfter, err := aiStreamRequest(ctx, aiCallClient, url, c.key, c.hasKey, body)
		if err == nil {
			return st, nil
		}
		f, _ := err.(*aiFailure)
		if f == nil || !f.retryable || attempt >= c.retries {
			return nil, err
		}
		wait := aiBackoff(attempt, retryAfter)
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return nil, err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, aiFailf(aiTimedOut, "slot %q: no answer within %s", c.slot, c.timeout).
				withInt("afterMs", int(c.timeout/time.Millisecond))
		}
	}
}

// aiStreamBody asks for a stream and for the usage in its last event.
func aiStreamBody(body string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body
	}
	m["stream"] = json.RawMessage("true")
	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(out)
}

type aiStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func aiDeltaEvent(text string) string {
	b, _ := json.Marshal(map[string]string{"delta": text})
	return string(b)
}

// next returns the stream's next event. done reports the last one.
func (st *aiStream) next() (event string, done bool, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return "", false, aiFailf(aiInvalidSetting, "the stream is closed").with("setting", "stream").with("reason", "closed")
	}
	if !st.sse {
		if !st.sent {
			st.sent = true
			var whole aiStreamChunk
			if err := json.Unmarshal([]byte(st.whole), &whole); err == nil && len(whole.Choices) > 0 && whole.Choices[0].Message.Content != "" {
				st.content.WriteString(whole.Choices[0].Message.Content)
				return aiDeltaEvent(whole.Choices[0].Message.Content), false, nil
			}
		}
		b, _ := json.Marshal(map[string]any{"done": true, "body": json.RawMessage(st.whole)})
		if !json.Valid([]byte(st.whole)) {
			return "", false, aiFailf(aiRemoteRejected, "%s answered a body that is not JSON: %s", st.target, aiTail(st.whole, 300))
		}
		st.prompt, st.complete = aiUsage(st.whole)
		return string(b), true, nil
	}
	for {
		line, rerr := st.rd.ReadString('\n')
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return st.final(), true, nil
			}
			var c aiStreamChunk
			if err := json.Unmarshal([]byte(data), &c); err != nil {
				return "", false, aiFailf(aiRemoteRejected, "%s sent an event that is not JSON: %s", st.target, aiTail(data, 300))
			}
			if len(c.Error) > 0 && string(c.Error) != "null" {
				return "", false, aiFailf(aiRemoteRejected, "%s ended the stream with an error: %s", st.target, aiTail(string(c.Error), 500))
			}
			if c.Model != "" {
				st.model = c.Model
			}
			if c.Usage != nil {
				st.prompt, st.complete = c.Usage.PromptTokens, c.Usage.CompletionTokens
			}
			if len(c.Choices) > 0 {
				if c.Choices[0].FinishReason != "" {
					st.finish = c.Choices[0].FinishReason
				}
				if d := c.Choices[0].Delta.Content; d != "" {
					st.content.WriteString(d)
					return aiDeltaEvent(d), false, nil
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return st.final(), true, nil
			}
			return "", false, aiFailf(aiUnreachable, "%s: the stream broke: %v", st.target, rerr).with("target", st.target)
		}
	}
}

// final is the done event: the answer as one chat-completions body.
func (st *aiStream) final() string {
	finish := orDefault(st.finish, "stop")
	body := map[string]any{
		"id":     "sky-stream",
		"object": "chat.completion",
		"model":  st.model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": st.content.String()},
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     st.prompt,
			"completion_tokens": st.complete,
			"total_tokens":      st.prompt + st.complete,
		},
	}
	b, _ := json.Marshal(map[string]any{"done": true, "body": body})
	return string(b)
}

// openStream is post for a stream: the slot's queue place, adapter and model,
// and its engine or remote server. The queue place is held until the stream
// ends.
func (s *aiSlot) openStream(path, body string) (string, error) {
	release, err := s.q.acquire()
	if err != nil {
		return "", err
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	ad, err := s.adapter()
	if err != nil {
		return "", err
	}
	body = aiPatchModel(aiStreamBody(body), s.requestModel(ad))
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
	var st *aiStream
	var engineDone func()
	if s.cfg.mode == "remote" || testModeActive() {
		url := aiJoinPath(s.cfg.baseURL, path)
		if s.cfg.mode == "local" {
			url = aiJoinPath(aiTestLocalBase(s.cfg.slot), path)
		}
		st, err = aiRemoteStream(ctx, s.cfg, url, body)
	} else {
		startCtx, cancelStart := context.WithTimeout(context.Background(), aiStartTimeout())
		e, eerr := s.ensureEngine(startCtx, ad)
		cancelStart()
		if eerr != nil {
			cancel()
			return "", eerr
		}
		st, engineDone, err = e.openStream(ctx, path, body)
	}
	if err != nil {
		cancel()
		if engineDone != nil {
			engineDone()
		}
		return "", err
	}
	held = false
	slot, mode, model := s.cfg.slot, s.cfg.mode, aiConsoleModel(s.cfg)
	st.onClose = append(st.onClose, func(err error) {
		cancel()
		release()
		if engineDone != nil {
			engineDone()
		}
		rec := aiCallRecord{TS: start.UTC(), Slot: slot, Mode: mode, Kind: aiCallKind(path),
			Model: model, Outcome: aiCallOutcome(err), LatencyMs: aiCallLatency(start)}
		rec.PromptTokens, rec.CompletionTokens = st.prompt, st.complete
		aiRecordCall(rec)
	})
	return aiStreamRegister(st, s.cfg.timeout), nil
}

// requestModel is the model a request names: the registry adapter's served
// name, the slot's adapter or model, or "local" for a managed engine.
func (s *aiSlot) requestModel(ad *aiResolvedAdapter) string {
	model := s.cfg.model
	if ad != nil {
		model = ad.servedName
	} else if s.cfg.adapter != "" {
		model = s.cfg.adapter
	}
	if s.cfg.mode == "local" && ad == nil {
		model = orDefault(model, "local")
		model = strings.TrimSpace(model)
		if strings.HasPrefix(model, "hf:") || strings.HasPrefix(model, "file:") {
			model = "local"
		}
	}
	return model
}

// openStream starts a streaming request on a managed engine. done ends the
// engine's in-flight count for it (the engine is not retired mid-answer).
func (e *aiEngine) openStream(ctx context.Context, path, body string) (*aiStream, func(), error) {
	e.mu.Lock()
	port, key := e.port, e.apiKey
	e.inflight++
	e.lastUse = time.Now()
	e.mu.Unlock()
	var once sync.Once
	done := func() {
		once.Do(func() {
			e.mu.Lock()
			e.inflight--
			e.lastUse = time.Now()
			retire := e.inflight == 0 && e.retireWhenIdle
			if retire {
				e.retireWhenIdle = false
			}
			e.mu.Unlock()
			if retire {
				go e.stop()
			}
		})
	}
	url := aiJoinPath(fmt.Sprintf("http://127.0.0.1:%d/v1", port), path)
	st, _, err := aiStreamRequest(ctx, aiEngineClient, url, key, true, body)
	if err != nil {
		if f, ok := err.(*aiFailure); ok && f.code == aiUnreachable {
			err = aiFailf(aiEngineFailed, "the local engine did not answer: %s", f.msg).with("reason", f.msg)
		}
		return nil, done, err
	}
	st.target = "the local engine"
	return st, done, nil
}

// Ai_endpointStreamOpen : Config -> String -> String -> Task Error String
func Ai_endpointStreamOpen(cfgArg, pathArg, bodyArg any) any {
	b := parseAiEndpointBuilder(cfgArg)
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		s, err := aiSlotFor(b)
		if err != nil {
			return aiTaskErr(err)
		}
		id, err := s.openStream(path, body)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](id)
	}
}

// Ai_localStreamOpen : Int -> String -> String -> Task Error String — a
// stream from a `Std.Ai.Local` engine.
func Ai_localStreamOpen(idArg, pathArg, bodyArg any) any {
	id := int64(AsInt(idArg))
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		e, err := aiEngineByID(id)
		if err != nil {
			return aiTaskErr(err)
		}
		body := aiPatchModel(aiStreamBody(body), "local")
		ctx, cancel := context.WithTimeout(context.Background(), aiLocalCallTimeout)
		var st *aiStream
		var done func()
		if testModeActive() {
			c := &aiSlotConfig{slot: "local", timeout: aiLocalCallTimeout}
			st, err = aiRemoteStream(ctx, c, aiTestLocalBase("local")+path, body)
		} else {
			startCtx, cancelStart := context.WithTimeout(context.Background(), aiStartTimeout())
			err = e.ensure(startCtx)
			cancelStart()
			if err == nil {
				st, done, err = e.openStream(ctx, path, body)
			}
		}
		if err != nil {
			cancel()
			if done != nil {
				done()
			}
			return aiTaskErr(err)
		}
		start := time.Now()
		slot, model := orDefault(e.slot, "local"), e.spec.model.String()
		st.onClose = append(st.onClose, func(err error) {
			cancel()
			if done != nil {
				done()
			}
			rec := aiCallRecord{TS: start.UTC(), Slot: slot, Mode: "local", Kind: aiCallKind(path),
				Model: model, Outcome: aiCallOutcome(err), LatencyMs: aiCallLatency(start)}
			rec.PromptTokens, rec.CompletionTokens = st.prompt, st.complete
			aiRecordCall(rec)
		})
		return Ok[any, any](aiStreamRegister(st, aiLocalCallTimeout))
	}
}

// Ai_streamNext : String -> Task Error String
func Ai_streamNext(idArg any) any {
	id := AsString(idArg)
	return func() any {
		st, err := aiStreamGet(id)
		if err != nil {
			return aiTaskErr(err)
		}
		ev, done, err := st.next()
		if err != nil {
			aiStreamDrop(id, err)
			return aiTaskErr(err)
		}
		if done {
			aiStreamDrop(id, nil)
		}
		return Ok[any, any](ev)
	}
}

// Ai_streamClose : String -> Task Error () — end a stream the caller stops
// reading (its callback failed). Closing an ended stream does nothing.
func Ai_streamClose(idArg any) any {
	id := AsString(idArg)
	return func() any {
		aiStreamDrop(id, aiFailf(aiInvalidSetting, "the caller closed the stream").with("setting", "stream").with("reason", "closed"))
		return Ok[any, any](struct{}{})
	}
}
