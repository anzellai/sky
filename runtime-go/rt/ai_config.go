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

// aiLayer resolves one setting through configLayers and reports its source.
// accept rejects a present-but-unparseable value, which then falls through
// to the next layer. Only ai_serve.go still calls it; every other AI reader
// uses aiLayerChecked, which refuses such a value instead of skipping it.
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

// aiExpect is the form a setting's value must take: the check, the words an
// error uses for the form, and whether the value may appear in a message. A
// value that could be secret (a key, a token, a URL that can carry
// credentials) is never shown.
type aiExpect struct {
	accept func(string) bool
	form   string
	secret bool
}

// aiBadSetting is the InvalidSetting failure for a value that does not
// parse. It names where the value came from (the variable, or the builder
// in code) and the form expected, and shows the value only when it cannot be
// secret.
func aiBadSetting(where, value string, ex aiExpect) *aiFailure {
	if ex.secret {
		return aiFailf(aiInvalidSetting, "%s is not valid: use %s", where, ex.form).
			with("setting", where).with("reason", "use "+ex.form)
	}
	return aiFailf(aiInvalidSetting, "%s=%q is not valid: use %s", where, strings.TrimSpace(value), ex.form).
		with("setting", where).with("reason", "use "+ex.form)
}

// aiLayerChecked resolves one setting like aiLayer, except that a present
// value that does not parse is an InvalidSetting failure, never skipped: a
// deploy whose variable is wrong must not run on a setting nobody chose.
// builderName names the code's builder (Local.withContext) for a value that
// came from code.
func aiLayerChecked(suffix, builder, builderName string, ex aiExpect) (string, string, error) {
	name := skyEnvName(suffix)
	envVal, envSet := lookupEnvRaw(name)
	operator := envSet && envVal != "" && !isSeededDefault(name) && !isConfigApplied(name)
	for i, v := range configLayers(suffix, builder) {
		if v == "" {
			continue
		}
		if ex.accept != nil && !ex.accept(v) {
			where := name
			if v == builder && !(i == 0 && operator) && builderName != "" {
				where = builderName
			}
			return "", "", aiBadSetting(where, v, ex)
		}
		if i == 0 && operator && v == envVal {
			return v, "env", nil
		}
		if v == builder {
			return v, "code", nil
		}
		return v, "env", nil
	}
	return "", "default", nil
}

// aiEnvChecked reads one global AI variable (no code layer) and refuses a
// value that does not parse. "" when it is unset.
func aiEnvChecked(suffix string, ex aiExpect) (string, error) {
	v, _, err := aiLayerChecked(suffix, "", "", ex)
	return strings.TrimSpace(v), err
}

// The forms of the AI settings, shared by resolution and the startup check
// (ai_startup.go), so the two cannot disagree.
var (
	aiExpectPositive    = aiExpect{accept: aiPositiveInt, form: "a whole number above 0"}
	aiExpectNonNegative = aiExpect{accept: aiNonNegativeInt, form: "a whole number, 0 or more"}
	aiExpectPort        = aiExpect{accept: aiPortNumber, form: "a port number from 0 to 65535 (0 picks a free port)"}
	aiExpectGpu         = aiExpect{accept: aiGpuText, form: "auto, cpu, all or a whole number of layers"}
	aiExpectMode        = aiExpect{accept: aiOneOf("local", "remote"), form: "local or remote"}
	aiExpectAuth        = aiExpect{accept: aiOneOf("bearer", "google"), form: "bearer or google"}
	aiExpectURL         = aiExpect{accept: aiHTTPURL, form: "an http:// or https:// URL", secret: true}
	aiExpectModelRef    = aiExpect{accept: aiModelRefText, form: "a model reference, hf:<org>/<repo>/<file>#sha256=<hex> or file:<path>#sha256=<hex>"}
	aiExpectAdapterRef  = aiExpect{accept: aiAdapterRefText, form: "an adapter name, or registry:<name>@<alias>"}
	aiExpectDeviceAdRef = aiExpect{accept: aiDeviceAdapterText, form: "a model reference (hf:… or file:…#sha256=<hex>) or registry:<name>@<alias>"}
	aiExpectAdapterList = aiExpect{accept: aiAdapterListText, form: "a list name=<model reference>, separated by commas, with an optional :<scale> after a name (support:0.8=file:/a.gguf#sha256=<hex>)"}
	aiExpectPlacement   = aiExpect{accept: aiOneOf("auto", "device", "server"), form: "auto, device or server"}
	aiExpectThreads     = aiExpect{accept: aiAtLeast(1), form: "a whole number of at least 1"}
	aiExpectGpuLayers   = aiExpect{accept: aiAtLeast(0), form: "a whole number of at least 0"}
	aiExpectAccel       = aiExpect{accept: aiOneOf("cpu", "metal", "cuda", "rocm", "vulkan"), form: "cpu, metal, cuda, rocm or vulkan"}
	aiExpectFetch       = aiExpect{accept: aiOneOf("on", "off", "1", "0", "true", "false", "yes", "no"), form: "on or off"}
	aiExpectTrainBack   = aiExpect{accept: aiOneOf("mlx", "unsloth", "vertex", "fake"), form: "mlx, unsloth, vertex or fake"}
	aiExpectRegistry    = aiExpect{accept: aiRegistryURL, form: "file://<dir>, gs://<bucket>/<prefix> or s3://<bucket>/<prefix>", secret: true}
)

func aiRegistryURL(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"file://", "gs://", "s3://"} {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			return true
		}
	}
	return false
}

func aiOneOf(words ...string) func(string) bool {
	return func(s string) bool {
		s = strings.ToLower(strings.TrimSpace(s))
		for _, w := range words {
			if s == w {
				return true
			}
		}
		return false
	}
}

func aiAtLeast(min int) func(string) bool {
	return func(s string) bool {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		return err == nil && n >= min
	}
}

func aiPortNumber(s string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil && n >= 0 && n <= 65535
}

func aiGpuText(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "cpu", "all":
		return true
	}
	return aiNonNegativeInt(s)
}

func aiHTTPURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") && len(s) > len("http://")) ||
		(strings.HasPrefix(s, "https://") && len(s) > len("https://"))
}

func aiModelRefText(s string) bool {
	_, err := aiParseModelRef(s)
	return err == nil
}

func aiRegistryRefText(s string) bool {
	ref, ok := strings.CutPrefix(strings.TrimSpace(s), aiRegistryScheme)
	if !ok {
		return false
	}
	n, a, ok := strings.Cut(ref, "@")
	return ok && n != "" && a != ""
}

// aiAdapterRefText accepts a slot's ADAPTER: a served adapter name, or a
// registry reference written in full.
func aiAdapterRefText(s string) bool {
	if strings.HasPrefix(strings.TrimSpace(s), aiRegistryScheme) {
		return aiRegistryRefText(s)
	}
	return strings.TrimSpace(s) != ""
}

func aiDeviceAdapterText(s string) bool {
	return aiRegistryRefText(s) || aiModelRefText(s)
}

func aiAdapterListText(s string) bool {
	_, err := aiParseNamedAdapters(s, "")
	return err == nil
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
	// draft is the draft model for speculative decoding (Local.withDraft);
	// zero when none. draftMax / draftMin are the tokens drafted per step
	// (0: llama-server's default).
	draft    aiModelRef
	draftMax int
	draftMin int
	// adapters are the named LoRA adapters loaded together on the engine
	// (Local.withAdapters). A call names one by its request's model; a call
	// that names none runs the base model.
	adapters []aiNamedAdapter
}

// aiNamedAdapter is one adapter of Local.withAdapters: the name a call
// selects it by, its pinned file, and the scale it is applied at.
type aiNamedAdapter struct {
	name  string
	ref   aiModelRef
	scale float64
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
	draft      string // Local.withDraft: a model reference
	draftMax   int
	draftMin   int
	adapters   string // Local.withAdapters: name[:scale]=<model reference>, comma-separated
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
		draft:      aiStrField(v, "Draft", "draft"),
		draftMax:   aiIntField(v, "DraftMax", "draftMax"),
		draftMin:   aiIntField(v, "DraftMin", "draftMin"),
		adapters:   aiStrField(v, "Adapters", "adapters"),
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
// `_PORT` / `_DRAFT` / `_DRAFT_MAX` / `_DRAFT_MIN` / `_ADAPTERS` over the
// spec's values. modelOverride is the slot's resolved MODEL (a model
// reference), which replaces the spec's model when set. A value that does
// not parse, from either layer, is InvalidSetting naming where it came from.
func resolveAiLocal(slot string, b aiLocalBuilder, modelOverride string, record func(aiSetting)) (aiLocalSpec, error) {
	get := func(key, builder, builderName string, ex aiExpect) (string, error) {
		if slot == "" {
			if builder == "" {
				return "", nil
			}
			if ex.accept != nil && !ex.accept(builder) {
				return "", aiBadSetting(builderName, builder, ex)
			}
			record(aiSetting{key: key, value: builder, source: "code"})
			return builder, nil
		}
		suffix := aiSlotSuffix(slot, key)
		v, src, err := aiLayerChecked(suffix, builder, builderName, ex)
		if err != nil {
			return "", err
		}
		if v != "" {
			record(aiSetting{key: key, name: skyEnvName(suffix), value: v, source: src})
		}
		return v, nil
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

	v, err := get("CONTEXT", itoaSet(b.context), "Local.withContext", aiExpectPositive)
	if err != nil {
		return spec, err
	}
	if v != "" {
		spec.context = aiAtoi(v)
	}
	if spec.gpu, err = get("GPU", b.gpu, "Local.withGpu", aiExpectGpu); err != nil {
		return spec, err
	}
	spec.gpu = strings.ToLower(strings.TrimSpace(spec.gpu))
	if spec.gpu == "" {
		spec.gpu = "auto"
	}
	spec.parallel = aiDefaultParallel
	if v, err = get("PARALLEL", itoaSet(b.parallel), "Local.withParallel", aiExpectPositive); err != nil {
		return spec, err
	} else if v != "" {
		spec.parallel = aiAtoi(v)
	}
	spec.idleUnload = aiDefaultIdleUnload()
	if v, err = get("IDLE_UNLOAD_S", itoaSet(b.idleUnload), "Local.withIdleUnload", aiExpectNonNegative); err != nil {
		return spec, err
	} else if v != "" {
		spec.idleUnload = time.Duration(aiAtoi(v)) * time.Second
	}
	if v, err = get("PORT", itoaSet(b.port), "Local.withPort", aiExpectPort); err != nil {
		return spec, err
	} else if v != "" {
		spec.port = aiAtoi(v)
	}

	// The draft model and how many tokens it drafts per step.
	draftName := func(key, builder string) string {
		if slot == "" {
			return builder
		}
		return skyEnvName(aiSlotSuffix(slot, key)) + " (or " + builder + " in code)"
	}
	if v, err = get("DRAFT", b.draft, "Local.withDraft", aiExpectModelRef); err != nil {
		return spec, err
	} else if v != "" {
		if spec.draft, err = aiParseModelRef(v); err != nil {
			return spec, err
		}
	}
	if v, err = get("DRAFT_MAX", itoaSet(b.draftMax), "Local.withDraftTokens", aiExpectPositive); err != nil {
		return spec, err
	} else if v != "" {
		spec.draftMax = aiAtoi(v)
	}
	if v, err = get("DRAFT_MIN", itoaSet(b.draftMin), "Local.withDraftTokens", aiExpectNonNegative); err != nil {
		return spec, err
	} else if v != "" {
		spec.draftMin = aiAtoi(v)
	}
	if spec.draft.sha256 == "" && (spec.draftMax > 0 || spec.draftMin > 0) {
		where := draftName("DRAFT_MAX", "Local.withDraftTokens")
		return spec, aiFailf(aiInvalidSetting, "%s sets the drafted tokens and there is no draft model; give the engine Local.withDraft, or unset it", where).
			with("setting", where).with("reason", "a draft token count needs a draft model")
	}
	if spec.draftMax > 0 && spec.draftMin > spec.draftMax {
		where := draftName("DRAFT_MIN", "Local.withDraftTokens")
		return spec, aiFailf(aiInvalidSetting, "%s: the minimum drafted tokens (%d) is above the maximum (%d)", where, spec.draftMin, spec.draftMax).
			with("setting", where).with("reason", "the minimum must not be above the maximum")
	}

	// The named adapters.
	if v, err = get("ADAPTERS", b.adapters, "Local.withAdapters", aiExpectAdapterList); err != nil {
		return spec, err
	} else if v != "" {
		where := draftName("ADAPTERS", "Local.withAdapters")
		if spec.adapters, err = aiParseNamedAdapters(v, where); err != nil {
			return spec, err
		}
	}
	if len(spec.adapters) > 0 && spec.adapter.sha256 != "" {
		where := draftName("ADAPTERS", "Local.withAdapters")
		return spec, aiFailf(aiInvalidSetting,
			"%s and Local.withAdapter are both set; an engine either applies one adapter to every call or loads named adapters that each call chooses. Name the single adapter in Local.withAdapters instead", where).
			with("setting", where).with("reason", "Local.withAdapter and Local.withAdapters cannot share an engine")
	}
	return spec, nil
}

// aiParseNamedAdapters reads Local.withAdapters' text (and the ADAPTERS
// variable): `name[:scale]=<model reference>`, separated by commas. A name is
// letters, digits, '.', '_' and '-', is not "local" (a call naming "local"
// runs the base model), and appears once. The scale is a number above 0
// (default 1). where names the setting in an error ("" for the form check).
func aiParseNamedAdapters(text, where string) ([]aiNamedAdapter, error) {
	fail := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if where == "" {
			return aiFailf(aiInvalidSetting, "%s", msg).with("setting", "adapters").with("reason", msg)
		}
		return aiFailf(aiInvalidSetting, "%s: %s", where, msg).with("setting", where).with("reason", msg)
	}
	var out []aiNamedAdapter
	seen := map[string]bool{}
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		head, ref, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fail("%q is not name=<model reference>", item)
		}
		name, scaleText, hasScale := strings.Cut(strings.TrimSpace(head), ":")
		name = strings.TrimSpace(name)
		if !aiAdapterNameOK(name) {
			return nil, fail("the adapter name %q is not valid: use letters, digits, '.', '_' and '-', and not \"local\"", name)
		}
		if seen[name] {
			return nil, fail("the adapter name %q appears twice", name)
		}
		seen[name] = true
		scale := 1.0
		if hasScale {
			f, err := strconv.ParseFloat(strings.TrimSpace(scaleText), 64)
			if err != nil || !(f > 0) || f > 100 {
				return nil, fail("adapter %q: the scale %q is not a number above 0 (and at most 100)", name, scaleText)
			}
			scale = f
		}
		m, err := aiParseModelRef(ref)
		if err != nil {
			return nil, fail("adapter %q: use a model reference, hf:<org>/<repo>/<file>#sha256=<hex> or file:<path>#sha256=<hex>", name)
		}
		if strings.Contains(m.file, ",") {
			return nil, fail("adapter %q: the path has a comma, which llama-server reads as a list; rename the file", name)
		}
		out = append(out, aiNamedAdapter{name: name, ref: m, scale: scale})
	}
	if len(out) == 0 {
		return nil, fail("the list names no adapter")
	}
	return out, nil
}

func aiAdapterNameOK(name string) bool {
	if name == "" || strings.EqualFold(name, "local") || len(name) > 64 {
		return false
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
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
	layer := func(key, builder, builderName string, ex aiExpect) (string, string, error) {
		suffix := aiSlotSuffix(b.slot, key)
		v, src, err := aiLayerChecked(suffix, builder, builderName, ex)
		if err != nil {
			return "", "", err
		}
		if v != "" {
			record(aiSetting{key: key, name: skyEnvName(suffix), value: v, source: src})
		}
		return v, src, nil
	}
	name := func(key string) string { return skyEnvName(aiSlotSuffix(b.slot, key)) }

	mode, modeSrc, err := layer("MODE", b.mode, "Endpoint.withMode", aiExpectMode)
	if err != nil {
		return nil, err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	// A URL that does not start with http:// or https:// is refused here,
	// whatever the mode: a wrong deploy variable must not be skipped. The
	// URL itself is never shown (it can carry credentials).
	url, urlSrc, err := layer("URL", b.url, "Endpoint.withUrl", aiExpectURL)
	if err != nil {
		return nil, err
	}
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
		c.baseURL = url
	}

	// The key is a Secret: read through the same layers, recorded without
	// its value.
	keySuffix := aiSlotSuffix(b.slot, "KEY")
	if k, src := aiLayer(keySuffix, b.key, nil); k != "" {
		c.key, c.hasKey = k, true
		record(aiSetting{key: "KEY", name: skyEnvName(keySuffix), value: "(secret)", source: src})
	}
	if c.model, _, err = layer("MODEL", b.model, "Endpoint.withModel", aiExpect{}); err != nil {
		return nil, err
	}
	if c.mode == "local" && c.model != "" {
		// A local slot's MODEL is a model reference: refuse one that does
		// not parse here, naming the variable, not at the engine start.
		if _, perr := aiParseModelRef(c.model); perr != nil {
			return nil, aiBadSetting(aiSourceText(aiSettingSource(c.settings, "MODEL"), name("MODEL"), "Endpoint.withModel"), c.model, aiExpectModelRef)
		}
	}
	if c.adapter, _, err = layer("ADAPTER", b.adapter, "Endpoint.withAdapter", aiExpectAdapterRef); err != nil {
		return nil, err
	}
	if strings.HasPrefix(c.adapter, aiRegistryScheme) {
		if !b.hasResolver || b.resolve == nil {
			return nil, aiFailf(aiNotConfigured,
				"slot %q: the adapter %q is a registry reference and the slot has no registry; give it Endpoint.fromRegistry (Registry.resolver …) in code",
				b.slot, c.adapter).with("setting", name("ADAPTER"))
		}
		c.registry, c.resolve = true, b.resolve
		c.adapterRefresh = 30 * time.Second
		v, _, err := layer("ADAPTER_REFRESH_S", itoaSet(b.adapterRefreshS), "Endpoint.withAdapterRefresh", aiExpectNonNegative)
		if err != nil {
			return nil, err
		}
		if v != "" {
			c.adapterRefresh = time.Duration(aiAtoi(v)) * time.Second
		}
	}

	c.auth = "bearer"
	v, _, err := layer("AUTH", b.auth, "Endpoint.withGoogleAuth", aiExpectAuth)
	if err != nil {
		return nil, err
	}
	if v != "" {
		c.auth = strings.ToLower(strings.TrimSpace(v))
	}
	c.idToken = b.idToken
	if c.auth == "google" && c.mode == "remote" && c.idToken == nil {
		return nil, aiFailf(aiNotConfigured, "slot %q: %s=google needs the Google ID-token source; build the slot with Std.Ai.Endpoint", b.slot, name("AUTH")).with("setting", name("AUTH"))
	}

	c.timeout = 120 * time.Second
	if v, _, err = layer("TIMEOUT_MS", itoaSet(b.timeoutMs), "Endpoint.withTimeout", aiExpectPositive); err != nil {
		return nil, err
	} else if v != "" {
		c.timeout = time.Duration(aiAtoi(v)) * time.Millisecond
	}

	if c.mode == "local" {
		lb := b.local
		if !b.hasLocal {
			lb = aiLocalBuilder{context: -1, parallel: -1, idleUnload: -1, port: -1, draftMax: -1, draftMin: -1}
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
	if v, _, err = layer("MAX_INFLIGHT", itoaSet(b.maxInFlight), "Endpoint.withMaxInFlight", aiExpectPositive); err != nil {
		return nil, err
	} else if v != "" {
		c.maxInFlight = aiAtoi(v)
	}
	c.queue = 4 * c.maxInFlight
	if v, _, err = layer("QUEUE", itoaSet(b.queue), "Endpoint.withQueue", aiExpectNonNegative); err != nil {
		return nil, err
	} else if v != "" {
		c.queue = aiAtoi(v)
	}
	c.queueWait = 30 * time.Second
	if v, _, err = layer("QUEUE_WAIT_MS", itoaSet(b.queueWaitMs), "Endpoint.withQueueWait", aiExpectPositive); err != nil {
		return nil, err
	} else if v != "" {
		c.queueWait = time.Duration(aiAtoi(v)) * time.Millisecond
	}
	c.retries = 2
	if v, _, err = layer("RETRIES", itoaSet(b.retries), "Endpoint.withRetry", aiExpectNonNegative); err != nil {
		return nil, err
	} else if v != "" {
		c.retries = aiAtoi(v)
	}
	return c, nil
}

// aiSettingSource is where a recorded setting came from ("" when it was
// not recorded).
func aiSettingSource(settings []aiSetting, key string) string {
	for _, s := range settings {
		if s.key == key {
			return s.source
		}
	}
	return ""
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
