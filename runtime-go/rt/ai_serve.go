//go:build !js

package rt

// ai_serve.go — the runtime half of Std.Ai.Serve: the GPU tier.
//
// The tier is a Sky program (Serve.run) that stands in front of an inference
// engine on a GPU machine. The engine is vLLM, started next to the tier (a
// second container in the same Cloud Run instance, or a process the operator
// runs) and reached on loopback; the tier is what callers see. It
//
//   - serves the OpenAI-compatible routes behind the service token, compared
//     in constant time (a Cloud Run service also checks a Google ID token
//     before a request reaches it);
//   - maps the requested `model` to a resident LoRA adapter, so one GPU serves
//     many fine-tuned variants of one base (multi-LoRA). An adapter's served
//     name is `<name>-v<n>`; a request for a version that is not resident
//     loads it on demand, evicting the least recently used adapter no alias
//     pins and no request uses;
//   - polls the registry's pointer objects (Std.Ai.Registry.publishPointers)
//     for each alias it serves and hot-loads a newly promoted version,
//     unloading the previous one only once its requests have finished;
//   - answers /health ready only when the engine is up and every alias is
//     loaded, and /metrics with its own counters and the engine's queue and
//     KV-cache signals.
//
// Sky resolves the registry (the `resolve` function reads the store's
// pointers and manifests and fetches each adapter's PEFT files, checked
// against their digests); Go holds the HTTP server, the adapter table and the
// loops. Every loop is a periodic.Guard cycle.
//
//	Ai_serveResolve : RawTier -> Task Error (List Setting)
//	Ai_serveRun     : Tier -> Task Error ()
//	Ai_serveTarget  : RawTarget -> Task Error (List Setting)

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sky-app/rt/periodic"
)

// aiTierConfig is a resolved tier.
type aiTierConfig struct {
	engine      string // "vllm"
	bind        string
	token       string
	engineURL   string
	baseID      string // <org>/<repo>@<commit>: what an adapter must be trained on
	baseName    string // the base's served name
	adapters    []string
	maxAdapters int
	poll        time.Duration
	modelsDir   string
	resolve     any // String -> Task Error TierAdapter
}

// aiTierAdapter is what the Sky resolver returns for a reference.
type aiTierAdapter struct {
	ref, label, servedName, baseID, digest, peft, config string
}

type aiResident struct {
	served   string
	digest   string
	inflight int
	retired  bool // an alias served it and moved on: unload once idle
	lastUsed time.Time
	loading  chan struct{} // closed when the load finished
	loadErr  error
}

// aiTier is a running tier.
type aiTier struct {
	cfg    aiTierConfig
	client *http.Client

	mu        sync.Mutex
	resident  map[string]*aiResident // by served name
	aliases   map[string]string      // ref -> served name it serves now
	aliasErr  map[string]string      // ref -> the last failure to resolve or load it
	engineUp  bool
	started   time.Time
	requests  map[string]int // "<model>|<outcome>" -> count
	loads     int
	unloads   int
	evictions int
	engineSig map[string]float64 // the engine's scraped signals

	stop chan struct{}
	srv  *http.Server
}

var (
	aiTiersMu sync.Mutex
	aiTiers   []*aiTier
)

// ─── resolution ─────────────────────────────────────────────────────────

func aiServeBuilder(v any) aiTierConfig {
	return aiTierConfig{
		engine:      aiStrField(v, "Engine", "engine"),
		bind:        aiStrField(v, "Bind", "bind"),
		engineURL:   aiStrField(v, "EngineUrl", "engineUrl"),
		baseID:      aiStrField(v, "BaseId", "baseId"),
		baseName:    aiStrField(v, "BaseName", "baseName"),
		adapters:    asStringList(recordField(v, "Adapters", "adapters")),
		maxAdapters: aiIntField(v, "MaxAdapters", "maxAdapters"),
		poll:        time.Duration(aiIntField(v, "PollS", "pollS")) * time.Second,
		modelsDir:   aiStrField(v, "ModelsDir", "modelsDir"),
	}
}

// aiServeLayer resolves one SKY_AI_SERVE_* setting; each suffix is a literal
// for the configuration census.
func aiServeLayer(key, code string, accept func(string) bool) (string, string) {
	switch key {
	case "ENGINE":
		return aiLayer("AI_SERVE_ENGINE", code, accept)
	case "BIND":
		return aiLayer("AI_SERVE_BIND", code, accept)
	case "ENGINE_URL":
		return aiLayer("AI_SERVE_ENGINE_URL", code, accept)
	case "ADAPTERS":
		return aiLayer("AI_SERVE_ADAPTERS", code, accept)
	case "MAX_ADAPTERS":
		return aiLayer("AI_SERVE_MAX_ADAPTERS", code, accept)
	case "MODELS":
		return aiLayer("AI_SERVE_MODELS", code, accept)
	case "POLL_S":
		return aiLayer("AI_SERVE_POLL_S", code, accept)
	}
	return "", "default"
}

// Ai_serveResolve : RawTier -> Task Error (List Setting) — the tier's
// settings, operator env > the tier's `withX` value > default, in a fixed
// order: ENGINE, BIND, ENGINE_URL, ADAPTERS, MAX_ADAPTERS, MODELS, POLL_S,
// TOKEN (its value never shown).
func Ai_serveResolve(rawArg any) any {
	b := aiServeBuilder(rawArg)
	hasToken := aiBoolField(rawArg, "HasToken", "hasToken")
	token := ""
	if hasToken {
		token = aiSecretString(recordField(rawArg, "Token", "token"))
	}
	return func() any {
		settings, _, err := aiServeSettings(b, token)
		if err != nil {
			return aiTaskErr(err)
		}
		out := []any{}
		for _, s := range settings {
			out = append(out, map[string]any{"key": s.key, "name": s.name, "value": s.value, "source": s.source})
		}
		return Ok[any, any](out)
	}
}

func aiServeSettings(b aiTierConfig, codeToken string) ([]aiSetting, aiTierConfig, error) {
	var out []aiSetting
	c := b
	get := func(key, code, def string, accept func(string) bool) string {
		v, src := aiServeLayer(key, code, accept)
		if v == "" {
			v, src = def, "default"
		}
		out = append(out, aiSetting{key: key, name: skyEnvName("AI_SERVE_" + key), value: v, source: src})
		return v
	}
	engineOK := func(s string) bool { return strings.TrimSpace(s) == "vllm" }
	if raw, ok := lookupEnvRaw(skyEnvName("AI_SERVE_ENGINE")); ok && raw != "" && !engineOK(raw) {
		return nil, c, aiFailf(aiInvalidSetting, "%s=%q: the GPU tier runs vllm", skyEnvName("AI_SERVE_ENGINE"), raw).with("setting", skyEnvName("AI_SERVE_ENGINE"))
	}
	c.engine = get("ENGINE", b.engine, "vllm", engineOK)
	c.bind = get("BIND", b.bind, "0.0.0.0:8080", nil)
	c.engineURL = strings.TrimRight(get("ENGINE_URL", b.engineURL, "http://127.0.0.1:8000", nil), "/")
	adapters := strings.Join(b.adapters, ",")
	adapters = get("ADAPTERS", adapters, "", nil)
	c.adapters = nil
	for _, a := range strings.Split(adapters, ",") {
		if a = strings.TrimPrefix(strings.TrimSpace(a), aiRegistryScheme); a != "" {
			if n, al, ok := strings.Cut(a, "@"); !ok || n == "" || al == "" {
				return nil, c, aiFailf(aiInvalidSetting, "%s: %q is not <name>@<alias>", skyEnvName("AI_SERVE_ADAPTERS"), a).with("setting", skyEnvName("AI_SERVE_ADAPTERS"))
			}
			c.adapters = append(c.adapters, a)
		}
	}
	c.maxAdapters = aiAtoi(get("MAX_ADAPTERS", itoaSet(b.maxAdapters), "4", aiPositiveInt))
	if c.maxAdapters < len(c.adapters) {
		c.maxAdapters = len(c.adapters)
	}
	c.modelsDir = get("MODELS", b.modelsDir, "/models", nil)
	pollS := int(b.poll / time.Second)
	if b.poll < 0 {
		pollS = -1
	}
	c.poll = time.Duration(aiAtoi(get("POLL_S", itoaSet(pollS), "30", aiPositiveInt))) * time.Second
	tokenSuffix := "AI_SERVE_TOKEN"
	if t, src := aiLayer(tokenSuffix, codeToken, nil); t != "" {
		c.token = t
		out = append(out, aiSetting{key: "TOKEN", name: skyEnvName(tokenSuffix), value: "(secret)", source: src})
	}
	return out, c, nil
}

func aiLoopbackBind(bind string) bool {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Ai_serveRun : Tier -> Task Error () — run the tier until the process ends.
// The tier record carries the resolved settings (Ai_serveResolve's), the
// base's id and served name, and the Sky resolver.
func Ai_serveRun(tierArg any) any {
	b := aiServeBuilder(tierArg)
	hasToken := aiBoolField(tierArg, "HasToken", "hasToken")
	token := ""
	if hasToken {
		token = aiSecretString(recordField(tierArg, "Token", "token"))
	}
	b.resolve = recordField(tierArg, "Resolve", "resolve")
	return func() any {
		_, cfg, err := aiServeSettings(b, token)
		if err != nil {
			return aiTaskErr(err)
		}
		cfg.resolve = b.resolve
		if cfg.token == "" && !aiLoopbackBind(cfg.bind) {
			return aiTaskErr(aiFailf(aiNotConfigured,
				"the GPU tier binds %s and has no service token; set %s (a Secret Manager reference on Cloud Run), or Serve.withToken in code, or bind it to loopback",
				cfg.bind, skyEnvName("AI_SERVE_TOKEN")).with("setting", skyEnvName("AI_SERVE_TOKEN")))
		}
		t := newAiTier(cfg)
		if err := t.serve(); err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](struct{}{})
	}
}

func newAiTier(cfg aiTierConfig) *aiTier {
	return &aiTier{
		cfg:       cfg,
		client:    &http.Client{Timeout: 0},
		resident:  map[string]*aiResident{},
		aliases:   map[string]string{},
		aliasErr:  map[string]string{},
		requests:  map[string]int{},
		engineSig: map[string]float64{},
		stop:      make(chan struct{}),
		started:   time.Now(),
	}
}

// serve listens, then waits for the engine, loads the aliases and keeps them
// current; it returns when the listener fails or close is called.
func (t *aiTier) serve() error {
	ln, err := net.Listen("tcp", t.cfg.bind)
	if err != nil {
		return aiFailf(aiInvalidSetting, "the GPU tier cannot listen on %s: %v", t.cfg.bind, err).with("setting", skyEnvName("AI_SERVE_BIND"))
	}
	return t.serveOn(ln)
}

func (t *aiTier) serveOn(ln net.Listener) error {
	t.srv = &http.Server{Handler: t, ReadHeaderTimeout: 30 * time.Second}
	aiTiersMu.Lock()
	aiTiers = append(aiTiers, t)
	aiTiersMu.Unlock()
	fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Serve: the GPU tier listens on %s (engine %s at %s, base %s, aliases %s)\n",
		ln.Addr(), t.cfg.engine, t.cfg.engineURL, t.cfg.baseName, strings.Join(t.cfg.adapters, ", "))
	go t.loops()
	err := t.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (t *aiTier) close() {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	if t.srv != nil {
		t.srv.Close()
	}
	aiTiersMu.Lock()
	for i, x := range aiTiers {
		if x == t {
			aiTiers = append(aiTiers[:i], aiTiers[i+1:]...)
			break
		}
	}
	aiTiersMu.Unlock()
}

// loops: wait for the engine, then keep the aliases and the engine's
// signals current.
func (t *aiTier) loops() {
	for !t.isEngineUp() {
		select {
		case <-t.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
		periodic.Guard("ai.serve-engine-wait", periodicReport, func() error {
			if t.engineHealthy() {
				t.mu.Lock()
				t.engineUp = true
				t.mu.Unlock()
			}
			return nil
		})
	}
	periodic.Guard("ai.serve-aliases", periodicReport, t.refreshAliases)
	go periodic.Every(periodic.Config{
		Name:     "ai.serve-aliases",
		Interval: t.cfg.poll,
		Stop:     t.stop,
		Report:   periodicReport,
		Work:     func(time.Time) error { return t.refreshAliases() },
	})
	periodic.Every(periodic.Config{
		Name:     "ai.serve-metrics",
		Interval: 15 * time.Second,
		Stop:     t.stop,
		Report:   periodicReport,
		Work:     func(time.Time) error { t.scrapeEngine(); t.drain(); return nil },
	})
}

func (t *aiTier) isEngineUp() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.engineUp
}

func (t *aiTier) engineHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", t.cfg.engineURL+"/health", nil)
	resp, err := t.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// ─── adapters ───────────────────────────────────────────────────────────

// resolveRef asks the Sky resolver for the version a reference names now.
func (t *aiTier) resolveRef(ref string) (aiTierAdapter, error) {
	if t.cfg.resolve == nil {
		return aiTierAdapter{}, aiFailf(aiNotConfigured, "the tier has no registry; give it Serve.withRegistry (or set %s)", skyEnvName("AI_REGISTRY")).with("setting", skyEnvName("AI_REGISTRY"))
	}
	res := AnyTaskRun(sky_call(t.cfg.resolve, ref))
	tag, okV, errV := anyResultView(res)
	if tag != 0 {
		return aiTierAdapter{}, &aiSkyError{v: errV}
	}
	a := aiTierAdapter{
		ref:        aiStrField(okV, "Ref", "ref"),
		label:      aiStrField(okV, "Label", "label"),
		servedName: aiStrField(okV, "ServedName", "servedName"),
		baseID:     aiStrField(okV, "BaseId", "baseId"),
		digest:     aiStrField(okV, "Digest", "digest"),
		peft:       aiStrField(okV, "Peft", "peft"),
		config:     aiStrField(okV, "Config", "config"),
	}
	if a.servedName == "" || a.peft == "" || a.config == "" {
		return a, aiFailf(aiInvalidSetting, "%s has no PEFT files in the registry: vLLM loads adapter_model.safetensors and adapter_config.json (register a trained adapter, whose companions carry them)", ref).with("setting", "adapter")
	}
	if a.baseID != t.cfg.baseID {
		return a, aiFailf(aiAdapterMismatch, "%s (%s) was trained on %s, and this tier serves %s; serve it on its own base", ref, a.label, orDefault(a.baseID, "an unknown base"), t.cfg.baseID).
			with("adapter", a.label).with("adapterBase", a.baseID).with("engineBase", t.cfg.baseID)
	}
	return a, nil
}

// ensureLoaded makes an adapter resident: its files copied under
// <models>/adapters/<served>/ and loaded into the engine. Concurrent callers
// for the same adapter wait for one load.
func (t *aiTier) ensureLoaded(a aiTierAdapter) error {
	t.mu.Lock()
	if r, ok := t.resident[a.servedName]; ok {
		ch := r.loading
		t.mu.Unlock()
		if ch != nil {
			<-ch
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		return r.loadErr
	}
	if err := t.makeRoomLocked(); err != nil {
		t.mu.Unlock()
		return err
	}
	r := &aiResident{served: a.servedName, digest: a.digest, lastUsed: time.Now(), loading: make(chan struct{})}
	t.resident[a.servedName] = r
	t.mu.Unlock()

	err := t.load(a)
	t.mu.Lock()
	r.loadErr = err
	close(r.loading)
	r.loading = nil
	if err != nil {
		delete(t.resident, a.servedName)
	} else {
		t.loads++
	}
	t.mu.Unlock()
	return err
}

func (t *aiTier) load(a aiTierAdapter) error {
	dir := filepath.Join(t.cfg.modelsDir, "adapters", a.servedName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return aiFailf(aiEngineFailed, "cannot write %s: %v", dir, err)
	}
	for src, name := range map[string]string{a.peft: "adapter_model.safetensors", a.config: "adapter_config.json"} {
		data, err := os.ReadFile(src)
		if err != nil {
			return aiFailf(aiEngineFailed, "cannot read %s's %s: %v", a.label, name, err)
		}
		if err := aiWriteFileAtomic(filepath.Join(dir, name), data); err != nil {
			return aiFailf(aiEngineFailed, "cannot write %s: %v", filepath.Join(dir, name), err)
		}
	}
	body, _ := json.Marshal(map[string]string{"lora_name": a.servedName, "lora_path": dir})
	status, text, err := t.enginePost("/v1/load_lora_adapter", body)
	if err != nil {
		return err
	}
	if status == 400 && strings.Contains(text, "already") {
		return nil
	}
	if status/100 != 2 {
		return aiFailf(aiEngineFailed, "the engine refused to load %s (HTTP %d): %s", a.label, status, aiTail(text, 300))
	}
	return nil
}

func (t *aiTier) enginePost(path string, body []byte) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", t.cfg.engineURL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, "", aiFailf(aiUnreachable, "the engine at %s: %v", aiRedactURL(t.cfg.engineURL), aiStripURL(err)).with("target", aiRedactURL(t.cfg.engineURL))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(data), nil
}

// pinned: the served names an alias serves now.
func (t *aiTier) pinnedLocked() map[string]bool {
	out := map[string]bool{}
	for _, s := range t.aliases {
		out[s] = true
	}
	return out
}

// makeRoomLocked evicts the least recently used adapter that no alias pins
// and no request uses, when the engine holds as many as it may.
func (t *aiTier) makeRoomLocked() error {
	if len(t.resident) < t.cfg.maxAdapters {
		return nil
	}
	pinned := t.pinnedLocked()
	var victim *aiResident
	for _, r := range t.resident {
		if pinned[r.served] || r.inflight > 0 || r.loading != nil {
			continue
		}
		if victim == nil || r.lastUsed.Before(victim.lastUsed) {
			victim = r
		}
	}
	if victim == nil {
		return aiFailf(aiOverloaded, "the tier holds %d adapters (withMaxAdapters) and every one is pinned by an alias or in use", t.cfg.maxAdapters)
	}
	delete(t.resident, victim.served)
	t.evictions++
	go t.unload(victim.served)
	return nil
}

func (t *aiTier) unload(served string) {
	body, _ := json.Marshal(map[string]string{"lora_name": served})
	if status, text, err := t.enginePost("/v1/unload_lora_adapter", body); err != nil || (status/100 != 2 && status != 404) {
		fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Serve: cannot unload %s from the engine: %v %s\n", served, err, aiTail(text, 200))
		return
	}
	os.RemoveAll(filepath.Join(t.cfg.modelsDir, "adapters", served))
	t.mu.Lock()
	t.unloads++
	t.mu.Unlock()
}

// refreshAliases resolves every alias and loads what moved. The previous
// version stays resident (and loaded) until drain finds it idle.
func (t *aiTier) refreshAliases() error {
	var errs []string
	for _, ref := range t.cfg.adapters {
		a, err := t.resolveRef(ref)
		if err == nil {
			err = t.ensureLoaded(a)
		}
		t.mu.Lock()
		if err != nil {
			t.aliasErr[ref] = aiErrorText(err)
			errs = append(errs, ref+": "+aiErrorText(err))
		} else {
			prev, had := t.aliases[ref]
			t.aliases[ref] = a.servedName
			if r := t.resident[a.servedName]; r != nil {
				r.retired = false
			}
			if had && prev != a.servedName {
				fmt.Fprintf(os.Stderr, "[sky] Std.Ai.Serve: %s moved from %s to %s\n", ref, prev, a.servedName)
				if r := t.resident[prev]; r != nil && !t.pinnedLocked()[prev] {
					r.retired = true
				}
			}
			delete(t.aliasErr, ref)
		}
		t.mu.Unlock()
	}
	t.drain()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// drain unloads the adapters an alias left behind once no request uses them.
// An adapter loaded on demand (not by an alias) stays until it is evicted.
func (t *aiTier) drain() {
	t.mu.Lock()
	pinned := t.pinnedLocked()
	var gone []string
	for _, r := range t.resident {
		if r.retired && !pinned[r.served] && r.inflight == 0 && r.loading == nil {
			delete(t.resident, r.served)
			gone = append(gone, r.served)
		}
	}
	t.mu.Unlock()
	for _, s := range gone {
		t.unload(s)
	}
}

// ready reports whether the engine is up and every alias is loaded, and why
// not.
func (t *aiTier) ready() (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.engineUp {
		return false, "the engine is starting"
	}
	for _, ref := range t.cfg.adapters {
		served, ok := t.aliases[ref]
		if !ok {
			if e := t.aliasErr[ref]; e != "" {
				return false, ref + ": " + e
			}
			return false, ref + " is loading"
		}
		if r := t.resident[served]; r == nil || r.loading != nil {
			return false, ref + " is loading"
		}
	}
	return true, ""
}

// ─── the HTTP surface ───────────────────────────────────────────────────

func (t *aiTier) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/startup":
		w.WriteHeader(200)
		io.WriteString(w, "ok\n")
	case r.URL.Path == "/health":
		ok, why := t.ready()
		if ok {
			aiServeJSON(w, 200, map[string]any{"status": "ready"})
			return
		}
		aiServeJSON(w, 503, map[string]any{"status": "starting", "reason": why})
	case r.URL.Path == "/metrics":
		t.metrics(w)
	case !t.authorised(r):
		aiServeError(w, 401, "authentication_error", "a valid service token is required (Authorization: Bearer)")
	case r.URL.Path == "/v1/models" && r.Method == "GET":
		t.proxy(w, r, nil, "")
	case r.Method == "POST" && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions" || r.URL.Path == "/v1/embeddings"):
		t.complete(w, r)
	default:
		aiServeError(w, 404, "not_found", "the tier serves /v1/chat/completions, /v1/completions, /v1/embeddings, /v1/models, /health and /metrics")
	}
}

// authorised compares the bearer token in constant time. A tier with no
// token (loopback only) lets everything through.
func (t *aiTier) authorised(r *http.Request) bool {
	if t.cfg.token == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + t.cfg.token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func aiServeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func aiServeError(w http.ResponseWriter, status int, typ, msg string) {
	aiServeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": status}})
}

// aiServedVersion parses `<name>-v<n>` into the registry reference
// `<name>@v<n>`.
func aiServedVersion(model string) (string, bool) {
	i := strings.LastIndex(model, "-v")
	if i <= 0 {
		return "", false
	}
	if _, err := strconv.Atoi(model[i+2:]); err != nil {
		return "", false
	}
	return model[:i] + "@v" + model[i+2:], true
}

// complete routes one completion to the engine: the base, a resident
// adapter, or a version loaded on demand.
func (t *aiTier) complete(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		aiServeError(w, 400, "invalid_request_error", "cannot read the request")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil {
		aiServeError(w, 400, "invalid_request_error", "the request is not JSON")
		return
	}
	if ok, why := t.ready(); !ok && !t.isEngineUp() {
		w.Header().Set("Retry-After", "5")
		aiServeError(w, 503, "unavailable", "the tier is starting: "+why)
		return
	}
	model := req.Model
	if model == "" {
		model = t.cfg.baseName
		body = []byte(aiPatchModel(string(body), model))
	}
	if model != t.cfg.baseName {
		t.mu.Lock()
		_, resident := t.resident[model]
		t.mu.Unlock()
		if !resident {
			ref, ok := aiServedVersion(model)
			if !ok {
				t.count(model, "unknown")
				aiServeError(w, 404, "not_found", "the model `"+model+"` is neither the base ("+t.cfg.baseName+") nor an adapter <name>-v<n>")
				return
			}
			a, err := t.resolveRef(ref)
			if err == nil && a.servedName != model {
				err = aiFailf(aiNotRegistered, "%s resolves to %s", ref, a.servedName).with("ref", ref)
			}
			if err == nil {
				err = t.ensureLoaded(a)
			}
			if err != nil {
				t.count(model, "load-failed")
				status := 404
				if f, ok := err.(*aiFailure); ok && (f.code == aiOverloaded || f.code == aiUnreachable || f.code == aiEngineFailed) {
					status = 503
				}
				aiServeError(w, status, "not_found", "cannot serve `"+model+"`: "+aiErrorText(err))
				return
			}
		}
	}
	t.proxy(w, r, body, model)
}

// proxy forwards to the engine and streams its answer back (SSE included),
// counting the request in flight against its adapter so nothing unloads it
// meanwhile.
func (t *aiTier) proxy(w http.ResponseWriter, r *http.Request, body []byte, model string) {
	if model != "" && model != t.cfg.baseName {
		t.mu.Lock()
		res := t.resident[model]
		if res == nil {
			t.mu.Unlock()
			aiServeError(w, 503, "unavailable", "`"+model+"` was unloaded; retry")
			return
		}
		res.inflight++
		res.lastUsed = time.Now()
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			res.inflight--
			t.mu.Unlock()
			t.drain()
		}()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, t.cfg.engineURL+r.URL.Path, rdr)
	if err != nil {
		aiServeError(w, 500, "internal_error", "cannot build the engine request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		t.count(model, "unreachable")
		aiServeError(w, 503, "unavailable", "the engine cannot be reached")
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	outcome := "ok"
	if resp.StatusCode/100 != 2 {
		outcome = "http-" + strconv.Itoa(resp.StatusCode)
	}
	if model != "" {
		t.count(model, outcome)
	}
}

func (t *aiTier) count(model, outcome string) {
	t.mu.Lock()
	t.requests[model+"|"+outcome]++
	t.mu.Unlock()
}

// ─── signals ────────────────────────────────────────────────────────────

// aiEngineSignals are the vLLM metrics the tier re-exports. vLLM renamed the
// KV-cache gauge (gpu_cache_usage_perc, then kv_cache_usage_perc); both are
// read.
var aiEngineSignals = map[string]string{
	"vllm:num_requests_running":  "running",
	"vllm:num_requests_waiting":  "waiting",
	"vllm:gpu_cache_usage_perc":  "kvCache",
	"vllm:kv_cache_usage_perc":   "kvCache",
	"vllm:num_preemptions_total": "preemptions",
}

func (t *aiTier) scrapeEngine() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", t.cfg.engineURL+"/metrics", nil)
	resp, err := t.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	sig := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		name := line
		if i := strings.IndexAny(line, "{ "); i >= 0 {
			name = line[:i]
		}
		key, ok := aiEngineSignals[name]
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			sig[key] += v
		}
	}
	t.mu.Lock()
	t.engineSig = sig
	t.mu.Unlock()
}

// aiTierStatus is one tier as the Sky Console shows it.
type aiTierStatus struct {
	Bind       string             `json:"bind"`
	Engine     string             `json:"engine"`
	Base       string             `json:"base"`
	Ready      bool               `json:"ready"`
	Reason     string             `json:"reason,omitempty"`
	Aliases    map[string]string  `json:"aliases"`
	Resident   []string           `json:"resident"`
	InFlight   int                `json:"inFlight"`
	Loads      int                `json:"loads"`
	Unloads    int                `json:"unloads"`
	Evictions  int                `json:"evictions"`
	Signals    map[string]float64 `json:"signals"`
	UptimeSecs int                `json:"uptimeSecs"`
}

func (t *aiTier) status() aiTierStatus {
	ok, why := t.ready()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := aiTierStatus{Bind: t.cfg.bind, Engine: t.cfg.engine, Base: t.cfg.baseName, Ready: ok, Reason: why,
		Aliases: map[string]string{}, Signals: map[string]float64{}, Loads: t.loads, Unloads: t.unloads,
		Evictions: t.evictions, UptimeSecs: int(time.Since(t.started).Seconds())}
	for k, v := range t.aliases {
		s.Aliases[k] = v
	}
	for k, v := range t.engineSig {
		s.Signals[k] = v
	}
	for n, r := range t.resident {
		s.Resident = append(s.Resident, n)
		s.InFlight += r.inflight
	}
	sort.Strings(s.Resident)
	return s
}

// aiTierStatuses is every tier this process runs (the console's view).
func aiTierStatuses() []aiTierStatus {
	aiTiersMu.Lock()
	tiers := append([]*aiTier(nil), aiTiers...)
	aiTiersMu.Unlock()
	out := []aiTierStatus{}
	for _, t := range tiers {
		out = append(out, t.status())
	}
	return out
}

func (t *aiTier) metrics(w http.ResponseWriter) {
	s := t.status()
	t.mu.Lock()
	keys := make([]string, 0, len(t.requests))
	for k := range t.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	counts := map[string]int{}
	for _, k := range keys {
		counts[k] = t.requests[k]
	}
	t.mu.Unlock()
	var b strings.Builder
	b.WriteString("# TYPE sky_ai_tier_requests_total counter\n")
	for _, k := range keys {
		model, outcome, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "sky_ai_tier_requests_total{model=%q,outcome=%q} %d\n", model, outcome, counts[k])
	}
	ready := 0
	if s.Ready {
		ready = 1
	}
	fmt.Fprintf(&b, "# TYPE sky_ai_tier_ready gauge\nsky_ai_tier_ready %d\n", ready)
	fmt.Fprintf(&b, "# TYPE sky_ai_tier_inflight gauge\nsky_ai_tier_inflight %d\n", s.InFlight)
	fmt.Fprintf(&b, "# TYPE sky_ai_tier_resident_adapters gauge\nsky_ai_tier_resident_adapters %d\n", len(s.Resident))
	fmt.Fprintf(&b, "# TYPE sky_ai_tier_adapter_loads_total counter\nsky_ai_tier_adapter_loads_total %d\n", s.Loads)
	fmt.Fprintf(&b, "# TYPE sky_ai_tier_adapter_unloads_total counter\nsky_ai_tier_adapter_unloads_total %d\n", s.Unloads)
	sigs := make([]string, 0, len(s.Signals))
	for k := range s.Signals {
		sigs = append(sigs, k)
	}
	sort.Strings(sigs)
	for _, k := range sigs {
		fmt.Fprintf(&b, "# TYPE sky_ai_engine_%s gauge\nsky_ai_engine_%s %g\n", aiSnake(k), aiSnake(k), s.Signals[k])
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	io.WriteString(w, b.String())
}

func aiSnake(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			b.WriteRune(c + 32)
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Ai_serveTarget : RawTarget -> Task Error (List Setting) — a deployment
// target's PROJECT, REGION and IMAGE, operator env > the target's value.
func Ai_serveTarget(rawArg any) any {
	code := map[string]string{
		"PROJECT": aiStrField(rawArg, "Project", "project"),
		"REGION":  aiStrField(rawArg, "Region", "region"),
		"IMAGE":   aiStrField(rawArg, "Image", "image"),
	}
	return func() any {
		out := []any{}
		for _, key := range []string{"PROJECT", "REGION", "IMAGE"} {
			var v, src string
			switch key {
			case "PROJECT":
				v, src = aiLayer("AI_SERVE_PROJECT", code[key], nil)
			case "REGION":
				v, src = aiLayer("AI_SERVE_REGION", code[key], nil)
			case "IMAGE":
				v, src = aiLayer("AI_SERVE_IMAGE", code[key], nil)
			}
			out = append(out, map[string]any{"key": key, "name": skyEnvName("AI_SERVE_" + key), "value": strings.TrimSpace(v), "source": src})
		}
		return Ok[any, any](out)
	}
}
