//go:build !js

package rt

// ai_device_serve.go — the server half of Std.Ai.Device.
//
// A slot given `Endpoint.withDevice` is registered by `main` (the build roots
// the binding and emits `rt.AiDeviceRegister`, rust/crates/lower). The server
// then answers three routes:
//
//	GET  /_sky/ai/device/<slot>        the slot's offer (ETag; If-None-Match → 304)
//	GET  /_sky/ai/blob/<sha256>        an offered file, Range requests allowed
//	GET  /_sky/ai/blob/<sha256>.chunks.json   its 64 MiB chunk digests
//	POST /_sky/ai/device/<slot>/call   the fallback call (absent for DeviceOnly)
//
// The blob route serves only the digests a current offer names (the
// allow-list), never a path. The call route follows the Server.rpc rules
// (same origin, application/json), takes at most 64 KiB and 30 calls a
// minute per session.
//
// On the server, the Std.Ai.Device kernels are the server slot itself: the
// offer says `here`, so `decide` answers OnServer and `AiDevice_server` calls
// the registered slot.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// aiDeviceSpec is a Std.Ai.Device.RawSpec as the runtime reads it.
type aiDeviceSpec struct {
	placement   string
	model       string
	adapter     string
	context     int
	maxDownload int64
	backends    []string
	metered     bool
}

func parseAiDeviceSpec(v any) aiDeviceSpec {
	s := aiDeviceSpec{
		placement:   aiStrField(v, "Placement", "placement"),
		model:       aiStrField(v, "Model", "model"),
		adapter:     aiStrField(v, "Adapter", "adapter"),
		context:     aiIntField(v, "Context", "context"),
		maxDownload: int64(aiIntField(v, "MaxDownloadBytes", "maxDownloadBytes")),
		metered:     aiBoolField(v, "Metered", "metered"),
	}
	for _, b := range AsList(recordField(v, "Backends", "backends")) {
		s.backends = append(s.backends, AsString(b))
	}
	return s
}

// aiDeviceEntry is one registered device slot.
type aiDeviceEntry struct {
	slot  string
	thunk func() any

	loadOnce sync.Once
	b        aiEndpointBuilder
	spec     aiDeviceSpec
	loadErr  error

	mu      sync.Mutex
	offer   *aiDeviceOffer
	offerAt time.Time
	files   map[string]string // sha256 → local path, for the blob route
}

var (
	aiDeviceMu      sync.Mutex
	aiDeviceEntries = map[string]*aiDeviceEntry{}
)

// AiDeviceRegister registers a device slot: `main` calls it once per
// `Endpoint.withDevice` binding before any handler runs. The thunk builds the
// endpoint value on first use.
func AiDeviceRegister(slot string, thunk func() any) {
	aiDeviceMu.Lock()
	defer aiDeviceMu.Unlock()
	aiDeviceEntries[slot] = &aiDeviceEntry{slot: slot, thunk: thunk}
}

// aiDeviceReset forgets every registered slot (tests).
func aiDeviceReset() {
	aiDeviceMu.Lock()
	aiDeviceEntries = map[string]*aiDeviceEntry{}
	aiDeviceMu.Unlock()
	aiDeviceLimiter.reset()
}

func aiDeviceEntryFor(slot string) (*aiDeviceEntry, error) {
	aiDeviceMu.Lock()
	e := aiDeviceEntries[slot]
	aiDeviceMu.Unlock()
	if e == nil {
		return nil, aiFailf(aiNotConfigured,
			"slot %q has no device spec on this server; give its Endpoint binding Endpoint.withDevice", slot).
			with("setting", "Endpoint.withDevice for slot "+slot)
	}
	e.loadOnce.Do(func() {
		v := e.thunk()
		cfg := v
		if adt, ok := unwrapAny(derefPointer(v)).(SkyADT); ok && len(adt.Fields) == 1 {
			cfg = adt.Fields[0]
		}
		e.b = parseAiEndpointBuilder(cfg)
		e.spec = parseAiDeviceSpec(recordField(cfg, "Device", "device"))
		if e.b.slot != slot {
			e.loadErr = aiFailf(aiInvalidSetting, "the device slot %q is registered for slot %q", e.b.slot, slot).
				with("setting", "slot")
		}
	})
	return e, e.loadErr
}

// aiDeviceOfferTTL is how long an offer is kept before it is resolved again,
// so an alias move reaches devices without a restart.
const aiDeviceOfferTTL = 30 * time.Second

// offerNow is the slot's current offer.
func (e *aiDeviceEntry) offerNow(ctx context.Context) (*aiDeviceOffer, error) {
	e.mu.Lock()
	if e.offer != nil && time.Since(e.offerAt) < aiDeviceOfferTTL {
		o := e.offer
		e.mu.Unlock()
		return o, nil
	}
	e.mu.Unlock()
	o, files, err := e.resolveOffer(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.offer, e.offerAt, e.files = o, time.Now(), files
	e.mu.Unlock()
	return o, nil
}

func aiDeviceDefaultBackends() []string {
	return []string{aiDeviceNative, aiDeviceWebGpu, aiDeviceWasmCpu}
}

// resolveOffer resolves the placement, model and adapter (operator env over
// code) and verifies the files the offer names.
func (e *aiDeviceEntry) resolveOffer(ctx context.Context) (*aiDeviceOffer, map[string]string, error) {
	placement, _ := aiLayer(aiSlotSuffix(e.slot, "PLACEMENT"), e.spec.placement, func(v string) bool {
		return v == "auto" || v == "device" || v == "server"
	})
	if placement == "" {
		placement = "server"
	}
	o := &aiDeviceOffer{
		Slot: e.slot, Placement: placement,
		Context: e.spec.context, MaxDownloadBytes: e.spec.maxDownload,
		Backends: e.spec.backends, Metered: e.spec.metered,
		Server: placement != "device",
	}
	if o.Context <= 0 {
		o.Context = aiDeviceDefaultContext
	}
	if o.MaxDownloadBytes <= 0 {
		o.MaxDownloadBytes = aiDeviceDefaultMaxDownload
	}
	if len(o.Backends) == 0 {
		o.Backends = aiDeviceDefaultBackends()
	}
	files := map[string]string{}
	if placement == "server" {
		return o, files, nil
	}
	modelRef, _ := aiLayer(aiSlotSuffix(e.slot, "DEVICE_MODEL"), orDefault(e.spec.model, e.b.local.model), nil)
	if modelRef == "" {
		name := skyEnvName(aiSlotSuffix(e.slot, "DEVICE_MODEL"))
		return nil, nil, aiFailf(aiNotConfigured,
			"slot %q has no device model; give it Device.withModel, Endpoint.withLocal, or set %s", e.slot, name).
			with("setting", name)
	}
	m, err := aiParseModelRef(modelRef)
	if err != nil {
		return nil, nil, err
	}
	path, err := aiEnsureModel(ctx, m)
	if err != nil {
		return nil, nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, aiFailf(aiNotProvisioned, "device model %s: %v", m.String(), err).
			with("what", m.String()).with("command", "sky ai provision")
	}
	o.Model = aiDeviceFile{Label: filepath.Base(m.file), Sha256: m.sha256, Bytes: st.Size(), URL: "/_sky/ai/blob/" + m.sha256}
	files[m.sha256] = path

	adapterRef, _ := aiLayer(aiSlotSuffix(e.slot, "DEVICE_ADAPTER"), orDefault(e.spec.adapter, orDefault(e.b.adapter, e.b.local.adapter)), nil)
	if adapterRef == "" {
		return o, files, nil
	}
	ad, refused, err := e.resolveDeviceAdapter(ctx, adapterRef)
	if err != nil {
		return nil, nil, err
	}
	if ad.baseDigest != "" && ad.baseDigest != m.sha256 {
		return nil, nil, aiFailf(aiAdapterMismatch,
			"adapter %s was trained on base %s, the device model is %s", ad.label, ad.baseDigest, m.sha256).
			with("adapter", ad.label).with("adapterBase", ad.baseDigest).with("engineBase", m.sha256)
	}
	if refused != "" {
		o.Refused = refused
		o.Adapter = &aiDeviceFile{Label: ad.label, Sha256: ad.digest, URL: ""}
		return o, files, nil
	}
	ast, err := os.Stat(ad.path)
	if err != nil {
		return nil, nil, aiFailf(aiNotProvisioned, "device adapter %s: %v", ad.label, err).
			with("what", ad.label).with("command", "sky ai registry pull")
	}
	o.Adapter = &aiDeviceFile{Label: ad.label, Sha256: ad.digest, Bytes: ast.Size(), URL: "/_sky/ai/blob/" + ad.digest}
	files[ad.digest] = ad.path
	return o, files, nil
}

// resolveDeviceAdapter resolves the device adapter: a registry reference
// through the slot's resolver (a version that is not cleared for devices is
// refused, with the reason), or a pinned file.
func (e *aiDeviceEntry) resolveDeviceAdapter(ctx context.Context, ref string) (*aiResolvedAdapter, string, error) {
	if strings.HasPrefix(ref, aiRegistryScheme) {
		if !e.b.hasResolver {
			return nil, "", aiFailf(aiInvalidSetting,
				"slot %q: the device adapter %s needs a registry; give the slot Endpoint.fromRegistry", e.slot, ref).
				with("setting", "adapter")
		}
		res := AnyTaskRun(sky_call2(e.b.resolve, strings.TrimPrefix(ref, aiRegistryScheme), true))
		tag, okV, errV := anyResultView(res)
		if tag != 0 {
			return nil, "", &aiSkyError{v: errV}
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
		if !aiBoolField(okV, "OnDevice", "onDevice") {
			return a, a.label + " is not cleared for devices; clear it with Registry.allowOnDevice", nil
		}
		return a, "", nil
	}
	m, err := aiParseModelRef(ref)
	if err != nil {
		return nil, "", err
	}
	if m.kind != "file" {
		return nil, "", aiFailf(aiInvalidSetting, "device adapter %q: an adapter is a registry reference or file:<path>#sha256=<hex>", ref).
			with("setting", "adapter")
	}
	path, err := aiEnsureModel(ctx, m)
	if err != nil {
		return nil, "", err
	}
	return &aiResolvedAdapter{ref: ref, label: filepath.Base(m.file), digest: m.sha256, path: path, format: "gguf"}, "", nil
}

// fileFor is the local path of an offered digest, or "" when no current
// offer names it.
func (e *aiDeviceEntry) fileFor(sum string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.files[sum]
}

// ─── routes ─────────────────────────────────────────────────────────────

// aiDeviceRoutesRegistered reports whether any device slot is registered,
// so a server without one serves none of the routes.
func aiDeviceRoutesRegistered() bool {
	aiDeviceMu.Lock()
	defer aiDeviceMu.Unlock()
	return len(aiDeviceEntries) > 0
}

// registerAiDeviceRoutes adds the device routes to a server's mux when the
// program has a device slot.
func registerAiDeviceRoutes(mux *http.ServeMux) {
	if !aiDeviceRoutesRegistered() {
		return
	}
	mux.HandleFunc("GET /_sky/ai/device/{slot}", aiDeviceOfferHandler)
	mux.HandleFunc("POST /_sky/ai/device/{slot}/call", aiDeviceCallHandler)
	mux.HandleFunc("GET /_sky/ai/blob/{name}", aiDeviceBlobHandler)
	// The browser worker (ai_device_worker.go), from memory like the boot
	// loader.
	mux.Handle(http.MethodGet+" "+AiDeviceWorkerPath, serveStaticJS(AiDeviceWorkerJS))
}

func aiDeviceWriteFailure(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	setSecurityHeaders(w.Header())
	w.WriteHeader(status)
	_, _ = w.Write(aiDeviceFailureJSON(err))
}

func aiDeviceOfferHandler(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	e, err := aiDeviceEntryFor(slot)
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusNotFound, err)
		return
	}
	o, err := e.offerNow(r.Context())
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	body, _ := json.Marshal(o)
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	setSecurityHeaders(w.Header())
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// aiDeviceFileOffered finds the local path of a digest some current offer
// names.
func aiDeviceFileOffered(sum string) string {
	aiDeviceMu.Lock()
	entries := make([]*aiDeviceEntry, 0, len(aiDeviceEntries))
	for _, e := range aiDeviceEntries {
		entries = append(entries, e)
	}
	aiDeviceMu.Unlock()
	for _, e := range entries {
		if p := e.fileFor(sum); p != "" {
			return p
		}
	}
	return ""
}

func aiDeviceBlobHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sum, chunks := strings.CutSuffix(name, ".chunks.json")
	if !aiValidSha256(sum) {
		http.NotFound(w, r)
		return
	}
	path := aiDeviceFileOffered(sum)
	if path == "" {
		// Not on the allow-list: no current offer names this digest.
		http.NotFound(w, r)
		return
	}
	setSecurityHeaders(w.Header())
	if chunks {
		body, err := aiDeviceChunkList(path, sum)
		if err != nil {
			aiDeviceWriteFailure(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(body)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// aiDeviceChunks is the chunk list of one file: its size, the chunk size and
// the SHA-256 of each chunk, so a device verifies and keeps each chunk as it
// arrives and resumes an interrupted download.
type aiDeviceChunks struct {
	Sha256     string   `json:"sha256"`
	Size       int64    `json:"size"`
	ChunkBytes int64    `json:"chunkBytes"`
	Chunks     []string `json:"chunks"`
}

var aiDeviceChunkMu sync.Mutex

// aiDeviceChunkList is the chunk list of a verified file, computed once and
// kept under the AI home (never beside the file, which may be the user's).
func aiDeviceChunkList(path, sum string) ([]byte, error) {
	aiDeviceChunkMu.Lock()
	defer aiDeviceChunkMu.Unlock()
	cache := filepath.Join(aiHome(), "device", "chunks", sum+".json")
	if b, err := os.ReadFile(cache); err == nil {
		return b, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := aiDeviceChunks{Sha256: sum, ChunkBytes: aiDeviceChunkBytes, Chunks: []string{}}
	whole := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		h := sha256.New()
		n, err := io.CopyBuffer(io.MultiWriter(h, whole), io.LimitReader(f, aiDeviceChunkBytes), buf)
		if n > 0 {
			out.Chunks = append(out.Chunks, hex.EncodeToString(h.Sum(nil)))
			out.Size += n
		}
		if err != nil {
			return nil, err
		}
		if n < aiDeviceChunkBytes {
			break
		}
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != sum {
		return nil, aiFailf(aiChecksumMismatch, "%s: expected sha256 %s, the file is sha256 %s", path, sum, got).
			with("expected", sum).with("actual", got)
	}
	b, _ := json.Marshal(out)
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err == nil {
		tmp := cache + ".tmp-" + aiRandomHex(4)
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, cache)
		}
	}
	return b, nil
}

// aiDeviceCallMaxBody is the largest fallback call body.
const aiDeviceCallMaxBody = 64 << 10

// aiDeviceCallsPerMinute is the fallback calls one session may make a minute.
const aiDeviceCallsPerMinute = 30

// aiDeviceCallPaths are the requests a device provider sends.
var aiDeviceCallPaths = map[string]bool{"/chat/completions": true, "/@root/tokenize": true}

type aiDeviceRate struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var aiDeviceLimiter = &aiDeviceRate{hits: map[string][]time.Time{}}

func (l *aiDeviceRate) reset() {
	l.mu.Lock()
	l.hits = map[string][]time.Time{}
	l.mu.Unlock()
}

// allow records one call for key and reports whether it is within the
// limit. Expired entries are dropped on the way, so the map stays bounded by
// the sessions active in the last minute.
func (l *aiDeviceRate) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Minute)
	for k, hs := range l.hits {
		if len(hs) == 0 || hs[len(hs)-1].Before(cut) {
			delete(l.hits, k)
		}
	}
	hs := l.hits[key]
	kept := hs[:0]
	for _, t := range hs {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= aiDeviceCallsPerMinute {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// aiDeviceCallKey is the session a fallback call counts against: the Sky
// session cookie or header, else the client's address.
func aiDeviceCallKey(r *http.Request) string {
	for _, name := range []string{"__Host-sky_sid", "sky_sid"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return "sid:" + aiShortHash(c.Value)
		}
	}
	if h := r.Header.Get(skySessionHeader); h != "" {
		return "sid:" + aiShortHash(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "addr:" + host
}

type aiDeviceCallBody struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

func aiDeviceCallHandler(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	e, err := aiDeviceEntryFor(slot)
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusNotFound, err)
		return
	}
	o, err := e.offerNow(r.Context())
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusServiceUnavailable, err)
		return
	}
	if !o.Server {
		// DeviceOnly: there is no server fallback to call.
		http.NotFound(w, r)
		return
	}
	if reason, ok := sameOriginGuard(r); !ok {
		aiDeviceWriteFailure(w, http.StatusForbidden, aiFailf(aiRemoteRejected, "%s", reason).withInt("status", 403))
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		aiDeviceWriteFailure(w, http.StatusUnsupportedMediaType, aiFailf(aiRemoteRejected, "the call body must be application/json").withInt("status", 415))
		return
	}
	if !aiDeviceLimiter.allow(aiDeviceCallKey(r), time.Now()) {
		f := aiFailf(aiOverloaded, "more than %d device fallback calls a minute", aiDeviceCallsPerMinute)
		f.retryAfterMs, f.httpStatus = 60000, http.StatusTooManyRequests
		w.Header().Set("Retry-After", "60")
		aiDeviceWriteFailure(w, http.StatusTooManyRequests, f)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, aiDeviceCallMaxBody))
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusRequestEntityTooLarge,
			aiFailf(aiRemoteRejected, "the call body is larger than %d bytes", aiDeviceCallMaxBody).withInt("status", 413))
		return
	}
	var call aiDeviceCallBody
	if json.Unmarshal(raw, &call) != nil || !aiDeviceCallPaths[call.Path] {
		aiDeviceWriteFailure(w, http.StatusBadRequest, aiFailf(aiRemoteRejected, "not a device call").withInt("status", 400))
		return
	}
	out, err := aiDeviceServerCall(e, call.Path, call.Body)
	if err != nil {
		aiDeviceWriteFailure(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	setSecurityHeaders(w.Header())
	_, _ = io.Copy(w, bytes.NewReader([]byte(out)))
}

// aiDeviceServerCall runs one call on the registered slot itself.
func aiDeviceServerCall(e *aiDeviceEntry, path, body string) (string, error) {
	s, err := aiSlotFor(e.b)
	if err != nil {
		return "", err
	}
	return s.call(path, body)
}

// ─── kernels (server side) ──────────────────────────────────────────────

func aiDeviceJSON(v any) any {
	b, _ := json.Marshal(v)
	return Ok[any, any](string(b))
}

// AiDevice_offer : String -> Task Error String — on the server the offer
// says `here`, so the decision is the server slot.
func AiDevice_offer(slotArg any) any {
	slot := AsString(slotArg)
	return func() any {
		e, err := aiDeviceEntryFor(slot)
		if err != nil {
			return aiTaskErr(err)
		}
		o := aiDeviceOffer{Slot: slot, Placement: "server", Model: aiDeviceFile{},
			Backends: []string{}, Server: true, Here: true}
		if e.spec.placement != "" {
			o.Placement = e.spec.placement
		}
		return aiDeviceJSON(o)
	}
}

// AiDevice_capabilities : String -> Task Error String — the server has no
// device backend.
func AiDevice_capabilities(_ any) any {
	return func() any {
		c := aiDeviceCaps{Platform: "server"}
		c.normalise()
		return aiDeviceJSON(c)
	}
}

// AiDevice_run : String -> String -> String -> String -> String -> Task Error String
func AiDevice_run(backendArg, slotArg, _, _, _ any) any {
	backend, slot := AsString(backendArg), AsString(slotArg)
	return func() any {
		return aiTaskErr(aiDeviceUnsupportedf(backend, "slot %q: the server runs no device backend", slot))
	}
}

// AiDevice_server : String -> String -> String -> Task Error String — the
// registered slot, called in this process.
func AiDevice_server(slotArg, pathArg, bodyArg any) any {
	slot, path, body := AsString(slotArg), AsString(pathArg), AsString(bodyArg)
	return func() any {
		e, err := aiDeviceEntryFor(slot)
		if err != nil {
			return aiTaskErr(err)
		}
		out, err := aiDeviceServerCall(e, path, body)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](out)
	}
}

// AiDevice_prepare : String -> String -> String -> Task Error String
func AiDevice_prepare(slotArg, backendArg, _ any) any {
	slot, backend := AsString(slotArg), AsString(backendArg)
	return func() any {
		return aiTaskErr(aiDeviceUnsupportedf(backend, "slot %q: the server downloads no device model", slot))
	}
}

// AiDevice_progress : String -> String -> Task Error String
func AiDevice_progress(_, _ any) any {
	return func() any { return aiDeviceJSON(aiDeviceProgress{State: "idle"}) }
}

// AiDevice_evict : String -> String -> Task Error String
func AiDevice_evict(_, _ any) any {
	return func() any { return Ok[any, any]("") }
}

// AiDeviceExportIfAsked handles `--sky-ai-export <slot> <dir>`, which `sky
// build --ai-embed <slot>` passes to the built server: it resolves the slot's
// offer exactly as the server would serve it (operator variables and the
// registry included), copies the model and the adapter into dir as
// `<sha256>.gguf`, writes the offer to `dir/offer.json` and exits. A native
// shell then carries the files in its bundle and never downloads them.
func AiDeviceExportIfAsked() {
	if len(os.Args) < 4 || os.Args[1] != "--sky-ai-export" {
		return
	}
	slot, dir := os.Args[2], os.Args[3]
	if err := aiDeviceExport(slot, dir); err != nil {
		fmt.Fprintf(os.Stderr, "sky --ai-embed %s: %v\n", slot, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func aiDeviceExport(slot, dir string) error {
	e, err := aiDeviceEntryFor(slot)
	if err != nil {
		return err
	}
	o, err := e.offerNow(context.Background())
	if err != nil {
		return err
	}
	if o.Placement == "server" {
		return aiFailf(aiInvalidSetting, "slot %q runs on the server; there is nothing to embed", slot).with("setting", "placement")
	}
	if o.Refused != "" {
		return aiFailf(aiNotPromotable, "slot %q: %s", slot, o.Refused).with("ref", slot).with("reason", o.Refused)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := []aiDeviceFile{o.Model}
	if o.Adapter != nil {
		files = append(files, *o.Adapter)
	}
	for _, f := range files {
		src := e.fileFor(f.Sha256)
		if src == "" {
			return aiFailf(aiNotProvisioned, "%s is not on this host", f.Label).with("what", f.Label).with("command", "sky ai provision")
		}
		if err := aiCopyFile(src, filepath.Join(dir, f.Sha256+".gguf")); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(o)
	return os.WriteFile(filepath.Join(dir, "offer.json"), b, 0o644)
}

func aiCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp-" + aiRandomHex(4)
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
