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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"reflect"
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

	// The registry adapter (cfg.registry): the current resolution, when it was
	// made, and the last failure to refresh it. resolveMu makes one caller
	// resolve while the others keep serving the current adapter.
	adMu      sync.Mutex
	ad        *aiResolvedAdapter
	adAt      time.Time
	adErr     string
	resolveMu sync.Mutex

	// The Google ID token (AUTH=google) and when it expires; idMu makes one
	// caller mint while the others wait for it.
	idMu  sync.Mutex
	idTok string
	idExp time.Time
}

// aiRootPrefix marks a path the server answers at its root, not under its
// OpenAI-compatible `/v1` base: llama-server's (and vLLM's) `/tokenize`.
// `Endpoint.tokenize` posts to `/@root/tokenize`.
const aiRootPrefix = "/@root"

// aiJoinPath is the URL of a request path on a server whose OpenAI-compatible
// base is `base` (ending `/v1`). A path under aiRootPrefix goes to the
// server's root instead.
func aiJoinPath(base, path string) string {
	if rest, ok := strings.CutPrefix(path, aiRootPrefix); ok {
		return strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1") + rest
	}
	return base + path
}

// aiRegistryScheme marks an adapter the slot resolves through its registry.
const aiRegistryScheme = "registry:"

// aiLocalAdapter is a LoRA adapter a local engine loads with its model
// (`--lora`): a verified GGUF file.
type aiLocalAdapter struct {
	path   string
	sha256 string
	label  string
}

// aiResolvedAdapter is the Sky `Endpoint.Adapter` a resolver returned.
type aiResolvedAdapter struct {
	ref, label, servedName, digest, baseDigest, format, path string
}

// aiSkyError carries a Sky `Error` value (a resolver's own failure) through
// Go code unchanged, so the caller sees exactly what the registry said.
type aiSkyError struct{ v any }

func (e *aiSkyError) Error() string { return fmt.Sprintf("%v", e.v) }

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

// engine returns the slot's engine for its current adapter, starting it on
// first use. When the registry moved the slot to another adapter, the slot
// switches to an engine with that adapter and retires the old one once its
// calls have finished.
func (s *aiSlot) ensureEngine(ctx context.Context, ad *aiResolvedAdapter) (*aiEngine, error) {
	spec := s.cfg.local
	if ad != nil {
		spec.adapter = aiLocalAdapter{path: ad.path, sha256: ad.digest, label: ad.label}
	}
	s.engineMu.Lock()
	var retired *aiEngine
	if s.engine == nil || s.engine.key != aiEngineKey(spec) {
		retired = s.engine
		s.engine = aiEngineFor(s.cfg.slot, spec)
	}
	e := s.engine
	s.engineMu.Unlock()
	if retired != nil {
		aiRetireEngine(retired)
	}
	return e, e.ensure(ctx)
}

// adapter returns the slot's registry adapter (nil when the slot has none),
// resolving it on first use and again when the refresh interval has passed.
// A failed refresh keeps the current adapter and is reported in describe and
// on stderr; a failed first resolution fails the call.
func (s *aiSlot) adapter() (*aiResolvedAdapter, error) {
	if !s.cfg.registry {
		return nil, nil
	}
	s.adMu.Lock()
	cur, at := s.ad, s.adAt
	s.adMu.Unlock()
	if cur != nil {
		if s.cfg.adapterRefresh <= 0 || time.Since(at) < s.cfg.adapterRefresh {
			return cur, nil
		}
		// Due: one caller refreshes, the others keep serving the current one.
		if !s.resolveMu.TryLock() {
			return cur, nil
		}
	} else {
		s.resolveMu.Lock()
	}
	defer s.resolveMu.Unlock()
	s.adMu.Lock()
	if s.ad != nil && s.ad != cur {
		// Another caller resolved while this one waited.
		ad := s.ad
		s.adMu.Unlock()
		return ad, nil
	}
	s.adMu.Unlock()

	ad, err := s.resolveAdapter()
	s.adMu.Lock()
	defer s.adMu.Unlock()
	s.adAt = time.Now()
	if err != nil {
		if cur == nil {
			return nil, err
		}
		msg := aiErrorText(err)
		if msg != s.adErr {
			fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Endpoint: slot %q: cannot re-resolve %s, still serving %s: %s\n", s.cfg.slot, s.cfg.adapter, cur.label, msg)
		}
		s.adErr = msg
		return cur, nil
	}
	s.ad, s.adErr = ad, ""
	return ad, nil
}

// resolveAdapter asks the slot's Sky resolver for the adapter its
// `registry:<name>@<alias>` names now. A local slot also checks the base
// model: an adapter trained on another base than the engine runs would load
// and answer nonsense, so it is AdapterMismatch instead.
func (s *aiSlot) resolveAdapter() (*aiResolvedAdapter, error) {
	ref := strings.TrimPrefix(s.cfg.adapter, aiRegistryScheme)
	local := s.cfg.mode == "local"
	res := AnyTaskRun(sky_call2(s.cfg.resolve, ref, local))
	tag, okV, errV := anyResultView(res)
	if tag != 0 {
		return nil, &aiSkyError{v: errV}
	}
	a := &aiResolvedAdapter{
		ref:        aiStrField(okV, "Ref", "ref"),
		label:      aiStrField(okV, "Label", "label"),
		servedName: aiStrField(okV, "ServedName", "servedName"),
		digest:     aiStrField(okV, "Digest", "digest"),
		baseDigest: aiStrField(okV, "BaseDigest", "baseDigest"),
		format:     aiStrField(okV, "Format", "format"),
		path:       aiStrField(okV, "Path", "path"),
	}
	if a.servedName == "" || !aiValidSha256(a.digest) {
		return nil, aiFailf(aiInvalidSetting, "slot %q: the registry resolved %s to an adapter with no name or digest", s.cfg.slot, ref).with("setting", "adapter")
	}
	if local {
		if a.baseDigest != s.cfg.local.model.sha256 {
			return nil, aiFailf(aiAdapterMismatch,
				"slot %q: %s (%s) was trained on base sha256 %s, but the engine runs %s (sha256 %s); serve it on its own base model, or promote an adapter trained on this one",
				s.cfg.slot, ref, a.label, a.baseDigest, s.cfg.local.model.file, s.cfg.local.model.sha256).
				with("adapter", a.label).with("adapterBase", a.baseDigest).with("engineBase", s.cfg.local.model.sha256)
		}
		if a.format != "gguf" || a.path == "" {
			return nil, aiFailf(aiInvalidSetting, "slot %q: %s is %s; a local engine loads a GGUF adapter file", s.cfg.slot, a.label, orDefault(a.format, "not a file")).with("setting", "adapter")
		}
	}
	return a, nil
}

// aiErrorText is a failure's message for a log line.
func aiErrorText(err error) string {
	if se, ok := err.(*aiSkyError); ok {
		return skyErrorMessage(se.v)
	}
	return err.Error()
}

// post sends one request body to path (relative to the base, "/chat/completions")
// through the queue, and returns the response body.
func (s *aiSlot) post(path, body string) (string, error) {
	return s.postWith(path, body, newAiCallFacts(s.cfg.slot, s.cfg.mode))
}

// postWith is post with the call's facts recorded in f (ai_call_facts.go).
func (s *aiSlot) postWith(path, body string, f *aiCallFacts) (string, error) {
	release, waited, err := s.q.acquire()
	f.setQueueWait(waited)
	if err != nil {
		return "", err
	}
	defer release()
	placed := time.Now()

	ad, err := s.adapter()
	if err != nil {
		f.startFailed(placed)
		return "", err
	}
	body = aiPatchModel(body, s.requestModel(ad))
	f.setAdapter(s.factsAdapter(body))

	if s.cfg.mode == "remote" || testModeActive() {
		url := aiJoinPath(s.cfg.baseURL, path)
		if s.cfg.mode == "local" {
			url = aiJoinPath(aiTestLocalBase(s.cfg.slot), path)
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
		defer cancel()
		extra := map[string]string{}
		if s.cfg.mode == "remote" && s.cfg.auth == "google" {
			tok, err := s.googleIDToken()
			if err != nil {
				f.startFailed(placed)
				return "", err
			}
			extra["X-Serverless-Authorization"] = "Bearer " + tok
		}
		f.sent(placed)
		out, err := aiRemotePostWith(ctx, s.cfg, url, body, extra)
		f.answered()
		f.timingsFrom(out)
		return out, err
	}
	// Starting an engine (a first fetch, a cold load) is bounded by the start
	// deadline, not by the call's timeout; the call's own timeout starts when
	// the engine is ready.
	startCtx, cancelStart := context.WithTimeout(context.Background(), aiStartTimeout())
	e, err := s.ensureEngine(startCtx, ad)
	cancelStart()
	if err != nil {
		f.startFailed(placed)
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
	defer cancel()
	f.sent(placed)
	out, err := e.post(ctx, s.cfg.timeout, path, body)
	f.answered()
	f.timingsFrom(out)
	return out, err
}

// call is post, recorded for the Sky Console's AI panel (ai_console.go).
func (s *aiSlot) call(path, body string) (string, error) {
	return s.callWith(path, body, newAiCallFacts(s.cfg.slot, s.cfg.mode))
}

// callWith is call with the facts kept in f, for a measured call. The
// console record's latency stays the whole wall time, queue included.
func (s *aiSlot) callWith(path, body string, f *aiCallFacts) (string, error) {
	start := time.Now()
	out, err := s.postWith(path, body, f)
	rec := aiCallRecord{TS: start.UTC(), Slot: s.cfg.slot, Mode: s.cfg.mode, Kind: aiCallKind(path),
		Model: aiConsoleModel(s.cfg), Outcome: aiCallOutcome(err), LatencyMs: aiCallLatency(start),
		QueueWaitMs: f.queueWaitMs()}
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
	return aiRemotePostWith(ctx, c, url, body, nil)
}

// aiRemotePostWith is aiRemotePost with extra request headers (the Google ID
// token of a slot with AUTH=google).
func aiRemotePostWith(ctx context.Context, c *aiSlotConfig, url, body string, extra map[string]string) (string, error) {
	for attempt := 0; ; attempt++ {
		out, retryAfter, err := aiPostOnceWith(ctx, url, c.key, c.hasKey, body, extra)
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
	return aiPostOnceWith(ctx, url, key, hasKey, body, nil)
}

func aiPostOnceWith(ctx context.Context, url, key string, hasKey bool, body string, extra map[string]string) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", 0, aiFailf(aiInvalidSetting, "bad URL: %v", err).with("setting", "url")
	}
	req.Header.Set("Content-Type", "application/json")
	if hasKey {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
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
	if s.cfg.registry {
		s.adMu.Lock()
		ad, adErr := s.ad, s.adErr
		s.adMu.Unlock()
		for i, st := range settings {
			m := st.(map[string]any)
			if m["key"] != "ADAPTER" {
				continue
			}
			v := s.cfg.adapter
			switch {
			case ad != nil && adErr != "":
				v += " -> " + ad.label + " (sha256:" + ad.digest[:12] + "; refresh failed: " + aiTail(adErr, 160) + ")"
			case ad != nil:
				v += " -> " + ad.label + " (sha256:" + ad.digest[:12] + ")"
			default:
				v += " (not resolved yet)"
			}
			m["value"] = v
			settings[i] = m
		}
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
		if _, err := s.adapter(); err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](s.description())
	}
}

// Ai_endpointInspect : Config -> Task Error Description — the slot's settings
// and their sources, without resolving its registry adapter and without
// starting anything. `Std.Ai.Pipeline` reads it to refuse a registry
// candidate while an operator variable pins the slot's adapter.
func Ai_endpointInspect(cfgArg any) any {
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
		ad, err := s.adapter()
		if err != nil {
			return aiTaskErr(err)
		}
		if s.cfg.mode == "local" && !testModeActive() {
			ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
			defer cancel()
			if _, err := s.ensureEngine(ctx, ad); err != nil {
				return aiTaskErr(err)
			}
		}
		return Ok[any, any](s.description())
	}
}

// skyErrorMessage is the message of a Sky `Error` value (its ErrorInfo's
// `message`), for a log line; any other value is printed as it is.
func skyErrorMessage(v any) string {
	ev := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if ev.IsValid() && ev.Kind() == reflect.Struct {
		if fields := ev.FieldByName("Fields"); fields.IsValid() && fields.Kind() == reflect.Slice && fields.Len() == 2 {
			if info := reflectElem(fields.Index(1)); info.IsValid() && info.Kind() == reflect.Struct {
				if m := info.FieldByName("Message"); m.IsValid() && m.Kind() == reflect.String {
					return m.String()
				}
			}
		}
	}
	return fmt.Sprintf("%v", v)
}

// googleIDToken is the slot's Google ID token for its URL's origin: minted
// by the Sky source (Std.Ai.Cloud.Google.idToken: a service-account key file
// or the metadata server), kept until five minutes before its `exp`. A
// failure to mint is the Sky source's own typed failure (NotConfigured when
// there is no credential).
func (s *aiSlot) googleIDToken() (string, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	if s.idTok != "" && time.Until(s.idExp) > 5*time.Minute {
		return s.idTok, nil
	}
	audience := aiRedactURL(s.cfg.baseURL)
	res := AnyTaskRun(sky_call(s.cfg.idToken, audience))
	tag, okV, errV := anyResultView(res)
	if tag != 0 {
		return "", &aiSkyError{v: errV}
	}
	tok := aiSecretString(okV)
	if tok == "" {
		return "", aiFailf(aiNotConfigured, "slot %q: the Google ID token for %s is empty", s.cfg.slot, audience).with("setting", "GOOGLE_APPLICATION_CREDENTIALS")
	}
	s.idTok, s.idExp = tok, aiJWTExpiry(tok, time.Now().Add(55*time.Minute))
	return tok, nil
}

// aiJWTExpiry reads a JWT's `exp` (unix seconds) without checking its
// signature (the token is ours, minted a moment ago); def when it has none.
func aiJWTExpiry(tok string, def time.Time) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return def
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return def
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return def
	}
	return time.Unix(claims.Exp, 0)
}
