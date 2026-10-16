//go:build !js

package rt

// ai_config.go — how a Std.Ai.Endpoint slot resolves its settings.
//
// Configuration is code. Each setting is a `withX` builder on the Sky value
// (the local default); an operator overrides it per environment with
// `<PREFIX>_AI_<SLOT>_<KEY>`. The order is the one every runtime setting
// uses, because the resolver IS the shared one (configLayers):
//
//	operator env  >  withX value in code  >  stdlib fallback
//
// There is no sky.toml layer for AI. Resolution happens when a slot is first
// used, not at compile time, so one binary runs in every tier.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// aiReservedSlots may not name a slot: they are the global variables' words
// (`<PREFIX>_AI_HOME` must never read as a slot called "home").
var aiReservedSlots = map[string]bool{
	"home": true, "offline": true, "fetch": true, "llama": true, "vllm": true,
	"accel": true, "train": true, "eval": true, "registry": true, "serve": true, "db": true,
}

// aiValidSlot checks a slot name: `[a-z][a-z0-9_]*`, not reserved.
func aiValidSlot(slot string) error {
	if slot == "" {
		return aiFailf(aiInvalidSetting, "the slot name is empty; name it, for example Endpoint.endpoint \"chat\"").with("setting", "slot")
	}
	for i, c := range slot {
		ok := c >= 'a' && c <= 'z' || i > 0 && (c >= '0' && c <= '9' || c == '_')
		if !ok {
			return aiFailf(aiInvalidSetting, "slot %q: a slot name is lower case, starts with a letter and uses only a-z, 0-9 and _", slot).with("setting", "slot")
		}
	}
	if aiReservedSlots[slot] {
		return aiFailf(aiInvalidSetting, "slot %q is a reserved word (it would collide with a global AI variable); choose another name", slot).with("setting", "slot")
	}
	return nil
}

// aiSlotSuffix is the env suffix of one slot setting: AI_CHAT_URL.
func aiSlotSuffix(slot, key string) string {
	return "AI_" + strings.ToUpper(slot) + "_" + key
}

// aiSetting is one resolved setting and where it came from, for describe and
// for error messages. A secret's value is never recorded.
type aiSetting struct {
	key    string // URL, MODEL, …
	name   string // the env variable an operator sets
	value  string
	source string // "env", "code" or "default"
}

// aiLayer resolves one slot setting through configLayers and reports its
// source. accept rejects a present-but-unparseable value, which then falls
// through to the next layer exactly as the Sky.Live settings do.
func aiLayer(suffix, builder string, accept func(string) bool) (string, string) {
	name := skyEnvName(suffix)
	envVal, envSet := lookupEnvRaw(name)
	operator := envSet && envVal != "" && !isSeededDefault(name) && !isConfigApplied(name)
	for i, v := range configLayers(suffix, builder) {
		if v == "" || (accept != nil && !accept(v)) {
			continue
		}
		if i == 0 && operator && v == envVal {
			return v, "env"
		}
		if v == builder {
			return v, "code"
		}
		return v, "env"
	}
	return "", "default"
}

func aiPositiveInt(s string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil && n > 0
}

func aiNonNegativeInt(s string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil && n >= 0
}

func aiAtoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// aiLocalSpec is a resolved managed-local engine specification.
type aiLocalSpec struct {
	model      aiModelRef
	adapter    aiLocalAdapter // a registry LoRA adapter loaded with the model; zero when none
	context    int            // 0: the default, reduced to fit
	threads    int            // 0: llama-server's default
	gpu        string
	parallel   int
	idleUnload time.Duration
	embeddings bool
	port       int // 0: a free port
}

// aiDefaultParallel is the engine slot count when nothing sets one.
const aiDefaultParallel = 2

// aiDefaultIdleUnload is the idle window in development; outside development
// an engine stays loaded (0).
func aiDefaultIdleUnload() time.Duration {
	if aiDevelopment() {
		return 600 * time.Second
	}
	return 0
}

// aiLocalBuilder is a Local.Spec as the Sky side passes it: unset numbers are
// -1 (so a deliberate 0, which means something, is kept) and unset strings "".
type aiLocalBuilder struct {
	model      string
	adapter    string // Local.withAdapter: `file:<path>#sha256=<hex>` of a GGUF LoRA adapter
	context    int
	gpu        string
	parallel   int
	idleUnload int
	embeddings bool
	port       int
}

func parseAiLocalBuilder(v any) aiLocalBuilder {
	return aiLocalBuilder{
		model:      aiStrField(v, "Model", "model"),
		adapter:    aiStrField(v, "Adapter", "adapter"),
		context:    aiIntField(v, "Context", "context"),
		gpu:        aiStrField(v, "Gpu", "gpu"),
		parallel:   aiIntField(v, "Parallel", "parallel"),
		idleUnload: aiIntField(v, "IdleUnload", "idleUnload"),
		embeddings: aiBoolField(v, "Embeddings", "embeddings"),
		port:       aiIntField(v, "Port", "port"),
	}
}

// aiStrField / aiIntField / aiBoolField read one record field and never
// panic: a missing or mistyped field reads as unset, and resolution then
// reports what is missing as a typed failure.
func aiStrField(v any, goName, skyName string) string {
	switch x := recordField(v, goName, skyName).(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

func aiIntField(v any, goName, skyName string) int {
	switch x := recordField(v, goName, skyName).(type) {
	case int:
		return x
	case int64:
		return int(x)
	case int32:
		return int(x)
	case float64:
		return int(x)
	case nil:
		return -1
	}
	return -1
}

func aiBoolField(v any, goName, skyName string) bool {
	b, _ := recordField(v, goName, skyName).(bool)
	return b
}

// itoaSet renders a builder number for configLayers: "" when unset (< 0).
func itoaSet(n int) string {
	if n < 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// resolveAiLocal resolves a local spec. slot is "" for Local.start, which has
// no slot and therefore no environment layer; a slot reads its
// `<PREFIX>_AI_<SLOT>_CONTEXT` / `_GPU` / `_PARALLEL` / `_IDLE_UNLOAD_S` /
// `_PORT` over the spec's values. modelOverride is the slot's resolved MODEL
// (a model reference), which replaces the spec's model when set.
func resolveAiLocal(slot string, b aiLocalBuilder, modelOverride string, record func(aiSetting)) (aiLocalSpec, error) {
	get := func(key, builder string, accept func(string) bool) string {
		if slot == "" {
			if builder != "" && (accept == nil || accept(builder)) {
				record(aiSetting{key: key, value: builder, source: "code"})
				return builder
			}
			return ""
		}
		suffix := aiSlotSuffix(slot, key)
		v, src := aiLayer(suffix, builder, accept)
		if v != "" {
			record(aiSetting{key: key, name: skyEnvName(suffix), value: v, source: src})
		}
		return v
	}
	spec := aiLocalSpec{embeddings: b.embeddings}
	modelStr := modelOverride
	if modelStr == "" {
		modelStr = b.model
	}
	if modelStr == "" {
		return spec, aiFailf(aiNotConfigured, "the local engine has no model; give it Local.model (or set %s to hf:<org>/<repo>/<file>#sha256=<hex>)",
			skyEnvName(aiSlotSuffix(orDefault(slot, "<SLOT>"), "MODEL"))).with("setting", skyEnvName(aiSlotSuffix(orDefault(slot, "<SLOT>"), "MODEL")))
	}
	m, err := aiParseModelRef(modelStr)
	if err != nil {
		return spec, err
	}
	spec.model = m
	if b.adapter != "" {
		a, err := aiParseModelRef(b.adapter)
		if err != nil {
			return spec, err
		}
		if a.kind != "file" {
			return spec, aiFailf(aiInvalidSetting, "adapter %q: a local adapter is a file, file:<path>#sha256=<hex>", b.adapter).with("setting", "adapter")
		}
		spec.adapter = aiLocalAdapter{path: a.file, sha256: a.sha256, label: filepath.Base(a.file)}
	}

	if v := get("CONTEXT", itoaSet(b.context), aiPositiveInt); v != "" {
		spec.context = aiAtoi(v)
	}
	gpuOK := func(s string) bool {
		switch strings.ToLower(s) {
		case "auto", "cpu", "all":
			return true
		}
		return aiNonNegativeInt(s)
	}
	spec.gpu = strings.ToLower(get("GPU", b.gpu, gpuOK))
	if spec.gpu == "" {
		spec.gpu = "auto"
	}
	spec.parallel = aiDefaultParallel
	if v := get("PARALLEL", itoaSet(b.parallel), aiPositiveInt); v != "" {
		spec.parallel = aiAtoi(v)
	}
	spec.idleUnload = aiDefaultIdleUnload()
	if v := get("IDLE_UNLOAD_S", itoaSet(b.idleUnload), aiNonNegativeInt); v != "" {
		spec.idleUnload = time.Duration(aiAtoi(v)) * time.Second
	}
	if v := get("PORT", itoaSet(b.port), aiNonNegativeInt); v != "" {
		spec.port = aiAtoi(v)
	}
	return spec, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// aiEndpointBuilder is an Endpoint as the Sky side passes it.
type aiEndpointBuilder struct {
	slot        string
	mode        string
	url         string
	key         string
	hasKey      bool
	model       string
	adapter     string
	timeoutMs   int
	maxInFlight int
	queue       int
	queueWaitMs int
	retries     int
	hasLocal    bool
	local       aiLocalBuilder
	// A registry resolver (Endpoint.fromRegistry): the Sky function that turns
	// `registry:<name>@<alias>` into a concrete adapter, and how often to ask
	// it again.
	hasResolver     bool
	resolverName    string
	resolve         any
	adapterRefreshS int
	// auth is Endpoint.withGoogleAuth ("google") or "" (the bearer key
	// alone); idToken is the Sky function that mints a Google ID token for an
	// audience (Std.Ai.Cloud.Google.idToken).
	auth    string
	idToken any
}

func parseAiEndpointBuilder(v any) aiEndpointBuilder {
	b := aiEndpointBuilder{
		slot:        aiStrField(v, "Slot", "slot"),
		mode:        aiStrField(v, "Mode", "mode"),
		url:         aiStrField(v, "Url", "url"),
		hasKey:      aiBoolField(v, "HasKey", "hasKey"),
		model:       aiStrField(v, "Model", "model"),
		adapter:     aiStrField(v, "Adapter", "adapter"),
		timeoutMs:   aiIntField(v, "TimeoutMs", "timeoutMs"),
		maxInFlight: aiIntField(v, "MaxInFlight", "maxInFlight"),
		queue:       aiIntField(v, "Queue", "queue"),
		queueWaitMs: aiIntField(v, "QueueWaitMs", "queueWaitMs"),
		retries:     aiIntField(v, "Retries", "retries"),
		hasLocal:    aiBoolField(v, "HasLocal", "hasLocal"),

		hasResolver:     aiBoolField(v, "HasResolver", "hasResolver"),
		resolverName:    aiStrField(v, "ResolverName", "resolverName"),
		adapterRefreshS: aiIntField(v, "AdapterRefreshS", "adapterRefreshS"),
		auth:            aiStrField(v, "Auth", "auth"),
		idToken:         recordField(v, "IdToken", "idToken"),
	}
	if b.hasResolver {
		b.resolve = recordField(v, "Resolve", "resolve")
	}
	if b.hasKey {
		b.key = aiSecretString(recordField(v, "Key", "key"))
	}
	if b.hasLocal {
		b.local = parseAiLocalBuilder(recordField(v, "Local", "local"))
	}
	return b
}

// aiSecretString unwraps a Sky Secret for the transport. The value goes into
// an Authorization header and nowhere else.
func aiSecretString(v any) string {
	switch x := v.(type) {
	case Secret:
		return x.v
	case *Secret:
		if x != nil {
			return x.v
		}
	}
	return ""
}

// fingerprint identifies a builder for the per-process memo. The key is
// hashed, never stored as text.
func (b aiEndpointBuilder) fingerprint() string {
	// A slot name names one slot in a process (its variables, its console
	// row and its engines are keyed by it), so the resolver is identified by
	// its name only: a function value has no stable identity to key on.
	resolver := ""
	if b.hasResolver {
		resolver = "resolver:" + b.resolverName
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d|%d|%d|%d|%v|%+v|%s|%d|%s", b.slot, b.mode, b.url,
		aiShortHash(b.key), b.model, b.adapter, b.timeoutMs, b.maxInFlight, b.queue,
		b.queueWaitMs, b.retries, b.hasLocal, b.local, resolver, b.adapterRefreshS, b.auth)
}

// aiSlotConfig is a fully resolved slot.
type aiSlotConfig struct {
	slot        string
	mode        string // "local" or "remote"
	baseURL     string // remote: the OpenAI-compatible base, ending /v1
	key         string
	hasKey      bool
	model       string
	adapter     string
	timeout     time.Duration
	maxInFlight int
	queue       int
	queueWait   time.Duration
	retries     int
	local       aiLocalSpec
	// registry is set when the adapter is `registry:<name>@<alias>`: the slot
	// resolves it through the resolver at start and again every
	// adapterRefresh (0: once).
	registry       bool
	resolve        any
	adapterRefresh time.Duration
	// auth is "bearer" (the key in Authorization) or "google" (also a Google
	// ID token for the URL's origin in X-Serverless-Authorization).
	auth     string
	idToken  any
	settings []aiSetting
}

// resolveAiSlot applies the local-or-remote rule (design §8.1):
//
//  1. A resolved MODE is obeyed. MODE=local together with a resolved URL is
//     an error naming both sources: a silent choice between them would let a
//     deploy ignore the operator.
//  2. A resolved URL means remote.
//  3. A withLocal spec means a managed local engine.
//  4. Otherwise NotConfigured, naming the variable to set.
//
// A remote endpoint never falls back to a local engine, at resolution or
// later when it is down.
func resolveAiSlot(b aiEndpointBuilder) (*aiSlotConfig, error) {
	if err := aiValidSlot(b.slot); err != nil {
		return nil, err
	}
	c := &aiSlotConfig{slot: b.slot}
	record := func(s aiSetting) { c.settings = append(c.settings, s) }
	layer := func(key, builder string, accept func(string) bool) (string, string) {
		suffix := aiSlotSuffix(b.slot, key)
		v, src := aiLayer(suffix, builder, accept)
		if v != "" {
			record(aiSetting{key: key, name: skyEnvName(suffix), value: v, source: src})
		}
		return v, src
	}
	name := func(key string) string { return skyEnvName(aiSlotSuffix(b.slot, key)) }

	modeOK := func(s string) bool {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "local" || s == "remote"
	}
	if raw, ok := lookupEnvRaw(name("MODE")); ok && raw != "" && !modeOK(raw) {
		return nil, aiFailf(aiInvalidSetting, "%s=%q: use local or remote", name("MODE"), raw).with("setting", name("MODE"))
	}
	mode, modeSrc := layer("MODE", b.mode, modeOK)
	mode = strings.ToLower(strings.TrimSpace(mode))
	url, urlSrc := layer("URL", b.url, nil)
	url = strings.TrimRight(strings.TrimSpace(url), "/")

	switch {
	case mode == "local" && url != "":
		return nil, aiFailf(aiInvalidSetting,
			"slot %q: MODE is local (from %s) and a URL is set (from %s); a local slot cannot also be remote. Unset %s, or set %s=remote",
			b.slot, aiSourceText(modeSrc, name("MODE"), "Endpoint.withMode"),
			aiSourceText(urlSrc, name("URL"), "Endpoint.withUrl"), name("URL"), name("MODE")).with("setting", name("MODE"))
	case mode == "remote":
		if url == "" {
			return nil, aiFailf(aiNotConfigured, "slot %q: MODE is remote and there is no URL; set %s to the OpenAI-compatible base URL (ending /v1)", b.slot, name("URL")).with("setting", name("URL"))
		}
		c.mode = "remote"
	case mode == "local":
		c.mode = "local"
	case url != "":
		c.mode = "remote"
	case b.hasLocal:
		c.mode = "local"
	default:
		return nil, aiFailf(aiNotConfigured,
			"slot %q is not configured: set %s to a remote OpenAI-compatible base URL, or %s=local with %s=hf:<org>/<repo>/<file>#sha256=<hex>, or give the slot Endpoint.withLocal in code",
			b.slot, name("URL"), name("MODE"), name("MODEL")).with("setting", name("URL"))
	}

	if c.mode == "remote" {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return nil, aiFailf(aiInvalidSetting, "%s=%q: the URL must start with http:// or https://", name("URL"), url).with("setting", name("URL"))
		}
		c.baseURL = url
	}

	// The key is a Secret: read through the same layers, recorded without
	// its value.
	keySuffix := aiSlotSuffix(b.slot, "KEY")
	if k, src := aiLayer(keySuffix, b.key, nil); k != "" {
		c.key, c.hasKey = k, true
		record(aiSetting{key: "KEY", name: skyEnvName(keySuffix), value: "(secret)", source: src})
	}
	c.model, _ = layer("MODEL", b.model, nil)
	c.adapter, _ = layer("ADAPTER", b.adapter, nil)
	if strings.HasPrefix(c.adapter, aiRegistryScheme) {
		if !b.hasResolver || b.resolve == nil {
			return nil, aiFailf(aiNotConfigured,
				"slot %q: the adapter %q is a registry reference and the slot has no registry; give it Endpoint.fromRegistry (Registry.resolver …) in code",
				b.slot, c.adapter).with("setting", name("ADAPTER"))
		}
		ref := strings.TrimPrefix(c.adapter, aiRegistryScheme)
		if n, a, ok := strings.Cut(ref, "@"); !ok || n == "" || a == "" {
			return nil, aiFailf(aiInvalidSetting, "%s=%q: write registry:<name>@<alias>", name("ADAPTER"), c.adapter).with("setting", name("ADAPTER"))
		}
		c.registry, c.resolve = true, b.resolve
		c.adapterRefresh = 30 * time.Second
		if v, _ := layer("ADAPTER_REFRESH_S", itoaSet(b.adapterRefreshS), aiNonNegativeInt); v != "" {
			c.adapterRefresh = time.Duration(aiAtoi(v)) * time.Second
		}
	}

	authOK := func(s string) bool {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "bearer" || s == "google"
	}
	if raw, ok := lookupEnvRaw(name("AUTH")); ok && raw != "" && !authOK(raw) {
		return nil, aiFailf(aiInvalidSetting, "%s=%q: use bearer or google", name("AUTH"), raw).with("setting", name("AUTH"))
	}
	c.auth = "bearer"
	if v, _ := layer("AUTH", b.auth, authOK); v != "" {
		c.auth = strings.ToLower(strings.TrimSpace(v))
	}
	c.idToken = b.idToken
	if c.auth == "google" && c.mode == "remote" && c.idToken == nil {
		return nil, aiFailf(aiNotConfigured, "slot %q: %s=google needs the Google ID-token source; build the slot with Std.Ai.Endpoint", b.slot, name("AUTH")).with("setting", name("AUTH"))
	}

	c.timeout = 120 * time.Second
	if v, _ := layer("TIMEOUT_MS", itoaSet(b.timeoutMs), aiPositiveInt); v != "" {
		c.timeout = time.Duration(aiAtoi(v)) * time.Millisecond
	}

	if c.mode == "local" {
		lb := b.local
		if !b.hasLocal {
			lb = aiLocalBuilder{context: -1, parallel: -1, idleUnload: -1, port: -1}
		}
		spec, err := resolveAiLocal(b.slot, lb, c.model, record)
		if err != nil {
			if f, ok := err.(*aiFailure); ok && f.code == aiNotConfigured {
				return nil, aiFailf(aiNotConfigured, "slot %q is local and has no model: set %s=hf:<org>/<repo>/<file>#sha256=<hex>, or give the slot Endpoint.withLocal (Local.model …) in code",
					b.slot, name("MODEL")).with("setting", name("MODEL"))
			}
			return nil, err
		}
		c.local = spec
		if c.model == "" {
			record(aiSetting{key: "MODEL", name: name("MODEL"), value: spec.model.String(), source: "code"})
		}
	}

	c.maxInFlight = 16
	if c.mode == "local" {
		c.maxInFlight = c.local.parallel
	}
	if v, _ := layer("MAX_INFLIGHT", itoaSet(b.maxInFlight), aiPositiveInt); v != "" {
		c.maxInFlight = aiAtoi(v)
	}
	c.queue = 4 * c.maxInFlight
	if v, _ := layer("QUEUE", itoaSet(b.queue), aiNonNegativeInt); v != "" {
		c.queue = aiAtoi(v)
	}
	c.queueWait = 30 * time.Second
	if v, _ := layer("QUEUE_WAIT_MS", itoaSet(b.queueWaitMs), aiPositiveInt); v != "" {
		c.queueWait = time.Duration(aiAtoi(v)) * time.Millisecond
	}
	c.retries = 2
	if v, _ := layer("RETRIES", itoaSet(b.retries), aiNonNegativeInt); v != "" {
		c.retries = aiAtoi(v)
	}
	return c, nil
}

func aiSourceText(src, envName, builder string) string {
	if src == "env" {
		return envName
	}
	return builder + " in code"
}

// target is what describe shows for where calls go: scheme, host and port of
// a remote URL (never its path or query, which can carry a token), or the
// local model.
func (c *aiSlotConfig) target() string {
	if c.mode == "local" {
		return c.local.model.String()
	}
	return aiRedactURL(c.baseURL)
}

func aiRedactURL(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return "(invalid URL)"
	}
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	return scheme + "://" + host
}
